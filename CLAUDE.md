# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

ccbunshin (影分身, "shadow clone"): a dependency-free Go tool that runs several Claude Code
configurations side by side - isolated configuration (endpoint, model, hooks) with shared
state (projects, history, todos, skills). It is built around two gateway profiles
(`examples/provider1.json`, `examples/provider2.json`) and an optional model-routed proxy
between them. It is implemented as a single Go binary in `cmd/ccbunshin` and released under
tags `v0.0.x`.

## Layout

`cmd/ccbunshin/` is the whole program: `main.go` (CLI, config, proxy server, shell
integration), `dialect.go` (dialect resolution), `translate.go` (request and response
translation), `stream.go` (SSE translation). Tests sit beside those (`main_test.go`,
`translate_test.go`, `stream_test.go`) with fixtures in `testdata/`. `docs/` holds the spec
and the proxy Plan docs, `examples/` the profile and proxy templates, `tests/` the shell and
installer scripts. There is no Go module at the repo root: always pass `-C cmd/ccbunshin`, or
build from that directory.

## Repo docs (read the relevant one before editing)

- `docs/CCBUNSHIN_IMPLEMENTATION.md` - the design and implementation notes (filename kept
  from an earlier working title). v2 chose per-profile `--settings` files over the earlier
  `CLAUDE_CONFIG_DIR` + symlinks design; later sections cover the project-aware `claude`
  wrapper and the implemented model-routed proxy. Read before changing CLI behavior.
- `docs/Claude Code Multi-Endpoint - Shared State Architecture Requirements.md` - the
  problem statement and hard requirements. Source of truth for WHAT must be solved.
- `docs/INVESTIGATION_FREE_PAID.md` - research: CCPG evaluation, GitHub alternatives,
  claude-rig deep-dive. Historical context; its verdicts are superseded by the
  implementation spec and the code.
- `docs/CC_SWITCH_BASE_URL_PIN.md` - tangential note on pinning `ANTHROPIC_BASE_URL` against
  cc-switch rewrites via `--settings`.
- `docs/PLAN_OPENAI_CHAT_DIALECT.md` - the openai-chat dialect in full: config surface,
  translation rules, and its risk register. Supersedes section 10 of the implementation notes
  for anything dialect-related.
- `docs/PLAN_CACHE_USAGE_PASSTHROUGH.md` - why usage reporting is split the way it is
  (decision 9), and the upstream facts that split rests on.
- `README.md` and `cmd/ccbunshin/README.md` - current CLI, proxy, and shell-integration
  documentation.

## Key decisions (do not silently reverse)

1. **Isolation mechanism: per-profile `--settings` files, not `CLAUDE_CONFIG_DIR`.**
   `--settings` sits at command-line precedence (above project/user settings, below managed).
   It is a per-key merge: a key set there replaces the same key in lower levels; omitted keys
   fall through. State paths (`~/.claude/projects`, `history.jsonl`, `todos/`, `skills/`,
   `$HOME/.claude.json`) are never relocated - shared by construction.
   `CLAUDE_CONFIG_DIR` was rejected because it relocates state too, forcing a version-fragile
   symlink map.
2. **Env blocks merge per-variable, verified.** Two rules, and both matter: a variable set in
   two files resolves to the higher-precedence one, and a variable set *only* in a lower file
   survives - a higher file's `env` block does not blank the lower one. That is what lets a
   profile name one variable and inherit the rest of the user's env block, which is the whole
   premise of decision 1. Verified 2026-09-12 with a `SessionStart` hook dumping `env` from a
   session launched with `--settings`: a probe variable set only in user settings and one set
   only in `--settings` both reached the session, and a variable set in both resolved to the
   `--settings` value. Note the published docs do not state the second rule - `settings.md`'s
   "an `env` block inside a settings file is an ordinary key and follows the levels above"
   plus "Lists merge instead of overriding" reads as wholesale replacement, which is wrong.
   Re-probe rather than trusting that reading.
3. **Isolation map**: `hooks`, `model`, `statusLine` are isolated per profile (wholesale key
   replacement; `hooks: {}` blanks company hooks). `permissions.allow` and other list keys
   merge across scopes - cannot subtract via `--settings`. Managed settings outrank
   `--settings`.
4. **Auth is out of scope**: Claude Code's native mechanisms handle it
   (`ANTHROPIC_AUTH_TOKEN` > `ANTHROPIC_API_KEY` > `apiKeyHelper`). ccbunshin never reads or
   writes credentials.
5. **Scope**: config isolation, directory-local (`local`) profiles, a global fallback profile
   (`global`), project-aware `claude` shell wrappers, the model-routed proxy, and self-update
   are implemented. Still out of scope: LeanCTX integration, GUI.
6. **The internal switcher** (company tool) writes global `~/.claude/settings.json` and stays
   untouched; profile launches override it via `--settings`.
7. **The project-aware wrapper is a thin router**: a `.ccbunshin-profile` marker (the same
   file `ccbunshin local` writes) selects a project. Bash and zsh wrappers call
   `ccbunshin resolve-provider` and route to `ccbunshin launch <provider>`. tcsh cannot
   express a conditional alias, so its wrapper delegates to the internal `ccbunshin run`,
   which resolves the provider or falls back to the original `claude` binary. Resolution is
   `findProfile`: the nearest marker, then the global selection from `ccbunshin global`, then
   nothing - so the global is a fallback and never overrides a project. Shell code
   never parses profile files and never maintains a Claude option list: `launch` forwards
   arguments unchanged and returns Claude's exit status.
8. **The proxy's protocol translation is opt-in per provider and never sends
   `reasoning_effort`.** `dialect` on a provider, route, or `model_dialects` entry selects
   between `anthropic` (default, byte-for-byte pass-through) and `openai-chat` (Anthropic
   `/v1/messages` translated onto OpenAI `/chat/completions`). Resolution is
   most-specific-first, and because a dialect belongs to a model rather than a provider,
   one gateway serving both kinds is expressed as two routes in different namespaces.
   `reasoning_effort` is never emitted and `thinking` is never forwarded: deriving the
   former from Anthropic `thinking` is the bug this replaces, and reasoning is recovered
   from the upstream response instead. Credentials are forwarded,
   never stored (reaffirms decision 4). Translation lives in `dialect.go`, `translate.go`,
   and `stream.go`.
9. **Usage accounting follows Anthropic's split, not the upstream's.** An OpenAI-chat upstream
   reports an inclusive `prompt_tokens` plus `prompt_tokens_details.cached_tokens`;
   `anthropicUsageFromUpstream` reports `cache_read_input_tokens` as the hit count and
   `input_tokens` as `prompt_tokens - cached`, clamped at zero, because Anthropic counts cache
   reads separately. The sum of the three input fields is therefore the upstream's
   `prompt_tokens`, which is what a client's context denominator depends on. Do not "simplify"
   this back to passing `prompt_tokens` through as `input_tokens`: the same prefix would then be
   counted twice and every cache percentage would read 0 again. `cache_creation_input_tokens`
   has no upstream counterpart and is always `0`. Reported usage supersedes
   `estimateRequestTokens` for `input_tokens`, not just `output_tokens`, in the trailing
   `message_delta`.

## Status

Implemented as a Go CLI in `cmd/ccbunshin` (no longer spec-only). The model-routed proxy
and the project-aware `claude` shell integration (bash/zsh/tcsh) are included. Releases are
tagged `v0.0.x`; pushing a `v*` tag triggers the GitHub Actions build-and-release workflow.
`install.sh` installs the latest GitHub release by default (`CCBUNSHIN_VERSION` pins a
specific version), so publishing a new tag requires no `install.sh` change. `ccbunshin
update` checks the latest release and, when a newer version exists, prints the current and
latest versions and replaces the binary in place; release binaries carry their version via
`-ldflags -X main.version`.

## Verification requirement

Every implementation change must include a way to verify the behavior. Add or update automated tests where practical, and document the verification command in the relevant README or implementation document. At minimum, run the validation appropriate to the changed code before declaring the work complete.

Verification commands for this repo:

```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
sh tests/shell-integration.sh   # project-aware claude wrapper across bash, zsh, tcsh
sh tests/install-test.sh        # installer defaults to the latest GitHub release
```

## Build identity

`version` (ldflags `-X main.version=<tag>`) holds a **release tag only**. `updateCLI`
feeds it straight to `compareVersions`, so a commit SHA there would corrupt the
comparison. Local builds are identified separately: `printVersion` falls back to
`localVersion()`, which joins the commit from the toolchain's VCS stamping
(`debug.ReadBuildInfo`, setting `vcs.revision`, shortened to `shortCommitLength`
= 7) with the `buildTime` ldflags stamp
(`-X main.buildTime=<YYYYMMDD-HHMM>`, layout `buildTimeLayout`). Either half is
dropped when unavailable. Build a local binary with:

```sh
go -C cmd/ccbunshin build -ldflags "-X main.buildTime=$(date +%Y%m%d-%H%M)" -o ccbunshin
```

Note that `go test` binaries carry no VCS stamping, so tests must not assume
`buildCommit()` is non-empty.


This repo is intended for public release; committed docs must contain no company-specific
names, URLs, or secrets (use the generic `free`/`paid` profile examples).