# OpenCode Go

Reference for using the OpenCode Go gateway as a ccbunshin provider. Secondary to
`docs/OPENCODE_ZEN.md`, which is the larger host and carries the real Claude models.

Verified 2026-09-13 against <https://opencode.ai/docs/go/#endpoints>. Redo the check when it matters;
the table changes as models are added.

## Endpoint map

28 models across three API shapes. Every model id appears on exactly one endpoint.

| Endpoint | Count | Models | Dialect |
|---|---|---|---|
| `/zen/go/v1/messages` | 8 | `minimax-m3`, `minimax-m2.7`, `minimax-m2.5`, `qwen3.8-max`, `qwen3.8-flash`, `qwen3.7-max`, `qwen3.7-plus`, `qwen3.6-plus` | `anthropic` |
| `/zen/go/v1/chat/completions` | 16 | `glm-5.3-flash`, `glm-5.3`, `glm-5.2`, `glm-5.1`, `kimi-k3`, `kimi-k2.7-code`, `kimi-k2.6`, `longcat-2.0`, `deepseek-v4.1-flash`, `deepseek-v4-pro`, `deepseek-v4-flash`, `deepseek-v4-flash-vision-exp`, `mimo-v2.5`, `mimo-v2.5-pro`, `hy4-preview`, `hy3` | `openai-chat` |
| `/zen/go/v1/responses` | 4 | `grok-4.6`, `gpt-5.6-luna`, `muse-spark-1.3-contributor`, `muse-spark-1.2-contributor` | `openai-responses` - **not implemented** |

## What works today

Two of the three shapes need no new code:

- **`/v1/messages`** - default `anthropic` dialect, byte-for-byte pass-through, `cache_control`
  preserved. A route and an upstream URL, nothing more.
- **`/chat/completions`** - `"dialect": "openai-chat"`, implemented.

`/v1/responses` is the gap, and the four models on it are reachable nowhere else on this host.
`gpt-*` and `muse-spark-*` are exclusive to it. See
`docs/PLAN_RESPONSES_DIALECT_AND_TOOL_SEARCH.md` Part A.

## Config

```json
{
  "providers": {
    "opencode-go": {
      "upstream": "https://opencode.ai/zen/go",
      "model_dialects": { "glm-5.3": "openai-chat" }
    }
  },
  "routes": [
    { "pattern": "claude-*", "provider": "opencode-go" },
    { "pattern": "glm-*", "provider": "opencode-go" },
    { "pattern": "deepseek-*", "provider": "opencode-go" }
  ]
}
```

With no `dialect` on the provider, the `/v1/messages` models stay on the pass-through default and
only the entries in `model_dialects` are translated. A `gpt-*` route needs the plan's Part A first.
