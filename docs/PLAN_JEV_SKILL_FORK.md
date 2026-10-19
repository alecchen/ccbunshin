# Plan: fork the TypeSafe skill for the Command Code endpoint

Status: not started. The official `typesafe-ai` plugin skill targets
`api.typesafe.ai`. This repo routes through `api.commandcode.ai/provider/v1`, which
serves the same System One contract for `typesafe/jev` and `typesafe/jev-1.14.0`.

The fork is a skill, not ccbunshin behavior. It lives in a skills directory, never in
this repo's `cmd/`, and this file is the brief for writing it.

## Why fork

The official skill is 149 lines, MIT licensed, and almost entirely host-agnostic:
concepts, primitive guidance, and links to `docs.typesafe.ai`. Little of its value
depends on the host. What it does not know is:

- the endpoint path under Command Code,
- the environment variable holding the key,
- the model ids that host accepts,
- that the official SDK targets the TypeSafe host and has no documented base-URL
  option, so requests here are plain HTTP.

A local fork keeps the concepts and replaces the transport facts, so an agent writes
a correct jev call without rediscovering the body shape each session.

## Verified upstream facts (2026-10-19)

Probed against `https://api.commandcode.ai/provider/v1`.

- `POST /systemone`, body `{model, state, questions}`, response
  `{model, answers, usage}`.
- Models: `typesafe/jev`, `typesafe/jev-1.14.0`. Any other id returns
  `400 {"code":"unsupported_model","message":"Model \"x\" is not supported on this endpoint. Use one of: ..."}`.
- Auth: `Authorization: Bearer $COMMANDCODE_API_KEY` and `x-api-key: $COMMANDCODE_API_KEY`
  both work. Stray Anthropic headers are ignored.
- Never streams: `stream: true` still returns `application/json`.
- Errors keep the upstream shape `{"error":{message,type,param,code}}`, including
  `422 cmd_zdr_no_providers` when `x-cmd-zdr: 1` is sent.
- Limits observed and documented: 100 questions per call (101 returns
  `at most 100 questions per call`), 64K tokens for state plus all questions, 32K for
  state plus the longest question.
- Question types: `noul` (`criteria` optional `{true, false}` descriptions, returns
  `noul`), `choice` (`criteria` map option to description, returns `choice`,
  `confidence`, `probabilities`), `score` (`criteria` array of at least two levels,
  lowest first, returns `score`, `confidence`, `legend`, `probabilities`).
- `state` may be a string, object, or array.

## Steps

1. Copy the official skill as the base: read
   `~/.claude/plugins/cache/typesafe-ai/typesafe/0.5.7/skills/typesafe-ai/SKILL.md`
   and its MIT `LICENSE`. Keep both, and keep the license header on the fork.
2. Replace the transport facts in the body. Endpoint, key variable, model ids, and
   the SDK paragraph change. Leave the concepts, primitive table, and
   `docs.typesafe.ai` links alone; they still describe the same model.
3. Write the request section around plain HTTP. One `curl` and one `fetch` example,
   each with a `noul`, a `choice`, and a `score`, plus a note that SDK requests go to
   the TypeSafe host and not here.
4. Decide the auth path and document it. Either the skill names
   `$COMMANDCODE_API_KEY`, or it sends to `http://127.0.0.1:3456` with no auth header
   and relies on the proxy's `auth_token_env` (`systemone` dialect, commit `8befbca`).
   The proxy path is one config change away and keeps the key out of every script.
5. Place it. `~/.claude/skills/jev/` for personal use, or a project
   `.claude/skills/jev/` if the integration belongs to one repo. Name it `jev`, not
   `typesafe-ai`, so it does not shadow the plugin skill.
6. Verify. Run one real request from the finished skill's own example, confirm the
   answer shape matches the documented one, and confirm an unsupported model id
   fails with a message that names the accepted ids.
7. Record what still does not work. Claude Code cannot route its own turns through
   jev, so the skill produces code, not thinking. State that in the skill's opening
   paragraph so a future agent does not try to use it as a model.

## Not in scope

- A proxy change. `systemone` (commit `8befbca`) is enough for the proxy option.
- A Claude Code model alias for jev. Claude Code sends Anthropic messages and jev
  refuses them; inventing a question set would be a different feature.
- Upstreaming the fork to the TypeSafe plugin. It is host-specific by construction.
