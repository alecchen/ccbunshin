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
supersedes `estimateRequestTokens` for `input_tokens`, not just `output_tokens`, in the trailing
`message_delta`.
