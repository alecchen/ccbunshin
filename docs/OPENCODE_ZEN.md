# OpenCode Zen

Reference for using the OpenCode Zen gateway as a ccbunshin provider. This is the host that
prompted `docs/PLAN_RESPONSES_DIALECT_AND_TOOL_SEARCH.md`: it carries the real `claude-*` models
(which pass through untouched) alongside a large set of models reachable only through OpenAI's
Responses API.

Verified 2026-09-13 against <https://opencode.ai/docs/zen/#endpoints>. Redo the check when it
matters; the table changes as models are added.

## Endpoint map

69 models across four API shapes. Every model id appears on exactly one endpoint.

| Endpoint | Count | Models (prefixes) | Dialect |
|---|---|---|---|
| `/zen/v1/messages` | 15 | all 11 `claude-*` (`claude-opus-5`, `claude-sonnet-5`, `claude-haiku-4-5`, `claude-fable-5-1`, `claude-fable-5`, `claude-opus-4-8` ... `4-5`, `claude-sonnet-4-6`, `claude-sonnet-4-5`), `qwen3.7-max`, `qwen3.7-plus`, `qwen3.6-plus`, `qwen3.5-plus` | `anthropic` |
| `/zen/v1/responses` | 27 | all 21 `gpt-*` (`gpt-6-astra`, `gpt-5.6-sol`, `gpt-5.6-terra`, `gpt-5.6-luna`, `gpt-5.5`, `gpt-5.5-pro`, `gpt-5.4`, `gpt-5.4-pro`, `gpt-5.4-mini`, `gpt-5.4-nano`, `gpt-5.3-codex`, `gpt-5.3-codex-spark`, `gpt-5.2`, `gpt-5.2-codex`, `gpt-5.1`, `gpt-5.1-codex`, `gpt-5.1-codex-max`, `gpt-5.1-codex-mini`, `gpt-5`, `gpt-5-codex`, `gpt-5-nano`), `grok-4.6`, `grok-4.5`, `grok-build-0.1`, `muse-spark-1.3`, `muse-spark-1.2`, `muse-spark-1.3-contributor-free` | `openai-responses` - **not implemented** |
| `/zen/v1/chat/completions` | 20 | `deepseek-v4-pro`, `deepseek-v4-flash`, `deepseek-v4-flash-vision-exp`, `minimax-m3`, `minimax-m2.7`, `minimax-m2.5`, `glm-5.3-flash`, `glm-5.3`, `glm-5.2`, `glm-5.1`, `glm-5`, `kimi-k2.5`, `kimi-k2.6`, `kimi-k2.7-code`, `kimi-k3`, `big-pickle`, and four `*-free` community models | `openai-chat` |
| `/zen/v1/models/<id>` | 7 | every `gemini-*`: `gemini-3.8-flash`, `gemini-3.7-flash`, `gemini-3.6-flash`, `gemini-3.5-flash`, `gemini-3.5-flash-lite`, `gemini-3.1-pro`, `gemini-3-flash` | **out of reach - see below** |

## What works today

- **`/v1/messages`** - the default `anthropic` dialect, byte-for-byte pass-through,
  `cache_control` breakpoints preserved. This covers all the Claude models, which is the main
  reason to use this host: no translation means no cache loss and nothing to lose in translation.
- **`/chat/completions`** - `"dialect": "openai-chat"`, implemented.

## The two gaps

**`/v1/responses`** (27 models) needs the `openai-responses` dialect from
`docs/PLAN_RESPONSES_DIALECT_AND_TOOL_SEARCH.md` Part A. The `gpt-*`, `grok-*`, and `muse-spark-*`
groups are exclusive to it: no other endpoint on this host serves them.

**`/zen/v1/models/<model-id>`** (7 Gemini models) does not fit the `dialect` mechanism at all. The
path varies per model, the shape is Google's native `generateContent` (`@ai-sdk/google`), and a
dialect in this repo maps a model to a wire format by appending a fixed suffix to the provider
upstream. Reaching these would need a per-model path template, which is a different feature. Noted
so the gap is deliberate.

## Config

```json
{
  "providers": {
    "opencode-zen": {
      "upstream": "https://opencode.ai/zen",
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

With no `dialect` on the provider itself, the `claude-*` and `qwen*` models stay on the pass-through
default and only the `model_dialects` entries are translated - which is what keeps prompt caching
intact for the Claude models. Adding a `gpt-*` or `muse-spark-*` route requires Part A first.
