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
translation), `stream.go` (SSE translation), `completion.go` (generated completion scripts),
`log.go` (log levels). Tests sit beside those (`main_test.go`, `translate_test.go`,
`stream_test.go`) with fixtures in `testdata/`. `docs/` holds the decision log, the spec, and
the Plan docs; `examples/` the profile and proxy templates; `tests/` the shell and installer
scripts; `install.sh` and `.github/workflows/build.yml` the release path. There is no Go

## Repo docs (read the relevant one before editing)

- `docs/DECISIONS.md` - the decision log: what each settled design decision is, why, and what the
  rejected alternative cost. Indexed in "Key decisions" below; read the entry before changing the
  behavior it covers.
- `docs/CCBUNSHIN_IMPLEMENTATION.md` - the design and implementation notes (filename kept
  from an earlier working title). v2 chose per-profile `--settings` files over the earlier
  `CLAUDE_CONFIG_DIR` + symlinks design; later sections cover the project-aware `claude`
  wrapper and the implemented model-routed proxy. Read before changing CLI behavior.
- `docs/PRIOR_ART.md` - the tools that already solve this problem, in two families, and why none
  was adopted. Historical survey, kept for "why not an existing tool" and for the two closest
  designs; its mechanisms are consolidated into `docs/DECISIONS.md`.
- `docs/Claude Code Multi-Endpoint - Shared State Architecture Requirements.md` - the
  problem statement and hard requirements. Source of truth for WHAT must be solved.
- `docs/PLAN_OPENAI_CHAT_DIALECT.md` - the openai-chat dialect in full: config surface,
  translation rules, and its risk register. Supersedes section 10 of the implementation notes
  for anything dialect-related.
- `docs/PLAN_CACHE_USAGE_PASSTHROUGH.md` - why usage reporting is split the way it is
  (decision 9 in `docs/DECISIONS.md`), and the upstream facts that split rests on.
- `docs/PLAN_RESPONSES_DIALECT_AND_TOOL_SEARCH.md` - planned work: a third dialect for OpenAI's
  Responses API, and what tool search (`ENABLE_TOOL_SEARCH`) does through the proxy. Read before
  changing dialect handling or tool translation.
- `docs/OPENCODE_ZEN.md` and `docs/OPENCODE_GO.md` - reference: the endpoint shape of those two
  gateways, which models each serves, and which dialect each group needs. Dated snapshots; re-verify
  against the live tables before trusting a model id.
- `docs/HARNESS_PUBLISH_GATING.md` - harness engineering, not ccbunshin behavior: how to stop an
  agent pushing, tagging, or publishing a release without approval. Covers `permissions.ask`, the
  `PreToolUse` publish-gate hook (including its `permission_mode` escalation to `deny`), the CI
  script-indirection scan, and the required-reviewer GitHub environment, with what each layer
  catches and what it misses. Read before changing publishing/release setup, and before trusting any
  claim that a gate "covers all git push operations".
- `README.md` and `cmd/ccbunshin/README.md` - current CLI, proxy, and shell-integration
  documentation.

## Key decisions (do not silently reverse)

`docs/DECISIONS.md` holds the log: what each decision is, why, and what the rejected alternative
cost. Read it before changing CLI behavior, the isolation mechanism, or the proxy. The index below
is only so you know what is already settled; if a change requires reversing one, say so rather than
making it quietly.

1. Isolation is per-profile `--settings` files, never `CLAUDE_CONFIG_DIR`.
2. `env` blocks merge per-variable: a variable set only in a lower file survives.
3. `hooks`, `model`, and `statusLine` isolate per profile; list keys merge and cannot be subtracted.
4. Auth is out of scope: ccbunshin never reads or writes credentials.
5. Scope: isolation, local/global profiles, shell wrappers, the proxy, self-update.
6. The internal switcher's global settings stay untouched.
7. The project-aware `claude` wrapper is a thin router over the `.ccbunshin-profile` marker.
8. Translation is opt-in per provider; a caller's effort is forwarded, never derived from `thinking`.
9. Usage reporting follows Anthropic's split, with cache reads subtracted out of `input_tokens`.
10. Profiles are validated as JSON objects and written atomically (temp file plus rename).
11. `proxy stop` confirms the daemon is gone before reporting success, and `restart` exists.
12. Completion scripts are generated by `ccbunshin completion`, not shipped, and `init` does not install them.

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
sh tests/completion-test.sh     # completion scripts, for bash and for zsh's two activation paths
sh tests/install-test.sh        # installer defaults to the latest GitHub release
sh tests/publish-gate-matrix.sh # publish-gate hook classifier; needs jq, edits no state
sh tests/publish-scan.sh        # CI script scan; no publish commands in tracked scripts
sh tests/publish-scan-test.sh   # the above, against temp-file fixtures
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

## Hidden commands

`usageText` and the completion scripts list only the user-facing commands.
`resolve-provider` (print the nearest provider) and `run` (run `claude` with it) are
dispatchable but deliberately hidden: the emitted bash/zsh wrappers call the first and the
tcsh wrapper the second. `topLevelCommands` in `completion.go` is that visible surface, and
`TestCompletionCoversCommands` checks it against the scripts, so adding a command to the
dispatch switch alone leaves it invisible at the prompt.


This repo is intended for public release; committed docs must contain no company-specific
names, URLs, or secrets (use the generic `free`/`paid` profile examples).