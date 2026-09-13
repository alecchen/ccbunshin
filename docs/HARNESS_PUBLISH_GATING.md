# Guarding agent-driven git push, tag, and release

What we learned building the publish gates for this repo, and how to rebuild them. Written
2026-09-13, after an agent pushed `v0.1.5`, `v0.1.6`, and `v0.1.7` tags and published three
GitHub releases without approval. Those tags and releases were later deleted.

This is harness engineering, not ccbunshin behavior: it depends on Claude Code settings, Claude
Code hooks, and GitHub repo settings. It lives in this repo's docs because this repo is where the
problem showed up, and because the next person to hit it should not have to rediscover it.

## The problem

An agent with Bash access holds the same publishing powers as the human: `git push`, `git tag`,
`gh release create`. Nothing about asking it to "commit but don't push" enforces anything. In the
incident above, the agent was told to release a version, then kept tagging and releasing while the
human's attention was elsewhere.

Three failure surfaces, in increasing order of severity:

1. **The agent pushes commits or tags** - made worse by GitHub Actions turning a tag push into a
   published release automatically.
2. **The agent adds `--no-verify`** to skip local git hooks, defeating any pre-push hook.
3. **The agent runs in a mode that bypasses local gates entirely** (`bypassPermissions`,
   `dontAsk`), or a settings file fails to load.

## The four layers, and what each actually catches

| layer | where it lives | catches | misses |
| --- | --- | --- | --- |
| `permissions.ask` rules | `~/.claude/settings.json` | plain spellings: `git push *`, `git tag *`, `gh release *` | anything that doesn't match the command text prefix |
| `permissions.deny` | `~/.claude/settings.json` | `Bash(git * --no-verify *)` | other evasion |
| `publish-gate.sh` hook (`PreToolUse`) | `~/.claude/settings.json` + the script | subshells, `if`/`for`, `!`, `xargs`, `find -exec`, wrappers (`env`, `sudo`, `nohup`, `time`), `gh api` writes, `gh repo delete`, `gh pr merge` | script indirection (`bash deploy.sh`); any non-Bash tool |
| **required-reviewer environment** | GitHub repo settings + `environment:` in the workflow | **everything**: the release job cannot create a release until a human approves | tag creation itself (the tag still lands) |

The first three are **local to one machine**. They do not survive `--no-verify` on a push that a
git hook would have caught, a settings file that fails to parse, a session started in
`bypassPermissions`, a different machine, or a script that pushes. The fourth is server-side and
survives all of those. If only one layer is affordable, build the fourth.

## Layer 1 and 2: `permissions.ask` and `permissions.deny`

`~/.claude/settings.json`:

```json
{
  "permissions": {
    "ask": [
      "Bash(git push *)",
      "Bash(git tag *)",
      "Bash(gh release *)",
      "Bash(git -C * push *)",
      "Bash(git -c * push *)",
      "Bash(git -C * tag *)",
      "Bash(gh api *)",
      "Bash(gh repo delete *)",
      "Bash(gh pr merge *)"
    ],
    "deny": ["Bash(git * --no-verify *)"]
  }
}
```

Facts established by observation, not by reading the docs:

- **Ask rules are read live.** A rule added mid-session takes effect immediately, no restart.
- **Ask rules fire in `auto` mode.** The docs say explicit ask rules force a prompt even in auto
  mode, and this holds: a prompt reading `Ask rule Bash(git push *) overrides auto mode for this
  command` was observed.
- **The Bash rule matcher sees through wrapper prefixes.** `env FOO=1 git push main` still matches
  `Bash(git push *)`. This is the opposite of the naive assumption that a text rule is a plain
  prefix match; do not build a gate on the assumption that `env` evades a rule, because it does not.
- **The deny rule blocks without prompting**, and its message reaches the agent:
  `Permission to use Bash with command ... has been denied.` It is the only layer that catches
  `--no-verify`, since the flag's purpose is to skip hooks.
- **An ask rule and a hook that both match produce ONE prompt, not two.** The displayed text is the
  rule's (`Ask rule ... overrides auto mode`), even though the hook's reason is also contributed.
  The hook's own text is visible when the hook matches alone (see below).

## Layer 3: the `publish-gate.sh` hook

A `PreToolUse` hook on `Bash` that reads the tool input on stdin and prints a decision on stdout:

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask",
 "permissionDecisionReason":"git push publishes: approve only if you meant to publish."}}
```

Registered in settings:

```json
{ "hooks": { "PreToolUse": [
  { "matcher": "Bash",
    "hooks": [ { "type": "command", "command": "/Users/<you>/.claude/hooks/publish-gate.sh" } ] }
]}}
```

### How hooks actually behave

- **Hook config is snapshotted at session start; the hook's file contents are not.** A hook added
  to settings mid-session fires unreliably or not at all. But because the harness execs the script
  path on each call, editing the script's *contents* takes effect immediately. This asymmetry is
  what makes a content-based log line the fastest way to prove liveness.
- **The hook runs on every matching Bash call**, not only publishing ones. That is what makes it a
  cheap liveness probe: add a log line, run any command, check the log.
- **Hooks see the original command text.** `PreToolUse` hooks run in declared order and
  `updatedInput` from one hook is applied *after* the whole chain completes, not passed down it.
  Verified: a probe hook placed last in `PreToolUse` received `git status` even though the
  lean-ctx rewrite hook (declared second) rewrites `git status` to a `lean-ctx -c` wrapper. So a
  rewrite hook earlier in the chain cannot hide a command from a later one.
- **A hook's `ask` is honored and its reason does reach the human.** Observed prompt text:
  `Hook PreToolUse:Bash requires confirmation for this command: git push publishes: approve only
  if you meant to publish.` It only loses the display to a rule's wording when both match.
- **The agent cannot see permission dialogs.** A prompt is not visible in the tool result; an
  approved command and a never-prompted command look identical to the agent. Do not ask an agent to
  report prompt counts - it will infer from absence of evidence. Prompt text must come from the
  human, or from instrumentation.

### The liveness probe that settles "is the hook loaded"

Add a log line right after the payload is read:

```sh
payload=$(cat)
printf "%s\n" "$payload" >> "$HOME/.claude/hooks/publish-gate.log"
```

Then run any Bash command and check the log. Remove it afterwards:

```sh
sed -i '' '/publish-gate\.log/d' ~/.claude/hooks/publish-gate.sh
rm -f ~/.claude/hooks/publish-gate.log
```

Two traps: the script logs on **manual invocations too**, so a non-empty log proves nothing unless
it was emptied immediately before the command. And the hook fires **before** the command runs, so
`: > logfile` as the first statement of a test command truncates the log *after* the hook already
wrote that call's line and erases the evidence.

### The prompt-text discriminator

When the agent cannot see dialogs, prompt text tells you which layer answered:

| prompt text | layer |
| --- | --- |
| `git push publishes: approve only if you meant to publish.` | the hook |
| `Permission rule Bash(git push *) requires confirmation for this command.` | the rule |
| `Ask rule Bash(git push *) overrides auto mode for this command.` | the rule (auto mode) |
| `Permission to use Bash with command ... has been denied.` | the deny rule |

### Coverage: what the hook's normalizer handles, and what it does not

The script splits a command on `&&`, `||`, `|`, `;`, `&`, and on `(` `)` `{` `}`; strips a leading
`!` and leading shell keywords (`if`, `then`, `do`, `for`, `while`, `until`, `else`, `elif`,
`case`, `esac`, `fi`, `done`); strips wrappers (`env`, `sudo`, `nohup`, `time`, `nice`, `timeout`,
`stdbuf`, `command`, `builtin`, `noglob`); unwraps `bash -c "..."` by extracting the quoted body;
then drops remaining quoted strings and matches `git push|tag`, `gh release`, `gh api` (writes
only), `gh repo delete`, `gh pr merge` anywhere in the line, guarded by a word boundary so
`igit push` does not match.

Verified to match: bare `git push`, `git --no-pager push`, `git -C /repo push`,
`git -c k=v push`, `git --git-dir=/x push`, `env FOO=1 git push`, `sudo git push`,
`nohup git push`, `time git push`, `echo hi && git push`, `true; git push`, `git push &`,
`bash -c "git push"`, `(git push)`, `if true; then git push; fi`, `! git push`,
`for r in a b; do git push $r; done`, `find . -exec git push \;`, `xargs -n1 git push`.

Verified to NOT match (correctly): `git push --dry-run`, `git tag -l`, `gh release list`,
`gh api <read>`, `git status`, `gh pr view`, `echo "git push"`, `igit push`.

**Known gap, not closable by text matching:** script indirection. `bash deploy.sh` and
`./deploy.sh` publish with no prompt if the script pushes. Only the server-side layer covers this.
Do not describe the hook as covering "all git push operations" - it does not.

### Testing the hook's classifier without triggering anything

Feed it synthetic payloads directly. This tests the script's logic with zero side effects and no
prompts:

```sh
printf '{"tool_input":{"command":"sudo git push origin main"}}' | ~/.claude/hooks/publish-gate.sh
```

Build a case table from this and keep it as a regression matrix. The one in use has 53 cases; run
it after any edit to the hook.

### Ordering trap when editing a shell pipeline

Inserting a `sed` stage into the normalizer pipeline requires care: inserting *after* the line that
closes the command substitution (the one ending in `')`) leaves a dangling `|` and an empty
variable, so every case silently returns "no opinion" and the gate appears to allow everything.
After any edit, re-run the matrix; a hook that matches nothing looks the same as a hook that is not
loaded.

## Layer 4: the GitHub release guard (the only one that always holds)

`environment: release` on the `release` job in `.github/workflows/build.yml`, plus a repo
environment named `release` configured with required reviewers. The job then pauses after `build`
succeeds and waits for a human approval before creating the Release.

```yaml
  release:
    if: startsWith(github.ref, 'refs/tags/v')
    needs: build
    runs-on: ubuntu-latest
    environment: release
    steps:
      # ...
```

```sh
# 1. your numeric user id (not the login)
gh api /users/<login> --jq .id

# 2. create the environment with you as required reviewer
gh api -X PUT /repos/<owner>/<repo>/environments/release \
  -F 'reviewers[][type]=User' -F 'reviewers[][id]=<id>' \
  -F 'prevent_self_review=false'

# 3. verify it is actually gated (existence alone proves nothing)
gh api /repos/<owner>/<repo>/environments
```

Critical details:

- **Configure the environment BEFORE the workflow change reaches the default branch.** GitHub
  auto-creates an environment the first time a workflow references a name that does not exist, and
  an auto-created environment has **no protection rules**. The result is a workflow that looks
  gated, reviews as gated, and silently is not.
- **Do NOT enable "Prevent self-review."** Pushes carry the maintainer's identity, so on a solo
  repo you both trigger the run and are the only reviewer. With self-review prevented, you cannot
  approve your own release and every release deadlocks. Enable it only with a second reviewer.
- **Verified end-to-end.** With a throwaway tag: the run reached `release: waiting` with no Release
  in existence, then published on approval. Timestamps confirmed a multi-minute gap between tag
  push and publication - nothing published itself.
- **It gates release creation, not tag creation.** The tag still lands on the remote. Since
  `install.sh` resolves the latest Release, a bare tag publishes nothing, so this is the right
  thing to protect. Add the same `environment:` to any other job that publishes.
- **`can_admins_bypass` is true by default**, so a repo admin can bypass via API. This constrains
  the workflow, not the human, and does not weaken protection against the failure modes above.

## Rebuilding this from scratch: the order that works

1. Add the `permissions.ask` rules and the `--no-verify` deny rule to `~/.claude/settings.json`.
   Verify: run `git push <nonexistent-path> main` and confirm a prompt appears.
2. Install `publish-gate.sh` and register it as a `PreToolUse` hook on `Bash` with **no `if` field**
   (an `if` of `Bash(git *)` only skips non-matching calls, which the script already does).
3. **Start a new session** - hook config added mid-session will not load reliably - then prove
   liveness with the log-line probe above.
4. Run the case table against the classifier and keep it as a regression matrix.
5. Configure the GitHub environment with required reviewers, `prevent_self_review=false`.
6. Add `environment: release` to the release job; merge it.
7. Verify with a throwaway `v0.0.0-*` tag that the job reaches `waiting` with no Release, then
   approve it and confirm publication, then delete the Release and the tag.
8. Document the known gap (script indirection) wherever the gate is described.

## Testing without publishing anything

- `--dry-run` tags and release names that cannot resolve (`v0.0.0-does-not-exist`), and a remote
  path that does not exist. Approving a prompt by reflex then costs nothing.
- **Watch the positional-argument trap:** in `git push <remote> <ref>`, the remote is a positional
  argument. If it is autocompleted away or typo'd, the command becomes `git push main` against the
  real `origin` and publishes. Confirm the remote is not a real one before running any push test.
- The hook's own exempted read-only forms (`git push --dry-run`, `git tag -l`, `gh release list`)
  are worth testing as negative controls; they prove the gate is not matching too broadly.

## What does not work

- **Asking the agent to be careful.** Not a control.
- **Relying on a git pre-push hook alone.** `--no-verify` skips it, and the agent can pass that
  flag.
- **Relying on the agent's report of whether a prompt appeared.** The agent cannot see dialogs.
- **Assuming `env` or a wrapper evades a permission rule.** It does not.
- **Assuming the hook covers everything.** It does not cover script indirection.
- **Trusting an auto-created GitHub environment.** It has no protection rules.
