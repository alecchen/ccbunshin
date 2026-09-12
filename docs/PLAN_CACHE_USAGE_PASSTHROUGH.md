# Plan: pass prompt-cache usage through to the client

Status: implemented. Kept as the design record: section 2 holds the verified upstream facts the
usage split rests on, which `docs/DECISIONS.md` decision 9 summarizes but does not reproduce. Where
it disagrees with the code, the code and decision 9 win.

## 1. Context

Claude Code's statusline renders a `cache:` segment from the session payload's
`.context_window.current_usage`: `cache_read_input_tokens / (input_tokens +
cache_creation_input_tokens + cache_read_input_tokens)`. In the current deployment that segment
reads `cache: 0%` on every redraw and never moves.

The counters are not Claude Code's to compute. It echoes what the API reported, so a translator
that hardcodes them to zero makes the segment structurally incapable of showing anything else.
ccbunshin does exactly that: `cmd/ccbunshin/stream.go:229` emits

```go
"usage": map[string]any{
    "input_tokens":                s.inputTokens,
    "output_tokens":               0,
    "cache_creation_input_tokens": 0,
    "cache_read_input_tokens":     0,
},
```

and `openAIUsage` (`translate.go:360`) has no field to carry a hit count even if one arrived.

`cmd/ccbunshin/README.md:140` states the rationale: "Prompt-cache breakpoints (`cache_control`)
are dropped, since chat-completions has no equivalent. Every turn is a cold prefill." The first
clause is correct and the second is not. The upstream caches prefixes automatically without any
`cache_control` marker, so the miss is on our side of the wire, not the upstream's.

The live deployment confirms ccbunshin is the source: Claude Code reaches the ccbunshin daemon
through one intermediate proxy, and no other component in the chain rewrites `usage`. The
statusline reads the very field this plan fills (`statusline_commandcode.sh:103`:
`jq '.cache_read_input_tokens // 0'`), so a non-zero count is sufficient to move the segment.

## 2. Verified upstream facts

Established by probing the upstream's `/v1/chat/completions` with the deployment's own key
(2026-09-12). These are what the design depends on.

1. **The upstream reports cache hits on both paths.** Non-streaming and streaming responses carry
   `usage.prompt_tokens_details.cached_tokens`. `usage.cache_creation_input_tokens` is present and
   always `0`.
2. **No `cache_control` is needed.** The probe sent no breakpoints. A repeated stable prefix hits
   the cache anyway, so this is automatic prefix caching keyed on content, not on an explicit
   marker.
3. **The hit is attributable to the caller's own prior request.** With a fresh UUID nonce embedded
   in the prefix, so no other traffic could have populated the entry:

   ```
   call 1 (cold) : prompt_tokens 2698, cached 0
   call 2 (warm) : prompt_tokens 2698, cached 2560
   call 3 (warm) : prompt_tokens 2698, cached 2560
   ```

   A prefix previously warmed by unrelated traffic also hits, which is the mechanism in (2).
4. **Streaming carries usage without opt-in.** The final chunk includes full `usage` with or
   without `stream_options: {include_usage: true}`. Risk 3 of `PLAN_OPENAI_CHAT_DIALECT.md:750`
   (the upstream 400ing on that field) does not apply, and ccbunshin needs to send neither.
5. **`input_tokens` is exclusive of cached tokens**, per Anthropic's definition: a reported
   `cache_read_input_tokens` is not also counted in `input_tokens`. The two together reconcile to
   the upstream's `prompt_tokens`. litellm's adapter splits the same way at
   `adapters/transformation.py:1414`:

   ```python
   input_tokens = max(usage.prompt_tokens - cache_read - cache_creation, 0)
   ```

   The `max` is load-bearing, not defensive: a provider that reports hits *inclusively* would
   otherwise yield a negative `input_tokens`, which Claude Code renders as a negative context
   figure rather than failing.

## 3. Design

### 3.1 Widen `openAIUsage` (`translate.go:360`)

```go
type openAIUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
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
```

Deepseek's flat `prompt_cache_hit_tokens` spelling is deliberately **not** read. This deployment's
upstream uses the nested name; a second field for a provider swap that is not planned is untested
code. A provider that spells it differently reports 0, which section 6 step 1 catches before any
translator debugging.

### 3.2 A shared usage split

Both response paths need the same subtraction, so it is one helper rather than two copies.

```go
// anthropicUsageFromUpstream converts an upstream usage report to Anthropic's fields.
// Anthropic reports cache reads separately from input_tokens, so the hit count is
// subtracted out; the clamp keeps a provider that counts hits inclusively from yielding a
// negative input count. cache_creation_input_tokens is always 0 - the upstream has no
// equivalent concept - and is emitted rather than omitted because a client doing
// cache_read / (input + cache_creation + cache_read) treats a missing key differently
// from a zero. A nil report is all zeros.
func anthropicUsageFromUpstream(usage *openAIUsage) map[string]any
```

All four keys are always present and integral. Omitting the cache keys on a cold turn was
considered and rejected: the statusline divides by their sum, and the four-key shape is the one
every client of this proxy has seen since the dialect landed.

### 3.3 Non-streaming response (`translate.go:458`)

Replace the two-field assignment with the helper. Today the map is built as
`{"input_tokens": 0, "output_tokens": 0}` at line 430 and only the two fields are overwritten.

### 3.4 Streaming response (`stream.go`)

The cache fields currently sit in the `message_start` map at line 229, which is written before any
upstream chunk has arrived, so they can only assert a zero there. Real values can only come from
the final usage-bearing chunk, so they are corrected in the `message_delta` at line 261, which
Claude Code merges into its running usage total.

`message_start` is left exactly as it is - not because the zero is knowable, but because it is
corrected one event later and it is the shape every client of this proxy has already seen. The
change is confined to the tail:

```go
// The estimate stands unless the upstream reported usage, which supersedes it.
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
	"type":  "message_delta",
	"delta": map[string]any{"stop_reason": s.stopReason, "stop_sequence": nil},
	"usage": usage,
})
```

### 3.5 Prefer the upstream's token count over the estimate (`main.go:244`)

`plan.inputTokens = estimateRequestTokens(body)` is currently the only source for `input_tokens`.
`estimateRequestTokens` is a 4-characters-per-token heuristic with a comment explaining why
(`translate.go:477`: the upstream 404s on `count_tokens` and Claude Code wants a number at
startup). An exact count reported by the upstream supersedes it.

Rule: `input_tokens` starts at the estimate in `message_start` and is corrected by the upstream's
`prompt_tokens - cached` in `message_delta` when usage arrives. The estimate remains the fallback
for the no-usage case, so no path loses the number it has today.

Note this changes an asserted behavior. `stream_test.go:357` asserts `input_tokens == 42` (the
estimate) against a fixture reporting `prompt_tokens: 7`. See 4.

### 3.6 Why the split matters beyond the cache percentage

The statusline's denominator is the sum of all three input fields
(`statusline_commandcode.sh:102`). Subtracting the cached count out of `input_tokens` is what keeps
that sum equal to the upstream's `prompt_tokens`; without it the cached prefix is counted twice and
the `ctx` bar overstates context use on every warm turn. The reconciliation is the property that
matters, not the percentage.

### 3.7 What this does not do

No cache writes. Anthropic's `cache_creation_input_tokens` has no upstream counterpart, so it stays
`0` and a cold turn renders as `0%` rather than negative or undefined. The statusline's
denominator still reconciles to `prompt_tokens`, which is the property that matters.

## 4. Tests

| Location | Change |
|---|---|
| `stream_test.go:357` | Fixture is `{"prompt_tokens":7,"completion_tokens":99}`; the assertion at line 357 expects the local estimate `42`. Under 3.5 it becomes `7`. Rename to state the intent (upstream count wins when reported) and add a second case with no usage chunk asserting the estimate path at 42, so the fallback stays covered. |
| `stream_test.go` | New case: final chunk carrying `prompt_tokens_details.cached_tokens`, asserting `input_tokens + cache_read == prompt_tokens`, and that all four usage keys appear in `message_delta`. |
| `stream_test.go` | New case: a provider reporting hits inclusively (`cached > prompt_tokens`) yields `input_tokens == 0`, not a negative count. |
| `translate_test.go:506,532` | Fixture `prompt_tokens: 11` with no details block; assertion `Input == 11` passes unchanged under the new rule. Add a details-bearing sibling asserting the split and `cache_creation_input_tokens == 0`. |
| `translate_test.go` | New case: an absent `usage` object emits all four keys as zero rather than omitting the cache keys. |

## 5. Docs to update

| File | Line | Now says | Should say |
|---|---|---|---|
| `cmd/ccbunshin/README.md` | 140 | "Prompt-cache breakpoints (`cache_control`) are dropped ... Every turn is a cold prefill." | Breakpoints are dropped, but the upstream caches prefixes automatically, so repeated prefixes do hit; the hit count is reported as `cache_read_input_tokens`. |
| `docs/PLAN_OPENAI_CHAT_DIALECT.md` | 307 | "Cache token fields are omitted as unknowable." | Reported from `prompt_tokens_details.cached_tokens`. |
| `docs/PLAN_OPENAI_CHAT_DIALECT.md` | 781 | Risk 11, "every turn is a cold prefill ... discovered from a bill". | Resolved; see this plan. |
| `docs/PLAN_OPENAI_CHAT_DIALECT.md` | 842 | Out of scope: "Prompt caching (`cache_control`) actually reaching the upstream". | Still out of scope for `cache_control`, but automatic prefix caching reports hits and they are now surfaced. |

## 6. Verification

`estimateRequestTokens` and the new split are both observable end to end. After rebuilding and
restarting the daemon:

1. Confirm the upstream field is still present and non-zero, so a silent schema change is ruled out
   before blaming the translator:

   ```bash
   curl -s -X POST "$GATEWAY_BASE_URL/v1/chat/completions" \
     -H "Authorization: Bearer $GATEWAY_API_KEY" \
     -H "content-type: application/json" \
     -d '{"model":"<upstream-model>","max_tokens":4,
          "messages":[{"role":"user","content":"hi"}]}' \
     | jq -c '.usage.prompt_tokens_details'
   ```

2. Run a session and read the counters Claude Code recorded:

   ```bash
   jq -c 'select(.message.usage) | .message.usage' \
     ~/.claude/projects/<project-slug>/*.jsonl | tail -5
   ```

   `cache_read_input_tokens` should be non-zero from the second turn on, and
   `input_tokens + cache_creation_input_tokens + cache_read_input_tokens` should equal the
   upstream's `prompt_tokens`.

3. The statusline's `cache:` segment should move off `0%` with no change to the statusline script.

Step 3 rests on one assumption: that Claude Code merges cache fields out of `message_delta` rather
than reading only `message_start`. **This is already demonstrated by the current transcripts**, not
inferred. `stream.go:229` writes `output_tokens: 0` in `message_start`, nothing else emits it
before the `message_delta` at line 261, and every usage object Claude Code has stored carries a
non-zero `output_tokens`:

```
"usage":{"input_tokens":59346,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":1353,...}
```

`1353` can only have arrived in `message_delta`, so the merge is field-wise over the usage object.
Cache keys riding in the same object are picked up the same way. The `message_start`-held-open
fallback is therefore not a realistic risk and is not implemented.

## 7. Risks

1. **Usage arrives after `message_start` is committed.** Structural: the SSE status is written
   before the first byte (`main.go:423`), so nothing in `message_start` can depend on upstream
   usage. The design corrects rather than predicts, and section 6 step 3 is the gate.
2. **The upstream changes the field name.** `cached_tokens` is the OpenAI/vLLM name. A rename
   would silently report `0`, which is the failure this plan exists to remove, so verification 1
   checks the raw field before any translator debugging.
3. **The estimate and the reported count disagree visibly.** `message_start` says one number and
   `message_delta` another. This is already true of `output_tokens`, so clients must tolerate it.
   Worth a line in the README's `usage` notes.
4. **Cache hits are not a client-controlled quantity.** The figure depends on the upstream's cache
   retention and eviction, so a low reading is not necessarily a local fault. The statusline's
   existing copy covers this; no change needed.
5. **A first turn still reads `0%`.** `cache_creation_input_tokens` has no upstream counterpart, so
   turn 1 cannot render as anything else. Inherent, not a defect.

## 8. Sequencing

1. Widen `openAIUsage` and add `anthropicUsageFromUpstream` plus unit tests. No behavior change; the
   existing suite is the gate.
2. Non-streaming path (3.3) and its tests.
3. Streaming path (3.4) and the `estimateRequestTokens` precedence change (3.5), together, since
   the second is what makes the first's assertions move.
4. Docs (section 5).
5. End-to-end verification (section 6). Steps 1 and 2 are independently shippable and land before
   anything user visible changes.
