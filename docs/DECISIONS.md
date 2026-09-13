# Decision log

The settled design decisions for ccbunshin. Each entry records what was decided and, where it
matters, why - usually because the rejected alternative looks reasonable until you know what it
costs, or because the behavior is not what the published upstream docs describe.

Read this before changing CLI behavior, the isolation mechanism, or the proxy.

**Do not silently reverse one.** If a change requires reversing a decision, say so rather than
making it quietly. The numbers are stable: other documents cite them (`decision 4`, `decision 9`),
so append new decisions and never renumber.

## 1. Isolation mechanism: per-profile `--settings` files, not `CLAUDE_CONFIG_DIR`

`--settings` sits at command-line precedence (above project/user settings, below managed). It is
a per-key merge: a key set there replaces the same key in lower levels; omitted keys fall through.
State paths (`~/.claude/projects`, `history.jsonl`, `todos/`, `skills/`, `$HOME/.claude.json`) are
never relocated - shared by construction.

`CLAUDE_CONFIG_DIR` was rejected because it is all-or-nothing: it relocates settings, session
history, plugins, and `.claude.json` together, so it is not settings-only isolation and would force
a version-fragile symlink map to restore the shared state. For the same `--settings` mechanism in
other tools, see `docs/PRIOR_ART.md`.

## 2. Env blocks merge per-variable, verified

An `env` block beats the process environment: exporting a variable in the shell does not override
the same key in a settings file, which is why per-profile isolation has to happen at the settings
layer rather than through exported variables.

Two rules govern the merge, and both matter: a variable set in two files resolves to the
higher-precedence one, and a variable set *only* in a lower file survives - a higher file's `env`
block does not blank the lower one. That is what lets a profile name one variable and inherit the
rest of the user's env block, which is the whole premise of decision 1.

Verified 2026-09-12 with a `SessionStart` hook dumping `env` from a session launched with
`--settings`: a probe variable set only in user settings and one set only in `--settings` both
reached the session, and a variable set in both resolved to the `--settings` value.

Note the published docs do not state the second rule - `settings.md`'s "an `env` block inside a
settings file is an ordinary key and follows the levels above" plus "Lists merge instead of
overriding" reads as wholesale replacement, which is wrong. Re-probe rather than trusting that
reading.

## 3. Isolation map

`hooks`, `model`, `statusLine` are isolated per profile (wholesale key replacement; `hooks: {}`
blanks company hooks). `permissions.allow` and other list keys merge across scopes - cannot
subtract via `--settings`. Managed settings outrank `--settings`.

## 4. Auth is out of scope

Claude Code's native mechanisms handle it (`ANTHROPIC_AUTH_TOKEN` > `ANTHROPIC_API_KEY` >
`apiKeyHelper`). ccbunshin never reads or writes credentials.

## 5. Scope

Config isolation, directory-local (`local`) profiles, a global fallback profile (`global`),
project-aware `claude` shell wrappers, the model-routed proxy, and self-update are implemented.
Still out of scope: LeanCTX integration, GUI.

## 6. The internal switcher

The company tool writes global `~/.claude/settings.json` and stays untouched; profile launches
override it via `--settings`.

## 7. The project-aware wrapper is a thin router

A `.ccbunshin-profile` marker (the same file `ccbunshin local` writes) selects a project. Bash and
zsh wrappers call `ccbunshin resolve-provider` and route to `ccbunshin launch <provider>`. tcsh
cannot express a conditional alias, so its wrapper delegates to the internal `ccbunshin run`, which
resolves the provider or falls back to the original `claude` binary.

Resolution is `findProfile`: the nearest marker, then the global selection from `ccbunshin global`,
then nothing - so the global is a fallback and never overrides a project. Shell code never parses
profile files and never maintains a Claude option list: `launch` forwards arguments unchanged and
returns Claude's exit status.

## 8. The proxy's protocol translation is opt-in per provider

And it never derives `reasoning_effort` from `thinking`. `dialect` on a provider, route, or
`model_dialects` entry selects between `anthropic` (default, byte-for-byte pass-through) and
`openai-chat` (Anthropic `/v1/messages` translated onto OpenAI `/chat/completions`). Resolution is
most-specific-first, and because a dialect belongs to a model rather than a provider, one gateway
serving both kinds is expressed as two routes in different namespaces.

**Deriving** `reasoning_effort` from Anthropic `thinking` is the bug this replaces, so `thinking`
is never forwarded and reasoning is recovered from the upstream response instead. A caller-**stated**
effort is a different thing and is forwarded: Claude Code sends `output_config.effort` on every
effort-capable request, and dropping it made `/effort`, `--effort`, and `effortLevel` silently do
nothing (verified 2026-09-12 with a stub upstream: `--model sonnet --effort max` sends
`{"effort":"max"}`).

The config's `effort` key is a **default for requests that state none, never an override** - Claude
Code re-sends the session's value each turn, so an override would make `/effort` a no-op, while the
traffic that states nothing (Haiku-class requests, which carry `output_config.format` and no
effort) is exactly what a default can reach.

Credentials are forwarded, never stored (reaffirms decision 4). Translation lives in `dialect.go`,
`translate.go`, and `stream.go`.

## 9. Usage accounting follows Anthropic's split, not the upstream's

An OpenAI-chat upstream reports an inclusive `prompt_tokens` plus
`prompt_tokens_details.cached_tokens`; `anthropicUsageFromUpstream` reports
`cache_read_input_tokens` as the hit count and `input_tokens` as `prompt_tokens - cached`, clamped
at zero, because Anthropic counts cache reads separately. The sum of the three input fields is
therefore the upstream's `prompt_tokens`, which is what a client's context denominator depends on.

Do not "simplify" this back to passing `prompt_tokens` through as `input_tokens`: the same prefix
would then be counted twice and every cache percentage would read 0 again.
`cache_creation_input_tokens` has no upstream counterpart and is always `0`. Reported usage
`message_delta`.

## 10. Profiles are written atomically and validated as objects

A profile is handed to Claude Code with `--settings`, so its bytes are a contract with another
program. Two properties follow, and both are enforced in `parseProfile` and `writeProfileFile`:

The top level must be a JSON object. `json.Valid` is not enough: it accepts `null`, `[]`, and
scalars, and Claude Code answers any of those with its "Settings Error" dialog naming the file
rather than a message the user can act on. That dialog's `Expected object, but received undefined`
line is a formatter fallback, not a description of the file - it is what every syntax-level failure
renders as, which is why it names no useful value.

Writes replace the file in one step (temp file plus `os.Rename`), so a reader sees the old profile
or the new one and never a truncated write. Profiles are read early: a login can read one within
seconds of a user session starting, while an update may be in flight. A half-written file reads as
invalid JSON, and before this the only symptom was Claude Code's dialog.

The rejected alternative was leaving both to the callers - `create` checking `json.Valid` while
`model` wrote back whatever it had read. That is the same check in several places with two different
answers, and it still lets a file through that Claude Code refuses to start with.

`launch` re-reads and re-validates the profile immediately before `exec`, so a profile that goes bad
is reported by name instead of opening a dialog. That check is shape-only: it does not duplicate
Claude Code's key validation, and unknown keys stay Claude Code's business.

## 11. `proxy stop` confirms the daemon is gone, and `restart` exists

`stop` reports the PID it signalled, waits for the process to disappear, and fails if it does not,
rather than deleting the PID file and printing "stopped" whatever happened. The wait matters because
the daemon shuts down with a 5s bound of its own: removing the PID file on the way out made a proxy
that was still draining connections, or still listening, indistinguishable from a stopped one - the
next `start` could then see a bound port, and a `restart` would race its own predecessor.

A signal that cannot be delivered at all is reported as a failure naming the PID, not swallowed:
"the process is there and it is not ours to stop" is precisely what the PID file cannot show. A
process that is already gone when the pid file says otherwise is reported as such and treated as
stopped.

`restart` is `stop` followed by `start`, in one command, because applying an edited config otherwise
takes both. It deliberately starts a proxy when none was running instead of refusing: an
unconditional restart is what a config-reload call site wants, and a stopped proxy is not an error
there.

`start`, `stop`, and `status` all name the PID. `start` is the one that writes it, so the report is
read from the child it just spawned rather than from the file; `status` adds the log path and the
uptime. Uptime is the PID file's mtime rounded to the second: the file is written once, at startup,
so its mtime is the start time. That is an approximation - it does not survive someone touching the
file - and it is one the alternative did not buy much over, since the only exact source would be a
second liveness probe on the daemon's side.

The rejected alternative was leaving `stop` silent and printing nothing on success, which is what the
`nohup`-style convention suggests: exit status as the only report. Silent success is indistinguishable
from a command that matched nothing, which is exactly the state `ccbunshin proxy start` used to leave
the user in.

## 12. Completion scripts are generated, and `init` does not install them

`ccbunshin completion <bash|zsh>` prints the script (`completion.go`); the source tree holds no
completion files. A script that lives next to the CLI it describes cannot drift from it, and a
generated one cannot either: the command list in it is checked against `topLevelCommands` by
`TestCompletionCoversCommands`, and `tests/completion-test.sh` drives both shells. A shipped file
would have to be regenerated for every command added, and would go stale in a checkout, a release
binary, and an in-place `ccbunshin update` independently.

`ccbunshin init` deliberately does not install it, for two reasons. The shells do not agree on where
a completion script goes: sourcing from the rc file is the only route bash has (it reads no
completion directory of its own - the on-demand one belongs to the separate `bash-completion`
package, which loads `<command>.bash` and only after it has itself been sourced), and zsh autoloads
from `fpath`, where it has a trap of its own - `compinit` reads only a file's leading `#compdef`
line and never runs the rest, so a script that is only on `fpath` registers the CLI completion and
silently not the `claude` one. Sourcing the file from an rc file, after compinit, registers both,
and that is what the script's own header recommends. Installing into an rc file is also a different
kind of change from the wrapper hook `init` appends: it needs a path the user chose, `uninstall`
would have to reverse it, and a mistake there breaks the user's shell rather than one command.

The rejected alternative was `init` writing the script itself and adding an `fpath` or `source` line
to each rc file. It buys one saved command and costs a wrong default for half the users - the `fpath`
form, the one that looks most natural for zsh, is the one that loses the `claude` completion.
