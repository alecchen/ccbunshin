# Plan: extract the publish gate as `claude-publish-guard`, and audit the current gate

Working note, 2026-09-14. Two questions were asked of the existing harness in
`docs/HARNESS_PUBLISH_GATING.md`:

1. Can layers 1-3 be split out into a standalone repo that serves both human
   users (as a Claude Code plugin) and this repo's CI (as a pinned dependency)?
2. Do the installed layers actually cover every repository on this machine today?

Answer to 1: partly. Three items in the original plan do not work as written, and
one phase rests on a premise the code contradicts. Answer to 2: mostly, with two
concrete holes found by auditing the local config. Both are below.

Per-machine audit specifics are deliberately generalized here (no profile names,
no local repository names) because this file is intended to be commit-safe under
the repo's public-release rule. The mechanism is the durable part.

## What was verified, and how

| Claim | Evidence |
| --- | --- |
| Settings scopes and their precedence | `code.claude.com/docs/en/settings`, `.../permissions` |
| Plugin manifest field list | `code.claude.com/docs/en/plugins-reference` |
| Plugin hook file shape | Real `hooks/hooks.json` in installed plugins under `~/.claude/plugins/cache/` |
| Marketplace schema and source forms | `.../plugin-marketplaces`, plus the 296-entry official catalog |
| `hooks` replaces wholesale | `docs/DECISIONS.md` decisions 1 and 3 |
| No settings-generation step in this repo | `cmd/ccbunshin/main.go`, `buildClaudeCommand` |
| The hook script is not in this repo | It lives only at `~/.claude/hooks/publish-gate.sh` |

## Part 1: the extraction

### Blockers

**`plugin.json` cannot carry permission rules.** The documented field list has no
`permissions` and no general `settings` block: `name, displayName, version,
description, author, homepage, repository, license, keywords, metadata,
defaultEnabled, skills, commands, agents, workflows, hooks, mcpServers,
outputStyles, lspServers, experimental.*, userConfig, channels, dependencies,
$schema`. The one settings-ish file a plugin ships is `<plugin-root>/settings.json`,
and it supports **only** `agent` and `subagentStatusLine`; unknown keys are
silently ignored. So layer 1/2 stays a hand-copied fragment under every
installation path, and the README table's layer 1/2 column is "no" everywhere.

**The CI invariant in the original Phase 3 is impossible as written.** `hooks` is
a wholesale key replacement and `--settings` sits above user settings, so any
profile that sets `hooks` at all blanks the plugin's and the user's registrations
for that session. Both shipped example profiles do exactly this (`"hooks": {}`).
There is also no generation step to test: `buildClaudeCommand` passes the profile
file itself to `--settings`; profiles are verbatim settings files, and the only
writes are a scaffold, a model edit, and an atomic file replace. Asserting that
"the generated settings.json's matcher covers every shell-capable tool" has
nothing to assert against.

**The proposed extension mechanism is wrong.** `PUBLISH_GATE_EXTRA_TOOLS` adds
nothing: the script classifies a command string and never sees which tool invoked
it. And the matcher is author-time fixed - there is no documented way for a user
to widen a plugin-provided hook's matcher. The working path is what the doc
already says: ship the `settings.json` registration as a second copy-paste
fragment, matcher anchored as `^(Bash|your_other_shell_tools)$`.

### Open, and cheap to settle

Plugin hooks are a **separate source** that "merge with your user and project
hooks". Whether a `--settings` file's `"hooks": {}` also blanks plugin hooks is
untested, and it decides whether the plugin path covers a wrapped session at all.
Settle it before writing any CI integration:

```sh
# scratch repo, plugin installed, run with a --settings file containing "hooks": {}
# fire a publish-shaped command; if the hook's decision appears, plugin hooks survive
```

### Feasibility per phase

| Phase | Verdict |
| --- | --- |
| 0 - name and scope | Fine. `claude-publish-guard` is free; handle is `alecchen`. |
| 1 - layout | Sound, but the artifact set is wrong. See below. |
| 2 - package as plugin | Feasible for the hook. Steps on extension and manifest permissions are as above. |
| 3 - wire into CI as a dependency | Not feasible as written. |
| 4 - README split | Feasible; the layer 1/2 answer is now known (no). |

Phase 1 corrections:

- The doc does not move verbatim. Roughly 200 lines (`## Why permissions.ask
  cannot gate an MCP shell`) are built around one specific MCP shell tool, plus
  17 tool-name occurrences, two home paths, and seven "on this machine" claims
  including a `permission_mode` observation. That section gets rewritten against
  a generic MCP-shell example.
- Two load-bearing files have no home in the layout because they are not in the
  repo at all: **`scripts/publish-gate.sh`** (only in `~/.claude/hooks/`) and the
  **settings registration fragment**. Without both, the repo ships a test matrix
  for a script it does not contain.
- `tests/publish-scan.sh` is repo-agnostic apart from its `tests/` path prefixes
  and does belong with the harness.
- Two staleness bugs to fix while moving: the doc shows the `publish-scan` job
  running only the test (it runs the scanner then the test), and says the
  allowlist holds one entry (it holds two).

Phase 3 alternatives, both honest:

- **(a) The profile declares the hook.** A profile that wants the guard carries
  its own `hooks.PreToolUse` block pointing at the vendored script. Then the CI
  assertions become tractable. Unresolved prerequisite: profiles are generic
  artifacts, so an absolute installed path is machine-specific; that has to be
  designed first.
- **(b) The guard never enters profiles.** Accept that a wrapped session has no
  local publish gate and rely on layer 4 plus branch protection. Cheaper and
  truthful, but then CI tests nothing about the guard.

Submodule mechanics: a repo cannot pin its own commit. A submodule inside this
repo pointing at another repo does work, but note that a submodule is a nested
git repository, and workspace trust is keyed per repository and does **not** cover
a nested one. Pin by fetching the tag in CI with the tag recorded in a tracked
file and re-verify on bump, or invert the dependency and run the integration test
in `claude-publish-guard` via a dispatch from this repo.

### Missing entirely: default-branch protection

The second incident was an agent pushing `main`. Nothing in layers 1-5, a plugin,
or a submodule catches a direct push to the default branch; layer 4 only gates the
release job that a *tag* push triggers. This is a layer 0 costing one repo setting
and is strictly stronger than anything the harness ships.

### Plugin facts worth carrying into the README

- **A plugin hook cannot be selectively disabled.** Only `disableAllHooks`
  (all-or-nothing) or an admin's `allowManagedHooksOnly`. Real adoption cost.
- **The matcher is three-way dispatch, not regex.** A matcher made only of
  letters, digits, `_`, `-`, spaces, `,`, `|` is an exact-name list; regex engages
  only when another character is present. So `^Bash$` works but is not equivalent
  to `Bash`, and a typo'd tool name in list form never matches and says nothing.
- **`if` is a per-handler field** using permission-rule syntax, exactly one rule,
  no `&&`/`||`.
- **`permission_mode`'s vocabulary is six and closed:** `default`, `plan`,
  `acceptEdits`, `auto`, `dontAsk`, `bypassPermissions`. The script's escalation
  set (`bypassPermissions|dontAsk`) is therefore complete as written.
- **A hook's `allow` is honored even in `dontAsk`**, where every other
  would-prompt call is auto-denied. A hook that fails open is still safe there,
  which is a real argument for the current fail-open design.
- Name collisions: `tarotene/publish-guard` is the closest prior art;
  `claude-plugins-official`, `claude-plugins-community`, `claude-community`, and
  `anthropic-plugins` are reserved names.

## Part 2: does the gate cover every repo today?

The user-scope file is what makes this global: it is read "in every project on
this machine", and both the rules and the hook registration live there.

```
hooks.PreToolUse: matcher "^(Bash|<your mcp shell tool>)$" -> ~/.claude/hooks/publish-gate.sh
permissions.ask:  git push / tag, gh release / api / repo delete / pr merge
permissions.deny: git push --no-verify (two rules)
```

Deny is unconditional: "If a tool is denied at any level, no other level can allow
it. ... deny rules from any scope are evaluated before allow rules."

### The rule that produces both holes

Precedence is per key, highest first: managed, command line (`--settings`), then
**project-local**, then **shared project**, then **user**. Both project scopes
outrank user scope. Two consequences:

**Hole 1 - a project-level `allow` can defeat a user-level `ask`.** An `allow`
rule for a publish spelling in a project settings file, against an `ask` rule for
the same spelling at user scope, means the prompt likely does not fire in that
repository. Deny rules still hold there. Any repo carrying such an allow has this.

**Hole 2 - any project `hooks` key silently drops the hook.** `hooks` is a
wholesale key replacement, which is the same mechanism this repo's profiles use
deliberately. A project settings file that defines `hooks` at all - for any
event, even an unrelated one - replaces the user-level `hooks` object and takes
the registration with it. Repos whose settings files have no `hooks` key are
unaffected.

### Which `claude` you get depends on the shell

The wrapper is installed in two shells on this machine and not in the third, and
only one repository carries a project marker. So the same directory resolves
differently by launch path: a wrapped shell applies a `--settings` profile, while
bash, the IDE extension, and the desktop app read user settings directly.

Whether that matters depends on the profile. A profile with no `hooks` key does
not blank the registration and its permission lists merge, so it is equivalent
today. A profile carrying `"hooks": {}` - which is exactly what `profile create`
scaffolds - removes the gate for that session. The scaffold is the thing to
watch.

### Two probes to run

Both point at a remote that cannot resolve, so approving costs nothing:

```sh
# hole 2: in a repo whose settings define a hooks key, does the gate fire?
git push /nonexistent-path main

# hole 1: in a repo whose settings allow a publish spelling, does the ask survive?
git push /nonexistent-path main
```

If hole 2 reproduces, the fix is to fold the publish-gate `PreToolUse` block into
that project's own `hooks` object, or drop the project's `hooks` key in favour of
user scope. If hole 1 reproduces, drop the project-level `allow` for publish
spellings - deny still holds, but the prompt is the layer that was lost.

## Suggested order of work

1. Run the two probes above and the plugin-hooks-vs-`--settings` probe. All three
   are read-only and decide the shape of everything else.
2. Add default-branch protection. One setting, catches the failure nothing else
   does.
3. Decide Phase 3 (a) or (b) before creating the new repo, since it determines
   whether the repo needs a path-portability story at all.
4. Build `claude-publish-guard` with the hook script, the registration fragment,
   and the corrected doc as its artifacts - not the doc alone.
5. Keep the README's layer table honest: the plugin path gives layer 3 only.
