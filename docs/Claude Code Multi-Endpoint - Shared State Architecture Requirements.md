# Claude Code Multi-Endpoint / Shared State Architecture

## 1. Background

The company provides two LLM gateways for Claude Code:

### FREE Gateway

- Free to use
- Provides:
  - Qwen3.8-27B
  - DeepSeek V4 Flash

### PAID Gateway

- API usage is billed
- Provides Claude models through Google Vertex
- Has additional company-specific Claude Code configuration:
  - Company hooks
  - OpenTelemetry (OTEL)
  - Potentially other company-specific environment variables/settings

Currently, the company provides a script similar to `cc-switch`.

The important characteristic is that the current switching mechanism is **global**:

```text
~/.claude/settings.json
```

The script modifies this single global settings file.

Therefore, at any given time, all Claude Code processes effectively use the same endpoint/configuration.

---

# 2. Current Problem

Currently:

```text
Claude Code instance A
        │
Claude Code instance B
        │
        ▼
~/.claude/settings.json
        │
        └── current selected endpoint
```

For example:

```text
Terminal A
└── Claude Code → FREE

Terminal B
└── Claude Code → PAID
```

This is problematic because switching the global settings affects all Claude Code instances.

The goal is to allow:

```text
Terminal A → FREE
Terminal B → PAID
```

**simultaneously**, including when both Claude Code instances operate in the same repository.

---

# 3. Primary Goal

Implement **per-process / per-instance configuration isolation** for Claude Code.

The desired user experience is:

```bash
claude-free
```

and:

```bash
claude-paid
```

These commands must be able to run simultaneously.

Example:

```text
Same repository

Terminal A:
    claude-free
    → FREE Gateway

Terminal B:
    claude-paid
    → PAID Gateway
```

Neither process should depend on or modify a global "currently selected endpoint".

---

# 4. Configuration Isolation

FREE and PAID need independent Claude Code configuration.

The following should be isolated:

### Endpoint

```text
FREE → FREE Gateway
PAID → PAID Gateway
```

### Model configuration

Conceptually:

```text
FREE:

opus   → DeepSeek V4 Flash
sonnet → Qwen3.8-27B
haiku  → Qwen3.8-27B
```

```text
PAID:

opus   → Vertex Claude Opus
sonnet → Vertex Claude Sonnet
haiku  → Vertex Claude Haiku
```

The exact model IDs should be configurable.

### Hooks

```text
FREE:
    company hooks = disabled

PAID:
    company hooks = enabled
```

### OTEL

```text
FREE:
    OTEL = disabled

PAID:
    OTEL = enabled
```

### Company-specific settings

Any company-specific:

- environment variables
- hooks
- telemetry configuration
- Claude Code settings

must be isolated between FREE and PAID.

---

# 5. Claude Code State Must Remain Shared

Do **not** create two completely independent Claude Code environments just to isolate configuration.

The following state should remain shared where technically safe:

- Skills
- Projects
- Project state
- Project JSONL / conversation data
- History
- Todos
- Repository / working tree
- Other Claude Code state that is unrelated to Gateway configuration

The desired architecture is:

```text
                 Shared Claude Code State
                         │
            ┌────────────┴────────────┐
            │                         │
       FREE instance             PAID instance
            │                         │
       FREE config               PAID config
            │                         │
       FREE Gateway              PAID Gateway
```

The key requirement is:

> **Configuration isolation without unnecessary state isolation.**

---

# 6. `CLAUDE_CONFIG_DIR`

Claude Code supports:

```bash
CLAUDE_CONFIG_DIR
```

A possible approach is to use:

```text
~/.claude-free/
~/.claude-paid/
```

for separate configuration roots.

However, `CLAUDE_CONFIG_DIR` normally isolates more than just `settings.json`.

Therefore, do not blindly create two completely independent config directories.

Instead, investigate whether the following architecture is safe:

```text
~/.claude-free/
    settings.json
    skills -> shared
    projects -> shared
    history -> shared
    todos -> shared
    ...

~/.claude-paid/
    settings.json
    skills -> shared
    projects -> shared
    history -> shared
    todos -> shared
    ...
```

Symlinks are acceptable if they are safe and supported by Claude Code.

Before implementing this, inspect the actual Claude Code filesystem/state usage and determine:

1. Which paths are configuration.
2. Which paths are persistent state.
3. Which paths can safely be shared.
4. Which paths must remain isolated.
5. Whether simultaneous writes to shared files are safe.

Do not assume that symlinking only `skills` is sufficient.

---

# 7. Do Not Modify Global Settings

The solution must not require repeatedly modifying:

```text
~/.claude/settings.json
```

For example, this workflow is NOT desired:

```text
switch → FREE
start Claude Code

switch → PAID
start another Claude Code
```

Instead:

```text
claude-free
```

should select FREE configuration for that process.

And:

```text
claude-paid
```

should select PAID configuration for that process.

Existing global settings should not be used as a global endpoint switch.

---

# 8. LeanCTX

LeanCTX is **not intended to be the provider/model routing layer**.

Its purpose in this architecture is:

> Context caching / context optimization to reduce token usage.

Conceptually:

```text
Claude Code
    │
    └── LeanCTX MCP
            │
            └── context cache / optimization
                    ↓
                 save tokens
```

LeanCTX and model/provider routing should remain separate concerns.

---

# 9. LeanCTX MCP

Ideally, FREE and PAID should not require separate LeanCTX caches merely because they use different LLM providers.

Investigate whether:

```text
Claude Code FREE ──┐
                   ├── shared LeanCTX functionality/cache
Claude Code PAID ──┘
```

is possible.

If Claude Code starts separate LeanCTX MCP processes through stdio, that is acceptable **only if this does not unnecessarily duplicate the actual cache/data**.

The important point is:

> Do not duplicate LeanCTX merely to support FREE/PAID model routing.

LeanCTX is for context/token optimization, not endpoint switching.

---

# 10. Custom Proxy

Instead of using LeanCTX as the provider routing layer, implement a separate custom proxy.

The custom proxy is responsible for:

- Receiving Claude Code Anthropic API requests.
- Selecting the appropriate Gateway.
- Selecting/mapping the upstream model.
- Forwarding requests to the selected Gateway.
- Forwarding streaming responses correctly.
- Optionally supporting fallback/routing logic if needed.

Conceptually:

```text
Claude Code
     │
     ▼
  Custom Proxy
     │
     ├── FREE Gateway
     │     ├── Qwen3.8-27B
     │     └── DeepSeek V4 Flash
     │
     └── PAID Gateway
           └── Vertex Claude
```

The proxy should **not** implement LeanCTX's context caching functionality.

Keep the responsibilities separate:

```text
Claude Code
    │
    ├── LeanCTX → context optimization / token saving
    │
    └── Custom Proxy → provider/model routing
```

---

# 11. Proxy Isolation

The proxy may use separate instances or separate listeners for FREE and PAID.

A simple and explicit design is:

```text
127.0.0.1:3456 → FREE
127.0.0.1:3457 → PAID
```

For example:

```text
claude-free
    │
    ▼
127.0.0.1:3456
    │
    ▼
FREE Gateway
```

```text
claude-paid
    │
    ▼
127.0.0.1:3457
    │
    ▼
PAID Gateway
```

This makes the routing decision explicit and avoids the proxy having to guess whether a request belongs to FREE or PAID.

A single proxy process with explicit per-request profile routing is also acceptable if it provides equivalent isolation.

Do not introduce complexity solely for multi-instance support unless necessary.

---

# 12. Linux VM Environment

The implementation will run inside a **Linux VM**.

Therefore:

- Do not design around macOS LaunchAgents.
- Linux `systemd` is preferred for long-running proxy services.
- Two independent services are acceptable, for example:

```text
lean-ctx-free.service
lean-ctx-paid.service
```

if two LeanCTX proxy instances are actually required.

For the custom proxy, systemd services may similarly be used.

Example architecture:

```text
Linux VM
│
├── Custom Proxy FREE
│      └── :3456
│
├── Custom Proxy PAID
│      └── :3457
│
└── LeanCTX
       └── MCP / context optimization
```

The proxy services should preferably bind to loopback unless external network access is explicitly required.

---

# 13. Same Repository Requirement

The most important operational requirement is that both profiles can operate in the same repository at the same time.

Example:

```text
~/project/

Terminal 1:
    claude-free

Terminal 2:
    claude-paid
```

They must not interfere with each other's:

- API endpoint
- model selection
- hooks
- OTEL
- configuration
- proxy routing

while still sharing the desired Claude Code project state.

---

# 14. Non-Goals

Do not:

1. Modify LeanCTX to become the provider routing layer.
2. Duplicate LeanCTX merely because FREE and PAID exist.
3. Create two completely independent Claude Code state directories unless technically unavoidable.
4. Depend on global modification of `~/.claude/settings.json`.
5. Assume `CLAUDE_CONFIG_DIR` automatically provides configuration-only isolation.
6. Use `routatic-proxy` for this company setup.

`routatic-proxy` is a **personal computer / OpenCode Go project** and is unrelated to the company's Gateway architecture.

---

# 15. Desired Final Architecture

The target architecture should look approximately like:

```text
                              Linux VM
                                 │
                  ┌──────────────┴──────────────┐
                  │                             │
             claude-free                  claude-paid
                  │                             │
            FREE config                   PAID config
                  │                             │
                  │                             │
                  ▼                             ▼
           Custom Proxy :3456            Custom Proxy :3457
                  │                             │
                  ▼                             ▼
           FREE Gateway                  PAID Gateway
                  │                             │
          Qwen / DeepSeek                 Vertex Claude


                  ┌─────────────────────────────┐
                  │          LeanCTX            │
                  │                             │
                  │ context cache / optimization│
                  │        save tokens           │
                  └─────────────────────────────┘


                  ┌─────────────────────────────┐
                  │     Shared Claude State      │
                  │                             │
                  │ skills                      │
                  │ projects                    │
                  │ project JSONL               │
                  │ history                     │
                  │ todos                       │
                  │ project state               │
                  └─────────────────────────────┘
```

The core separation is:

```text
Claude Code configuration
        ↓
FREE / PAID isolation

Custom Proxy
        ↓
Gateway / model routing

LeanCTX
        ↓
Context caching / token saving

Claude Code state
        ↓
Shared where safe
```

---

# 16. Success Criteria

The implementation is successful if all of the following are true:

### 1. Simultaneous FREE and PAID

```text
claude-free → FREE Gateway
claude-paid → PAID Gateway
```

can run simultaneously.

### 2. Same repository

Both can run in the same repository without endpoint/configuration interference.

### 3. No global switching

Neither command requires changing the global:

```text
~/.claude/settings.json
```

to select its endpoint.

### 4. Configuration isolation

FREE and PAID have independent:

- endpoint
- model defaults/mapping
- hooks
- OTEL
- company-specific configuration

### 5. Shared state

Skills, projects, project JSONL, history, todos and other appropriate Claude Code state remain shared.

### 6. LeanCTX remains independent

LeanCTX continues to provide context optimization / caching for token savings and is not responsible for FREE/PAID provider routing.

### 7. Proxy is independently responsible for routing

The custom proxy handles:

```text
Claude API
    → provider
    → model
    → upstream Gateway
```

without taking over LeanCTX functionality.

### 8. Linux-friendly

The solution works cleanly in the Linux VM and can use systemd for persistent services.