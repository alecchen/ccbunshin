# Plan: Anthropic to OpenAI-chat translation layer (`dialect`)

Status: proposal, not implemented.

## 1. Context

The current setup drives Claude Code through litellm purely as a format shim: Claude Code speaks
Anthropic `/v1/messages`, and the upstream model only accepts OpenAI `/v1/chat/completions`.
litellm is a large Python dependency and a second daemon in the request path, kept alive for that
conversion alone.

It also gets one param wrong for this upstream. litellm translates Anthropic `thinking` into a
`reasoning_effort` field that the upstream rejects:

```
Invalid option: expected one of "low"|"medium"|"high"|"xhigh"|"max"
param: reasoning_effort
```

That is patched today with `additional_drop_params: ["reasoning_effort"]` in the litellm config.

ccbunshin already contains a model-routed reverse proxy in Go: `proxy.ServeHTTP` at
`cmd/ccbunshin/main.go:119`. It has routing, streaming plumbing, a PID lifecycle, a systemd unit,
and a test harness. It forwards Anthropic-shaped requests byte-for-byte, which is right for an
Anthropic-dialect gateway and useless for an OpenAI-dialect one.

This change adds a per-provider `dialect` so a provider can declare `openai-chat`, plus the
translation that dialect implies. Outcome: ccbunshin replaces litellm for this deployment, litellm
and its `config.yaml` are dropped, and the `reasoning_effort` failure disappears structurally
because the proxy maps upstream `reasoning` back into real Anthropic thinking blocks instead of
deriving an effort tier.

## 2. Verified upstream facts

These were established by probing a live gateway, then genericized to the `free`/`paid` example
convention. The behaviors and error shapes are what the design depends on and are worth
re-verifying against any new upstream; the host, plan names, and model ids are not.

1. **The endpoint split is enforced per model, not per deployment.** The API exposes both
   `/chat/completions` (OpenAI dialect) and `/messages` (Anthropic dialect). A Claude id on the
   OpenAI path, or an OSS id on the Anthropic path, is refused, and the upstream names the correct
   endpoint in its error: the OpenAI path answers `Model "X" is not supported on this endpoint.
   Use /<base>/chat/completions for OpenAI and OSS models.`, and the Anthropic path answers
   `Model "X" must be called via /<base>/messages (Anthropic Messages shape).`
2. **The plan behind the probe included no Claude models.** Requests for Claude ids were refused
   with a plan-entitlement error, so Claude Code's model ids could only be remapped to an OSS
   model. Translation is mandatory in that configuration, not an optimisation. Verify entitlement
   separately per deployment: a higher tier would make the Anthropic-dialect path reachable and
   change what the routes should look like.
3. **`GET /v1/models` works** and returned a mix of `claude-*`, `gpt-*`, and prefixed OSS ids. The
   list does **not** label which dialect an id belongs to and carries no endpoint field, so it
   cannot be used to derive the mapping; see the classification method in the README.
4. **`POST /v1/messages/count_tokens` returns 404.**
5. **Streaming reasoning arrives natively**, as `choices[0].delta.reasoning` (string) plus
   `delta.reasoning_details` (array of `{type:"reasoning.text",text,format,index}`), distinct from
   `delta.content`. Both appeared on every streamed response, with identical text, which is why the
   translator reads one and ignores the other on any chunk carrying both.
6. **Streaming tool calls arrive as index-keyed fragments.** The first delta carries
   `{index, id, type, function:{name, arguments:""}}`; every later delta carries only
   `{index, function:{arguments:"<partial>"}}`. Parallel calls key off `delta.tool_calls[].index`.
7. **The final chunk carries `finish_reason` and `usage` together**, followed by a literal
   `data: [DONE]` line. `usage` has `prompt_tokens`, `completion_tokens`, and
   `completion_tokens_details.reasoning_tokens`. Usage was sent even without
   `stream_options.include_usage`, so the translator does not request it.
8. **Non-streaming responses** carry `choices[0].message.content`, `message.reasoning`, and
   `message.reasoning_details`.

## 3. Decisions taken

1. **Auth**: forward the caller's own credential. Use inbound `Authorization: Bearer ...` when
   present, otherwise convert inbound `x-api-key` into `Authorization: Bearer <value>` and drop
   `x-api-key`. No credential is read from or written to config, which keeps
   `docs/Claude Code Multi-Endpoint - Shared State Architecture Requirements.md:418`
   ("Authentication is not part of the proxy config") and CLAUDE.md decision 4 ("ccbunshin never
   reads or writes credentials") intact.
2. **`count_tokens`**: answered locally with a character-based estimate, but **only** when the
   routed provider's dialect is `openai-chat`. An `anthropic` dialect provider keeps forwarding
   `/v1/messages/count_tokens` upstream, so a deployment whose gateway implements it sees no
   behavior change.
3. **Thinking**: map upstream `reasoning` to real Anthropic thinking blocks, streaming and
   non-streaming.
4. **Route pattern**: `claude-*` (not `*`), which keeps non-Claude ids rejected per the
   requirements doc line 420 ("Unknown or missing models must be rejected rather than routed by
   guesswork").
5. **Scope**: Anthropic to OpenAI-chat only, one way. Anthropic-native providers keep
   byte-for-byte pass-through and must be provably unaffected.

## 4. Design

### 4.1 Config schema

One optional per-provider key, plus one companion key the wildcard route needs.

```go
type provider struct {
	Upstream     string            `json:"upstream"`
	Timeout      string            `json:"timeout,omitempty"`
	Models       map[string]string `json:"models,omitempty"`
	Dialect      string            `json:"dialect,omitempty"`       // new: "anthropic" (default) | "openai-chat"
	DefaultModel string            `json:"default_model,omitempty"` // new: rewrite target when `models` has no entry
}

type loadedProvider struct {
	upstream     *url.URL
	timeout      time.Duration
	models       map[string]string
	dialect      dialect
	defaultModel string
}
```

`dialect` must be optional-with-default. `proxyStart` (`main.go:717`) calls `loadConfig` and
refuses to start on an invalid config, so a required key would break every existing install,
including configs at `~/.config/ccbunshin/proxy.json` and `/etc/ccbunshin/proxy.json`.

```go
type dialect string

const (
	dialectAnthropic  dialect = "anthropic"   // default: byte-for-byte pass-through
	dialectOpenAIChat dialect = "openai-chat" // Anthropic /v1/messages -> OpenAI /chat/completions
)

// parseDialect maps an optional config value to a dialect. An empty value is the
// pass-through dialect so configs written before dialects existed keep working.
func parseDialect(providerName, value string) (dialect, error)
```

Error wording follows the existing `provider %q ...` idiom in `loadConfig`:

```go
return "", fmt.Errorf("provider %q dialect must be %q or %q", providerName, dialectAnthropic, dialectOpenAIChat)
```

`default_model` exists because `rewriteModel` (`main.go:204`) only rewrites exact keys in
`models`. A `claude-*` route with no per-id entry would forward `claude-future-9` verbatim and the
upstream would reject it. With `default_model` set, any routed model lacking an explicit `models`
entry is rewritten to it, so the wildcard route works for model ids that did not exist when the
config was written. `models` still wins when it has a key.

**Do not add `DisallowUnknownFields`.** A typo'd `"dialct"` would then silently mean pass-through
rather than erroring, which is a small footgun, but enabling strict unknown-field rejection would
break any user config carrying extra keys, which is the larger regression. The docs bullet in
section 6 is the mitigation.

Deployment config this targets, using the repo's `example.invalid` convention:

```json
{
  "port": 3456,
  "providers": {
    "provider2": {
      "upstream": "https://free-gateway.example.invalid/provider/v1",
      "dialect": "openai-chat",
      "default_model": "oss-model"
    }
  },
  "routes": [{ "pattern": "claude-*", "provider": "provider2" }]
}
```

`loadConfig` should additionally reject a provider whose dialect is `openai-chat` and which has
neither `models` nor `default_model`, since that configuration can only forward ids the upstream
will refuse.

### 4.2 Files

Keep the single-binary, zero-dependency property. `go.mod` has no `require` block and CLAUDE.md
states "dependency-free" as a project property, so stdlib only. `main.go` keeps the CLI, config,
routing, lifecycle, and the pass-through path.

| File | Contents | Rough size |
|---|---|---|
| `cmd/ccbunshin/dialect.go` | new. `dialect` type, `parseDialect`, header policy, translated-response header policy, translated-error writer | ~90 lines |
| `cmd/ccbunshin/translate.go` | new. Request and non-streaming response translation, token estimate | ~320 lines |
| `cmd/ccbunshin/stream.go` | new. The SSE state machine | ~360 lines |
| `cmd/ccbunshin/main.go` | modified. Dispatcher, `ServeHTTP` split, config wiring, template | ~+60/-25 |
| `cmd/ccbunshin/translate_test.go`, `cmd/ccbunshin/stream_test.go` | new tests | - |

`ServeHTTP` becomes a dispatcher with three arms: local endpoints (`/healthz`,
`/v1/messages/count_tokens`, `/model/info`), the existing pass-through path extracted verbatim
into `serveAnthropic`, and a new `serveOpenAIChat`.

```go
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request)
func (p *proxy) serveAnthropic(w http.ResponseWriter, r *http.Request, body []byte, provider loadedProvider, client *http.Client)
func (p *proxy) serveOpenAIChat(w http.ResponseWriter, r *http.Request, body []byte, requestedModel string, provider loadedProvider, client *http.Client)
func (p *proxy) serveCountTokens(w http.ResponseWriter, r *http.Request, body []byte)
func (p *proxy) serveModelInfo(w http.ResponseWriter, r *http.Request)
```

Extracting `serveAnthropic` as a pure move is deliberate: the eight existing proxy tests then
become the regression suite for the pass-through requirement without needing new tests for it.

Routing stays on the Anthropic-shaped body and happens **before** translation, so `requestModel`
and `providerFor` are untouched and the model the client asked for is what routes. The requested
model id is captured before `rewriteModel` and threaded into the response translation, so the
response echoes `claude-opus-5` rather than the upstream's `oss-model` id.

### 4.3 Request translation (Anthropic to OpenAI)

`translateAnthropicRequest(body []byte) ([]byte, error)` decodes into a typed
`anthropicRequest` and **builds a fresh** `openAIChatRequest`. It never mutates the inbound body.

Decoding into a typed struct is the load-bearing choice. It makes "these fields must never be
sent" a structural property rather than a denylist someone can forget to extend: a field the
struct does not declare cannot reach the upstream no matter what the client sends.

```go
type anthropicRequest struct {
	Model         string             `json:"model"`
	MaxTokens     int                `json:"max_tokens"`
	System        json.RawMessage    `json:"system"`   // string | []block
	Messages      []anthropicMessage `json:"messages"`
	Stream        bool               `json:"stream"`
	Temperature   *float64           `json:"temperature"`
	TopP          *float64           `json:"top_p"`
	StopSequences []string           `json:"stop_sequences"`
	Tools         []anthropicTool    `json:"tools"`
	ToolChoice    json.RawMessage    `json:"tool_choice"`
	// Deliberately absent: thinking, output_config, reasoning_effort, top_k, metadata,
	// betas, mcp_servers, container, service_tier, context_management, diagnostics.
}
```

Field mapping:

- `model`: the body's model, which is already the post-rewrite value.
- `system`: string form used directly; block-array form concatenates each `type:"text"` block's
  `text` with `"\n\n"`. Emitted as `messages[0] = {role:"system",content:<text>}` when non-empty.
- `messages[]`, string content: passed through unchanged.
- `messages[]`, block array: partition and emit in this order.
  1. `tool_result` blocks become `{role:"tool", tool_call_id, content:<stringified>}` each. Tool
     messages must directly follow the assistant turn that requested them, so they come first.
  2. `tool_use` blocks (assistant) collect into one assistant message's `tool_calls`.
  3. Text-like blocks become one user or assistant message with concatenated text, emitted after
     the tool messages.
- `tool_use` to `tool_calls[]` entry: `{id, type:"function", function:{name, arguments:<JSON
  string>}}`. `input` is marshalled to a string; an empty `input` becomes `"{}"`.
- `tool_result` content flattened to a string: text blocks joined, image blocks dropped.
  `is_error` has no OpenAI equivalent, so prepend `"Error: "` to the text so the model can see
  the failure rather than silently treating it as success.
- `image` (base64 source) to `{"type":"image_url","image_url":{"url":"data:<media_type>;base64,<data>"}}`.
  URL source passes the URL through. The message's `content` becomes a parts array only when an
  image is present.
- `thinking` and `redacted_thinking` blocks in history are **dropped entirely**, including their
  `signature`. They are our own synthesized blocks being echoed back, and forwarding fabricated
  reasoning to the model is worse than dropping it.
- `tools[]`: `{name, description, input_schema}` to `{type:"function", function:{name,
  description, parameters:<input_schema verbatim>}}`. Dropped: `cache_control`, `strict`,
  `defer_loading`, `allowed_callers`, and anything with no `input_schema` (server tools), which
  has no OpenAI equivalent.
- `tool_choice`: `auto` to `"auto"`, `any` to `"required"`, `none` to `"none"`, `tool` to
  `{type:"function",function:{name}}`. `disable_parallel_tool_use` dropped.
- Scalars: `max_tokens` to `max_tokens` (not `max_completion_tokens`; this is a vLLM-flavoured
  endpoint). `temperature` and `top_p` forwarded as pointers so an explicit `0` survives.
  `stop_sequences` to `stop`. `stream` forwarded. When streaming, add
  `stream_options: {"include_usage": true}` so the final chunk carries usage (see risk 3).
- Adjacent same-role messages with string content are merged with `"\n\n"`, skipped when either
  side carries `tool_calls` or is a `tool` message. Cheap, and it prevents a vLLM-style upstream
  from rejecting non-alternating roles. Empty messages are never emitted.
- Unknown input fields are ignored by construction. This matters: the API versioning policy (see
  section 9) permits new optional request fields with no version bump.

**Never sent, and why it matters:** `reasoning_effort` under no circumstance. `thinking:
{type:"enabled",budget_tokens:N}`, `thinking: {type:"disabled"}`, `thinking: {type:"adaptive"}`,
and `output_config.effort` all produce **no** `reasoning_effort` field. The upstream rejects
`none` and `minimal` outright and is inconsistent about the rest, and litellm's adapter maps
Anthropic `thinking` to `reasoning_effort`, which is exactly the bug being avoided. Reasoning is
recovered on the response side instead. `top_k` is also dropped: it is not in the OpenAI chat
schema and a strict server would 400 on it.

### 4.4 Response translation (non-streaming)

Input is an OpenAI `chat.completion`; output is an Anthropic message envelope.

```json
{
  "id": "msg_<suffix from the upstream id, or synthesized>",
  "type": "message",
  "role": "assistant",
  "model": "<the model the client asked for>",
  "content": [ ...blocks... ],
  "stop_reason": "end_turn",
  "stop_sequence": null,
  "usage": {"input_tokens": 123, "output_tokens": 45}
}
```

Block order is fixed and matters: **thinking, then text, then tool_use.** A thinking block after a
text block would be rejected by an Anthropic-shaped client.

1. Non-empty `message.reasoning` (or the concatenated text of `reasoning_details`) becomes
   `{"type":"thinking","thinking":<text>,"signature":<synthetic constant>}`.
2. Non-empty `message.content` becomes `{"type":"text","text":<content>}`.
3. Each `tool_calls[i]` becomes `{"type":"tool_use","id":<id or synthesized>,"name":
   <function.name>,"input":<decoded arguments>}`. `function.arguments` is a JSON string that must
   be decoded into an object. If it does not parse, emit `"input": {}` and log rather than failing
   the whole response, and never emit `input` as a string, which Anthropic clients reject.

`stop_reason` mapping: `stop` to `end_turn`, `length` to `max_tokens`, `tool_calls` or
`function_call` to `tool_use`, `content_filter` to `refusal`, `null` or unknown to `end_turn`.
`stop_sequence` is always `null`, since the upstream does not report which sequence fired.

`usage`: `prompt_tokens` to `input_tokens`, `completion_tokens` to `output_tokens`. Reasoning
tokens are already included in `completion_tokens`, and Anthropic counts thinking in
`output_tokens` too, so no adjustment. Cache hits are reported from
`prompt_tokens_details.cached_tokens` as `cache_read_input_tokens`, subtracted out of
`input_tokens` the way Anthropic reports them; `cache_creation_input_tokens` has no upstream
counterpart and stays `0`. See `PLAN_CACHE_USAGE_PASSTHROUGH.md`.

**Errors must be translated, not forwarded.** Claude Code's error classifier keys on the Anthropic
envelope, so a raw OpenAI error body would be misread. A non-2xx upstream response is converted to
`{"type":"error","error":{"type":"<mapped>","message":"<upstream message>"}}` at the **same** HTTP
status, with `invalid_request_error` for 400 and 422 and `rate_limit_error` for 429 (otherwise
`api_error`). The raw OpenAI body must not appear in the client response.

### 4.5 Streaming state machine

#### Reading: `bufio.Reader`, not a per-`Write` translator

Do not implement this as an `io.Writer` fed by `io.Copy`. `io.Copy` hands over arbitrary slices,
so a `Write` method would have to own leftover buffering, split on `\n\n` itself, and flush after
every fragment.

Pull instead:

```go
func (s *streamTranslator) run(body io.Reader) error {
	reader := bufio.NewReaderSize(body, 64*1024)
	// process line even when err != nil: the final line may lack a trailing newline
	line, err := reader.ReadString('\n')
	...
}
```

`bufio.Reader.ReadString('\n')` reassembles across TCP chunk boundaries and, unlike
`bufio.Scanner`, has no maximum token size. That matters: a large tool-call argument fragment or a
server that batches many deltas into one line can exceed `Scanner`'s 64 KiB `MaxScanTokenSize`,
and `Scan` would then fail with `ErrTooLong` mid-stream, after `WriteHeader`, where the only
recourse is an in-band error event. If `Scanner` is preferred for length, it must be given
`scanner.Buffer(make([]byte, 0, 64*1024), 4<<20)` and still handle `ErrTooLong`.

Per-line rules, handling both `\n` and `\r\n`:

- Blank line: dispatch the accumulated data (multiple `data:` lines joined with `"\n"`), reset.
- `data: ` prefix: strip and append.
- `:` comment, `event:`, `id:`, `retry:`: ignored. OpenAI's stream sets no meaningful `event:`.
- Payload `[DONE]`: stop reading, then finish.
- More than a sanity limit (say 4 MiB for one event): protocol error, emit an error event rather
  than growing without bound.

#### Emitted event sequence

Written as `event: <name>\ndata: <json>\n\n`, flushed after each event.

```
event: message_start
data: {"type":"message_start","message":{"id":"msg_x","type":"message","role":"assistant",
       "model":"claude-opus-5","content":[],"stop_reason":null,"stop_sequence":null,
       "usage":{"input_tokens":N,"output_tokens":0}}}

-- only if reasoning appeared
event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":""}}
event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"..."}}
event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"<synthetic>"}}
event: content_block_stop
data: {"type":"content_block_stop","index":0}

-- only if text appeared
event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}
event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"..."}}
event: content_block_stop
data: {"type":"content_block_stop","index":1}

-- one per tool call, in first-appearance order
event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"Bash","input":{}}}
event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"command"}}
event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},
       "usage":{"output_tokens":M,"input_tokens":N}}
event: message_stop
data: {"type":"message_stop"}
```

`message_start` is emitted lazily, on the first successfully received chunk, not before
`client.Do` returns. That keeps a pre-stream upstream failure convertible into a real HTTP status.

#### States and transitions

```go
type streamState int

const (
	stateStart streamState = iota // message_start emitted, no block open
	stateThinking
	stateText
	stateTool
	stateDone
)
```

`closeBlock()` is the only place blocks close:

| Current state | `closeBlock()` emits | Next state |
|---|---|---|
| `stateThinking` | `signature_delta` then `content_block_stop` | `stateStart` |
| `stateText` | `content_block_stop` | `stateStart` |
| `stateTool` | `content_block_stop` | `stateStart` |
| `stateStart` | nothing | `stateStart` |

Per incoming chunk, apply deltas in the upstream's own order: `reasoning`, `reasoning_details`,
`content`, `tool_calls`.

| Event | Current state | Action |
|---|---|---|
| non-empty `delta.reasoning` | `stateStart` | open `thinking` block at `nextIndex++`, then `thinking_delta` |
| | `stateThinking` | `thinking_delta` |
| | `stateText` / `stateTool` | drop and log once: late reasoning cannot be ordered before an open block without violating Anthropic block ordering |
| `delta.reasoning_details` | as above | only when `delta.reasoning` was empty on this chunk, using the concatenated `text` of entries |
| non-empty `delta.content` | `stateThinking` | `closeBlock()`, open `text` at `nextIndex++`, then `text_delta` |
| | `stateStart` | open `text`, then `text_delta` |
| | `stateText` | `text_delta` |
| | `stateTool` | ignore: do not reopen a text block after tool calls began |
| `delta.tool_calls[i]` | index not yet seen | `closeBlock()`, buffer the fragment, open `tool_use` at `nextIndex++` once `function.name` is known, flush any buffered arguments as `input_json_delta` |
| | already open | append `function.arguments` fragment as `input_json_delta` with `partial_json` equal to the raw fragment |
| `delta.function_call` (legacy) | - | treat as tool index 0 |
| `choices[0].finish_reason != null` | - | record only, do not emit yet |
| `usage != null` | - | record `prompt_tokens` and `completion_tokens`; this chunk typically has no choices |

Index assignment: one monotonic `nextIndex`, incremented only in `content_block_start`. A
`map[int]int` from the upstream's `tool_calls[].index` to the Anthropic index, because tool calls
arrive index-keyed and out of band with block order. A missing `index` on a fragment means `0`.

Tool-call accumulation: the upstream is not guaranteed to put `id` and `name` on the first
fragment. The accumulator holds `{id, name, arguments, bufferedUntilOpen, anthropicIndex}`. If an
`arguments` fragment arrives before `name` is known it is appended to `bufferedUntilOpen` and
flushed as one `input_json_delta` when the block opens. Cheap, and it removes a class of ordering
bugs. Synthesize an id (`toolu_<n>`) when the upstream omits one.

Never emit a delta whose payload is empty. An empty `text_delta` inside an open `thinking` block
is a hard error for strict Anthropic clients.

Termination: on `[DONE]` or clean EOF, `closeBlock()`, then `message_delta`, then `message_stop`,
then flush. `message_stop` carries no usage.

Usage: `message_start.usage.input_tokens` is the local estimate (the same function `count_tokens`
uses, applied to the translated request body), because nothing else is available before the first
token. `message_delta.usage` carries the upstream's `completion_tokens` as `output_tokens` when a
usage chunk arrived, otherwise a local estimate of the emitted text. Including `input_tokens` in
`message_delta` is a small judgment call: Anthropic's own stream sends it, and it lets a client
correct the initial estimate. Dropping it is the conservative alternative.

Errors after commit: `w.WriteHeader` has already run, so the status cannot change and
`http.Error` would corrupt the SSE body. Emit `event: error` with
`{"type":"error","error":{"type":"api_error","message":...}}` and return. No `message_stop`
follows an `error` event. If nothing has been emitted yet (`started == false`), the caller may
instead use the pre-stream error path with a real HTTP status; the two paths are mutually
exclusive by exactly that flag.

### 4.6 Headers

Request, `openai-chat` dialect only. Build into a fresh header set:

- `Authorization`: forwarded when non-empty; otherwise `x-api-key` becomes
  `Authorization: Bearer <value>`; otherwise absent.
- Dropped: `x-api-key`, `anthropic-version`, `anthropic-beta`,
  `anthropic-dangerous-direct-browser-access`, `Host`, `Content-Length`, `Accept-Encoding`.
- Set: `Content-Type: application/json`.
- Credential values are never logged. The proxy log line for a request should name only the model,
  provider, and dialect.

Response, translated paths only. Never `copyHeaders`:

- Non-stream: `Content-Type: application/json`.
- Stream: `Content-Type: text/event-stream`, `Cache-Control: no-cache`,
  `X-Accel-Buffering: no`.
- Never `Content-Length`, `Content-Encoding`, or `Transfer-Encoding`. The body no longer matches
  what the upstream described, and Go's server sets chunked framing itself once `Content-Length`
  is absent. Copying an upstream `Content-Encoding: gzip` while writing plaintext SSE is the
  specific failure this prevents.

`/healthz` and the `anthropic` dialect pass-through keep the existing `copyHeaders` behavior
unchanged.

### 4.7 Paths and routes

```go
func (p *proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/healthz" { /* unchanged */ }

	body, err := io.ReadAll(r.Body)   // unchanged
	model, err := requestModel(body)  // unchanged

	if r.URL.Path == "/model/info" {
		p.serveModelInfo(w, r)
		return
	}

	provider, ok := p.config.providerFor(model)
	if !ok {
		http.Error(w, "no provider route for model", http.StatusBadRequest)
		return
	}
	if r.URL.Path == "/v1/messages/count_tokens" && provider.dialect == dialectOpenAIChat {
		p.serveCountTokens(w, r, body)
		return
	}

	client := p.client
	if provider.timeout > 0 {
		client = &http.Client{Timeout: provider.timeout}
	}

	requestedModel := model
	body = rewriteModel(body, provider.models)

	if provider.dialect == dialectOpenAIChat {
		p.serveOpenAIChat(w, r, body, requestedModel, provider, client)
		return
	}
	p.serveAnthropic(w, r, body, provider, client)
}
```

Path mapping for `serveOpenAIChat`:

```go
target := *provider.upstream
target.Path = strings.TrimRight(provider.upstream.Path, "/") + "/chat/completions"
target.RawQuery = "" // ?beta=true is Anthropic-specific and meaningless on this endpoint
```

So `POST /v1/messages` becomes `<base>/chat/completions`. The query is dropped rather than
forwarded, since a strict gateway may reject unknown params.

`/v1/messages/count_tokens`, `openai-chat` dialect: answered locally as `{"input_tokens": N}` at
HTTP 200, `application/json`. The estimate is `ceil(total_chars / 4)` over system text, message
text, `tool_result` text, `tool_use.input` JSON, and tool names and descriptions, plus a small
per-message constant. It is rough by construction; the docs should say so rather than imply
accuracy. Any non-2xx upstream error would otherwise 404 here, which Claude Code hits at startup.

`/model/info`: Claude Code probes it at startup and litellm answers it. It carries no `model`
field, so `providerFor` cannot dispatch on it and it must be answered at the proxy level.
Synthesize a LiteLLM-shaped list from `routes` and each provider's `models` and `default_model`.
**Uncertain:** it is not known what Claude Code requires beyond a JSON array it can enumerate. The
safe fallback is a 404, which Claude Code tolerates by using its built-in model list. Implement the
synthesized response, verify against a real Claude Code start, and downgrade to 404 if it
misbehaves. This is the item most likely to need empirical adjustment (risk 7).

`GET /v1/models`: forwarded unchanged. It carries no `model` field so it cannot be
dialect-dispatched, and nothing on the Claude Code request path depends on its shape. Translating
the OpenAI list into an Anthropic list is a possible follow-up.

## 5. Tests

Existing idiom: one behavior per test, a short comment naming the regression, `httptest`
throughout, `testProvider` at `main_test.go:18` building a `loadedProvider` from an
`httptest.Server`. `httptest.NewRecorder` implements `http.Flusher`, so the streaming path takes
the flusher branch under test without extra scaffolding.

Helpers to add: `testProviderDialect` (with `testProvider` delegating so existing call sites are
untouched), `anthropicTestProvider`, `parseSSE`, `eventNames`, `eventText`, and a test reader that
returns one byte per `Read`.

Config and routing:

1. `TestLoadConfigDialectDefaultsToAnthropic` - a config with no `dialect` loads as pass-through.
   Guards the back-compat promise.
2. `TestLoadConfigAcceptsOpenAIChatDialect`.
3. `TestLoadConfigRejectsUnknownDialect` - assert the error names the provider, in the existing
   wording idiom.
4. `TestLoadConfigRejectsOpenAIChatWithoutModelMapping` - a provider with neither `models` nor
   `default_model` is rejected.
5. `TestProxyInitTemplateIsValid` - extend `TestProxyInitWritesTemplate` to assert the template's
   provider loads with the expected dialect, so a template edit that drops the key is caught.
6. `TestProxyTranslatesMessagesPathForOpenAIChat` - upstream asserts path `/chat/completions` and
   an empty query.
7. `TestProxyAnthropicDialectKeepsMessagesPath` - the pass-through provider still gets
   `/v1/messages` with its query intact. Guards the extraction into `serveAnthropic`.

Auth and headers:

8. `TestProxyOpenAIChatForwardsAuthorization` - inbound `Authorization: Bearer sk-x` reaches the
   upstream unchanged, `x-api-key` absent.
9. `TestProxyOpenAIChatConvertsAPIKeyHeader` - inbound `x-api-key` alone becomes
   `Authorization: Bearer <value>`.
10. `TestProxyOpenAIChatDropsAnthropicHeaders` - `anthropic-version`, `anthropic-beta`, and
    `x-api-key` absent upstream; `Content-Type: application/json` present.
11. `TestProxyTranslatedResponseDropsUpstreamContentHeaders` - upstream sets a lying
    `Content-Length` and `Content-Encoding: gzip`; assert the client response has neither.

Request translation:

12. `TestTranslateRequestSystemForms` - string and block-array `system` produce the same
    `messages[0]`.
13. `TestTranslateRequestNeverEmitsReasoningEffort` - input carrying `thinking:{type:"enabled",
    budget_tokens:2048}` and `output_config.effort`; assert the output contains no
    `reasoning_effort`, no `thinking`, and no `effort`. This is the load-bearing regression.
14. `TestTranslateRequestDropsEchoedThinkingBlocks` - an assistant message with `thinking` and
    `redacted_thinking` blocks plus text; only the text survives.
15. `TestTranslateRequestToolUseAndToolResult` - `tool_use` becomes `tool_calls[0]` with
    `arguments` as a JSON string; `tool_result` becomes `{role:"tool",tool_call_id}`.
16. `TestTranslateRequestToolResultsPrecedeUserText` - a user message with `[text, tool_result]`
    emits the `tool` message before the `user` message.
17. `TestTranslateRequestToolsAndToolChoice` - `input_schema` to `parameters`; `any` to
    `"required"`; `tool` to a function object; a server tool with no `input_schema` is dropped.
18. `TestTranslateRequestDropsUnsupportedFields` - `top_k`, `metadata`, `betas`, `mcp_servers`,
    `output_config` all absent from the output.
19. `TestTranslateRequestImageBecomesDataURL`.
20. `TestTranslateRequestMergesAdjacentSameRoleMessages`.
21. `TestTranslateRequestIgnoresUnknownFields` - an unrecognized top-level field is ignored, not
    rejected. Guards the forward-compatibility requirement from section 9.

Non-streaming response:

22. `TestTranslateResponseEnvelope` - `stop_reason`, `stop_sequence: null`, usage, and the echoed
    requested model rather than the upstream id.
23. `TestTranslateResponseReasoningBecomesThinkingBlock` - reasoning lands as `content[0].type ==
    "thinking"` with a non-empty signature, text as `content[1]`.
24. `TestTranslateResponseToolCallsBecomeToolUseBlocks` - `input` is a decoded object, not a
    string.
25. `TestTranslateResponseMalformedToolArgumentsFallBackToEmptyObject`.
26. `TestProxyOpenAIChatNonStreamingEndToEnd` - full `ServeHTTP` round trip; status,
    `Content-Type`, and body shape.
27. `TestProxyOpenAIChatTranslatesUpstreamError` - upstream 400 with an OpenAI error body; client
    sees 400 and an Anthropic `{"type":"error",...}` envelope, and the raw OpenAI body does not
    appear.

Streaming:

28. `TestStreamReasoningThenTextOrdering` - assert the exact ordered event names
    `[message_start, content_block_start, content_block_delta(thinking),
    content_block_delta(signature), content_block_stop, content_block_start,
    content_block_delta(text), content_block_stop, message_delta, message_stop]` and that the
    thinking block's index is 0 and the text block's is 1. This is the "thinking closes before
    text opens" guard.
29. `TestStreamSplitsAcrossTCPBoundaries` - the same payload fed one byte per `Read`; assert
    byte-identical output to the unsplit case. Direct regression for the buffering choice.
30. `TestProxyOpenAIChatStreamsToRecorder` - end-to-end through `ServeHTTP` against a handler that
    writes SSE in small pieces with a flush between them; assert `text/event-stream`, exactly one
    `message_start` and one `message_stop`, and the expected concatenated text.
31. `TestStreamAccumulatesToolCallFragments` - `name` on chunk 1, `arguments` split across chunks
    2 to 4, `id` only on chunk 1; one `tool_use` block whose `input_json_delta` parts concatenate
    to the full JSON, at the correct Anthropic index.
32. `TestStreamToolArgumentsBeforeName` - an `arguments` fragment arriving before `function.name`;
    the buffered fragment is flushed at block open with order preserved.
33. `TestStreamReasoningDetailsFallback` - reasoning present only in `reasoning_details`.
34. `TestStreamDoesNotDoubleCountReasoning` - a chunk carrying both `reasoning` and an identical
    `reasoning_details` entry; the text appears exactly once.
35. `TestStreamUsageAndStopReason` - the final usage chunk maps to
    `message_delta.usage.output_tokens`; `finish_reason:"tool_calls"` maps to
    `stop_reason:"tool_use"`.
36. `TestStreamEmptyUpstream` - `[DONE]` immediately; `message_start`, `message_delta` with
    `end_turn`, `message_stop`, no panic, no content blocks.
37. `TestStreamMidStreamErrorEmitsErrorEvent` - the reader errors after `message_start`; assert an
    `error` event, no following `message_stop`, and that the status stays 200 with no second
    `WriteHeader`.
38. `TestProxyAnthropicDialectStreamIsByteIdentical` - an `anthropic` dialect provider forwards an
    SSE payload byte for byte.

Count tokens and routing:

39. `TestProxyCountTokensAnsweredLocallyForOpenAIChat` - the upstream handler calls `t.Error` if
    invoked; assert 200 and `{"input_tokens": N}` with `N > 0`.
40. `TestProxyCountTokensForwardsForAnthropicDialect` - the upstream sees
    `/v1/messages/count_tokens`.
41. `TestCountTokensEstimateGrowsWithInput` - table-driven monotonicity.
42. `TestProxyOpenAIChatStillRejectsUnroutedModel` - a model outside `claude-*` returns 400.
    Guards requirements doc line 420 under the new dialect.
43. `TestProxyInitTemplateMatchesExample` - compare `proxyInitTemplate` with
    `examples/proxy.json`. Cheap drift guard, mildly path-fragile.

**Prerequisite before writing tests 33 and 34:** capture one real upstream stream with `curl -N`
and a `stream: true` request, save it as a fixture, and write those cases against the actual
payload rather than a reconstruction. The `reasoning` versus `reasoning_details` interplay is the
highest-uncertainty part of the design and should not be guessed at.

## 6. Docs to update

- `README.md` - add a "Translation (`dialect`)" subsection after the routing paragraph at
  183-185; add `dialect` and `default_model` bullets to the `proxy.json` keys list at 187-191,
  including values, default, and optionality; one sentence that the inbound credential is
  forwarded and never configured.
- `cmd/ccbunshin/README.md` - the same two bullets in its duplicate keys list at 118-122; a
  "Translating providers" paragraph after line 116 covering reasoning to thinking blocks, tool
  translation, `count_tokens` being answered locally with an estimate, and the explicit statement
  that `reasoning_effort` is never sent. Note that a typo in `dialect` silently means pass-through.
- `docs/CCBUNSHIN_IMPLEMENTATION.md` section 10 (185-193) - a paragraph on the dialect option,
  that translation is one-way, and why reasoning is recovered on the response side instead of
  requested via `reasoning_effort`.
- `examples/proxy.json` - add `"dialect": "openai-chat"` to a provider, in the same change as the
  identical edit to `proxyInitTemplate`.
- `CLAUDE.md` - add a decision: translation is opt-in per provider via `dialect`, default
  pass-through; `reasoning_effort` is never emitted; credentials are forwarded, never stored.
- `docs/Claude Code Multi-Endpoint - Shared State Architecture Requirements.md` - append an
  "implemented as" note to section 11 rather than rewriting the requirement, stating that a
  provider may declare a translation dialect and that lines 418 and 420 still hold under it.

## 7. Verification

```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
sh tests/shell-integration.sh
sh tests/install-test.sh
```

End-to-end against the real upstream, using a scratch config and port so the running proxy is
untouched:

```sh
mkdir -p /tmp/ccbunshin-e2e
go -C cmd/ccbunshin build -o /tmp/ccbunshin-e2e/ccbunshin ./
CCBUNSHIN_PROXY_CONFIG=/tmp/ccbunshin-e2e/proxy.json \
CCBUNSHIN_PROXY_BIN=/tmp/ccbunshin-e2e/ccbunshin \
  ccbunshin proxy start
```

Then point a Claude Code profile's `ANTHROPIC_BASE_URL` at the proxy and confirm, in order:

1. A plain turn answers.
2. A tool call round-trips, meaning `tool_use` to `tool_result` to the next turn all work. This is
   the check that exercises the streaming accumulator against the real upstream.
3. Streaming shows thinking.
4. Startup does not error, which covers `/model/info` and `count_tokens`.

Acceptance: with litellm stopped and `ANTHROPIC_BASE_URL` pointed at the ccbunshin proxy, a real
Claude Code session completes a tool-using turn, and no `reasoning_effort` reaches the upstream.

## 8. Risks, ranked

1. **Reasoning delta shape (`reasoning` versus `reasoning_details`).** If a chunk carries both
   with the same text, a naive implementation emits the thinking text twice; if the upstream ever
   sends reasoning only in `reasoning_details`, a `reasoning`-only implementation silently drops
   all thinking. Mitigation: prefer `reasoning` and ignore `reasoning_details` on any chunk where
   `reasoning` is non-empty (test 34), support the fallback (test 33), and capture a real stream
   as a fixture first.
2. **Streaming state machine correctness.** Interleaved thinking, text, and tool deltas with index
   assignment is where this breaks subtly. Mitigation: chunk-split invariance (test 29) plus tests
   28, 31, 32, and a real captured session.
3. **`stream_options: {"include_usage": true}` unsupported.** If the upstream 400s on it, every
   streaming request fails, which is the whole product. Mitigation: if `client.Do` returns 400 or
   422 and the body mentions `stream_options`, retry once without it; always carry a local
   output-token estimate so `message_delta.usage` is populated regardless. Must land in the same
   change as the streaming path, not as a follow-up.
4. **Synthetic thinking signature.** The upstream has no equivalent, so the signature is
   fabricated. It is structurally required by the Anthropic block shape and is stripped on the
   inbound path, so it never reaches a real Anthropic API through this proxy. It would be rejected
   if such a transcript were sent directly to Anthropic. Mitigation: one documented constant, a
   comment naming the constraint, and a docs line that this dialect is for OpenAI-compatible
   upstreams only.
5. **Tool-call fragment accumulation.** Fragments can carry `arguments` before `name`, omit
   `index`, or use the legacy `function_call` shape with no id. Mitigation: the buffered-until-open
   accumulator and synthesized ids; tests 31 and 32.
6. **Claude Code request-shape drift.** litellm absorbs client variation; a hand-written mapper
   does not. Mitigation: the versioning policy in section 9 makes ignoring unknown fields a design
   requirement, the pass-through dialect remains available, and section 9 is the standing review
   process.
7. **`/model/info` expectation.** The required shape is unknown; litellm answers it, which suggests
   Claude Code reads it when present. Mitigation: synthesize the LiteLLM shape, verify against a
   real Claude Code start before declaring done, and keep a 404 downgrade as a one-line fallback.
8. **Mid-stream failure after `WriteHeader`.** The status is committed, so an error can only be
   signalled in band. Mitigation: emit an Anthropic `error` event and return, never `http.Error`,
   with the `started` flag keeping the two error paths mutually exclusive. Test 37.
9. **Response header leakage.** A blind `copyHeaders` on a translated response would advertise the
   upstream's `Content-Length` and `Content-Encoding` for a body we rewrote. Mitigation: fresh
   response headers on translated paths only; test 11.
10. **`max_tokens` mismatch.** Claude Code requests very large `max_tokens` for Opus-class ids;
    the OSS upstream may cap lower and reject. Mitigation: pass through and surface the upstream
    error legibly via the error translation in section 4.4. A per-provider clamp is the obvious
    follow-up if it bites; adding config surface speculatively is worse than waiting for the error.
11. **Prompt-cache semantics are partially lost.** `cache_control` breakpoints cannot be expressed
    to an OpenAI-chat upstream, but such upstreams cache a repeated prefix automatically, so turns
    are not cold prefills. Resolved: the hit count is reported as `cache_read_input_tokens`. See
    `PLAN_CACHE_USAGE_PASSTHROUGH.md`.
12. **Config typo silently means pass-through**, since there is no `DisallowUnknownFields`. An
    accepted tradeoff; the docs bullet is the mitigation.
13. **Modest scope creep in `ServeHTTP`.** The branch is small, but the pass-through path must not
    change. Mitigation: tests 7 and 38.

## 9. Sequencing

1. `dialect` config, validation, and tests 1 to 5. Independently shippable, no behavior change.
2. Extract `serveAnthropic` and add the dispatcher. Pure refactor; the existing tests are the
   gate.
3. Request translation, tests 12 to 21.
4. Non-streaming response translation and error translation, tests 22 to 27.
5. Capture a real upstream stream, then the streaming state machine, tests 28 to 38.
6. `count_tokens` and `/model/info`, tests 39 to 42.
7. Template, example, and docs.

Steps 1 and 2 can land before the translation exists at all, which keeps each diff reviewable.

## 10. Compatibility review

`platform.claude.com/docs/en/api/versioning` is the page that answers "how do I know what
changed". Its stated guarantee is load-bearing for this design:

> For any given version with the Messages API, Anthropic preserves: Existing input parameters,
> Existing output parameters. However, Anthropic may do the following: Add additional optional
> inputs / Add additional values to the output / Change conditions for specific error types / Add
> new variants to enum-like output values (for example, streaming event types).

and "Generally, if you are using the API as documented in this reference, Anthropic will not break
your usage."

The practical reading for this translation layer: **new Anthropic request fields can appear with
no version bump, and new streaming event types can appear.** Both are additive, so the mapper must
ignore unknown input fields (guaranteed by the typed decode, test 21) and ignore unknown SSE event
types rather than erroring. That is a design requirement, not only a review item.

Add a short "Compatibility review" section to `cmd/ccbunshin/README.md` listing what to re-check
before a release:

- Anthropic API release notes - https://platform.claude.com/docs/en/release-notes/api
- Claude Code release notes - https://platform.claude.com/docs/en/release-notes/claude-code
- Claude Code changelog, raw and diffable -
  https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md
- Messages and headers reference - https://platform.claude.com/docs/en/api/messages and
  https://platform.claude.com/docs/en/api/beta-headers
- Streaming event reference - https://platform.claude.com/docs/en/build-with-claude/streaming
- API versioning policy - https://platform.claude.com/docs/en/api/versioning

All of these were verified to resolve. The Claude Code changelog is worth watching specifically
because it documents env vars and endpoint behavior that affect a gateway (it currently mentions
`CLAUDE_CODE_GATEWAY_MODEL_DISCOVERY_TIMEOUT_MS` for gateway `/v1/models` discovery, which is why
`/v1/models` is noted above as a follow-up).

## 11. Out of scope

- `/v1/models` response translation
- The reverse direction (OpenAI to Anthropic) and any provider other than OpenAI-chat
- Retries, fallbacks, and spend accounting, which litellm provided and this does not
- Prompt caching (`cache_control`) actually reaching the upstream. Automatic prefix caching is a
  different mechanism and its hits are now surfaced; see `PLAN_CACHE_USAGE_PASSTHROUGH.md`.
- Removing litellm from the machine; this change makes that safe, it does not do it
