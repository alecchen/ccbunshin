# Guarding agent-driven git push, tag, and release

What we learned building the publish gates for this repo, and how to rebuild them. Written
2026-09-13, after an agent pushed `v0.1.5`, `v0.1.6`, and `v0.1.7` tags and published three
GitHub releases without approval. Those tags and releases were later deleted. Amended the same
day, after the gates failed in a second and worse way: an agent pushed `main` and tagged
`v0.1.5` with no prompt at all, because the shell it was using was not the one the gates watch.

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
4. **The agent never runs the gated tool at all** - it runs the same command through a different
   tool. See the next section; this is the failure that actually happened twice.

## The four layers, and what each actually catches

| layer | where it lives | catches | misses |
| --- | --- | --- | --- |
| `permissions.ask` rules | `~/.claude/settings.json` | plain spellings: `git push *`, `git tag *`, `gh release *` | anything that doesn't match the command text prefix; **any tool that is not Bash** (no MCP spelling exists) |
| `permissions.deny` | `~/.claude/settings.json` | `Bash(git push --no-verify*)` and `Bash(git * push --no-verify*)` - push only, so local `--no-verify` stays unprompted | other evasion; **any tool that is not Bash**; a local `--no-verify` (deliberate) |
| `publish-gate.sh` hook (`PreToolUse`) | `~/.claude/settings.json` + the script | subshells, `if`/`for`, `!`, `xargs`, `find -exec`, wrappers (`env`, `sudo`, `nohup`, `time`), `gh api` writes, `gh repo delete`, `gh pr merge` - and, once the matcher lists them, non-Bash shell tools | script indirection (`bash deploy.sh`); any shell tool the matcher does not name |
| **required-reviewer environment** | GitHub repo settings + `environment:` in the workflow | **everything**: the release job cannot create a release until a human approves | tag creation itself (the tag still lands) |

The first three are **local to one machine**. They do not survive `--no-verify` on a push that a
git hook would have caught, a settings file that fails to parse, a session started in
`bypassPermissions`, a different machine, or a script that pushes. The fourth is server-side and
survives all of those. If only one layer is affordable, build the fourth.

## The gates are per-tool, and one tool is not the other

Every local layer above is attached to a **tool name**. `permissions.ask` rules are spelled
`Bash(git push *)`, and the hook is registered with `"matcher": "Bash"`. Both of those match the
native Bash tool. Neither has any effect on a command that reaches the machine through a different
tool that also runs shell commands - an MCP shell server being the obvious one.

When that happens the gates do not *fail open* in the sense of matching and then being overridden.
They are **never evaluated**. There is no prompt, no deny, no log line, and nothing in the tool
result that distinguishes it from an approved command. From the agent's side, and from the
transcript, an un-gated publish looks exactly like a permitted one.

Observed on this machine, 2026-09-13. `mcp__lean-ctx__ctx_shell` is an MCP tool that runs shell
commands, and the session policy routing work through `ctx_*` tools made it the default shell:

- `git push /nonexistent-path main` through it: exit 128, no prompt. The `Bash(git push *)` ask
  rule should have matched.
- `git push --no-verify /nonexistent-path main` through it: exit 128, no prompt. The
  `Bash(git * --no-verify *)` **deny** rule should have blocked it outright, and deny rules block
  without prompting even in auto mode.
- The same session's transcript shows 27 calls to `mcp__lean-ctx__ctx_shell` and zero to `Bash`.
- Control: a `ctx_patch` on the hook file itself *was* blocked, by the auto-mode classifier. So
  MCP tools are enforced against in general; it is the shell wrapper that carries none of the
  Bash-scoped rules.

The lesson generalizes past this one tool: **enumerate the tools that can run a shell, and check
each one against your gates individually.** A gate you have not tested against every shell tool is
a gate you are assuming works. The liveness probe below is how you find out, and it should be run
per tool, not once.

### Fixing it

Hook matchers are regexes against the tool name, so a `PreToolUse` matcher can cover both:

```json
{ "matcher": "^(Bash|mcp__lean-ctx__ctx_shell)$",
  "hooks": [ { "type": "command", "command": "/Users/<you>/.claude/hooks/publish-gate.sh" } ] }
```

Anchor it. `"Bash"` unanchored is a substring match, so it also matches `BashOutput`; the anchored
form matches only the two tools you mean.

The hook script needs no change for this, because the payload shape happens to match: `ctx_shell`'s
tool input is `{"command": "..."}`, the same field the script already reads with
`jq -r '.tool_input.command'`. That is luck, not design - verify the payload shape of any tool you
add rather than assuming it. `tests/publish-gate-matrix.sh` keeps a separate case set with the MCP
tool name in the payload so that a future release changing that shape fails a test instead of
silently disabling the gate.

**`permissions.ask` cannot be fixed the same way.** Command-text rules have no MCP spelling, so
there is no `mcp__lean-ctx__ctx_shell(git push *)` to add. For non-Bash shell tools the hook is not
a backup layer, it is the *only* local layer. This is a reason to keep the hook script's rules at
least as broad as the `permissions.ask` list, since it alone has to cover both cases. The next
section is why, in detail.

Changing a matcher is a change to the `hooks` **config**, which snapshots at session start, so
**start a new session** before trusting it. Then re-run the matrix, and confirm the prompt actually
appears by running a publish command through the newly covered tool against a remote that does not
exist.

## Why `permissions.ask` cannot gate an MCP shell

The short version: **settings rules match tool names; hooks match tool inputs.** Every limitation
below follows from that one line. Sourced from `code.claude.com/docs/en/permissions` (permission
rule syntax, wildcard patterns, "what a Bash rule doesn't match"), read 2026-09-14.

### Arguments are not matchable in a settings-file rule for any `mcp__` tool

`Tool(param:value)` is the only argument-matching rule form, and it is explicitly closed to MCP.
The docs, verbatim:

> To match a parameter on an MCP tool, pass a deny rule with `--disallowedTools`. When Claude Code
> loads a settings file, it skips any `mcp__` rule that has parentheses.

So `mcp__lean-ctx__ctx_shell(command:git push *)` is not a rule that fails to match - it is
**discarded at load**. That is a worse failure mode than a non-match, because the rule reads as
protection in the settings file while not being in effect. The skip surfaces in the
invalid-settings dialog at interactive session start, and in `claude doctor` output. A publish
gate that is silently dropped is exactly the failure this document exists to prevent, so do not
write a parenthesized MCP rule and assume it counts as a layer.

### There is no `ask` equivalent of `--disallowedTools`

The escape hatch for MCP parameters exists for **deny only**. There is no `--askTools`-style flag,
so argument-level *prompting* on an MCP tool has no supported spelling at all. Denying the whole
tool is the only settings-file option, and even that is a CLI flag rather than a checkable
settings entry. For a prompt rather than a block, a `PreToolUse` hook is the only mechanism.

### `ctx_shell` is excluded twice over, not once

Two independent reasons its `command` cannot be matched, pointing at the same parameter:

- MCP tools cannot match parameters through a settings file at all (above).
- `command` is Bash's **primary content field**, which is never matchable even for built-in tools:

> A rule like `Bash(command:rm *)` would be bypassable by a compound command, so Claude Code
> ignores it and emits a startup warning.

An MCP shell taking a parameter literally named `command` sits at the intersection of both
exclusions. This is also why the hook is the right layer rather than a workaround: a `PreToolUse`
hook receives the full tool-input JSON and can inspect any field, including the primary content
field that settings rules refuse.

### What MCP rules *can* express

| form | works? |
| --- | --- |
| `mcp__lean-ctx` (whole server), `mcp__lean-ctx__ctx_shell` (whole tool) | yes |
| `mcp__lean-ctx__ctx_*` in **allow** | yes - globs are accepted after a literal `mcp__<server>__` prefix, and the server segment must be glob-free |
| `mcp__*`, `"*"`, `"B*"` in allow | no - "skipped with a warning and doesn't auto-approve anything" |
| anything with arguments, in allow, ask, or deny | no |

Granularity is therefore all-or-nothing at the tool level: gate `ctx_shell` entirely - which kills
every shell command, read-only ones like `git status` included - or not at all. There is no way to
ask on the publishing subset.

### Corollary: `Bash(git push *)` is weaker than it reads

Same doc, "what a Bash rule doesn't match". A rule matches the command text Claude writes, after
compound-command splitting and wrapper stripping, so it does not match the same program invoked
another way:

| Rule | Doesn't stop |
| --- | --- |
| `Bash(git push *)` | `git -C . push origin main`, `git -c push.default=current push origin main`, `git 'push' origin main` |
| `Bash(curl *)` | `/usr/bin/curl ...`, `sh -c 'curl ...'` |
| `Bash(rm *)` | `/bin/rm -rf build/`, `bash -c 'rm -rf build/'` |

The docs are explicit that this is not a security boundary around the program: "your other rules
and the permission mode decide the commands in the last column." The two `git -C` / `git -c` forms
are why the ask list carries four `git` entries instead of two. `git 'push'` remains uncovered
locally, though the hook strips quotes before matching and so does catch it.

### The one lead worth testing

The MCP docs mention that a server can declare `_meta["anthropic/requiresUserInteraction"] = true`
on a tool to force a prompt for it, and that allow rules do not skip that prompt. If a shell server
exposed that, the gate could live in the server rather than in config. **Unverified** - no check
has been made of whether any MCP shell server here supports it, and it is recorded as a lead, not
as something to rely on.

### Verified here vs. read from the docs

Established on this machine 2026-09-13/14:

- the ask list is Bash-only, and `ctx_shell` carries none of it
- `ctx_shell`'s tool input is flat `{"command": "..."}` - from a real transcript, not from a guess
- a `git push` through `ctx_shell` produced no prompt and no deny, while a `ctx_patch` on the hook
  file *was* blocked by the auto-mode classifier, so MCP tools are not globally exempt

Taken from the docs and **not** locally exercised: the load-time skip of parenthesized `mcp__`
rules, the allow-glob restrictions, and the `--disallowedTools` deny route. The load-time skip is
the one worth confirming before leaning on it - an invalid-rule report for it would appear in
`claude doctor` in a fresh session.

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
    "deny": ["Bash(git push --no-verify*)", "Bash(git * push --no-verify*)"]
  }
}
```

Two `--no-verify` rules, both naming `push`. **The space before a trailing `*` is part of the
rule**, so a rule ending in the flag with no `*` after it requires a token to follow; and a `*`
placed before `push` requires the literal text `" push"`, which `git push` does not contain. Both
traps fail the same silent way: no match, no warning. Narrowing to `push` is deliberate - see
"Scope: gate the remote, not the flag" below for why a local `git commit --no-verify` must stay
ungated.

Facts established by observation, not by reading the docs:

- **Ask rules are read live.** A rule added mid-session takes effect immediately, no restart.
- **Ask rules fire in `auto` mode.** The docs say explicit ask rules force a prompt even in auto
  mode, and this holds: a prompt reading `Ask rule Bash(git push *) overrides auto mode for this
  command` was observed.
- **The Bash rule matcher sees through wrapper prefixes.** `env FOO=1 git push main` still matches
  `Bash(git push *)`. This is the opposite of the naive assumption that a text rule is a plain
  prefix match; do not build a gate on the assumption that `env` evades a rule, because it does not.
- **The deny rule blocks without prompting**, and its message reaches the agent:
  `Permission to use Bash with command ... has been denied.` It must name `push` explicitly (see
  "Scope: gate the remote, not the flag"), and either way it is Bash-only.
- **An ask rule and a hook that both match produce ONE prompt, not two.** The displayed text is the
  rule's (`Ask rule ... overrides auto mode`), even though the hook's reason is also contributed.
  The hook's own text is visible when the hook matches alone (see below).
- **A rule covers one tool.** Every entry above is `Bash(...)`. None of them applies to a shell
  reached through another tool, and there is no way to write an argument-matching rule for one. See
  "Why `permissions.ask` cannot gate an MCP shell" above.

## Layer 3: the `publish-gate.sh` hook

A `PreToolUse` hook registered on every tool that can run a shell - see "The gates are per-tool"
above for why one tool name is not enough. It reads the tool input on stdin and prints a decision
on stdout:

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"ask",
 "permissionDecisionReason":"git push publishes: approve only if you meant to publish."}}
```

Registered in settings:

```json
{ "hooks": { "PreToolUse": [
  { "matcher": "^(Bash|mcp__lean-ctx__ctx_shell)$",
    "hooks": [ { "type": "command", "command": "/Users/<you>/.claude/hooks/publish-gate.sh" } ] }
]}}
```

Extend the alternation for each shell-capable tool you have. Anchor it, or the matcher
substring-matches every tool whose name contains `Bash`.

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
`for r in a b; do git push $r; done`, `find . -exec git push \;`, `xargs -n1 git push`,
`git push --no-verify`.

Verified to NOT match (correctly): `git push --dry-run`, `git tag -l`, `gh release list`,
`gh api <read>`, `git status`, `gh pr view`, `echo "git push"`, `igit push`, `git commit -m x`,
`git commit --no-verify`, `curl --no-verify`.

**Known gap, not closable by text matching:** script indirection. `bash deploy.sh` and
`./deploy.sh` publish with no prompt if the script pushes. Only the server-side layer covers this.
Do not describe the hook as covering "all git push operations" - it does not.

### Scope: gate the remote, not the flag

`--no-verify` is only interesting on `git push`. It is the escape hatch out of a `pre-push` hook,
and a push is what publishes. On a local command it is ordinary use - `git commit --no-verify` and
`git rebase --no-verify` touch nothing remote and are often deliberate - so gating them buys no
safety and costs the user a prompt on every one of them, which is the thing auto mode exists to
avoid. **A gate that fires on harmless local commands trains the user to approve without reading,
which is worse than no gate at all.**

So the target is: every push spelling with the flag is gated, and no local command is.

- **The hook needs nothing added.** Its publishing clause matches `git push` and ignores the rest
  of the line, so `git push --no-verify` was already caught - verified before any change was made.
  An earlier revision of this document added a clause matching `git` plus `--no-verify` on *any*
  subcommand. That was wrong for the reason above and has been removed.
- **The deny rules must name `push`.** The original pair was `Bash(git * --no-verify *)` and
  `Bash(git * --no-verify)`, which match *any* git subcommand - so `git commit --no-verify -m x`
  was denied, not just prompted. Both were replaced. The working pair is:

  ```json
  "Bash(git push --no-verify*)",
  "Bash(git * push --no-verify*)"
  ```

  Both are Bash-only, as every command-text rule is.
- **Watch the literal-space trap when writing these.** `Bash(git * push --no-verify*)` requires the
  literal text `" push"` - a space, then `push` - which `git push --no-verify` does not contain,
  because there is no space between `git` and `push`. That shape silently matches almost nothing.
  The bare form needs its own rule with no `*` between `git` and `push`. This is the same class of
  error as the trailing-space trap above, and it fails the same way: no match, no warning.
- **Verified after narrowing:** `git push --no-verify /nonexistent-path main` denied;
  `git -C <path> push --no-verify /nonexistent-path main` denied; `git commit --no-verify -m x`,
  `git rebase --no-verify` and the other local subcommands allowed. `tests/publish-gate-matrix.sh`
  asserts both directions in its `no-verify` block.

### Testing the hook's classifier without triggering anything

Feed it synthetic payloads directly. This tests the script's logic with zero side effects and no
prompts:

```sh
printf '{"tool_input":{"command":"sudo git push origin main"}}' | ~/.claude/hooks/publish-gate.sh
```

`sh tests/publish-gate-matrix.sh` is the case table kept as a regression matrix - 73 cases, run it
after any edit to the hook, and after adding a tool to the matcher. It asserts both directions
(what must ask, what must stay silent), includes a case set carrying the MCP tool name in the
payload so a payload-shape change fails loudly, and never executes the commands it tests. It takes
an optional path argument, so an edited copy can be tested before it is installed:
`sh tests/publish-gate-matrix.sh ./edited-copy.sh`.

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
2. Install `publish-gate.sh` and register it as a `PreToolUse` hook with a matcher listing **every
   shell-capable tool you have**, anchored (`^(Bash|mcp__<tool>)$`), and **no `if` field** (an `if` of
   `Bash(git *)` only skips non-matching calls, which the script already does).
3. **Start a new session** - hook config added mid-session will not load reliably - then prove
   liveness with the log-line probe above, **once per tool in the matcher**. A tool you did not
   probe is a tool you are assuming is covered.
4. Run the matrix (`sh tests/publish-gate-matrix.sh`) against the classifier.
5. Configure the GitHub environment with required reviewers, `prevent_self_review=false`.
6. Add `environment: release` to the release job; merge it.
7. Verify with a throwaway `v0.0.0-*` tag that the job reaches `waiting` with no Release, then
   approve it and confirm publication, then delete the Release and the tag.
8. Document the known gaps - script indirection, and any shell tool left out of the matcher -
   wherever the gate is described.

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
- **Assuming "Bash" means "the shell".** It means one tool. A command run through any other
  shell-capable tool - an MCP shell server, a language server, a background-job tool - reaches the
  machine with none of the Bash-scoped rules applied, and nothing in the transcript says so.
- **Writing an argument-matching rule for an MCP tool.** A parenthesized `mcp__` rule is discarded
  when the settings file loads, so it protects nothing while looking like it does. MCP rules are
  tool-granular only; see "Why `permissions.ask` cannot gate an MCP shell" above.
- **Gating a flag everywhere it appears.** `--no-verify` is only dangerous on a push. Gating it on
  `git commit` too fires a prompt on ordinary local work, and a prompt that fires on harmless
  commands is one the user learns to approve without reading. Scope gates to the operation that
  matters, not to the flag; see "Scope: gate the remote, not the flag" above.
- **Trusting an auto-created GitHub environment.** It has no protection rules.
