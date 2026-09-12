package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// openAITestProvider builds a provider for a translated upstream, mirroring testProvider
// so the dialect cases read like the existing ones.
func openAITestProvider(t *testing.T, server *httptest.Server, models map[string]string) loadedProvider {
	t.Helper()
	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	return loadedProvider{
		upstream: parsed,
		timeout:  time.Second,
		models:   models,
		dialect:  dialectOpenAIChat,
	}
}

func testRoute(pattern, providerName string) loadedRoute {
	return loadedRoute{pattern: pattern, provider: providerName}
}

// translatedBody runs one request through the proxy against the given upstream and
// returns the response.
func translatedBody(t *testing.T, provider loadedProvider, pattern, requestBody string) *httptest.ResponseRecorder {
	t.Helper()
	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{"p": provider},
			routes:    []loadedRoute{testRoute(pattern, "p")},
		},
		client: provider.upstreamClient(),
	}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages", strings.NewReader(requestBody))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-Api-Key", "sk-test")
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}

func (p loadedProvider) upstreamClient() *http.Client {
	return &http.Client{Timeout: time.Second}
}

// --- dialect resolution ---

func TestPlanForDefaultsToAnthropicDialect(t *testing.T) {
	cfg := loadedConfig{
		providers: map[string]loadedProvider{"p": {}},
		routes:    []loadedRoute{testRoute("claude-*", "p")},
	}
	plan, ok := cfg.planFor("claude-opus-5")
	if !ok {
		t.Fatal("model did not route")
	}
	if plan.dialect != dialectAnthropic {
		t.Fatalf("dialect = %q, want %q", plan.dialect, dialectAnthropic)
	}
	if plan.targetModel != "claude-opus-5" {
		t.Fatalf("target = %q, want the requested id unchanged", plan.targetModel)
	}
}

func TestPlanForProviderDialectAndDefaultModel(t *testing.T) {
	cfg := loadedConfig{
		providers: map[string]loadedProvider{
			"p": {dialect: dialectOpenAIChat, defaultModel: "oss-model"},
		},
		routes: []loadedRoute{testRoute("claude-*", "p")},
	}
	plan, _ := cfg.planFor("claude-future-9")
	if plan.dialect != dialectOpenAIChat {
		t.Fatalf("dialect = %q", plan.dialect)
	}
	// A model with no explicit mapping must still reach a target the upstream accepts.
	if plan.targetModel != "oss-model" {
		t.Fatalf("target = %q, want default_model", plan.targetModel)
	}
}

func TestPlanForRouteDialectOverridesProvider(t *testing.T) {
	cfg := loadedConfig{
		providers: map[string]loadedProvider{"p": {dialect: dialectAnthropic}},
		routes: []loadedRoute{{
			pattern:    "claude-*",
			provider:   "p",
			dialect:    dialectOpenAIChat,
			configured: true, // as loadConfig sets it for a stated dialect
		}},
	}
	plan, _ := cfg.planFor("claude-opus-5")
	if plan.dialect != dialectOpenAIChat {
		t.Fatalf("route dialect did not win: %q", plan.dialect)
	}
}

func TestPlanForModelDialectOverridesRoute(t *testing.T) {
	cfg := loadedConfig{
		providers: map[string]loadedProvider{"p": {dialect: dialectOpenAIChat}},
		routes: []loadedRoute{{
			pattern:  "claude-*",
			provider: "p",
			models:   map[string]string{"claude-opus-5": "oss-model"},
			modelDialects: map[string]dialect{
				"oss-model": dialectAnthropic,
			},
		}},
	}
	plan, _ := cfg.planFor("claude-opus-5")
	if plan.targetModel != "oss-model" {
		t.Fatalf("target = %q", plan.targetModel)
	}
	// The override keys on the target id, since that is what the upstream sees.
	if plan.dialect != dialectAnthropic {
		t.Fatalf("per-model dialect did not win: %q", plan.dialect)
	}
}

func TestPlanForRouteModelsOverrideProviderModels(t *testing.T) {
	cfg := loadedConfig{
		providers: map[string]loadedProvider{
			"p": {models: map[string]string{"claude-opus-5": "provider-target"}},
		},
		routes: []loadedRoute{{
			pattern:  "claude-*",
			provider: "p",
			models:   map[string]string{"claude-opus-5": "route-target"},
		}},
	}
	plan, _ := cfg.planFor("claude-opus-5")
	if plan.targetModel != "route-target" {
		t.Fatalf("target = %q, want the route map to win", plan.targetModel)
	}
}

func TestPlanForTargetModelFallbackChain(t *testing.T) {
	providers := map[string]loadedProvider{
		"p": {models: map[string]string{"mapped": "from-provider"}, defaultModel: "from-default"},
	}
	cfg := loadedConfig{
		providers: providers,
		routes:    []loadedRoute{{pattern: "*", provider: "p", models: map[string]string{"route-mapped": "from-route"}}},
	}
	cases := map[string]string{
		"route-mapped": "from-route",    // route map wins
		"mapped":       "from-provider", // then provider map
		"unmapped":     "from-default",  // then the provider default
	}
	for model, want := range cases {
		plan, ok := cfg.planFor(model)
		if !ok {
			t.Fatalf("%s did not route", model)
		}
		if plan.targetModel != want {
			t.Errorf("%s target = %q, want %q", model, plan.targetModel, want)
		}
	}
}

// The namespace is what lets two routes disagree about dialect, so this pins that a
// narrower route does not get swallowed by a broader earlier one.
func TestNamespacedRoutesDoNotCollide(t *testing.T) {
	cfg := loadedConfig{
		providers: map[string]loadedProvider{
			"provider2": {dialect: dialectOpenAIChat, defaultModel: "oss-model"},
			"vertex":    {dialect: dialectAnthropic},
		},
		routes: []loadedRoute{
			{pattern: "claude-*", provider: "provider2"},
			{pattern: "vertex-claude-*", provider: "vertex"},
		},
	}
	claude, _ := cfg.planFor("claude-opus-5")
	if claude.dialect != dialectOpenAIChat {
		t.Fatalf("claude-* dialect = %q", claude.dialect)
	}
	vertex, ok := cfg.planFor("vertex-claude-opus")
	if !ok {
		t.Fatal("vertex-claude-opus did not route")
	}
	if vertex.dialect != dialectAnthropic {
		t.Fatalf("namespaced route dialect = %q, want anthropic", vertex.dialect)
	}
	if vertex.targetModel != "vertex-claude-opus" {
		t.Fatalf("namespaced target = %q", vertex.targetModel)
	}
}

func TestParseDialectRejectsUnknownValues(t *testing.T) {
	if _, err := parseDialect(`provider "p"`, "openai"); err == nil {
		t.Fatal("an unknown dialect was accepted")
	}
	if _, err := parseDialect(`provider "p"`, "anthropic"); err != nil {
		t.Fatalf("anthropic rejected: %v", err)
	}
	if _, err := parseDialect(`provider "p"`, ""); err != nil {
		t.Fatalf("empty dialect rejected: %v", err)
	}
}

// Every other resolution test builds loadedRoute literals, which bypasses loadConfig and
// so cannot catch a normalization bug in it. This one resolves through the real config
// path: parseDialect normalizes an unset dialect to anthropic, and a route that fails to
// distinguish "unset" from "explicitly anthropic" masks its provider's dialect entirely.
func TestPlanForResolvesProviderDialectThroughLoadConfig(t *testing.T) {
	file := writeConfig(t, `{
	  "port": 3456,
	  "providers": {
	    "provider2": {
	      "upstream": "https://gw.example.invalid",
	      "dialect": "openai-chat",
	      "default_model": "oss-model"
	    }
	  },
	  "routes": [{"pattern": "claude-*", "provider": "provider2"}]
	}`)
	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	plan, ok := cfg.planFor("claude-opus-5")
	if !ok {
		t.Fatal("model did not route")
	}
	if plan.dialect != dialectOpenAIChat {
		t.Fatalf("dialect = %q, want the provider's openai-chat to reach the plan", plan.dialect)
	}
	if plan.targetModel != "oss-model" {
		t.Fatalf("target = %q", plan.targetModel)
	}
}

// The converse: a route that explicitly states a dialect still wins over its provider.
func TestPlanForRouteDialectWinsThroughLoadConfig(t *testing.T) {
	file := writeConfig(t, `{
	  "port": 3456,
	  "providers": {
	    "p": {"upstream": "https://gw.example.invalid", "dialect": "anthropic"}
	  },
	  "routes": [{"pattern": "claude-*", "provider": "p", "dialect": "openai-chat",
	              "models": {"claude-opus-5": "oss-model"}}]
	}`)
	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	plan, _ := cfg.planFor("claude-opus-5")
	if plan.dialect != dialectOpenAIChat {
		t.Fatalf("dialect = %q, want the route's explicit value", plan.dialect)
	}
}

// A config written before dialects existed must still resolve to pass-through.
func TestPlanForLegacyConfigIsPassThrough(t *testing.T) {
	file := writeConfig(t, `{
	  "port": 3456,
	  "providers": {"p": {"upstream": "https://gw.example.invalid"}},
	  "routes": [{"pattern": "claude-*", "provider": "p"}]
	}`)
	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatalf("a pre-dialect config failed to load: %v", err)
	}
	plan, _ := cfg.planFor("claude-opus-5")
	if plan.dialect != dialectAnthropic {
		t.Fatalf("dialect = %q, want pass-through", plan.dialect)
	}
}

func TestLoadConfigRejectsOpenAIChatWithoutModelMapping(t *testing.T) {
	file := writeConfig(t, `{
	  "port": 3456,
	  "providers": {"p": {"upstream": "https://gw.example.invalid", "dialect": "openai-chat"}},
	  "routes": [{"pattern": "claude-*", "provider": "p"}]
	}`)
	_, err := loadConfig(file)
	if err == nil {
		t.Fatal("openai-chat with no mapping was accepted")
	}
	if !strings.Contains(err.Error(), "claude-*") {
		t.Fatalf("error does not name the route: %v", err)
	}
}

func TestLoadConfigRejectsUnknownDialectInConfig(t *testing.T) {
	file := writeConfig(t, `{
	  "port": 3456,
	  "providers": {"p": {"upstream": "https://gw.example.invalid", "dialect": "bogus"}},
	  "routes": [{"pattern": "claude-*", "provider": "p"}]
	}`)
	_, err := loadConfig(file)
	if err == nil || !strings.Contains(err.Error(), "p") {
		t.Fatalf("error = %v, want it to name the provider", err)
	}
}

func writeConfig(t *testing.T, body string) string {
	t.Helper()
	file := t.TempDir() + "/proxy.json"
	if err := os.WriteFile(file, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	return file
}

// --- request translation ---

// The rule this guards is that effort is never DERIVED: the upstream's vocabulary is not
// Anthropic's, and litellm's mistake was translating thinking into reasoning_effort. A
// value the caller stated is forwarded; thinking still yields nothing.
func TestTranslateRequestEffortComesOnlyFromTheCaller(t *testing.T) {
	body := `{
	  "model": "claude-opus-5",
	  "max_tokens": 4096,
	  "thinking": {"type": "enabled", "budget_tokens": 2048},
	  "messages": [{"role": "user", "content": "hi"}]
	}`
	// thinking alone states no effort, so with no configured default nothing is emitted:
	// derivation is what this guards, not a configured fallback.
	out, err := translateAnthropicRequest([]byte(body), "oss-model", "")
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"reasoning_effort", `"thinking"`, `"effort"`, "output_config"} {
		if strings.Contains(string(out), forbidden) {
			t.Errorf("translated body leaked %s: %s", forbidden, out)
		}
	}
}

func TestTranslateRequestForwardsCallerEffort(t *testing.T) {
	body := `{
	  "model": "claude-opus-5",
	  "max_tokens": 4096,
	  "output_config": {"effort": "high"},
	  "messages": [{"role": "user", "content": "hi"}]
	}`
	// A configured default must not displace what the caller stated: /effort would
	// otherwise be a no-op wherever a route sets one.
	out, err := translateAnthropicRequest([]byte(body), "oss-model", "low")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"reasoning_effort":"high"`) {
		t.Errorf("caller effort was not forwarded: %s", out)
	}
}

func TestTranslateRequestEffortDefaultFillsSilence(t *testing.T) {
	// The haiku shape: output_config present but carrying only format, so no effort.
	body := `{
	  "model": "claude-haiku-4-5",
	  "max_tokens": 4096,
	  "output_config": {"format": {"type": "json_schema"}},
	  "messages": [{"role": "user", "content": "hi"}]
	}`
	out, err := translateAnthropicRequest([]byte(body), "oss-model", "low")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `"reasoning_effort":"low"`) {
		t.Errorf("configured default was not applied: %s", out)
	}
	// output_config itself still must not reach the upstream.
	if strings.Contains(string(out), "output_config") {
		t.Errorf("translated body leaked output_config: %s", out)
	}
}

func TestTranslateRequestSystemForms(t *testing.T) {
	asString, err := translateAnthropicRequest([]byte(
		`{"model":"m","system":"be terse","messages":[{"role":"user","content":"hi"}]}`), "oss", "")
	if err != nil {
		t.Fatal(err)
	}
	asBlocks, err := translateAnthropicRequest([]byte(
		`{"model":"m","system":[{"type":"text","text":"be terse"}],"messages":[{"role":"user","content":"hi"}]}`), "oss", "")
	if err != nil {
		t.Fatal(err)
	}
	var a, b struct {
		Messages []map[string]any `json:"messages"`
	}
	if json.Unmarshal(asString, &a) != nil || json.Unmarshal(asBlocks, &b) != nil {
		t.Fatal("output was not valid JSON")
	}
	if a.Messages[0]["content"] != "be terse" || b.Messages[0]["content"] != "be terse" {
		t.Fatalf("system forms differ: %v vs %v", a.Messages[0], b.Messages[0])
	}
}

func TestTranslateRequestToolUseAndToolResult(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"assistant","content":[{"type":"tool_use","id":"toolu_1","name":"Bash","input":{"command":"ls"}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":"a.txt"}]}
	]}`
	out, err := translateAnthropicRequest([]byte(body), "oss", "")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Messages []struct {
			Role       string `json:"role"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID       string `json:"id"`
				Function struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Messages) != 2 {
		t.Fatalf("messages = %d, want 2", len(decoded.Messages))
	}
	call := decoded.Messages[0].ToolCalls[0]
	if call.Function.Arguments != `{"command":"ls"}` {
		t.Fatalf("arguments = %q, want a JSON string", call.Function.Arguments)
	}
	if decoded.Messages[1].Role != "tool" || decoded.Messages[1].ToolCallID != "toolu_1" {
		t.Fatalf("tool result = %+v", decoded.Messages[1])
	}
}

func TestTranslateRequestToolResultsPrecedeUserText(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"out"},{"type":"text","text":"carry on"}]}
	]}`
	out, _ := translateAnthropicRequest([]byte(body), "oss", "")
	var decoded struct {
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if json.Unmarshal(out, &decoded) != nil {
		t.Fatal("invalid JSON")
	}
	if len(decoded.Messages) != 2 || decoded.Messages[0].Role != "tool" || decoded.Messages[1].Role != "user" {
		t.Fatalf("roles = %+v, want tool then user", decoded.Messages)
	}
}

func TestTranslateRequestDropsEchoedThinkingBlocks(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"assistant","content":[
	    {"type":"thinking","thinking":"fabricated","signature":"s"},
	    {"type":"redacted_thinking","data":"x"},
	    {"type":"text","text":"real"}]}
	]}`
	out, _ := translateAnthropicRequest([]byte(body), "oss", "")
	if strings.Contains(string(out), "fabricated") || strings.Contains(string(out), "redacted") {
		t.Fatalf("thinking blocks were forwarded: %s", out)
	}
}

func TestTranslateRequestToolsAndToolChoice(t *testing.T) {
	body := `{"model":"m","tool_choice":{"type":"any"},
	  "tools":[{"name":"Bash","description":"run","input_schema":{"type":"object"}},
	           {"type":"web_search_20260209"}],
	  "messages":[{"role":"user","content":"hi"}]}`
	out, _ := translateAnthropicRequest([]byte(body), "oss", "")
	var decoded struct {
		Tools      []map[string]any `json:"tools"`
		ToolChoice any              `json:"tool_choice"`
	}
	if json.Unmarshal(out, &decoded) != nil {
		t.Fatal("invalid JSON")
	}
	if len(decoded.Tools) != 1 {
		t.Fatalf("tools = %d, want the server tool dropped", len(decoded.Tools))
	}
	if decoded.ToolChoice != "required" {
		t.Fatalf("tool_choice = %v, want required", decoded.ToolChoice)
	}
}

func TestTranslateRequestDropsUnsupportedFields(t *testing.T) {
	body := `{"model":"m","max_tokens":10,"top_k":40,"metadata":{"user_id":"u"},
	  "mcp_servers":[{"url":"x"}],"temperature":0.5,"messages":[{"role":"user","content":"hi"}]}`
	out, _ := translateAnthropicRequest([]byte(body), "oss", "")
	text := string(out)
	for _, field := range []string{"top_k", "metadata", "mcp_servers", "user_id"} {
		if strings.Contains(text, field) {
			t.Errorf("leaked %s: %s", field, text)
		}
	}
	// An explicit 0 temperature must survive the round trip.
	if !strings.Contains(text, `"temperature":0.5`) {
		t.Errorf("temperature was not forwarded: %s", text)
	}
}

func TestTranslateRequestImageBecomesDataURL(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[
	  {"type":"image","source":{"type":"base64","media_type":"image/png","data":"QUJD"}},
	  {"type":"text","text":"what is this"}]}]}`
	out, _ := translateAnthropicRequest([]byte(body), "oss", "")
	if !strings.Contains(string(out), "data:image/png;base64,QUJD") {
		t.Fatalf("image was not converted: %s", out)
	}
}

func TestTranslateRequestMergesAdjacentSameRoleMessages(t *testing.T) {
	body := `{"model":"m","messages":[
	  {"role":"user","content":"one"},{"role":"user","content":"two"}]}`
	out, _ := translateAnthropicRequest([]byte(body), "oss", "")
	var decoded struct {
		Messages []map[string]any `json:"messages"`
	}
	if json.Unmarshal(out, &decoded) != nil {
		t.Fatal("invalid JSON")
	}
	if len(decoded.Messages) != 1 {
		t.Fatalf("messages = %d, want them merged", len(decoded.Messages))
	}
}

// The API versioning policy allows new optional request fields with no version bump, so
// an unrecognized field must be ignored rather than rejected.
func TestTranslateRequestIgnoresUnknownFields(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],
	  "some_future_field":{"nested":true},"context_management":{"edits":[]}}`
	if _, err := translateAnthropicRequest([]byte(body), "oss", ""); err != nil {
		t.Fatalf("an unknown field broke the translation: %v", err)
	}
}

// --- response translation ---

func TestTranslateResponseEnvelope(t *testing.T) {
	body := `{"id":"gen_123","model":"oss-model","choices":[{"finish_reason":"stop",
	  "message":{"content":"hello"}}],"usage":{"prompt_tokens":11,"completion_tokens":4}}`
	out, err := translateOpenAIResponse([]byte(body), "claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		ID         string `json:"id"`
		Type       string `json:"type"`
		Model      string `json:"model"`
		StopReason string `json:"stop_reason"`
		Usage      struct {
			Input         int `json:"input_tokens"`
			Output        int `json:"output_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Content []map[string]any `json:"content"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	// The caller must see the model it asked for, not the routing target.
	if decoded.Model != "claude-opus-5" {
		t.Fatalf("model = %q, want the requested id", decoded.Model)
	}
	if decoded.Type != "message" || decoded.StopReason != "end_turn" {
		t.Fatalf("envelope = %s", out)
	}
	if decoded.Usage.Input != 11 || decoded.Usage.Output != 4 {
		t.Fatalf("usage = %+v", decoded.Usage)
	}
	if decoded.Usage.CacheCreation != 0 || decoded.Usage.CacheRead != 0 {
		t.Fatalf("cache = %d/%d, want zero when the upstream reported no hits",
			decoded.Usage.CacheCreation, decoded.Usage.CacheRead)
	}
	if decoded.Content[0]["type"] != "text" {
		t.Fatalf("content = %+v", decoded.Content)
	}
}

// A reported cache hit is split out of input_tokens, so the fields still sum to
// the upstream's prompt_tokens.
func TestTranslateResponseSplitsCacheReadTokens(t *testing.T) {
	body := `{"id":"gen_1","model":"oss-model","choices":[{"finish_reason":"stop",
	  "message":{"content":"hi"}}],"usage":{"prompt_tokens":1000,"completion_tokens":9,
	  "prompt_tokens_details":{"cached_tokens":768}}}`
	out, err := translateOpenAIResponse([]byte(body), "claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Usage struct {
			Input         int `json:"input_tokens"`
			Output        int `json:"output_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Usage.CacheRead != 768 {
		t.Fatalf("cache_read_input_tokens = %d, want 768", decoded.Usage.CacheRead)
	}
	if decoded.Usage.CacheCreation != 0 {
		t.Fatalf("cache_creation_input_tokens = %d, want 0", decoded.Usage.CacheCreation)
	}
	if total := decoded.Usage.Input + decoded.Usage.CacheCreation + decoded.Usage.CacheRead; total != 1000 {
		t.Fatalf("input+cache = %d, want the upstream's prompt_tokens 1000", total)
	}
	if decoded.Usage.Output != 9 {
		t.Fatalf("output_tokens = %d, want 9", decoded.Usage.Output)
	}
}

// A response with no usage object at all still carries every key, as an integer:
// a client dividing by their sum must not receive a missing field.
func TestTranslateResponseWithoutUsageKeepsEveryKey(t *testing.T) {
	body := `{"id":"gen_2","model":"oss-model","choices":[{"finish_reason":"stop",
	  "message":{"content":"hi"}}]}`
	out, err := translateOpenAIResponse([]byte(body), "claude-opus-5")
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Usage map[string]any `json:"usage"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"input_tokens", "output_tokens", "cache_creation_input_tokens", "cache_read_input_tokens"} {
		value, ok := decoded.Usage[key]
		if !ok {
			t.Fatalf("usage is missing %s: %+v", key, decoded.Usage)
		}
		if _, ok := value.(float64); !ok {
			t.Fatalf("%s = %v, want a number", key, value)
		}
	}
}

func TestTranslateResponseReasoningBecomesThinkingBlock(t *testing.T) {
	body := `{"id":"g","choices":[{"finish_reason":"stop","message":{
	  "reasoning":"I thought about it","reasoning_details":[{"type":"reasoning.text","text":"I thought about it"}],
	  "content":"answer"}}]}`
	out, _ := translateOpenAIResponse([]byte(body), "claude-opus-5")
	var decoded struct {
		Content []map[string]any `json:"content"`
	}
	if json.Unmarshal(out, &decoded) != nil {
		t.Fatal("invalid JSON")
	}
	if len(decoded.Content) != 2 {
		t.Fatalf("content blocks = %d, want thinking then text", len(decoded.Content))
	}
	if decoded.Content[0]["type"] != "thinking" {
		t.Fatalf("first block = %+v, want thinking", decoded.Content[0])
	}
	// The upstream sends reasoning and reasoning_details with identical text; reading
	// both would double every token.
	if decoded.Content[0]["thinking"] != "I thought about it" {
		t.Fatalf("thinking = %v, want it counted once", decoded.Content[0]["thinking"])
	}
	if decoded.Content[1]["type"] != "text" {
		t.Fatalf("second block = %+v", decoded.Content[1])
	}
}

func TestTranslateResponseToolCallsBecomeToolUseBlocks(t *testing.T) {
	body := `{"id":"g","choices":[{"finish_reason":"tool_calls","message":{"tool_calls":[
	  {"id":"call_1","function":{"name":"Bash","arguments":"{\"command\":\"ls\"}"}}]}}]}`
	out, _ := translateOpenAIResponse([]byte(body), "claude-opus-5")
	var decoded struct {
		StopReason string           `json:"stop_reason"`
		Content    []map[string]any `json:"content"`
	}
	if json.Unmarshal(out, &decoded) != nil {
		t.Fatal("invalid JSON")
	}
	if decoded.StopReason != "tool_use" {
		t.Fatalf("stop_reason = %q", decoded.StopReason)
	}
	input, ok := decoded.Content[0]["input"].(map[string]any)
	if !ok || input["command"] != "ls" {
		// input must be an object; Anthropic rejects a JSON string here.
		t.Fatalf("input = %#v, want a decoded object", decoded.Content[0]["input"])
	}
}

func TestTranslateResponseMalformedToolArgumentsFallBackToEmptyObject(t *testing.T) {
	body := `{"id":"g","choices":[{"message":{"tool_calls":[
	  {"id":"c","function":{"name":"Bash","arguments":"{not json"}}]}}]}`
	out, err := translateOpenAIResponse([]byte(body), "m")
	if err != nil {
		t.Fatalf("a malformed argument string failed the whole response: %v", err)
	}
	if !strings.Contains(string(out), `"input":{}`) {
		t.Fatalf("input did not degrade to an empty object: %s", out)
	}
}

// --- count tokens ---

func TestProxyCountTokensAnsweredLocally(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("count_tokens reached the upstream; it must be answered locally")
	}))
	defer upstream.Close()
	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{"p": openAITestProvider(t, upstream, nil)},
			routes:    []loadedRoute{testRoute("claude-*", "p")},
		},
		client: upstream.Client(),
	}
	request := httptest.NewRequest(http.MethodPost, "http://proxy.test/v1/messages/count_tokens",
		strings.NewReader(`{"model":"claude-opus-5","messages":[{"role":"user","content":"hello there"}]}`))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var decoded struct {
		InputTokens int `json:"input_tokens"`
	}
	if json.Unmarshal(response.Body.Bytes(), &decoded) != nil {
		t.Fatalf("body = %s", response.Body.String())
	}
	if decoded.InputTokens <= 0 {
		t.Fatalf("input_tokens = %d, want a positive estimate", decoded.InputTokens)
	}
}

func TestCountTokensEstimateGrowsWithInput(t *testing.T) {
	small, _ := json.Marshal(map[string]any{
		"model":    "m",
		"messages": []any{map[string]any{"role": "user", "content": "hi"}},
	})
	large, _ := json.Marshal(map[string]any{
		"model":    "m",
		"messages": []any{map[string]any{"role": "user", "content": strings.Repeat("word ", 400)}},
	})
	if estimateRequestTokens(small) >= estimateRequestTokens(large) {
		t.Fatal("estimate did not grow with input")
	}
}

// The init template and the example config are maintained by hand, so this pins them
// together. TestProxyInitWritesTemplate separately proves the template is valid.
func TestProxyInitTemplateMatchesExample(t *testing.T) {
	example, err := os.ReadFile("../../examples/proxy.json")
	if err != nil {
		t.Skipf("example config unavailable: %v", err)
	}
	if string(example) != proxyInitTemplate {
		t.Fatal("proxyInitTemplate and examples/proxy.json have drifted apart")
	}
}

// The template must load, and its translating provider must actually resolve to the
// translating dialect rather than silently staying on the default.
func TestProxyInitTemplateResolvesTranslatingProvider(t *testing.T) {
	file := writeConfig(t, proxyInitTemplate)
	cfg, err := loadConfig(file)
	if err != nil {
		t.Fatalf("template failed validation: %v", err)
	}
	plan, ok := cfg.planFor("deepseek-anything")
	if !ok {
		t.Fatal("template route did not match")
	}
	if plan.dialect != dialectOpenAIChat {
		t.Fatalf("dialect = %q, want openai-chat from the provider", plan.dialect)
	}
	if plan.targetModel != "oss-model" {
		t.Fatalf("target = %q, want default_model", plan.targetModel)
	}
	// The pass-through provider must be unaffected.
	pass, _ := cfg.planFor("claude-opus-4-1")
	if pass.dialect != dialectAnthropic {
		t.Fatalf("pass-through dialect = %q", pass.dialect)
	}
}

// --- resolve endpoint ---

// The statusline reads .target and strips the provider prefix, so this pins the exact
// contract it depends on. A change to the JSON shape silently breaks the model name in
// every user's prompt line, which is why it is asserted rather than assumed.
func TestProxyResolveReportsTargetForStatusline(t *testing.T) {
	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{
				"provider2": {dialect: dialectOpenAIChat, defaultModel: "oss-model"},
			},
			routes: []loadedRoute{testRoute("claude-*", "provider2")},
		},
		client: http.DefaultClient,
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "http://proxy.test/ccbunshin/resolve?model=claude-opus-5", nil))

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	var decoded struct {
		Requested string `json:"requested"`
		Target    string `json:"target"`
		Dialect   string `json:"dialect"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("body = %s", response.Body.String())
	}
	if decoded.Requested != "claude-opus-5" {
		t.Fatalf("requested = %q", decoded.Requested)
	}
	// The statusline strips everything before the last slash, so the target must keep
	// the provider prefix rather than being pre-trimmed here.
	if decoded.Target != "oss-model" {
		t.Fatalf("target = %q, want the full upstream id with its prefix", decoded.Target)
	}
	if decoded.Dialect != string(dialectOpenAIChat) {
		t.Fatalf("dialect = %q", decoded.Dialect)
	}
}

func TestProxyResolveReportsUnroutedModel(t *testing.T) {
	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{"p": {dialect: dialectOpenAIChat, defaultModel: "m"}},
			routes:    []loadedRoute{testRoute("claude-*", "p")},
		},
		client: http.DefaultClient,
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "http://proxy.test/ccbunshin/resolve?model=gpt-4o", nil))
	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 for an unrouted model", response.Code)
	}
}

// Without ?model= the route table is returned, so a client can discover routes without
// reading the config file.
func TestProxyResolveListsRoutes(t *testing.T) {
	handler := &proxy{
		config: loadedConfig{
			providers: map[string]loadedProvider{
				"provider2": {dialect: dialectOpenAIChat, defaultModel: "oss-model"},
			},
			routes: []loadedRoute{testRoute("claude-*", "provider2")},
		},
		client: http.DefaultClient,
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response,
		httptest.NewRequest(http.MethodGet, "http://proxy.test/ccbunshin/resolve", nil))

	var decoded struct {
		Routes []struct {
			Pattern      string `json:"pattern"`
			Provider     string `json:"provider"`
			DefaultModel string `json:"default_model"`
		} `json:"routes"`
	}
	if json.Unmarshal(response.Body.Bytes(), &decoded) != nil {
		t.Fatalf("body = %s", response.Body.String())
	}
	if len(decoded.Routes) != 1 || decoded.Routes[0].Pattern != "claude-*" {
		t.Fatalf("routes = %+v", decoded.Routes)
	}
	if decoded.Routes[0].DefaultModel != "oss-model" {
		t.Fatalf("default_model = %q", decoded.Routes[0].DefaultModel)
	}
}
