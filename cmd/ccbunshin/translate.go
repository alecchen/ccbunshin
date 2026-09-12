package main

import (
	"encoding/json"
	"fmt"
)

// syntheticThinkingSignature fills the signature field on a thinking block. The upstream
// has no equivalent, but the Anthropic block shape carries one. It is stripped again on
// the inbound path, so it never reaches a real Anthropic API through this proxy - but a
// transcript holding it would be rejected if pointed directly at Anthropic, which is why
// the openai-chat dialect is only for OpenAI-compatible upstreams.
const syntheticThinkingSignature = "ccbunshin-openai-chat"

type anthropicRequest struct {
	Model         string           `json:"model"`
	MaxTokens     int              `json:"max_tokens"`
	System        json.RawMessage  `json:"system"`
	Messages      []map[string]any `json:"messages"`
	Stream        bool             `json:"stream"`
	Temperature   *float64         `json:"temperature"`
	TopP          *float64         `json:"top_p"`
	StopSequences []string         `json:"stop_sequences"`
	Tools         []map[string]any `json:"tools"`
	ToolChoice    json.RawMessage  `json:"tool_choice"`
	// Deliberately absent, so a field missing from this struct cannot reach the
	// upstream no matter what the caller sends: thinking, output_config,
	// reasoning_effort, top_k, metadata, betas, mcp_servers, container,
	// service_tier, context_management, diagnostics.
}

type openAIToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIMessage struct {
	Role       string           `json:"role"`
	Content    any              `json:"content,omitempty"`
	ToolCalls  []openAIToolCall `json:"tool_calls,omitempty"`
	ToolCallID string           `json:"tool_call_id,omitempty"`
}

type openAIChatRequest struct {
	Model       string          `json:"model"`
	Messages    []openAIMessage `json:"messages"`
	MaxTokens   int             `json:"max_tokens,omitempty"`
	Stream      bool            `json:"stream,omitempty"`
	Temperature *float64        `json:"temperature,omitempty"`
	TopP        *float64        `json:"top_p,omitempty"`
	Stop        []string        `json:"stop,omitempty"`
	Tools       []any           `json:"tools,omitempty"`
	ToolChoice  any             `json:"tool_choice,omitempty"`
}

// anthropicSystemText flattens either accepted system form to text.
func anthropicSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	var blocks []map[string]any
	if json.Unmarshal(raw, &blocks) != nil {
		return ""
	}
	out := ""
	for _, block := range blocks {
		part, ok := block["text"].(string)
		if !ok || part == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += part
	}
	return out
}

// blockText joins the text of the text-typed blocks in a content array.
func blockText(blocks []any) string {
	out := ""
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block["type"] != "text" {
			continue
		}
		part, _ := block["text"].(string)
		if part == "" {
			continue
		}
		if out != "" {
			out += "\n\n"
		}
		out += part
	}
	return out
}

// imageParts converts the image blocks in a content array. It returns nil when there
// are none, which lets the caller keep plain string content for the common case.
func imageParts(blocks []any) []any {
	var parts []any
	for _, raw := range blocks {
		block, ok := raw.(map[string]any)
		if !ok || block["type"] != "image" {
			continue
		}
		source, _ := block["source"].(map[string]any)
		if source == nil {
			continue
		}
		switch source["type"] {
		case "base64":
			media, _ := source["media_type"].(string)
			if media == "" {
				media = "image/jpeg"
			}
			data, _ := source["data"].(string)
			if data == "" {
				continue
			}
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": "data:" + media + ";base64," + data},
			})
		case "url":
			url, _ := source["url"].(string)
			if url == "" {
				continue
			}
			parts = append(parts, map[string]any{
				"type":      "image_url",
				"image_url": map[string]any{"url": url},
			})
		}
	}
	return parts
}

// toolResultText flattens tool_result content. is_error has no OpenAI equivalent, so it
// is surfaced as a text prefix; otherwise the model would read a failed command as a
// successful one.
func toolResultText(block map[string]any) string {
	text := ""
	switch content := block["content"].(type) {
	case string:
		text = content
	case []any:
		text = blockText(content)
	}
	if isError, _ := block["is_error"].(bool); isError {
		text = "Error: " + text
	}
	return text
}

func toolArguments(input any) string {
	if input == nil {
		return "{}"
	}
	encoded, err := json.Marshal(input)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

// translateToolChoice maps the Anthropic variants onto OpenAI ones.
func translateToolChoice(raw json.RawMessage) any {
	if len(raw) == 0 {
		return nil
	}
	var choice map[string]any
	if json.Unmarshal(raw, &choice) != nil {
		return nil
	}
	switch choice["type"] {
	case "auto":
		return "auto"
	case "any":
		return "required"
	case "none":
		return "none"
	case "tool":
		name, _ := choice["name"].(string)
		return map[string]any{"type": "function", "function": map[string]any{"name": name}}
	}
	return nil
}

// translateTools converts Anthropic custom tools to OpenAI function tools. A tool with
// no input_schema is a server-side tool (web search and friends) and has no equivalent,
// so it is dropped rather than sent as a malformed function.
func translateTools(tools []map[string]any) []any {
	var out []any
	for _, tool := range tools {
		schema, ok := tool["input_schema"]
		if !ok {
			continue
		}
		name, _ := tool["name"].(string)
		if name == "" {
			continue
		}
		function := map[string]any{"name": name, "parameters": schema}
		if description, ok := tool["description"].(string); ok {
			function["description"] = description
		}
		out = append(out, map[string]any{"type": "function", "function": function})
	}
	return out
}

// translateAnthropicMessages converts the message array, emitting tool results before
// the turn they belong to so a chat-completions upstream sees tool calls answered
// immediately, which is what its role alternation expects.
func translateAnthropicMessages(messages []map[string]any) ([]openAIMessage, error) {
	var out []openAIMessage
	for _, message := range messages {
		role, _ := message["role"].(string)
		switch content := message["content"].(type) {
		case string:
			if content != "" {
				out = append(out, openAIMessage{Role: role, Content: content})
			}
		case []any:
			var toolResults []openAIMessage
			var toolCalls []openAIToolCall
			for _, raw := range content {
				block, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				switch block["type"] {
				case "tool_result":
					id, _ := block["tool_use_id"].(string)
					toolResults = append(toolResults, openAIMessage{
						Role:       "tool",
						ToolCallID: id,
						Content:    toolResultText(block),
					})
				case "tool_use":
					id, _ := block["id"].(string)
					name, _ := block["name"].(string)
					toolCalls = append(toolCalls, openAIToolCall{
						ID:   id,
						Type: "function",
						Function: struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						}{Name: name, Arguments: toolArguments(block["input"])},
					})
				}
			}
			out = append(out, toolResults...)

			text := blockText(content)
			if text == "" && len(toolCalls) == 0 {
				continue
			}
			entry := openAIMessage{Role: role, ToolCalls: toolCalls}
			if parts := imageParts(content); len(parts) > 0 {
				if text != "" {
					parts = append([]any{map[string]any{"type": "text", "text": text}}, parts...)
				}
				entry.Content = parts
			} else {
				entry.Content = text
			}
			out = append(out, entry)
		}
	}
	return mergeSameRole(out), nil
}

// mergeSameRole collapses adjacent messages that share a role and carry plain string
// content. Tool messages and tool calls are left alone, since merging them would break
// the call-and-result pairing a chat-completions upstream matches on.
func mergeSameRole(messages []openAIMessage) []openAIMessage {
	var out []openAIMessage
	for _, message := range messages {
		previous, ok := message.Content.(string)
		if ok && len(out) > 0 {
			last := out[len(out)-1]
			prior, priorOK := last.Content.(string)
			if priorOK && last.Role == message.Role && len(last.ToolCalls) == 0 && last.ToolCallID == "" {
				out[len(out)-1].Content = prior + "\n\n" + previous
				continue
			}
		}
		out = append(out, message)
	}
	return out
}

// translateAnthropicRequest builds an OpenAI chat-completions body from an Anthropic
// messages body. It constructs a fresh request rather than mutating the inbound one.
func translateAnthropicRequest(body []byte, targetModel string) ([]byte, error) {
	var request anthropicRequest
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, fmt.Errorf("request body must be valid JSON: %w", err)
	}
	messages, err := translateAnthropicMessages(request.Messages)
	if err != nil {
		return nil, err
	}
	if system := anthropicSystemText(request.System); system != "" {
		messages = append([]openAIMessage{{Role: "system", Content: system}}, messages...)
	}
	out := openAIChatRequest{
		Model:       targetModel,
		Messages:    messages,
		MaxTokens:   request.MaxTokens,
		Stream:      request.Stream,
		Temperature: request.Temperature,
		TopP:        request.TopP,
		Stop:        request.StopSequences,
		Tools:       translateTools(request.Tools),
		ToolChoice:  translateToolChoice(request.ToolChoice),
	}
	return json.Marshal(out)
}

// --- response direction ---

type openAIResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		FinishReason string `json:"finish_reason"`
		Message      struct {
			Content          string          `json:"content"`
			Reasoning        string          `json:"reasoning"`
			ReasoningDetails []reasoningPart `json:"reasoning_details"`
			ToolCalls        []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"message"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

type reasoningPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	// The upstream nests the prompt-cache hit count under details. Its spelling is the
	// OpenAI one; a provider that names it differently reports no hits rather than
	// failing.
	PromptTokensDetails *struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"prompt_tokens_details"`
}

// cachedTokens is the prompt-cache hit count, 0 when the upstream reported none.
func (u *openAIUsage) cachedTokens() int {
	if u == nil || u.PromptTokensDetails == nil {
		return 0
	}
	return u.PromptTokensDetails.CachedTokens
}

// anthropicUsageFromUpstream converts an upstream usage report to Anthropic's fields.
// Anthropic reports cache reads separately from input_tokens, so the hit count is
// subtracted out; the clamp keeps a provider that counts hits inclusively from yielding a
// negative input count. cache_creation_input_tokens has no upstream counterpart and is
// always 0, but is emitted rather than omitted: a client computing
// cache_read / (input + cache_creation + cache_read) reads a missing key as an error, not
// as a zero. A nil report is all zeros.
func anthropicUsageFromUpstream(usage *openAIUsage) map[string]any {
	if usage == nil {
		return map[string]any{
			"input_tokens":                0,
			"output_tokens":               0,
			"cache_creation_input_tokens": 0,
			"cache_read_input_tokens":     0,
		}
	}
	cached := usage.cachedTokens()
	return map[string]any{
		"input_tokens":                max(usage.PromptTokens-cached, 0),
		"output_tokens":               usage.CompletionTokens,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     cached,
	}
}

// reasoningText prefers the `reasoning` string and falls back to reasoning_details. The
// upstream sends both with identical text, so reading one and ignoring the other is what
// keeps the thinking block from carrying every token twice.
func reasoningText(reasoning string, details []reasoningPart) string {
	if reasoning != "" {
		return reasoning
	}
	out := ""
	for _, part := range details {
		if part.Text == "" {
			continue
		}
		out += part.Text
	}
	return out
}

// mapStopReason translates an OpenAI finish reason to the Anthropic equivalent. An
// unrecognized value becomes end_turn rather than failing the response.
func mapStopReason(finishReason string) string {
	switch finishReason {
	case "length":
		return "max_tokens"
	case "tool_calls", "function_call":
		return "tool_use"
	case "content_filter":
		return "refusal"
	}
	return "end_turn"
}

// parseToolArguments decodes a tool call's argument string into an object. Anthropic
// requires an object, so a malformed string degrades to an empty one rather than
// emitting a string where an object belongs.
func parseToolArguments(arguments string) any {
	if arguments == "" {
		return map[string]any{}
	}
	var decoded any
	if json.Unmarshal([]byte(arguments), &decoded) != nil {
		return map[string]any{}
	}
	if _, ok := decoded.(map[string]any); !ok {
		return map[string]any{}
	}
	return decoded
}

func anthropicMessageID(upstreamID string) string {
	if upstreamID == "" {
		return "msg_ccbunshin"
	}
	return "msg_" + upstreamID
}

// translateOpenAIResponse builds an Anthropic message envelope. Block order is fixed:
// thinking, then text, then tool_use, because a thinking block after text is rejected by
// an Anthropic-shaped client.
func translateOpenAIResponse(body []byte, requestedModel string) ([]byte, error) {
	var response openAIResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, fmt.Errorf("upstream response must be valid JSON: %w", err)
	}
	content := []any{}
	stopReason := "end_turn"
	usage := anthropicUsageFromUpstream(nil)

	if len(response.Choices) > 0 {
		choice := response.Choices[0]
		stopReason = mapStopReason(choice.FinishReason)
		if thinking := reasoningText(choice.Message.Reasoning, choice.Message.ReasoningDetails); thinking != "" {
			content = append(content, map[string]any{
				"type":      "thinking",
				"thinking":  thinking,
				"signature": syntheticThinkingSignature,
			})
		}
		if choice.Message.Content != "" {
			content = append(content, map[string]any{"type": "text", "text": choice.Message.Content})
		}
		for index, call := range choice.Message.ToolCalls {
			id := call.ID
			if id == "" {
				id = fmt.Sprintf("toolu_%d", index+1)
			}
			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  call.Function.Name,
				"input": parseToolArguments(call.Function.Arguments),
			})
		}
	}
	if response.Usage != nil {
		usage = anthropicUsageFromUpstream(response.Usage)
	}

	return json.Marshal(map[string]any{
		"id":            anthropicMessageID(response.ID),
		"type":          "message",
		"role":          "assistant",
		"model":         requestedModel,
		"content":       content,
		"stop_reason":   stopReason,
		"stop_sequence": nil,
		"usage":         usage,
	})
}

// --- token estimate ---

// estimateRequestTokens approximates the input token count from character volume. It is
// a heuristic, not a tokenizer: the upstream 404s on count_tokens, and Claude Code asks
// for a number at startup, so a rough answer beats an error. Four characters per token
// is the usual English approximation.
func estimateRequestTokens(body []byte) int {
	var request anthropicRequest
	if json.Unmarshal(body, &request) != nil {
		return 0
	}
	characters := len(anthropicSystemText(request.System))
	for _, message := range request.Messages {
		switch content := message["content"].(type) {
		case string:
			characters += len(content)
		case []any:
			for _, raw := range content {
				block, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				switch block["type"] {
				case "text":
					text, _ := block["text"].(string)
					characters += len(text)
				case "tool_result":
					characters += len(toolResultText(block))
				case "tool_use":
					characters += len(toolArguments(block["input"]))
				}
			}
		}
		characters += len(message["role"].(string)) + 4
	}
	for _, tool := range request.Tools {
		name, _ := tool["name"].(string)
		description, _ := tool["description"].(string)
		characters += len(name) + len(description)
		if schema, err := json.Marshal(tool["input_schema"]); err == nil {
			characters += len(schema)
		}
	}
	return (characters + 3) / 4
}
