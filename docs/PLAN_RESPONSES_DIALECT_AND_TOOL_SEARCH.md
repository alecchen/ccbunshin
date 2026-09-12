# Plan: a Responses dialect, and tool search

Status: planned, not implemented. Two independent pieces of future work, kept in one file because
they share a trigger: an upstream that serves some models only on an OpenAI-style Responses API, and
a client that defers tool definitions until they are searched for.

- **Part A** - a third dialect value, `openai-responses`, so a provider can route to a gateway that
  speaks `/v1/responses` instead of `/chat/completions`.
- **Part B** - tool search (`ENABLE_TOOL_SEARCH`): what the wire format is, what today's
  translation does with it, and why pass-through is the only correct answer.

Part A is a feature. Part B is mostly a record of what already works and what to re-check, because
the safest implementation of tool search is the one that does nothing.

Host-specific endpoint maps, with the model lists and which dialect each model needs, live
separately: `docs/OPENCODE_ZEN.md` (the host that prompted this work) and `docs/OPENCODE_GO.md`.

## 1. Context

`docs/DECISIONS.md` decision 8 makes the dialect a property of a model rather than a provider, and
expresses a mixed gateway as two routes in different namespaces. That architecture was built for a
gateway serving two protocols. It still holds for one serving three - which is what Part A adds -
and the host referenced in `docs/OPENCODE_ZEN.md` is the case: one upstream URL, `claude-*` models
on `/v1/messages` needing nothing, chat models on `/v1/chat/completions` needing `openai-chat`, and
a large `gpt-*` / `muse-spark-*` / `grok-*` group reachable only on `/v1/responses`.

That is the shape the `model_dialects` map was designed for: the dialect is keyed on the model, so a
single provider can mix all three without a second provider or a second route namespace.

### 1.1 A shape that does not fit `dialect`

One group on that host is out of reach for a reason worth recording, because it is not a missing
dialect value. Seven `gemini-*` models are served at `/zen/v1/models/<model-id>` with
`@ai-sdk/google` - a path that varies per model, carrying Google's native `generateContent` shape.

A dialect here maps a model to a *wire format* and appends a fixed suffix to the provider upstream.
A per-model path with a third body format does not fit that: it would need a path template on the
provider, which is a different mechanism from `dialect`. Deliberately out of scope (section 9), not
an oversight.

## 2. Pass-through must not be disturbed

The `anthropic` dialect is the cheapest win on any gateway: a provider pointed at it with the
default dialect routes its `/v1/messages` models byte-for-byte, `cache_control` breakpoints included.
Those are the models that keep prompt caching intact, and on the host in `docs/OPENCODE_ZEN.md` they
are the Claude models - the ones a user most wants a cache hit on. Part A adds a dialect; it must
leave this path exactly as it is.

## 3. Part A - the `openai-responses` dialect

### 3.1 Verified upstream facts

From OpenAI's migration guide and streaming reference (2026-09-13):

- Endpoint is `POST /v1/responses`. `input` accepts a string or a list of message-like items;
  `instructions` carries the system-level guidance.
- Tools are **flat**, not nested: `{"type": "function", "name": ..., "description": ...,
  "parameters": ...}`. The chat-completions `{"type":"function","function":{...}}` wrapper is gone.
  `strict` now *defaults to on* when omitted (falls back if the schema is incompatible), so a
  translated request should set `strict: false` explicitly to keep chat-completions behavior.
- Tool calls and their results are correlated by `call_id`, not by a tool-call id in the message.
- The response has a typed `output` array instead of `choices`. Item types include `message`
  (with `output_text` content parts), `function_call`, `function_call_output`, and `reasoning`.
- **Stateful by default.** `store: false` opts out. ZDR organizations get `store: false` enforced.
- Streaming is typed SSE: `response.created`, `response.output_text.delta`, `response.completed`,
  `error`, plus `response.function_call_arguments.delta` / `.done` for tool calls, and
  `response.output_item.added` / `.done`.
- Usage carries `input_tokens`, `output_tokens`, and `input_tokens_details.cached_tokens` - the
  same split decision 9 already handles, which is why `anthropicUsageFromUpstream` should generalize
  rather than be duplicated.

To confirm at implementation time, rather than assumed here: the exact reasoning-delta event name
(the streaming page did not list one), whether the token cap field is `max_output_tokens`, and what
the gateway does under `store: false`.

### 3.2 Request translation

A new `translateAnthropicRequestForResponses` beside the existing one, or the existing function
gaining a shape parameter. Mapping:

| Anthropic | Responses |
|---|---|
| `system` | `instructions` |
| `messages[].content` text blocks | `input` items with `input_text` |
| `tool_use` block (assistant) | `function_call` item, `call_id` carried |
| `tool_result` block (user) | `function_call_output` item |
| `tools[]` | flat function objects, `strict: false` |
| `max_tokens` | `max_output_tokens` (confirm the name) |
| `stream` | `stream` |
| - | `store: false`, always |

`store: false` is not optional. This repo forwards a caller's credential but never stores it
(decision 4); silently making a third party retain the conversation would contradict the spirit of
that, and for Grok 4.6 the ZDR note on the same page says the stateful Responses API is disabled
anyway. Setting it explicitly is the only behavior that is correct for both.

The `thinking`-never-forwarded rule (decision 8) and the effort-forwarding rule both carry over
unchanged: `reasoning` is not a request knob this translation sets, and a caller-stated effort still
has no Responses equivalent unless the gateway documents one.

### 3.3 Response translation

Non-streaming builds the same Anthropic envelope `translateOpenAIResponse` does today, so the two
share a shape: walk `output`, emit `reasoning` as a thinking block (last, per the existing
"thinking first" ordering rule), `message` items' `output_text` as text, `function_call` items as
`tool_use`. `stop_reason` comes from the response's `status`/`incomplete_details` rather than a
`finish_reason`.

Streaming needs a second translator alongside `streamTranslator`. The state machine is simpler in
one way (typed events, so no `[DONE]` sentinel and no finish_reason parsing) and harder in another
(function call arguments arrive as a stream of deltas keyed by `item_id`, and the item must be closed
before the next opens). The existing invariants still apply: block ordering is fixed at thinking
then text then tool_use, a signature is synthesized on the thinking block, and the trailing
`message_delta` carries the upstream's usage when it arrives.

### 3.4 Code surface

- `dialect.go` - add `dialectOpenAIResponses`, extend `parseDialect`.
- `main.go` - `validateDialectTargets` tests `== dialectOpenAIChat` twice (lines 191 and 193) and
  names it in the error message at 203; generalize to "this dialect needs a model id the upstream
  will accept", so a Responses route without a mapping is rejected at load for the same reason a
  chat route is.
- `main.go` - `ServeHTTP`'s `count_tokens` branch (252) and the dialect dispatch (260) both test
  `== dialectOpenAIChat`; both need the new value. Where a translated provider is routed,
  `/v1/messages/count_tokens` is still answered locally.
- `main.go` - `planDialectFor` and `routeTableJSON` need no change beyond being dialect-agnostic.
- `translate.go`, `stream.go` - the new translation functions.
- `README.md`, `cmd/ccbunshin/README.md` - the dialect list becomes three values, and the recorded
  lesson that "a typo silently means pass-through" needs revisiting: with three values, a typo is
  more likely and more costly.

### 3.5 Config

One provider, three dialects, expressed with `model_dialects` keyed on the upstream model id, which
is exactly what decision 8 built it for. The host-specific version with real model ids is in
`docs/OPENCODE_ZEN.md`; the shape is:

```json
{
  "provider2": {
    "upstream": "https://gateway.example.invalid",
    "models": { "claude-opus-5": "gpt-5.6-luna" },
    "model_dialects": {
      "gpt-5.6-luna": "openai-responses",
      "glm-5.3": "openai-chat"
    }
  }
}
```

Models not named in `model_dialects` fall through to the provider default, so the models on
`/v1/messages` stay on `anthropic` with no entry at all. That is what keeps prompt caching intact
for them (section 2).

## 4. Part B - tool search

### 4.1 What the client does

Claude Code's MCP tool search is on by default: when MCP tool descriptions exceed 10% of the
context window, they are deferred and discovered through the `MCPSearch` tool instead of loaded up
front. `auto:N` sets that threshold; `alwaysLoad` on an MCP server opts a whole server out;
adding `MCPSearch` to `disallowedTools` disables the feature.

Behind a custom `ANTHROPIC_BASE_URL` the behavior has changed more than once, which is why this
needs watching rather than assuming:

- 2.1.72 made tool search activate behind `ANTHROPIC_BASE_URL` **as long as `ENABLE_TOOL_SEARCH` is
  set** - so against the proxy it is opt-in, not automatic.
- A later release fixed 400s against third-party gateways by having tool search detect a proxy
  endpoint and disable `tool_reference` blocks itself.
- On Vertex AI it is off by default to avoid an unsupported beta-header error, opt-in via the same
  variable.

### 4.2 The wire format

Request side:

- A tool search tool in `tools`: `{"type": "tool_search_tool_regex_20251119", "name":
  "tool_search_tool_regex"}`, or the `_bm25_` variant. It must never carry `defer_loading`.
- Deferred tools keep their full definition and add `"defer_loading": true`. A deferred tool cannot
  also carry `cache_control` (the API 400s).
- Every tool definition is still sent on every request, including deferred ones - the upstream needs
  them server-side to expand references. `defer_loading` controls context, not payload.

Response and history side, which is the part that matters here:

- The response can contain `server_tool_use` (id `srvtoolu_...`), `tool_search_tool_result` (with a
  nested `tool_search_tool_search_result` holding `tool_references[]` of
  `{"type":"tool_reference","tool_name":...}`), then the ordinary `tool_use`.
- The assistant content must be passed back **unchanged**, including both search blocks, and a
  `tool_result` is never sent for an `srvtoolu_` id.

### 4.3 What the proxy does today, and why it is the right answer

- **`anthropic` dialect (pass-through):** nothing to do. The request and the history go upstream
  byte-for-byte, beta headers included, so a real Anthropic endpoint sees exactly what Claude Code
  sent. This is the only dialect where tool search is fully correct, and it is another argument for
  the pass-through default (decision 8).
- **`openai-chat` dialect:** `translateTools` builds each upstream tool fresh from `name`,
  `description`, and `input_schema`, so `defer_loading` is dropped and no non-standard field leaks
  upstream. The search tool itself has no `input_schema` and is dropped by the same function that
  drops server-side tools. Net effect: the upstream sees ordinary function tools and loads all of
  them up front. Tool search stops saving context, but nothing breaks and nothing malformed is sent.
  The dropped `server_tool_use` and `tool_search_tool_result` blocks from history have no
  chat-completions equivalent and are the only available choice.
- **`openai-responses` (Part A):** `defer_loading` again must not be forwarded, and the flat tool
  format must be built from the same three fields. Worth an explicit test, since the flat format is
  easier to leak extra keys into than the nested one.

So Part B requires **no new code**. What it requires is (a) a test pinning the current stripping
behavior so a future refactor of `translateTools` cannot start forwarding `defer_loading`, (b) a note
in the README about what tool search does and does not buy behind the proxy, and (c) re-checking the
changelog before each release, because this feature's proxy behavior has moved twice already.

### 4.4 Open question to verify, not assume

Whether an upstream rejects an assistant message whose search blocks were stripped, leaving a turn
where the model's recorded reasoning no longer explains the tool call it made. The translation is
lossy by necessity; whether it is *rejected* is an empirical question. Test with a real tool-search
session against a chat-completions provider before documenting it as safe.

## 5. Tests

Part A, alongside the existing dialect tests:

1. Request translation: `system` to `instructions`; text, `tool_use`, and `tool_result` blocks to
   `input` items with `call_id` correlation.
2. Tools are flat with `strict: false`; no `function` wrapper.
3. `store: false` is always present.
4. Non-streaming response: `output` items to thinking/text/tool_use blocks, in the fixed order.
5. Streaming: the typed events to the Anthropic SSE sequence, including function-call argument
   deltas and a usage report that moves `input_tokens` as well as `output_tokens`.
6. Usage: `input_tokens_details.cached_tokens` splits out into `cache_read_input_tokens` with the
   hit count subtracted from `input_tokens`, exactly as decision 9 requires for the chat dialect.
7. `validateDialectTargets` rejects an `openai-responses` route with no model mapping.

Part B:

8. `translateTools` drops a tool with no `input_schema` and never emits `defer_loading`,
   `cache_control`, or any key beyond `name`, `description`, `parameters`.
9. A history containing `server_tool_use` and `tool_search_tool_result` blocks translates without
   error and without emitting a `tool_result` for the `srvtoolu_` id.

## 6. Verification

The suite covers both parts, then end to end against a stub upstream that serves the three endpoint
paths, so the dialect-per-model resolution is exercised the way a real gateway would exercise it:

```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
```

For Part A, run the proxy with `CCBUNSHIN_LOG=debug` against the stub, send one request per model,
and confirm each lands on its own path with its own translation and that the per-request log line
names the expected dialect. For Part B, run a session with `ENABLE_TOOL_SEARCH` set and enough MCP
tools to trip the deferral threshold, and confirm from the debug log that the tool count reaching the
upstream is the full set (deferral is client-side and cannot be honored) and that no request is
rejected.

## 7. Risks

1. **Three dialects make a typo more likely, and a typo is silent.** Today a misspelled `dialect`
   falls back to pass-through. Adding a third value raises the cost of that behavior and is the
   reason section 3.4 flags it: consider rejecting an unknown value at load rather than normalizing
   it. That would be a breaking change for a config that relies on the fallback, so it is a decision,
   not a cleanup.
2. **`store: false` and provider behavior.** A gateway may ignore it, or may require it. Verify per
   gateway rather than assuming the field is honored.
3. **The Responses translation is a second full state machine.** `stream.go` is 387 lines for one
   dialect; a second one doubles the surface that must keep the block-ordering invariants. Share what
   can be shared (the block open/close helpers, the usage split) and accept the rest as duplication
   rather than building an abstraction over two implementations.
4. **Tool search behavior behind a proxy has changed twice in the changelog.** Anything documented
   in Part B is a snapshot. The durable statement is the mechanism (pass-through is exact;
   translation is lossy), not the current client behavior.
5. **Model lists move; endpoint shapes do not.** `docs/OPENCODE_ZEN.md` and `docs/OPENCODE_GO.md`
   carry dated snapshots. Re-read them against the live tables before relying on a specific model id,
   and do not copy model ids into config examples as if stable.

## 8. Sequencing

Part B first, because it is documentation plus existing-behavior tests and it de-risks Part A's tool
handling before the flat format makes stripping harder to reason about. Then Part A:

1. `dialect.go`: the new value, `parseDialect`, and generalizing `validateDialectTargets`. Tests.
2. Non-streaming request and response translation. Tests.
3. The streaming translator. Tests.
4. `main.go` dispatch, the `count_tokens` branch, and the debug log line. Tests.
5. Docs: both READMEs, decision 8 (which currently says "those are the only two values"), and the
   "Compatibility review" list in `cmd/ccbunshin/README.md`. `docs/OPENCODE_ZEN.md` and
   `docs/OPENCODE_GO.md` move their `/v1/responses` rows from "not implemented" to a dialect value.

## 9. Out of scope

- The per-model path shape (`/v1/models/<id>`, Google's `generateContent`) described in section 1.1.
  It needs a path template on the provider, which is a different feature from `dialect`, and a
  fourth body format this repo has no translation for.
- `/v1/models` translation, still unimplemented from the chat dialect's own out-of-scope list.
- The reverse direction: Responses or chat-completions input onto an Anthropic upstream.
- Honoring `defer_loading` upstream. It cannot be honored through a translating dialect, because the
  feature is server-side and the upstream running the search is not the one holding the catalog.
- Structured outputs (`response_format` to `text.format`) and the removal of `n`. Neither is used by
  Claude Code, and both would need a caller to ask for them.
