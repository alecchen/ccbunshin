# Prior art: per-process Claude Code isolation

Existing tools that solve the same problem ccbunshin solves, and why ccbunshin did not adopt one.
Surveyed 2026-09-05, before ccbunshin was written; the comparison is kept because two of the
entries remain the closest designs to this one, and because "why not an existing tool" comes up
for every tool in this space.

The mechanism ccbunshin uses is decision 1 in `DECISIONS.md`. This file is the survey behind it.

## Two families

- **`--settings` family** - pass the per-provider settings as a CLI flag on each launch. State
  stays in `~/.claude` and is shared by construction. Nothing global is written.
  (`luckybilly/cc-switch-helper`, `guyskk/claude-code-config-switcher`)
- **`CLAUDE_CONFIG_DIR` family** - each profile is its own config directory. Settings are fully
  isolated by default; state is isolated or shared depending on the tool.
  (`edimuj/claude-rig`, `guibes/claude-profile-switch`, `0xNyk/silo`, `quinnjr/claude-code-profiles`)

ccbunshin chose the first family. See decision 1 for why the second was rejected.

## Comparison

| Rank | Repo | Approach | Fits? |
|---|---|---|---|
| 1 | `edimuj/claude-rig` | Per-rig `CLAUDE_CONFIG_DIR`. `settings.json`, `CLAUDE.md`, `.claude.json` always per-rig; hooks/skills/agents per-rig with global inheritance; conversations/history/todos/projects isolated by default but shareable per item (`claude-rig share <rig> <items>`). Launches via `exec` with `CLAUDE_CONFIG_DIR` set, so two rigs run simultaneously. | Best design match - config always isolated, state shareable on demand, no global writes. Rejected as a dependency, not as a design: dormant, and its default-isolated state is the wrong default for this problem. |
| 2 | `luckybilly/cc-switch-helper` (`ccs`) | Reads the cc-switch SQLite DB only; passes the entire effective settings (env + hooks + statusLine + plugins) via `claude --settings <json>` per terminal; never writes `~/.claude/settings.json`. | Strong fit, and the same mechanism ccbunshin uses. Rejected because it depends on cc-switch: it has no way to express a profile that does not exist in that DB. |
| 3 | `guibes/claude-profile-switch` (`cps`) | Per-terminal `CLAUDE_CONFIG_DIR` profiles, git-backed with age encryption. | Global switcher: `cps use` replaces `$HOME/.claude.json`, so not truly simultaneous. |
| 4 | `0xNyk/silo` | Per-invocation `CLAUDE_CONFIG_DIR`. | Isolates history per profile - state not shared. |
| 5 | `quinnjr/claude-code-profiles` | Per-session `CLAUDE_CONFIG_DIR` via a `claude()` wrapper; shared skills pool. | Per-session; history isolated per profile. |
| 6 | `larcane97/clausona` | Profile switcher; MCP/plugins/settings shared via symlinks. | Global switcher - one active profile, not simultaneous. |
| 7 | `guyskk/claude-code-config-switcher` (`ccc`) | Per-process env via `--settings`, but writes merged settings back to `~/.claude/settings.json` and `current_provider` to `ccc.json` on every switch. | Not a fit: hooks, permissions, and plugins stay global (last-switch-wins). Violates decision 6. |
| 8 | `Danielmelody/ccconfig` | Per-profile `start` with `ANTHROPIC_BASE_URL` / `AUTH_TOKEN` / `MODEL` env vars. | No hooks handling. |
| 9 | `musistudio/claude-code-router` (CCR) | Single local endpoint proxy. Routing is model-name-based, not per-process. | The proxy half of this problem, not the isolation half. |
| 10 | `farion1231/cc-switch` | Global switcher. | Not per-process isolation at all. |

Star counts and last-commit dates are omitted deliberately: both move, and neither decides the
question. What decides it is the two families above and whether state stays shared by construction.

## Source evidence

| Claim | Location |
|---|---|
| `--settings` merges per-key; lower-level value kept for omitted keys | code.claude.com/docs/en/settings |
| `CLAUDE_CONFIG_DIR` relocates the whole `~/.claude` tree | code.claude.com/docs/en/settings, /docs/en/claude-directory |
| `ccs` reads only the cc-switch DB, never `~/.claude/settings.json` | github.com/luckybilly/cc-switch-helper (`src/db.js`, `src/launcher.js`) |
| `ccc` writes merged settings to `~/.claude/settings.json` and `current_provider` to `ccc.json` on every switch | github.com/guyskk/claude-code-config-switcher (`internal/provider/provider.go`, `internal/config/config.go`) |
| claude-rig: settings/`CLAUDE.md`/`.claude.json` always per-rig; state shareable per item; simultaneous via `CLAUDE_CONFIG_DIR` + `exec` | github.com/edimuj/claude-rig (`docs/isolation.md`, README) |
| `cps`: per-terminal `CLAUDE_CONFIG_DIR` profiles, git-backed | github.com/guibes/claude-profile-switch (README) |
| `silo`: per-invocation `CLAUDE_CONFIG_DIR`, history isolated per profile | github.com/0xNyk/silo (README) |
| `clausona`: global switcher, shared env via symlinks | github.com/larcane97/clausona (README) |
