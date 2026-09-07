# Investigation: Does CCPG Solve the FREE/PAID Multi-Endpoint Problem?

> Date: 2026-09-05
> Scope: Evaluate whether this repo (`claude-code-provider-gateway`, CCPG) satisfies the requirements in
> `Claude Code Multi-Endpoint - Shared State Architecture Requirements.md` (the "Requirements").
> Method: source-level analysis of the daemon (routing, sessions, launch flow, config schema).

---

## 1. Verdict

**CCPG solves ~70% of the problem.** It provides the custom proxy, provider/model routing,
simultaneous per-session isolation, shared Claude Code state, and Linux/systemd support.

**It does NOT solve hooks / OTEL / company-settings isolation.** That is a hard requirement
(Requirements section 4, success criterion 4), so CCPG alone is not a complete solution.
The missing piece must be layered on top via `CLAUDE_CONFIG_DIR`-based profiles
(Requirements section 6) or a code change to the launcher.

The two gateways map cleanly onto CCPG as two **custom Anthropic-compatible providers**
(base URL + API key pointing at the company FREE and PAID gateways). The commands become
`ccpg --free-gateway` and `ccpg --paid-gateway` (custom provider slugs), wrapped as
`claude-free` / `claude-paid` aliases.

---

## 2. Requirement-by-requirement mapping

| Requirement | Status | Evidence / Notes |
|---|---|---|
| 1. Simultaneous FREE + PAID | **Yes** | Per-session launch tokens + config snapshots. See section 3. |
| 2. Same repository | **Yes** | Single daemon, shared `~/.claude`; session routing is token-scoped, not path-scoped. |
| 3. No global `~/.claude/settings.json` switching | **Yes** | CCPG never writes `~/.claude/settings.json`. Endpoint injected as per-process env vars. It does mutate its own daemon config on each launch, but session snapshots make that harmless to running sessions. |
| 4. Endpoint isolation | **Yes** | Per-session config snapshot selects the gateway. |
| 4. Model defaults/mapping | **Yes (with caveat)** | Per-session model catalog + primary-model memory. Caveat: global tier-routing rules leak across sessions (section 5, Gap B). |
| 4. **Hooks isolation** | **No** | Both sessions load the same `~/.claude/settings.json` -> same company hooks. |
| 4. **OTEL isolation** | **No** | Same settings file -> same OTEL env vars. |
| 4. Company-specific env/settings | **No** | `ProviderConfig` has no per-provider env injection (`config/schema.ts:72-92`). |
| 5. Shared state (skills/projects/history/todos) | **Yes** | Uses real `~/.claude`; no `CLAUDE_CONFIG_DIR` splitting. |
| 6. LeanCTX independent | **Yes** | CCPG never touches LeanCTX. Its RTK token-saver is separate and optional. |
| 7. Proxy does routing, not caching | **Yes** | That is the daemon's core job. |
| 8. Linux/systemd | **Yes** | Standalone bun-compiled binaries for linux-x64/arm64 (`packages/daemon/package.json` `compile:linux-x64`), binds `127.0.0.1`, Docker/Web mode. |

---

## 3. How the launch / session mechanism works

`ccpg --<provider>` is a shell function (installed by the panel's Terminal Integration) that:

1. Calls `POST /api/launch/prepare` on the panel port (`packages/daemon/src/panel/services/launch-prepare.ts`).
2. `prepareLaunch()` mutates the **daemon's own config** (`activeProvider`, `modelMode`), calls `saveConfig()`, clears `~/.claude/cache/gateway-models.json`.
3. Creates a session with a **unique launch auth token** and stores a **config snapshot**:
   `startSession()` clones the config into `sessionProfiles` (`packages/daemon/src/runtime/sessions/index.ts`, `startSession` / `cloneConfig`).
4. Returns shell-evaluable exports scoped to that subshell:
   `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_BASE_URL=http://127.0.0.1:<proxyPort>`,
   `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`, `CC_GATEWAY_SESSION_ID`
   (`launch-prepare.ts`, `buildShellExports`).
5. Runs `claude` in the subshell with those env vars.

On each request the proxy middleware resolves the session from the auth token
(`packages/daemon/src/proxy/middleware/auth.ts`, `resolveSessionIdFromAuthToken`), and
`MessageService.createMessage` routes using **the session's snapshot config, not the live
global config**:

```ts
const config = getSessionConfig(sessionId) ?? this.runtime.currentConfig();
```
(`packages/daemon/src/proxy/services/messages/message-service.ts`)

Per-session **primary-model memory**: when a session selects a model, background
`claude-haiku-*` / `claude-sonnet-*` / `claude-opus-*` calls from that session are redirected
to the session's chosen model (`setSessionPrimaryModel` / `getSessionPrimaryModel`,
`message-service.ts`). This is the per-process isolation mechanism; it is achieved within a
single daemon on a single proxy port rather than two ports.

---

## 4. What CCPG provides that matches the requirements

- **Custom proxy (Req 10, 11, 7).** The daemon IS the custom proxy: receives Anthropic
  Messages API requests, resolves model -> provider, translates protocols, streams responses
  back, propagates cancellation.
- **Model routing (Req 4).** Tier rules (`routing.opus/sonnet/haiku/default`,
  `config/schema.ts:149`), provider-prefixed model IDs (`anthropic/<provider>/<model>`),
  per-session primary-model memory, and Model Chains for fallback.
- **Two gateways as custom providers.** `ProviderConfig.custom.compatibility: "openai" | "anthropic"`
  (`config/schema.ts:86-91`). Both company gateways (Anthropic-compatible, since Claude Code
  currently talks to them) fit as custom providers with `baseUrl` + `apiKey`.
- **Shared state (Req 5).** No `CLAUDE_CONFIG_DIR` splitting; Claude Code state stays in
  `~/.claude`, shared across sessions.
- **Linux/systemd (Req 12, 8).** Standalone compiled daemon binary; binds `127.0.0.1`;
  Docker/Web mode with env-configured ports and `CCPG_CONFIG_DIR`.

---

## 5. Gaps and how to close them

### Gap A - Hooks / OTEL / company settings isolation (blocker)

The launcher sets only 4 env vars and does **not** set `CLAUDE_CONFIG_DIR`. Both sessions
therefore load the same `~/.claude/settings.json`, so company hooks, OTEL env, and any
company-specific settings apply to FREE and PAID alike.

Requirements section 6 already scopes the fix: `~/.claude-free/` and `~/.claude-paid/`
profiles with `settings.json` (env block with endpoint, hooks, OTEL) and symlinks for shared
state. CCPG does not implement this. Two options:

1. **Wrapper approach (no CCPG change).** Write `claude-free` / `claude-paid` shell functions
   that set `CLAUDE_CONFIG_DIR` per profile, then invoke the ccpg launch flow. Requires the
   symlink map investigation (which `~/.claude/*` paths are safe to share) - CCPG does not
   answer this.
2. **Extend CCPG.** Add per-profile `CLAUDE_CONFIG_DIR` export to the launcher/shell snippet.
   Larger change; still requires the symlink map.

### Gap B - Global tier-routing rules leak across sessions

`config.routing` is a single global set (`Record<RoutingTier, RoutingRule>`). If an
`opus -> DeepSeek` rule is enabled for FREE, PAID sessions inherit it for background tier
calls, because tier-source routing is not overridden by session primary-model memory.

**Safe configuration for this use case: leave tier rules disabled** and rely on per-session
model selection + primary-model memory, which is fully session-isolated.

### Gap C - Single daemon, single port

All sessions share one proxy port (default 49250). Requirements section 11 permits "a single
proxy process with explicit per-request profile routing... if it provides equivalent
isolation" - it does. If two fully independent daemons are ever needed, `CCPG_CONFIG_DIR`
plus per-config ports supports that (two systemd services), but it is not required.

### Gap D - Daemon config mutation on every launch

`prepareLaunch` sets `activeProvider` / `modelMode` in the daemon config on every launch
(last-write-wins). Running sessions are unaffected (snapshots), but any request using the
global auth token (not a session token) routes via the last-launched provider. Normal
`ccpg` usage always uses session tokens.

---

## 6. Migration notes

- The README warns that `ANTHROPIC_AUTH_TOKEN` / `ANTHROPIC_BASE_URL` in the
  `env` block of `~/.claude/settings.json` override the gateway and must be removed before
  using CCPG. The company's current cc-switch script writes those, so a one-time cleanup of
  the global settings is required.
- The FREE gateway must be Anthropic-compatible (or OpenAI-compatible for CCPG to translate).
  DeepSeek is Anthropic-compatible; the company FREE gateway already serves Claude Code, so
  it is assumed compatible. Same for the PAID gateway (Vertex Claude).

---

## 7. Recommended target architecture

```text
                            Linux VM
                               |
              +---------------+---------------+
              |                               |
        claude-free                      claude-paid
          |  CLAUDE_CONFIG_DIR=~/.claude-free   |  CLAUDE_CONFIG_DIR=~/.claude-paid
          |  (symlinks -> shared state)         |  (symlinks -> shared state)
          v                               v
   ccpg --free-gateway              ccpg --paid-gateway
          |                               |
          +--------------+----------------+
                         |
                 CCPG daemon (single, :49250)
                 session-scoped routing
                         |
          +--------------+----------------+
          |                               |
    FREE Gateway                    PAID Gateway
    Qwen / DeepSeek                 Vertex Claude

          Shared ~/.claude state (skills, projects, history, todos)
          LeanCTX MCP shared (context optimization, not routing)
```

Key separation (matches Requirements section 15):
- **Claude Code configuration** -> FREE/PAID isolation via `CLAUDE_CONFIG_DIR` profiles (Gap A)
- **Custom proxy** -> CCPG daemon (gateway/model routing)
- **LeanCTX** -> context caching / token saving, untouched by CCPG
- **Claude Code state** -> shared via symlinks

---

## 8. Source evidence

| Claim | Location |
|---|---|
| Launch mutates daemon config, creates session | `packages/daemon/src/panel/services/launch-prepare.ts` (`prepareLaunch`) |
| Session stores config snapshot | `packages/daemon/src/runtime/sessions/index.ts` (`startSession` -> `cloneConfig`) |
| Auth token resolves to session | `packages/daemon/src/proxy/middleware/auth.ts` |
| Routing uses session snapshot config | `packages/daemon/src/proxy/services/messages/message-service.ts` |
| Primary-model memory for background calls | `packages/daemon/src/proxy/services/messages/message-service.ts` |
| Global tier routing rules | `packages/daemon/src/config/schema.ts:149` (`Config.routing`) |
| Custom provider shape (no env injection) | `packages/daemon/src/config/schema.ts:72-92` (`ProviderConfig`) |
| Config dir / paths | `packages/daemon/src/config/paths.ts` (`CCPG_CONFIG_DIR` support) |
| Standalone Linux binaries | `packages/daemon/package.json` (`compile:linux-x64`, `compile:linux-arm64`) |
| Docker/Web env-configured ports | `docker-compose.yml` |

---

## 9. GitHub alternatives comparison

> Added 2026-09-05. Searched GitHub (`gh search repos`) for existing projects that solve the
> same problem. The core discovery: `claude --settings <file-or-json>` is the documented
> per-process isolation mechanism.

### The `--settings` mechanism (key discovery)

Claude Code settings precedence (highest first): Managed > **Command line (`--settings`)** >
Project local > Shared project > User. Per the official docs:

> "Claude Code merges JSON you pass with `--settings` with your settings files by the same
> rules as the other levels: it takes a key you set here over the same key in local, project,
> or user settings, and keeps the lower-level value for a key you omit."

Consequences for this problem:

- **Endpoint/env isolation: works.** A per-process settings file's `env` block overrides the
  global `~/.claude/settings.json` env block.
- **Hooks isolation: works only if the per-process file explicitly defines `hooks`.** Hooks
  omitted from `--settings` fall through to the global settings. So company hooks must live in
  per-provider settings (or be blanked per profile), not only in `~/.claude/settings.json`.
- **`CLAUDE_CONFIG_DIR` is all-or-nothing.** It relocates settings, session history, plugins,
  and `.claude.json` together. It is not settings-only isolation; shared-state symlinks
  (Requirements section 6) are still required if used.

### Ranked candidates

Two families solve the per-process problem differently:
- **`--settings` family** (`ccs`): pass the full per-provider settings as a CLI flag. State stays in `~/.claude`, always shared. Nothing global is written.
- **`CLAUDE_CONFIG_DIR` family** (claude-rig, cps, silo, clausona, claude-profile): each profile is its own config directory. Settings are fully isolated by default; state is isolated or shared depending on the tool.

| Rank | Repo | Stars | Approach | Fits? |
|---|---|---|---|---|
| 1 | `edimuj/claude-rig` | 8 | Per-rig `CLAUDE_CONFIG_DIR`. `settings.json`, `CLAUDE.md`, `.claude.json` always per-rig; hooks/skills/agents per-rig with global inheritance; conversations/history/todos/projects isolated by default but **shareable per-item** (`claude-rig share <rig> <items>`). Launches via `exec` with `CLAUDE_CONFIG_DIR` set - two rigs run simultaneously. Linux full support, single Go binary. | **Best fit** - config always isolated (hooks/OTEL/env per rig), state shareable on demand, no global settings writes, no cc-switch dependency, per-project selection available |
| 2 | `luckybilly/cc-switch-helper` (`ccs`) | 40 | Reads CC-Switch SQLite DB only; passes the **entire** effective settings (env + hooks + statusLine + plugins) via `claude --settings <json>` per terminal; never writes `~/.claude/settings.json`. State always shared. Linux (bash/zsh/fish). | Strong fit if the company uses cc-switch - simplest state-sharing model; hooks must be configured per provider in the cc-switch DB |
| 3 | `guibes/claude-profile-switch` (`cps`) | 5 | Per-terminal `CLAUDE_CONFIG_DIR` profiles, git-backed with age encryption. "Run `work` in one terminal and `personal` in another." | Per-terminal works; no per-item share control (state isolated per profile) |
| 4 | `0xNyk/silo` | 5 | Per-invocation `CLAUDE_CONFIG_DIR` (`silo run work`). | Isolates history per profile - state not shared |
| 5 | `quinnjr/claude-code-profiles` | 92 | Per-session `CLAUDE_CONFIG_DIR` via a `claude()` wrapper; shared skills pool. | Per-session; history/state isolated per profile |
| 6 | `larcane97/clausona` | 44 | Profile switcher; MCP/plugins/settings symlinked shared. | Global switcher - one active profile, not simultaneous |
| 7 | `guyskk/claude-code-config-switcher` (`ccc`) | 85 | Standalone binary. Per-process **env** via `--settings`, BUT writes merged settings back to `~/.claude/settings.json` and stores `current_provider` in `ccc.json` on every switch. | **Global switcher** - env isolated per-process only; hooks/permissions/plugins and current-provider state are global (last-switch-wins). Not a fit for per-process isolation |
| 8 | `Danielmelody/ccconfig` | 62 | npm. Per-profile `start` with `ANTHROPIC_BASE_URL`/`AUTH_TOKEN`/`MODEL` env vars. | Simple - no hooks handling |
| 9 | `musistudio/claude-code-router` (CCR) | ~37k | Single local endpoint proxy/control plane. Routing is model-name-based, not per-process. | Same isolation gap as CCPG + heavier desktop dependency |
| 10 | `farion1231/cc-switch` | ~131k | Global switcher (the tool the company's script resembles). Has a newer `cc-switch start claude <name>` subcommand. | Helpers above are the actual per-terminal fix |

### Recommendation

**Activity check (2026-09-05):** every small tool in this space is quiet - claude-rig last
commit 2026-06-08, `ccs` 2026-07-10, `cps` 2026-04-21. Only the large projects are active
(CCPG, CCR, cc-switch), and none of those isolate hooks. A dormant single-maintainer tool is
a real risk for a company production setup.

**Recommended path: build a small in-house wrapper** using the documented mechanisms, with
claude-rig as the design reference. The mechanism is stable and documented (`--settings`,
`CLAUDE_CONFIG_DIR`, symlinks), the wrapper is ~50-100 lines, and there is no third-party
dependency risk. Two routes:

1. **`CLAUDE_CONFIG_DIR` + symlinks route** (claude-rig's design, Requirements section 6):
   `~/.claude-free/` and `~/.claude-paid/`, each with its own `settings.json` (endpoint, hooks,
   OTEL) and symlinks for shared state (projects, history, todos, skills, plugins). Claude Code
   reads only the profile dir's `settings.json`, so hooks/OTEL are fully isolated; state stays
   shared via symlinks. `claude-free` / `claude-paid` are one-line wrappers that set
   `CLAUDE_CONFIG_DIR` and exec claude.
2. **`--settings` route** (`ccs`'s design): per-process settings file with env + hooks passed
   via `claude --settings`. State always shared (in `~/.claude`). Simpler, but `~/.claude/settings.json`
   is still read for omitted keys, so the per-process file must explicitly set `hooks` to blank
   the global ones.

**Adopting an existing tool** is still reasonable in two cases: `ccs` if the company already
uses cc-switch (accepting a 2-month-old repo), or claude-rig if the team is willing to fork
and maintain it (its isolation model is the best match and the codebase is small Go stdlib).

The proxy question (Requirements sections 10-11) only matters if the gateways need protocol
translation, or local tier-to-model mapping / request logging / fallback is required. If the
company gateways are Anthropic-compatible (they are - Claude Code talks to them directly
today), direct connection is strictly simpler and tier mapping stays the gateway's job. If a
proxy is still wanted for tier mapping, combine both: per-profile `--settings` env points
`ANTHROPIC_BASE_URL` at CCPG/CCR, and CCPG/CCR does the routing.

### Source evidence (GitHub search)

| Claim | Location |
|---|---|
| `--settings` merge semantics (per-key override, lower-level kept for omitted keys) | code.claude.com/docs/en/settings |
| `CLAUDE_CONFIG_DIR` relocates the whole `~/.claude` tree | code.claude.com/docs/en/settings, /docs/en/claude-directory |
| `ccs` reads only CC-Switch DB, never `~/.claude/settings.json` | github.com/luckybilly/cc-switch-helper (`src/db.js`, `src/launcher.js`) |
| `ccc` writes merged settings to `~/.claude/settings.json` and `current_provider` to `ccc.json` on every switch | github.com/guyskk/claude-code-config-switcher (`internal/provider/provider.go`, `internal/config/config.go`) |
| claude-rig: settings/CLAUDE.md/.claude.json always per-rig; state shareable per-item; simultaneous via `CLAUDE_CONFIG_DIR` + `exec` | github.com/edimuj/claude-rig (`docs/isolation.md`, README) |
| cps: per-terminal `CLAUDE_CONFIG_DIR` profiles, git-backed | github.com/guibes/claude-profile-switch (README) |
| silo: per-invocation `CLAUDE_CONFIG_DIR`, history isolated per profile | github.com/0xNyk/silo (README) |
| clausona: global switcher, shared env via symlinks | github.com/larcane97/clausona (README) |

---

## 10. Deep-dive pros/cons (source-level)

> Added 2026-09-05. Cloned claude-rig, cc-switch-helper, claude-profile-switch into a scratch
> dir and read the actual implementations.

### claude-rig (from `paths.go`, `main.go`, `commands.go`)

Isolation map (concrete): always per-rig = `settings.json`, `skills`, `plugins`, `agents`,
`hooks`, `CLAUDE.md`; auth shared via symlinked `.credentials.json` + `statsig`; everything
else in `~/.claude/` symlinked (shared) by default, but 21 state items (`projects`,
`history.jsonl`, `todos`, `conversations`, `sessions`, ...) are **isolated by default** and
must be explicitly shared with `claude-rig share <rig> <items>`. Launch sets
`CLAUDE_CONFIG_DIR=<rigDir>`, adds `--add-dir ~/.claude` (loads global CLAUDE.md), then
`exec`s claude. No global active state - each launch names its rig explicitly.

| Pros | Cons |
|---|---|
| `settings.json` + hooks always per-rig -> full hooks/OTEL isolation; global settings bypassed | Dormant ~3 months (last commit 2026-06-08, v0.31.0) |
| Explicit per-item share/isolate for state | Default isolates state (projects/history/todos) - opposite of the Requirements' default; must `share` each item per rig |
| Simultaneous rigs; no writes to `~/.claude/settings.json` or `$HOME/.claude.json` (per-rig `.claude.json` seeded) | Injects a bundled plugin into every launch (`--plugin-dir`) - extra component to audit |
| Auth sharing via `--link-auth` | Manages claude binary versions (pinning/latest-on-disk) - extra complexity |
| Linux full support, single Go binary, stdlib only, has tests | Always loads global `~/.claude/CLAUDE.md` via `--add-dir` |
| Per-project auto-selection (`.claude-rig` file) | 9 stars, 2 forks - tiny community |

### cc-switch-helper / `ccs` (from `launcher.js`, `db.js`)

| Pros | Cons |
|---|---|
| Simplest: full settings via `claude --settings <json>`, never writes `~/.claude/settings.json` | Requires cc-switch (the tool that rewrites ANTHROPIC_BASE_URL - see CC_SWITCH_BASE_URL_PIN.md) |
| State always shared - no isolation to undo | Hooks/OTEL isolated only if per-provider config in the cc-switch DB explicitly sets them; global settings hooks still apply otherwise (`--settings` is a per-key merge) |
| Hooks/statusLine/plugins per provider via cc-switch DB | Depends on cc-switch DB schema (fragile to cc-switch changes) |
| 41 stars, last commit 2026-07-10 | 2 months quiet |

### claude-profile-switch / `cps` (from `bin/cps`, `lib/core.sh`, `lib/shell.sh`)

| Pros | Cons |
|---|---|
| `CLAUDE_CONFIG_DIR` per profile, git-backed, age-encrypted credentials | Global switcher: `cps use` writes the active profile to a global file and replaces `$HOME/.claude.json` - not truly simultaneous |
| Sharing via `cps link <item> <profile>` | Dormant 4.5 months, 18 open issues for 5 stars |
| Per-shell activation via hook | Bash + git + encryption + sync = more moving parts |

### Verdict

claude-rig is still the best design (per-rig `settings.json`/hooks + explicit state sharing is
exactly the target architecture), but its default-isolated state, bundled-plugin injection,
and 3-month dormancy make adoption risky. For a company, the strongest path is a small
in-house wrapper modeled on claude-rig's isolation map - see `CCBUNSHIN_IMPLEMENTATION.md`.