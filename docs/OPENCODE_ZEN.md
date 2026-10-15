# OpenCode Zen

Reference for using the OpenCode Zen gateway as a ccbunshin provider. This is the host that
prompted `docs/PLAN_RESPONSES_DIALECT_AND_TOOL_SEARCH.md`: it carries the real `claude-*` models
(which pass through untouched) alongside a large set of models reachable only through OpenAI's
Responses API.

Verified 2026-10-15 by listing <https://opencode.ai/inference/v1/models> with a live key and probing
every advertised id. Redo the check when it matters; the table changes as models are added, and the
previous revision of this file was wrong within a month (see "Since 2026-09-13").

## Endpoint map

The base is `https://opencode.ai/inference/v1`. Probed 2026-10-15: the listing advertises 49 ids,
and each is served by exactly one endpoint. "Wrong endpoint" is a clean signal rather than a generic
404 - the gateway answers `400 ModelProtocolUnsupported` - which is what makes the classification
below mechanical instead of a reading of the docs table.

| Endpoint | Count | Models | Dialect |
|---|---|---|---|
| `/inference/v1/messages` | 8 | `claude-fable-5-1`, `claude-haiku-4-5`, `claude-haiku-5-5`, `claude-opus-5-5`, `claude-sonnet-5`, `claude-sonnet-5-5`, `qwen3.6-plus`, `qwen3.8-flash` | `anthropic` |
| `/inference/v1/responses` | 15 | 13 priced: `gpt-5.3-codex`, `gpt-5.4-mini`, `gpt-5.4-nano`, `gpt-5.5`, `gpt-5.5-pro`, `gpt-5.6-terra`, `gpt-6-astra`, `gpt-6-sol`, `gpt-6.1-sol`, `gpt-6-luna`, `grok-4.7`, `grok-build-0.1`, `muse-spark-1.3`; 2 free: `muse-spark-1.3-contributor-free`, `muse-spark-1.2-contributor-free` | `openai-responses` - **not implemented** |
| `/inference/v1/chat/completions` | 16 | 8 priced: `deepseek-v4.1-flash`, `deepseek-v4-pro`, `glm-5.3`, `glm-5.3-flash`, `kimi-k3`, `minimax-m3`, `mistral-large-4`, `qwen3.8-max`; 8 free, listed below | `openai-chat` |
| `/inference/v1/models/<id>:generateContent` | 4 | `gemini-3.1-pro`, `gemini-3.5-flash-lite`, `gemini-3.8-flash`, `gemini-4-argon` | Google `generateContent` - **out of reach, see below** |
| `/inference/v1/systemone` | 2 | `jev-1.13`, `jev-1.13-free` | **undetermined, see below** |

The remaining 4 of the 49 are advertised but unreachable: `kimi-k2.7-code`,
`claude-opus-5-5-backup`, `gpt-5.3-codex-spark`, and the placeholder `test`. See "Models that fail
despite being advertised".

Every `/responses` call needs `max_output_tokens >= 16`. Below that the gateway forwards the request
and returns the upstream's complaint instead of rejecting it locally. Note that `qwen3.8-max` is the
one `qwen` id on `/chat/completions` rather than `/messages`.

## The free tier cannot be used from here

The free models are advertised, and a valid key reaches them, but they are gated on client identity
rather than on the key:

```
$ curl .../chat/completions -H "Authorization: Bearer $KEY" -d '{"model":"big-pickle",...}'
403 {"type":"error","error":{"type":"FreeTierError",
     "message":"OpenCode's free tier can only be used from within OpenCode. ..."}}
```

The same key on the same endpoint succeeds for a priced model (`glm-5.3` -> 200), and the same key
on `/messages` succeeds for `claude-haiku-4-5`. So the credential is fine and the restriction is
attached to the caller, not to the account's spend. The 403 carries no `WWW-Authenticate` and no
header naming the criterion, so what it keys on is not observable from outside.

**Consequence for ccbunshin:** free models are unreachable through the proxy, because the proxy is
not OpenCode. Do not route to them and expect the gateway to serve them. This is not a translation
gap that Part A of the Responses plan would close.

| Free id | Served by | Verdict 2026-10-15 |
|---|---|---|
| `big-pickle`, `cat-jam-free`, `mimo-v2.6-flash-free`, `nemotron-3.5-lightning-free`, `step-5-preview-free`, `longcat-2.5-preview-free`, `deepseek-v4.1-flash-free`, `space-bunny-free` | `/chat/completions` | 403 `FreeTierError` |
| `nemotron-3-ultra-free` | `/chat/completions` | 403 `FreeTierError`, and **absent from the listing** - it answered, but nothing advertises it |
| `muse-spark-1.3-contributor-free`, `muse-spark-1.2-contributor-free` | `/responses` | 403 `FreeTierError` |
| `jev-1.13-free` | `/systemone` | not determinable - see below |

Two free ids the docs still list are gone: `mimo-v2.5-free` and `exo-free` both answer
`410 ModelDeprecated`, and neither appears in the listing any more. `mimo-v2.5-free` names
`mimo-v2.6-flash-free` as its replacement.

## What works today

- **`/inference/v1/messages`** - the default `anthropic` dialect, byte-for-byte pass-through,
  `cache_control` breakpoints preserved. This covers the Claude models, which is the main reason to
  use this host: no translation means no cache loss and nothing to lose in translation.
- **`/inference/v1/chat/completions`** - `"dialect": "openai-chat"`, implemented.

## The three gaps

**`/inference/v1/responses`** (15 ids) needs the `openai-responses` dialect from
`docs/PLAN_RESPONSES_DIALECT_AND_TOOL_SEARCH.md` Part A. `gpt-*`, `grok-4.7`, `grok-build-0.1` and
`muse-spark-1.3` are exclusive to it: no other endpoint on this host serves them.

**`/inference/v1/models/<id>:generateContent`** (4 Gemini models) does not fit the `dialect`
mechanism. The shape is Google's native `generateContent`, and two details break the repo's
"append a fixed suffix to the upstream" model:

- The path is per model, not per dialect: `<id>` is the model.
- The suffix is `:generateContent`, and the request would have to be built against the bare
  `inference` root rather than the `/inference/v1` base.

Reaching these needs a per-model path template, which is a different feature. Noted so the gap is
deliberate. The shape that works is `{"contents":[{"role":"user","parts":[{"text":"..."}]}]}` - the
explicit `role` is required, and omitting it returns
`400 INVALID_ARGUMENT: Please use a valid role: user, model`. Without the `:generateContent` suffix
the same path returns a bodyless 400.

**`/inference/v1/systemone`** (2 ids) is undetermined. The path exists and answers under this key,
but every body shape tried - `messages`, `input`, and `prompt` - returns
`400 {"detail":{"error_type":"api_usage_error","message":"Invalid request."}}`, against a 404 for an
unknown path. Whether `jev-1.13-free` is open or gated is therefore unknown; it is not a 403, so it
is not the same gate as the rest of the free tier.

## Models that fail despite being advertised

The listing is not a promise of service. Observed 2026-10-15:

| Model | Response |
|---|---|
| `kimi-k2.7-code` | `410 Upstream request failed: Endpoint is unavailable` |
| `claude-opus-5-5-backup` | `400 ... Your credit balance is too low` - the key is out of credit on the backup route |
| `gpt-5.4`, `gpt-5.2` | `403 Upstream request failed: Model access is disabled` - both are absent from the listing |
| `gpt-5.3-codex-spark` | `404 The model gpt-5.3-codex-spark-preview does not exist` - the advertised id does not match the upstream's |
| `test` | `403 FreeTierError` - a placeholder entry in the public listing |
| `hy3`, `hy4-preview`, `longcat-2.0` | `400 Model is unavailable` - absent from the listing, served only by the Go tier |

Treat any route as unverified until a request against it has returned 200.

## Since 2026-09-13

The previous revision was wrong on three counts. The base path moved, the model list shrank, and the
free set turned over.

- **`/zen/v1/*` is dead.** It returns `401 Missing API key` even with a valid key. The live base is
  `/inference/v1/*`, which matches what the docs now publish.
- **The listing shrank from 65 ids to 49.** Departed: `claude-opus-5`, `claude-fable-5`, all five
  `claude-opus-4-*`, both `claude-sonnet-4-*`, `deepseek-v4-flash`,
  `deepseek-v4-flash-vision-exp`, `glm-5`/`glm-5.1`/`glm-5.2`, nine older `gpt-5*` ids,
  `grok-4.5`/`grok-4.6`, `kimi-k2.5`/`kimi-k2.6`, `minimax-m2.5`/`minimax-m2.7`, `muse-spark-1.2`,
  and three `qwen3.*` ids. A route config naming any of them needs updating.
- **Arrived:** `claude-opus-5-5`, `claude-sonnet-5-5`, `claude-haiku-5-5`, `deepseek-v4.1-flash`,
  `gpt-6-sol`, `gpt-6.1-sol`, `gpt-6-luna`, `grok-4.7`, `mistral-large-4`, `qwen3.8-max`,
  `gemini-4-argon`, and the free and placeholder ids in the tables above.

## OpenCode Go

Checked the same day, because a base-path move on one tier says nothing about the other. Go did
**not** move: both `/zen/go/v1/messages` and `/inference/go/v1/messages` exist and reach an upstream,
and both now demand an `x-opencode-session` header:

- No session header: `400 MissingSessionID`, pointing at
  <https://opencode.ai/docs/go/#where-can-i-use-it>.
- With one, on `/inference/go/v1/messages`: `403 An active OpenCode Go subscription is required to
  use Go models.`
- With one, on `/zen/go/v1/messages`: `401 Upstream request failed: Go requires a credential` - this
  key is not scoped to Go.

So `docs/OPENCODE_GO.md`'s endpoint map may still hold, but its config does not: a Go subscription
and a session header are both prerequisites, and neither is something ccbunshin sets. Re-verify that
file against a Go subscription before trusting it.

## Config

The upstream carries the whole path up to and including the version segment, because the proxy
appends only the endpoint's bare name (`/chat/completions`, or the caller's own path for the
pass-through dialect). `https://opencode.ai/inference/v1` is therefore the value, not
`https://opencode.ai/inference` - the latter yields `/inference/chat/completions`, which 404s, and
`.../v1/v1/...` double-prefixes the same way.

```json
{
  "providers": {
    "opencode-zen": {
      "upstream": "https://opencode.ai/inference/v1",
      "model_dialects": { "glm-5.3": "openai-chat", "deepseek-v4-pro": "openai-chat" }
    }
  },
  "routes": [
    { "pattern": "claude-*", "provider": "opencode-zen" },
    { "pattern": "glm-*", "provider": "opencode-zen" },
    { "pattern": "deepseek-*", "provider": "opencode-zen" }
  ]
}
```

With no `dialect` on the provider itself, the `claude-*` and `qwen3.6-plus`/`qwen3.8-flash` models
stay on the pass-through default and only the `model_dialects` entries are translated - which is what
keeps prompt caching intact for the Claude models. Adding a `gpt-*`, `grok-*` or `muse-spark-*` route
requires Part A first, and a `gemini-*` route needs the per-model path template described above.
`qwen3.8-max` is the trap in its family: it is the one `qwen` id on `/chat/completions` rather than
`/messages`, so it needs its own `model_dialects` entry.
