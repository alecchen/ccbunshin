package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

// byteReader returns one byte per Read, which is the worst case for an SSE parser: no
// read boundary lines up with a frame, a line, or even a JSON token.
type byteReader struct {
	data []byte
	pos  int
}

func (r *byteReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	if len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

type sseEvent struct {
	Name string
	Data string
}

func parseSSE(t *testing.T, body string) []sseEvent {
	t.Helper()
	var events []sseEvent
	for _, frame := range strings.Split(body, "\n\n") {
		frame = strings.TrimSpace(frame)
		if frame == "" {
			continue
		}
		var event sseEvent
		for _, line := range strings.Split(frame, "\n") {
			if name, ok := strings.CutPrefix(line, "event: "); ok {
				event.Name = name
			} else if data, ok := strings.CutPrefix(line, "data: "); ok {
				event.Data = data
			}
		}
		events = append(events, event)
	}
	return events
}

func eventNames(events []sseEvent) []string {
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Name)
	}
	return names
}

// blockDeltas collects the deltas for one content block index.
func blockDeltas(t *testing.T, events []sseEvent, index float64) []map[string]any {
	t.Helper()
	var deltas []map[string]any
	for _, event := range events {
		if event.Name != "content_block_delta" {
			continue
		}
		var payload struct {
			Index float64        `json:"index"`
			Delta map[string]any `json:"delta"`
		}
		if json.Unmarshal([]byte(event.Data), &payload) != nil {
			continue
		}
		if payload.Index == index {
			deltas = append(deltas, payload.Delta)
		}
	}
	return deltas
}

func blockStarts(t *testing.T, events []sseEvent) map[float64]map[string]any {
	t.Helper()
	starts := map[float64]map[string]any{}
	for _, event := range events {
		if event.Name != "content_block_start" {
			continue
		}
		var payload struct {
			Index        float64        `json:"index"`
			ContentBlock map[string]any `json:"content_block"`
		}
		if json.Unmarshal([]byte(event.Data), &payload) != nil {
			continue
		}
		starts[payload.Index] = payload.ContentBlock
	}
	return starts
}

func runStream(t *testing.T, source string) []sseEvent {
	t.Helper()
	var out bytes.Buffer
	translator := newStreamTranslator(&out, nil, "claude-opus-5", 42)
	if err := translator.run(strings.NewReader(source)); err != nil {
		t.Fatalf("run: %v", err)
	}
	return parseSSE(t, out.String())
}

func chunk(delta string) string {
	return "data: " + delta + "\n\n"
}

// --- the real captured stream ---

// TestStreamReasoningThenTextOrdering drives the translator with a captured upstream
// stream. The block order matters: a thinking block must be closed before the text block
// opens, so both get sequential indices.
func TestStreamReasoningThenTextOrdering(t *testing.T) {
	source, err := readFixture("testdata/stream-reasoning.sse")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	events := runStream(t, source)
	names := eventNames(events)

	if names[0] != "message_start" {
		t.Fatalf("first event = %q", names[0])
	}
	if names[len(names)-1] != "message_stop" {
		t.Fatalf("last event = %q", names[len(names)-1])
	}
	if names[len(names)-2] != "message_delta" {
		t.Fatalf("second to last = %q", names[len(names)-2])
	}
	if count := strings.Count(strings.Join(names, ","), "message_start"); count != 1 {
		t.Fatalf("message_start appeared %d times", count)
	}

	starts := blockStarts(t, events)
	thinking, ok := starts[0]
	if !ok || thinking["type"] != "thinking" {
		t.Fatalf("index 0 = %+v, want a thinking block", thinking)
	}
	text, ok := starts[1]
	if !ok || text["type"] != "text" {
		t.Fatalf("index 1 = %+v, want a text block", text)
	}

	// The reasoning text must survive, and must be counted once: the upstream sends
	// both `reasoning` and an identical `reasoning_details` entry.
	thinkingDeltas := blockDeltas(t, events, 0)
	joined := ""
	for _, delta := range thinkingDeltas {
		if delta["type"] == "thinking_delta" {
			joined += delta["thinking"].(string)
		}
	}
	if joined == "" {
		t.Fatal("no thinking text was emitted")
	}
	if strings.Count(joined, "The user asks") > 1 {
		t.Fatalf("reasoning was double counted: %q", joined)
	}

	// message_start must advertise the model the caller asked for, not the target.
	var start struct {
		Message struct {
			Model string `json:"model"`
		} `json:"message"`
	}
	if json.Unmarshal([]byte(events[0].Data), &start) != nil {
		t.Fatal("message_start was not valid JSON")
	}
	if start.Message.Model != "claude-opus-5" {
		t.Fatalf("model = %q, want the requested id", start.Message.Model)
	}
}

func readFixture(path string) (string, error) {
	data, err := osReadFile(path)
	return string(data), err
}

// --- chunk boundary invariance ---

// TestStreamSplitsAcrossTCPBoundaries feeds the same captured stream one byte at a time.
// Translation must not depend on where the read boundaries fall.
func TestStreamSplitsAcrossTCPBoundaries(t *testing.T) {
	source, err := readFixture("testdata/stream-reasoning.sse")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	whole := runStream(t, source)

	var out bytes.Buffer
	translator := newStreamTranslator(&out, nil, "claude-opus-5", 42)
	if err := translator.run(&byteReader{data: []byte(source)}); err != nil {
		t.Fatalf("run: %v", err)
	}
	bytewise := parseSSE(t, out.String())

	if len(whole) != len(bytewise) {
		t.Fatalf("event count differs: %d whole vs %d bytewise", len(whole), len(bytewise))
	}
	for index := range whole {
		// message_start carries a random message id, so compare it structurally.
		if whole[index].Name == "message_start" {
			continue
		}
		if whole[index].Name != bytewise[index].Name || whole[index].Data != bytewise[index].Data {
			t.Fatalf("event %d differs:\n whole:    %+v\n bytewise: %+v", index, whole[index], bytewise[index])
		}
	}
}

// --- synthetic streams ---

func TestStreamAccumulatesToolCallFragments(t *testing.T) {
	source := chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","function":{"name":"Bash","arguments":""}}]}}]}`) +
		chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"{\"command\""}}]}}]}`) +
		chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"ls\"}"}}]}}]}`) +
		chunk(`{"choices":[{"delta":{},"finish_reason":"tool_calls"}]}`) +
		"data: [DONE]\n\n"

	events := runStream(t, source)
	starts := blockStarts(t, events)
	block, ok := starts[0]
	if !ok || block["type"] != "tool_use" {
		t.Fatalf("block = %+v, want tool_use", block)
	}
	if block["name"] != "Bash" || block["id"] != "call_1" {
		t.Fatalf("tool block = %+v", block)
	}
	partial := ""
	for _, delta := range blockDeltas(t, events, 0) {
		if delta["type"] == "input_json_delta" {
			partial += delta["partial_json"].(string)
		}
	}
	if partial != `{"command":"ls"}` {
		t.Fatalf("reassembled arguments = %q", partial)
	}
	// finish_reason tool_calls maps to Anthropic's tool_use.
	var final struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
	}
	for _, event := range events {
		if event.Name == "message_delta" {
			if json.Unmarshal([]byte(event.Data), &final) != nil {
				t.Fatal("message_delta was not valid JSON")
			}
		}
	}
	if final.Delta.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q", final.Delta.StopReason)
	}
}

// The upstream is not guaranteed to send the function name before the first arguments
// fragment, so the arguments must be held until the block can open.
func TestStreamToolArgumentsBeforeName(t *testing.T) {
	source := chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"arguments":"{\"a\""}}]}}]}`) +
		chunk(`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"name":"Bash","arguments":":1}"}}]}}]}`) +
		"data: [DONE]\n\n"

	events := runStream(t, source)
	starts := blockStarts(t, events)
	if starts[0]["name"] != "Bash" {
		t.Fatalf("block = %+v", starts[0])
	}
	partial := ""
	for _, delta := range blockDeltas(t, events, 0) {
		if delta["type"] == "input_json_delta" {
			partial += delta["partial_json"].(string)
		}
	}
	if partial != `{"a":1}` {
		t.Fatalf("arguments = %q, want the buffered fragment flushed in order", partial)
	}
}

func TestStreamReasoningDetailsFallback(t *testing.T) {
	source := chunk(`{"choices":[{"delta":{"reasoning_details":[{"type":"reasoning.text","text":"only here"}]}}]}`) +
		chunk(`{"choices":[{"delta":{"content":"answer"}}]}`) +
		"data: [DONE]\n\n"

	events := runStream(t, source)
	joined := ""
	for _, delta := range blockDeltas(t, events, 0) {
		if delta["type"] == "thinking_delta" {
			joined += delta["thinking"].(string)
		}
	}
	if joined != "only here" {
		t.Fatalf("reasoning = %q, want the details fallback to be used", joined)
	}
}

// The upstream sends `reasoning` and an identical `reasoning_details` entry together, so
// reading both would emit every token twice.
func TestStreamDoesNotDoubleCountReasoning(t *testing.T) {
	source := chunk(`{"choices":[{"delta":{"reasoning":"twice","reasoning_details":[{"type":"reasoning.text","text":"twice"}]}}]}`) +
		"data: [DONE]\n\n"

	events := runStream(t, source)
	joined := ""
	for _, delta := range blockDeltas(t, events, 0) {
		if delta["type"] == "thinking_delta" {
			joined += delta["thinking"].(string)
		}
	}
	if joined != "twice" {
		t.Fatalf("reasoning = %q, want it emitted exactly once", joined)
	}
}

// The upstream's usage is authoritative: its prompt_tokens replaces the local
// estimate, and the cache fields it reports are passed through with the hit count
// subtracted out of input_tokens, the way Anthropic reports them.
func TestStreamUsageAndStopReason(t *testing.T) {
	source := chunk(`{"choices":[{"delta":{"content":"hi"}}]}`) +
		chunk(`{"choices":[{"delta":{},"finish_reason":"length"}],"usage":{"prompt_tokens":7,"completion_tokens":99}}`) +
		"data: [DONE]\n\n"

	events := runStream(t, source)
	var final struct {
		Delta struct {
			StopReason string `json:"stop_reason"`
		} `json:"delta"`
		Usage struct {
			Input         int `json:"input_tokens"`
			Output        int `json:"output_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	for _, event := range events {
		if event.Name == "message_delta" {
			if json.Unmarshal([]byte(event.Data), &final) != nil {
				t.Fatal("message_delta was not valid JSON")
			}
		}
	}
	if final.Delta.StopReason != "max_tokens" {
		t.Fatalf("stop_reason = %q, want max_tokens", final.Delta.StopReason)
	}
	if final.Usage.Output != 99 {
		t.Fatalf("output_tokens = %d, want the upstream's count", final.Usage.Output)
	}
	if final.Usage.Input != 7 {
		t.Fatalf("input_tokens = %d, want the upstream's count over the estimate", final.Usage.Input)
	}
	if final.Usage.CacheCreation != 0 || final.Usage.CacheRead != 0 {
		t.Fatalf("cache = %d/%d, want zero when the upstream reported no hits",
			final.Usage.CacheCreation, final.Usage.CacheRead)
	}
}

// With no usage chunk at all the estimate is all there is, and it must survive:
// dropping it would report an input of zero on every such stream.
func TestStreamFallsBackToEstimateWithoutUsage(t *testing.T) {
	source := chunk(`{"choices":[{"delta":{"content":"hi"}}]}`) +
		chunk(`{"choices":[{"delta":{},"finish_reason":"stop"}]}`) +
		"data: [DONE]\n\n"

	events := runStream(t, source)
	var final struct {
		Usage struct {
			Input  int `json:"input_tokens"`
			Output int `json:"output_tokens"`
		} `json:"usage"`
	}
	for _, event := range events {
		if event.Name == "message_delta" {
			if json.Unmarshal([]byte(event.Data), &final) != nil {
				t.Fatal("message_delta was not valid JSON")
			}
		}
	}
	if final.Usage.Input != 42 {
		t.Fatalf("input_tokens = %d, want the local estimate", final.Usage.Input)
	}
	if final.Usage.Output == 0 {
		t.Fatal("output_tokens = 0, want a count derived from what was emitted")
	}
}

// A reported cache hit is split out of input_tokens, so the two together still
// reconcile to the upstream's prompt_tokens.
func TestStreamReportsCacheReadTokens(t *testing.T) {
	source := chunk(`{"choices":[{"delta":{"content":"hi"}}]}`) +
		chunk(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,`+
			`"completion_tokens":20,"prompt_tokens_details":{"cached_tokens":768}}}`) +
		"data: [DONE]\n\n"

	for _, event := range runStream(t, source) {
		if event.Name != "message_delta" {
			continue
		}
		var payload struct {
			Usage struct {
				Input         int `json:"input_tokens"`
				CacheCreation int `json:"cache_creation_input_tokens"`
				CacheRead     int `json:"cache_read_input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(event.Data), &payload) != nil {
			t.Fatal("message_delta was not valid JSON")
		}
		if payload.Usage.CacheRead != 768 {
			t.Fatalf("cache_read_input_tokens = %d, want 768", payload.Usage.CacheRead)
		}
		if payload.Usage.CacheCreation != 0 {
			t.Fatalf("cache_creation_input_tokens = %d, want 0", payload.Usage.CacheCreation)
		}
		if total := payload.Usage.Input + payload.Usage.CacheCreation + payload.Usage.CacheRead; total != 1000 {
			t.Fatalf("input+cache = %d, want the upstream's prompt_tokens 1000", total)
		}
		return
	}
	t.Fatal("no message_delta in the stream")
}

// A provider that counts cache hits inclusively would otherwise make the
// subtraction negative, which a client renders as a negative context figure.
func TestStreamClampsInclusiveCacheCount(t *testing.T) {
	source := chunk(`{"choices":[{"delta":{"content":"hi"}}]}`) +
		chunk(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,`+
			`"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":400}}}`) +
		"data: [DONE]\n\n"

	for _, event := range runStream(t, source) {
		if event.Name != "message_delta" {
			continue
		}
		var payload struct {
			Usage struct {
				Input int `json:"input_tokens"`
			} `json:"usage"`
		}
		if json.Unmarshal([]byte(event.Data), &payload) != nil {
			t.Fatal("message_delta was not valid JSON")
		}
		if payload.Usage.Input != 0 {
			t.Fatalf("input_tokens = %d, want 0 rather than a negative count", payload.Usage.Input)
		}
		return
	}
	t.Fatal("no message_delta in the stream")
}

func TestStreamEmptyUpstream(t *testing.T) {
	events := runStream(t, "data: [DONE]\n\n")
	names := eventNames(events)
	want := "message_start,message_delta,message_stop"
	if got := strings.Join(names, ","); got != want {
		t.Fatalf("events = %q, want %q", got, want)
	}
}

// Once the status is committed an error can only be reported in band, and no message_stop
// may follow it.
func TestStreamMidStreamErrorEmitsErrorEvent(t *testing.T) {
	var out bytes.Buffer
	translator := newStreamTranslator(&out, nil, "claude-opus-5", 1)
	// A truncated chunk makes the JSON unreadable partway through the stream.
	source := chunk(`{"choices":[{"delta":{"content":"hi"}}]}`) + chunk(`{"choices":[{"delta":{"content":"br`)
	err := translator.run(strings.NewReader(source))
	if err == nil {
		t.Fatal("an unreadable chunk did not produce an error")
	}
	translator.emitError(err.Error())

	events := parseSSE(t, out.String())
	names := eventNames(events)
	if names[len(names)-1] != "error" {
		t.Fatalf("last event = %q, want error", names[len(names)-1])
	}
	for _, name := range names {
		if name == "message_stop" {
			t.Fatal("message_stop followed an error event")
		}
	}
}

// --- end to end through ServeHTTP ---

func TestProxyOpenAIChatNonStreamingEndToEnd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("path = %s, want the translated path", r.URL.Path)
		}
		if r.URL.RawQuery != "" {
			t.Errorf("query = %q, want the Anthropic-only query dropped", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"gen_1","choices":[{"finish_reason":"stop",
		  "message":{"content":"hi there","reasoning":"because"}}],
		  "usage":{"prompt_tokens":5,"completion_tokens":2}}`))
	}))
	defer upstream.Close()

	response := translatedBody(t, openAITestProvider(t, upstream, map[string]string{"claude-opus-5": "oss-model"}),
		"claude-*", `{"model":"claude-opus-5","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("content type = %q", got)
	}
	var decoded struct {
		Type    string `json:"type"`
		Content []struct {
			Type string `json:"type"`
		} `json:"content"`
	}
	if json.Unmarshal(response.Body.Bytes(), &decoded) != nil {
		t.Fatalf("body = %s", response.Body.String())
	}
	if decoded.Type != "message" || len(decoded.Content) != 2 || decoded.Content[0].Type != "thinking" {
		t.Fatalf("envelope = %s", response.Body.String())
	}
}

func TestProxyOpenAIChatRequiresBearerFromAPIKey(t *testing.T) {
	var seenAuth, seenAPIKey string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenAuth = r.Header.Get("Authorization")
		seenAPIKey = r.Header.Get("X-Api-Key")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	translatedBody(t, openAITestProvider(t, upstream, map[string]string{"claude-opus-5": "oss-model"}),
		"claude-*", `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	if seenAuth != "Bearer sk-test" {
		t.Fatalf("Authorization = %q, want the x-api-key promoted to Bearer", seenAuth)
	}
	if seenAPIKey != "" {
		t.Fatalf("x-api-key was forwarded to an OpenAI-chat upstream: %q", seenAPIKey)
	}
}

// The caller's own Bearer token wins when present, since that is the credential the
// profile supplied.
func TestProxyOpenAIChatForwardsBearer(t *testing.T) {
	var seen string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{"p": openAITestProvider(t, upstream, map[string]string{"claude-opus-5": "oss-model"})},
			routes:    []loadedRoute{testRoute("claude-*", "p")},
		},
		client: upstream.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer profile-token")
	request.Header.Set("Anthropic-Version", "2023-06-01")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if seen != "Bearer profile-token" {
		t.Fatalf("Authorization = %q", seen)
	}
}

func TestProxyOpenAIChatDropsAnthropicHeaders(t *testing.T) {
	var version, beta string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		version = r.Header.Get("Anthropic-Version")
		beta = r.Header.Get("Anthropic-Beta")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{"p": openAITestProvider(t, upstream, map[string]string{"claude-opus-5": "oss-model"})},
			routes:    []loadedRoute{testRoute("claude-*", "p")},
		},
		client: upstream.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages",
		strings.NewReader(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("Anthropic-Beta", "prompt-caching-2024-07-31")
	handler.ServeHTTP(httptest.NewRecorder(), request)

	if version != "" || beta != "" {
		t.Fatalf("Anthropic headers leaked: version=%q beta=%q", version, beta)
	}
}

// A translated body no longer matches the upstream's encoding, so those headers must not
// be copied through.
func TestProxyTranslatedResponseDropsUpstreamContentHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Encoding", "gzip")
		w.Header().Set("Content-Length", "999999")
		_, _ = w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"ok"}}]}`))
	}))
	defer upstream.Close()

	response := translatedBody(t, openAITestProvider(t, upstream, map[string]string{"claude-opus-5": "oss-model"}),
		"claude-*", `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	if got := response.Header().Get("Content-Encoding"); got != "" {
		t.Fatalf("Content-Encoding leaked: %q", got)
	}
	if got := response.Header().Get("Content-Length"); got == "999999" {
		t.Fatalf("the upstream's Content-Length was copied to a body we rewrote")
	}
}

func TestProxyOpenAIChatTranslatesUpstreamError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad model","type":"invalid_request_error"}}`))
	}))
	defer upstream.Close()

	response := translatedBody(t, openAITestProvider(t, upstream, map[string]string{"claude-opus-5": "oss-model"}),
		"claude-*", `{"model":"claude-opus-5","messages":[{"role":"user","content":"hi"}]}`)

	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want the upstream status preserved", response.Code)
	}
	var decoded struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(response.Body.Bytes(), &decoded) != nil {
		t.Fatalf("body = %s", response.Body.String())
	}
	if decoded.Type != "error" || decoded.Error.Type != "invalid_request_error" {
		t.Fatalf("envelope = %s, want the Anthropic shape", response.Body.String())
	}
	if decoded.Error.Message != "bad model" {
		t.Fatalf("message = %q, want the upstream's text so a bad mapping is legible", decoded.Error.Message)
	}
}

func TestProxyOpenAIChatStreamsToRecorder(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		for _, frame := range []string{
			chunk(`{"choices":[{"delta":{"reasoning":"think"}}]}`),
			chunk(`{"choices":[{"delta":{"content":"answer"}}]}`),
			chunk(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"completion_tokens":3}}`),
			"data: [DONE]\n\n",
		} {
			_, _ = io.WriteString(w, frame)
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	response := translatedBody(t, openAITestProvider(t, upstream, map[string]string{"claude-opus-5": "oss-model"}),
		"claude-*", `{"model":"claude-opus-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)

	if got := response.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	events := parseSSE(t, response.Body.String())
	names := strings.Join(eventNames(events), ",")
	if strings.Count(names, "message_start") != 1 || strings.Count(names, "message_stop") != 1 {
		t.Fatalf("stream framing = %q", names)
	}
	text := ""
	for _, delta := range blockDeltas(t, events, 1) {
		if delta["type"] == "text_delta" {
			text += delta["text"].(string)
		}
	}
	if text != "answer" {
		t.Fatalf("text = %q", text)
	}
}

func osReadFile(path string) ([]byte, error) { return os.ReadFile(path) }
