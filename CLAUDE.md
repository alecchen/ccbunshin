# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

ccbunshin (影分身, "shadow clone"): a dependency-free tool to run multiple Claude Code
configurations side by side - isolated configuration (endpoint, model, hooks, OTEL) with
shared state (projects, history, todos, skills). Built for a company with two LLM gateways
(FREE: Qwen/DeepSeek, PAID: Vertex Claude). Currently spec-only; no code yet.

## Repo docs (read the relevant one before editing)

- `Claude Code Multi-Endpoint - Shared State Architecture Requirements.md` - the problem
  statement and hard requirements. Source of truth for WHAT must be solved.
- `INVESTIGATION_FREE_PAID.md` - research: CCPG evaluation, GitHub alternatives, claude-rig
  deep-dive. Historical context; its verdicts are superseded by the implementation spec.
- `CCPROF_IMPLEMENTATION.md` - the design (filename kept from the working title). v2 chose
  per-profile `--settings` files over the earlier `CLAUDE_CONFIG_DIR` + symlinks design; v3
  adds the design-review decisions.
- `CC_SWITCH_BASE_URL_PIN.md` - tangential note on pinning `ANTHROPIC_BASE_URL` against
  cc-switch rewrites via `--settings`.

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
5. **Scope**: phase 1 = config isolation (bash tool). Phase 2 = multi-endpoint proxy
   (deferred; open questions in spec section 10). No LeanCTX integration, no GUI, no binary
   version management.
6. **The internal switcher** (company tool) writes global `~/.claude/settings.json` and stays
   untouched; profile launches override it via `--settings`.

## Status

Draft spec, no implementation. Phase-1 tool (CLI: `init`/`create`/`launch`/`model`/`list`/
`status`/`doctor`/`delete`) is the next deliverable. No build/test commands exist yet.

## Verification requirement

Every implementation change must include a way to verify the behavior. Add or update automated tests where practical, and document the verification command in the relevant README or implementation document. At minimum, run syntax, lint, build, or test validation appropriate to the changed code before declaring the work complete.


This repo is intended for public release; committed docs must contain no company-specific
names, URLs, or secrets (use the generic `free`/`paid` profile examples).