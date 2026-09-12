package main

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
)

type streamState int

const (
	stateStart streamState = iota
	stateThinking
	stateText
	stateTool
)

// maxEventBytes bounds one accumulated SSE event. A stream that never sends a blank
// line would otherwise grow without limit.
const maxEventBytes = 4 << 20

type toolAccumulator struct {
	id      string
	name    string
	opened  bool
	pending string // arguments that arrived before the name was known
}

// streamTranslator turns an OpenAI chat-completions SSE stream into the Anthropic event
// sequence. It reads with bufio and writes whole frames, so event boundaries need not
// line up with anything: the upstream's chunking is invisible to the output.
type streamTranslator struct {
	writer  io.Writer
	flusher http.Flusher

	requestedModel string
	messageID      string
	inputTokens    int
	reasoningChars int
	textChars      int

	nextIndex int // index allocated to the next block
	openIndex int // index of the open block, -1 when none
	state     streamState
	started   bool
	finished  bool

	tools      map[int]*toolAccumulator
	stopReason string
	usage      *openAIUsage
}

func newStreamTranslator(writer io.Writer, flusher http.Flusher, requestedModel string, inputTokens int) *streamTranslator {
	return &streamTranslator{
		writer:         writer,
		flusher:        flusher,
		requestedModel: requestedModel,
		messageID:      "msg_" + randomHex(12),
		inputTokens:    inputTokens,
		openIndex:      -1,
		tools:          map[int]*toolAccumulator{},
		stopReason:     "end_turn",
	}
}

func randomHex(bytes int) string {
	buf := make([]byte, bytes)
	if _, err := rand.Read(buf); err != nil {
		return "000000000000"
	}
	return hex.EncodeToString(buf)
}

func (s *streamTranslator) emit(event string, payload any) {
	encoded, err := json.Marshal(payload)
	if err != nil {
		return
	}
	fmt.Fprintf(s.writer, "event: %s\ndata: %s\n\n", event, encoded)
	if s.flusher != nil {
		s.flusher.Flush()
	}
}

// emitError reports a failure after the status is committed, where an in-band event is
// the only remaining channel. No message_stop follows an error.
func (s *streamTranslator) emitError(message string) {
	s.emit("error", map[string]any{
		"type":  "error",
		"error": map[string]any{"type": "api_error", "message": message},
	})
}

// openBlock allocates the next index and marks the block open.
func (s *streamTranslator) openBlock(state streamState, block map[string]any) {
	s.openIndex = s.nextIndex
	s.nextIndex++
	s.state = state
	s.emit("content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         s.openIndex,
		"content_block": block,
	})
}

func (s *streamTranslator) delta(delta map[string]any) {
	s.emit("content_block_delta", map[string]any{
		"type":  "content_block_delta",
		"index": s.openIndex,
		"delta": delta,
	})
}

// closeBlock ends the open block. A thinking block closes with a signature, matching the
// signature-bearing shape it was opened with.
func (s *streamTranslator) closeBlock() {
	if s.openIndex < 0 {
		return
	}
	if s.state == stateThinking {
		s.delta(map[string]any{"type": "signature_delta", "signature": syntheticThinkingSignature})
	}
	s.emit("content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": s.openIndex,
	})
	s.openIndex = -1
	s.state = stateStart
}

func (s *streamTranslator) appendReasoning(text string) {
	if s.state == stateText || s.state == stateTool {
		// Late reasoning cannot be ordered before an open block without breaking
		// Anthropic block ordering, so it is dropped rather than interleaved.
		log.Printf("proxy: dropping reasoning that arrived after content began")
		return
	}
	if s.state != stateThinking {
		s.closeBlock()
		s.openBlock(stateThinking, map[string]any{
			"type":      "thinking",
			"thinking":  "",
			"signature": "",
		})
	}
	s.reasoningChars += len(text)
	s.delta(map[string]any{"type": "thinking_delta", "thinking": text})
}

func (s *streamTranslator) appendText(text string) {
	if s.state == stateTool {
		return
	}
	if s.state != stateText {
		s.closeBlock()
		s.openBlock(stateText, map[string]any{"type": "text", "text": ""})
	}
	s.textChars += len(text)
	s.delta(map[string]any{"type": "text_delta", "text": text})
}

// appendToolCall accumulates a fragment. The upstream sends id and name only on the
// first fragment for an index and arguments on every one, and may send arguments before
// the name, so the block opens on the first fragment that carries a name.
func (s *streamTranslator) appendToolCall(call openAIStreamDeltaToolCall) {
	accumulator, ok := s.tools[call.Index]
	if !ok {
		accumulator = &toolAccumulator{}
		s.tools[call.Index] = accumulator
	}
	if call.ID != "" {
		accumulator.id = call.ID
	}
	if call.Function.Name != "" {
		accumulator.name = call.Function.Name
	}
	arguments := call.Function.Arguments
	if accumulator.opened {
		if arguments != "" {
			s.delta(map[string]any{"type": "input_json_delta", "partial_json": arguments})
		}
		return
	}
	if accumulator.name == "" {
		// An arguments fragment can precede the name; hold it until the block opens.
		accumulator.pending += arguments
		return
	}
	s.closeBlock()
	id := accumulator.id
	if id == "" {
		id = fmt.Sprintf("toolu_%d", call.Index+1)
	}
	s.openBlock(stateTool, map[string]any{
		"type":  "tool_use",
		"id":    id,
		"name":  accumulator.name,
		"input": map[string]any{},
	})
	accumulator.opened = true
	// Anything held from before the name was known, then this fragment, in arrival order.
	if held := accumulator.pending + arguments; held != "" {
		accumulator.pending = ""
		s.delta(map[string]any{"type": "input_json_delta", "partial_json": held})
	}
}

func (s *streamTranslator) start() {
	if s.started {
		return
	}
	s.started = true
	s.emit("message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            s.messageID,
			"type":          "message",
			"role":          "assistant",
			"model":         s.requestedModel,
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":                s.inputTokens,
				"output_tokens":               0,
				"cache_creation_input_tokens": 0,
				"cache_read_input_tokens":     0,
			},
		},
	})
}

// finish closes the open block and emits the closing events. It is idempotent so a
// stream ending on [DONE] and then EOF cannot emit the tail twice.
func (s *streamTranslator) finish() {
	if s.finished {
		return
	}
	s.finished = true
	s.closeBlock()
	// The estimate stands in for the upstream's count until usage arrives, at which
	// point the reported figure supersedes it - including input_tokens, which the
	// upstream knows exactly and estimateRequestTokens only approximates.
	usage := map[string]any{
		"input_tokens":                s.inputTokens,
		"output_tokens":               (s.textChars + s.reasoningChars + 3) / 4,
		"cache_creation_input_tokens": 0,
		"cache_read_input_tokens":     0,
	}
	if s.usage != nil {
		usage = anthropicUsageFromUpstream(s.usage)
	}
	s.emit("message_delta", map[string]any{
		"type": "message_delta",
		"delta": map[string]any{
			"stop_reason":   s.stopReason,
			"stop_sequence": nil,
		},
		"usage": usage,
	})
	s.emit("message_stop", map[string]any{"type": "message_stop"})
}

// run reads the upstream SSE stream and writes the translated one. ReadString is used
// rather than bufio.Scanner because Scanner's token ceiling can be exceeded by a large
// tool-call fragment, and Scan failing mid-stream would leave only an in-band error to
// report it with.
func (s *streamTranslator) run(body io.Reader) error {
	reader := bufio.NewReaderSize(body, 64*1024)
	var data strings.Builder
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				done, derr := s.dispatch(data.String())
				data.Reset()
				if derr != nil {
					return derr
				}
				if done {
					return nil
				}
			case strings.HasPrefix(trimmed, "data:"):
				if data.Len() > 0 {
					data.WriteString("\n")
				}
				data.WriteString(strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
			default:
				// event:, id:, retry: and comments carry nothing the translation needs.
			}
			if data.Len() > maxEventBytes {
				return fmt.Errorf("stream event exceeded %d bytes", maxEventBytes)
			}
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			if data.Len() > 0 {
				done, derr := s.dispatch(data.String())
				if derr != nil {
					return derr
				}
				if done {
					return nil
				}
			}
			s.start()
			s.finish()
			return nil
		}
	}
}

// dispatch handles one complete SSE event, reporting whether the stream is finished.
func (s *streamTranslator) dispatch(payload string) (bool, error) {
	if payload == "" {
		return false, nil
	}
	if payload == "[DONE]" {
		s.start()
		s.finish()
		return true, nil
	}
	var chunk openAIStreamChunk
	if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
		return false, fmt.Errorf("upstream sent an unreadable stream chunk: %w", err)
	}
	s.start()
	s.handleChunk(chunk)
	return false, nil
}

// handleChunk applies one upstream delta. The order follows the upstream's own, so block
// ordering stays monotonic.
func (s *streamTranslator) handleChunk(chunk openAIStreamChunk) {
	if len(chunk.Choices) > 0 {
		delta := chunk.Choices[0].Delta
		if finish := chunk.Choices[0].FinishReason; finish != nil {
			s.stopReason = mapStopReason(*finish)
		}
		if reasoning := reasoningText(delta.Reasoning, delta.ReasoningDetails); reasoning != "" {
			s.appendReasoning(reasoning)
		}
		if delta.Content != "" {
			s.appendText(delta.Content)
		}
		for _, call := range delta.ToolCalls {
			s.appendToolCall(call)
		}
	}
	if chunk.Usage != nil {
		s.usage = chunk.Usage
	}
}

type openAIStreamChunk struct {
	Choices []struct {
		FinishReason *string     `json:"finish_reason"`
		Delta        openAIDelta `json:"delta"`
	} `json:"choices"`
	Usage *openAIUsage `json:"usage"`
}

type openAIDelta struct {
	Reasoning        string                      `json:"reasoning"`
	ReasoningDetails []reasoningPart             `json:"reasoning_details"`
	Content          string                      `json:"content"`
	ToolCalls        []openAIStreamDeltaToolCall `json:"tool_calls"`
}

type openAIStreamDeltaToolCall struct {
	Index    int    `json:"index"`
	ID       string `json:"id"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
