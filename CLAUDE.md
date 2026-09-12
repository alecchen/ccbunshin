# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

ccbunshin (影分身, "shadow clone"): a dependency-free Go tool that runs several Claude Code
configurations side by side - isolated configuration (endpoint, model, hooks) with shared
state (projects, history, todos, skills). It is built around two LLM gateway profiles (the
generic `free`/`paid` examples) and an optional model-routed proxy between them. It is
implemented as a single Go binary in `cmd/ccbunshin` and released under tags `v0.0.x`.

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
2. **Env blocks merge per-variable** (assumed; verify empirically before relying on it - see
   spec section 9). Profile files define only what differs and inherit the rest of the user's
   env block.
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