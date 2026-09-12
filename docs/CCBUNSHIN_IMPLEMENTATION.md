# ccbunshin — Claude Code Shadow Clones

> **ccbunshin** (影分身, "shadow clone"). Design/spec for a small, dependency-free tool that
> runs multiple Claude Code configurations side by side: isolated **configuration** (endpoint,
> model, hooks, OTEL env) with **shared state** (projects, history, todos, skills).
>
> The metaphor: each profile is a shadow clone of your Claude Code. Clones share the
> original's memories - skills, history, todos - and each is deployed with a different jutsu
> (endpoint, model, hooks, OTEL). Deploy several at once; when a clone disperses, its
> knowledge returns to the original.
>
> Status: draft spec v3 (2026-09-06). v2 chose per-profile `--settings` files over the v1
> `CLAUDE_CONFIG_DIR` + symlinks design; v3 adds the design-review decisions (no auth
> handling, per-variable env merging, `model` command, trimmed `doctor`). Mechanism research:
> `INVESTIGATION_FREE_PAID.md`. Requirements:
> `Claude Code Multi-Endpoint - Shared State Architecture Requirements.md`.
>
> Repo scope, two phases:
> 1. **Config isolation** (this doc) - per-process FREE/PAID configuration via `--settings`.
> 2. **Multi-endpoint proxy** (phase 2, separate component) - gateway/model routing. Design
>    deferred; open questions in section 10.

---

## 1. Problem

Claude Code keeps all configuration and state in one `~/.claude/` directory. The company's
internal switcher writes the single global `~/.claude/settings.json`, which affects every
running Claude Code instance.

Target:

```text
Terminal A: claude-free   -> FREE gateway  (Qwen / DeepSeek, no company hooks, no OTEL)
Terminal B: claude-paid   -> PAID gateway  (Vertex Claude, company hooks, OTEL)
```

- both running at the same time, in the same repository,
- without writing to the global `~/.claude/settings.json`,
- while sharing Claude Code state: projects, session transcripts, history, todos, skills.

## 2. Approach: per-profile `--settings` files

Claude Code settings precedence (highest first): Managed > **Command line (`--settings`)** >
Project local > Shared project > User. `--settings` is a per-key merge: a key set there
overrides the same key in project/user settings; a key omitted falls through to lower levels.

**Env blocks merge per-variable** (assumed from the managed-settings carve-out that "the env
block merges per key"; not explicitly documented for non-managed levels - see section 9 for
the empirical verification). So a profile file defines only what differs (`env` vars like
`ANTHROPIC_BASE_URL`, `hooks`, `model`, `statusLine`) and inherits everything else from the
user's env block - including auth, which is never the tool's concern (section 6).

Why not `CLAUDE_CONFIG_DIR` (v1)? It relocates the whole config tree including state, so
sharing state requires a per-version symlink map. New state paths added by future Claude Code
versions silently land in the profile dir and state diverges. `--settings` has no such map:
state never moves.

Why not plain env vars? The `env` block in `~/.claude/settings.json` overrides process
environment, so `ANTHROPIC_BASE_URL=... claude` does not stick. `--settings` sits above user
settings and wins.

## 3. Architecture

```text
~/.claude/settings.json          canonical baseline (shared, non-isolated keys only)
~/.claude-profiles/
  free.json                      --settings file: FREE endpoint, hooks: {}, model
  paid.json                      --settings file: PAID endpoint, company hooks, OTEL, model
```

```json
// free.json - blank hooks = company hooks OFF
{ "env":   { "ANTHROPIC_BASE_URL": "http://127.0.0.1:3456" },
  "hooks": {},
  "model": "qwen3.8-27b" }
```

```json
// paid.json - company hooks + OTEL enabled
{ "env":   { "ANTHROPIC_BASE_URL": "http://127.0.0.1:3457",
             "OTEL_EXPORTER_OTLP_ENDPOINT": "..." },
  "hooks": { "PreToolUse": [{ "matcher": "Bash", "hooks": [{ "type": "command", "command": "..." }] }] },
  "model": "claude-sonnet-4-5" }
```

No tokens in profile files - auth is the user's settings concern (section 6).

Launch:

```text
claude-free  =  ccbunshin launch free  =  claude --settings ~/.claude-profiles/free.json
claude-paid  =  ccbunshin launch paid  =  claude --settings ~/.claude-profiles/paid.json
```

Each launch may name its profile explicitly, or use the nearest `.ccbunshin-profile` marker in the current directory or a parent, falling back to the global selection outside any project. Explicit names take precedence. The global selection is a fallback, not shared mutable state: a launch resolves its profile from the marker, the global file, or the argument, in that order, so two profiles still run simultaneously, in the same repository, without interference.

## 4. Isolation map

| Item | Default | Mechanism |
|---|---|---|
| `env` vars (endpoint, OTEL) | **isolated per-variable** | profile `env` merges with user `env`; profile wins for the vars it defines |
| `hooks` | **isolated** | profile `hooks` replaces global `hooks`; `{}` blanks |
| `model` | **isolated** | profile `model` (or `--model` flag) |
| `statusLine`, other scalar keys | isolated | profile key replaces global |
| `permissions.allow` | shared (merges) | list keys combine across scopes; cannot subtract via `--settings` |
| `skills/`, `projects/`, `history.jsonl`, `todos/`, `conversations/` | **shared** | untouched - always `~/.claude/` |
| `$HOME/.claude.json` (auth, MCP, trust) | **shared** | untouched |
| `CLAUDE.md` | shared | global CLAUDE.md still loads (known limitation: no per-profile global instructions) |
| auth credentials | user's concern | native mechanisms, section 6 |

## 5. CLI

```text
ccbunshin init                        one-time: create ~/.claude-profiles/, install wrappers
ccbunshin create <name> [--from <file>]  scaffold a profile settings file from a template
ccbunshin launch [<name>] [args...]  claude --settings <file> "$@"
ccbunshin local [<name>|--unset]       set, show, or clear the directory-local profile
ccbunshin global [<name>|--unset]      set, show, or clear the global (fallback) profile
ccbunshin model <name> <model>        set the profile's default model
ccbunshin list                        list profiles and the keys each covers
ccbunshin status <name>               show profile file, diff vs global baseline
ccbunshin doctor [--fix]              validate profile JSON; warn on missing isolated keys
                                      (hooks, model); flag stale endpoint/token residue in
                                      global settings
ccbunshin delete <name>               remove a profile file (never touches ~/.claude/)
```

Shell wrappers (installed by `ccbunshin init` into `~/.zshrc` / `~/.bashrc` / `~/.tcshrc`,
idempotent marker block, removed by `ccbunshin uninstall`):

```bash
claude-free() { ccbunshin launch free "$@"; }
claude-paid() { ccbunshin launch paid "$@"; }
# generic: claude-<name>() { ccbunshin launch <name> "$@"; }
```

## 6. Auth - out of scope

Claude Code has native credential mechanisms; ccbunshin never reads or writes credentials.
Authentication precedence (docs): `ANTHROPIC_AUTH_TOKEN` > `ANTHROPIC_API_KEY` >
`apiKeyHelper` script > OAuth. The user configures auth in their own settings - shared in
`~/.claude/settings.json`, or per-profile by setting the `apiKeyHelper` key (or env vars) in
a profile file. Because env blocks merge per-variable and `apiKeyHelper` is a top-level
settings key that profile files omit, user-level auth applies to all profiles unless a
profile overrides it.

## 7. Coexistence with the internal switcher

The company's internal switcher is not touched. It may keep writing
`~/.claude/settings.json`; every profile launch overrides the isolated keys via
`--settings`. Recommended one-time cleanup: strip `env`/`hooks`/`model` from
`~/.claude/settings.json`, leaving a shared baseline (auth may stay), so `/status` stays
readable and stale provider values do not linger. `doctor` flags stale endpoint/token
residue in global settings.

## 8. Implementation notes

- **Language**: bash (single script, zero dependencies, easy for a company to audit).
- **Core functions**: `create_profile` (scaffold `--settings` file, chmod 600), `launch`
  (`exec claude --settings`), `doctor` (JSON validation + diff vs global).
- **Profile files are static, generated once.** Editing a provider config means editing the
  profile file, running `ccbunshin model`, or regenerating from a template.
- **Idempotent create**: re-running `create` refreshes the template, never overwrites user
  edits without `--force`.
- **Concurrency**: two profiles share state by construction - Claude Code keys session
  transcripts by session id. The tool needs no locking.
- **Wrapper shells**: bash/zsh functions, tcsh aliases.

## 9. Testing

- **Empirical verification first** (the load-bearing assumptions, each a 5-minute test):
  - env blocks merge per-variable across `--settings` and user settings (set `FOO` in
    `~/.claude/settings.json` env, `BAR` in a `--settings` file, dump `env` from a
    `UserPromptSubmit` hook, assert both reach the session).
  - `hooks` in a `--settings` file replaces global hooks wholesale.
  - `permissions.defaultMode` behavior from `--settings` (docs note `auto`/`bypassPermissions`
    are restricted to user/managed scope).
- Unit: profile file generation, `model` edit, doctor diff.
- Integration: create `free` and `paid` profiles; launch both in the same repository; assert
  each routes to its own endpoint, hooks differ, and `projects/`/`history.jsonl` are shared
  (same files, no divergence).
- Regression: add a hook to `~/.claude/settings.json`; `doctor` flags profiles that do not
  define `hooks`; launch still isolates for profiles that do.

## 10. Phase 2 - model-routed proxy (implemented)

The `proxy/` Go service owns provider routing independently from LeanCTX. It starts one listener on the numeric `port` in a user-editable JSON config file, typically behind the single LeanCTX proxy listener on port 5000 or 4444.

Each Anthropic request must contain a model. Ordered glob routes select the provider: patterns without `*` are exact matches, while patterns such as `claude-*` match prefixes. Provider definitions contain upstream URLs, timeouts, and optional model rewrites. Authentication remains in Claude Code settings and environment through native mechanisms such as `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, or `apiKeyHelper`; the proxy config contains no credentials.

### Protocol translation (`dialect`)

A provider may declare `"dialect": "openai-chat"` when the upstream speaks OpenAI's `/chat/completions` API rather than Anthropic's `/v1/messages`. The proxy then translates one way only, Anthropic to OpenAI-chat, and translates the response back: `system`, content blocks, `tools`, and `tool_choice` on the request; `reasoning` to `thinking` blocks, `tool_calls` to `tool_use`, and `finish_reason` to `stop_reason` on the response. `/v1/messages/count_tokens` is answered locally with a character estimate, and upstream errors are rewrapped in Anthropic's error envelope at the same status.

Dialect resolution is layered, most-specific first: a route's `model_dialects` entry for the target model, then the route's `dialect`, then the provider's `dialect`, then `anthropic`. The default is pass-through, so a config written before dialects existed behaves exactly as it did. Because the dialect is a property of the model rather than the provider, one gateway serving both kinds of model can be expressed with two routes in different namespaces.

The translation deliberately never emits `reasoning_effort`. Deriving it from Anthropic's `thinking` is precisely the failure this replaces: the request is sent without any effort tier, and reasoning is recovered from the response's `reasoning` field instead. The `thinking` parameter itself is not forwarded either.

### Logging

The daemon logs through a level threshold set by `CCBUNSHIN_LOG`: `debug`, `info` (the default), `warn`, `error`. A line is written when its own level is at or above the threshold. `info` carries one line per request (requested and target model, provider, dialect, status, response bytes, elapsed time) plus startup and shutdown; `warn` carries requests the proxy itself rejects and reasoning dropped mid-stream; `error` carries upstream failures, streams cut short after their headers were sent, and any request answered 5xx; `debug` adds the routing decision, the upstream URL, and per-stream upstream usage including the cache hit count. An unrecognized value fails `proxy start` rather than logging nothing, and the CLI reads the same variable so a typo fails before a daemon is spawned. Credentials are never logged and URLs are redacted; no request or response body is written at any level. The startup line names the provider and route counts and the active level. Thresholding lives in `log.go`; the call sites are at the request path's decision points in `main.go` and `stream.go`.

See `examples/proxy.json` and `cmd/ccbunshin/README.md`. Configure the file with `CCBUNSHIN_PROXY_CONFIG`.

The requirements doc describes the custom proxy that maps Claude Code requests to the FREE and PAID gateways while keeping LeanCTX responsible only for context optimization.

## 11. Non-goals

- **No `CLAUDE_CONFIG_DIR` / symlink management** (v1 design, superseded).
- **No auth handling** (native Claude Code mechanisms, section 6).
- **No proxy in phase 1.** No LeanCTX integration (separate concern). No GUI. No binary
  version management.

## 12. Related work

- `luckybilly/cc-switch-helper` (`ccs`) - the same `--settings` mechanism; depends on
  cc-switch.
- `edimuj/claude-rig` - the `CLAUDE_CONFIG_DIR` design reference (v1), dormant.
- `claude-code-provider-gateway` (CCPG) - the proxy design reference for phase 2.