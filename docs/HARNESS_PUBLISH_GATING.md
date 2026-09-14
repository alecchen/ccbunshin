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

Failure surfaces, in increasing order of severity:

1. **The agent pushes commits or tags** - made worse by GitHub Actions turning a tag push into a
   published release automatically.
2. **The agent adds `--no-verify`** to skip local git hooks, defeating any pre-push hook.
3. **The agent runs in a mode that bypasses local gates entirely** (`bypassPermissions`,
   `dontAsk`), or a settings file fails to load. The hook layer now blocks instead of asking in
   those two modes - see "When `ask` means nothing" - but the `permissions` rules still cannot
   fire there at all, and a settings file that fails to parse takes the hook registration with it.
4. **The agent never runs the gated tool at all** - it runs the same command through a different
   tool. See the next section; this is the failure that actually happened twice.
5. **The agent publishes through a script** the hook only sees the name of. Partially covered by
   layer 5's CI scan; see "Layer 5" below for exactly how far that goes.

## The five layers, and what each actually catches

| layer | where it lives | catches | misses |
| --- | --- | --- | --- |
| `permissions.ask` rules | `~/.claude/settings.json` | plain spellings: `git push *`, `git tag *`, `gh release *` | anything that doesn't match the command text prefix; **any tool that is not Bash** (no MCP spelling exists); **`bypassPermissions` / `dontAsk`**, where the prompt is never shown |
| `permissions.deny` | `~/.claude/settings.json` | `Bash(git push --no-verify*)` and `Bash(git * push --no-verify*)` - push only, so local `--no-verify` stays unprompted | other evasion; **any tool that is not Bash**; a local `--no-verify` (deliberate) |
| `publish-gate.sh` hook (`PreToolUse`) | `~/.claude/settings.json` + the script | subshells, `if`/`for`, `!`, `xargs`, `find -exec`, wrappers (`env`, `sudo`, `nohup`, `time`), `gh api` writes, `gh repo delete`, `gh pr merge` - and, once the matcher lists them, non-Bash shell tools. Denies in `bypassPermissions` / `dontAsk` for every tool in its matcher | script indirection (`bash deploy.sh`), except what layer 5 catches statically; any shell tool the matcher does not name |
| `tests/publish-scan.sh` in CI | `.github/workflows/build.yml`, `publish-scan` job | a publish command written **literally** into a tracked script | a command assembled at runtime, encoded, or fetched from a remote; files that are not scripts; anything the matcher never sees |
| **required-reviewer environment** | GitHub repo settings + `environment:` in the workflow | **everything**: the release job cannot create a release until a human approves | tag creation itself (the tag still lands) |

The first three are **local to one machine**. They do not survive `--no-verify` on a push that a
git hook would have caught, a settings file that fails to parse, a different machine, or a script
that pushes. Layer 3 now denies rather than asks when the session has no human in it (see
"When `ask` means nothing" below), which closes the `bypassPermissions` case for every tool in its
matcher - but an `ask` rule or a deny rule still cannot fire in those modes, and both remain
Bash-only. Layer 4 is server-side and survives all of those. Layer 5 is repo-side and runs in CI,
but only over tracked files, and only against literal text. If only one layer is affordable, build
layer 4.

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
- a `git push` through `ctx_shell` produced **no prompt and no deny** while the matcher was still
  `"Bash"`, while a `ctx_patch` on the hook file *was* blocked by the auto-mode classifier, so MCP
  tools are not globally exempt. This is the observation the matcher fix responds to - it predates
  `ctx_shell` being added to the alternation, and it is **no longer the current behavior** (below)
- **`permission_mode` is a real top-level `PreToolUse` field, and its live value here is `auto`** -
  captured from a real payload, not inferred. `auto` is on none of the five names Claude Code
  documents, which is why the hook's `case` defaults to `ask` rather than enumerating modes.
- **A hook-returned `ask` does surface as a prompt in `auto` mode, with the hook's own reason, and
  the command runs only on approval.** Verified 2026-09-14 by running `git push /nonexistent-path
  main` through `ctx_shell` - chosen because the `Bash(...)` ask rules do not cover MCP tools, so
  the hook was the *only* layer that could have prompted and the result cannot be attributed to a
  rule. A log line recorded `MODE=auto` and `DECISION=ask`; the human saw a prompt and approved;
  the command then failed on the bogus remote with exit 128. The `git push` through `ctx_shell` no
  longer goes unprompted, now that the matcher covers it.
- **A hook-returned `deny` actually blocks, and the block is visible to the agent.** Verified by
  mutation: the escalation's two mode names were temporarily swapped for `auto` (the live value -
  mutating to `default` would not have fired), then a real `git push /nonexistent-path main` came
  back as `Error: git push publishes, and permission_mode='auto' means no one is present to approve
  it, so the publish is denied.` The command did not run, and no human was needed to confirm it.
  The original pattern was restored and the matrix re-run (94/0). So `permission_mode` is read
  correctly from a real payload, and a deny reaches the agent as a tool result.

Taken from the docs and **not** locally exercised: the load-time skip of parenthesized `mcp__`
rules, the allow-glob restrictions, and the `--disallowedTools` deny route. The load-time skip is
the one worth confirming before leaning on it - an invalid-rule report for it would appear in
`claude doctor` in a fresh session.

**The `deny` path is verified too, and the way it was verified is reusable.** The escalation was
first exercised against synthetic `bypassPermissions` / `dontAsk` payloads only, which is not the
same as proving a real tool call gets blocked in a real mode. The gap was closed by mutation: the
two mode names in the hook's `case` were temporarily replaced with the mode the session actually
reports, a real publish-shaped command was run through a covered tool, and the command came back as

```
Error: git push publishes, and permission_mode='auto' means no one is present to approve it, so
the publish is denied.
```

The hook's own reason, in the tool result, naming the live mode - and the command never ran. The
original pattern was then restored and the matrix re-run.

Two things this settles that the `ask` test could not:

- **A `deny` is observable without a human.** It reaches the agent as a tool result, so the loop
  closes on its own. An `ask` never can - it needs a person to say they saw the prompt. When the
  question is "is this gate wired up," prefer testing the `deny` path for that reason.
- **`permission_mode` is read correctly from the real payload, not just from a fixture.** The reason
  string echoed back the live value, which is the same field the real escalation branches on.

**Mutate to the mode you are actually in, not to `default`.** An earlier draft of this paragraph
suggested pointing the pattern at `default`. That would not have fired in the session this was
tested in, whose payload reports `auto` - the mutation has to name a mode that will really match,
or the test silently proves nothing and looks like a failure. Check what your payload carries
first (the log-line probe above), then mutate to that value.

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

The same payload carries `permission_mode`, and the decision escalates to `deny` in the two modes
where the prompt would never reach anyone:

```json
{"hookSpecificOutput":{"hookEventName":"PreToolUse","permissionDecision":"deny",
 "permissionDecisionReason":"git push publishes, and permission_mode='bypassPermissions' means no one is present to approve it, so the publish is denied."}}
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

**Keep the probe to a single line, or that `sed` will not remove it cleanly.** The removal above
matches one line and deletes one line. A probe written as a `printf ... \` continuation is two
lines, so deleting the one holding `publish-gate.log` leaves the trailing backslash joining the
next line of the script - and the script then prints its debug text to **stdout**, which is the
decision channel, while `sh -n` still reports the file as fine. The failure is silent: the emitted
JSON is buried in extra output. After any probe removal, confirm stdout is exactly the decision:

```sh
printf '{"permission_mode":"auto","tool_input":{"command":"git push"}}' |
  ~/.claude/hooks/publish-gate.sh | wc -l    # 1 if the hook emitted a decision, 0 if it did not
```

Note that `wc -l` is only a count check; it says nothing about whether the prompt the decision asks
for ever reached a human. That is the next trap, and it needs a different instrument.

Two traps: the script logs on **manual invocations too**, so a non-empty log proves nothing unless
it was emptied immediately before the command. And the hook fires **before** the command runs, so
`: > logfile` as the first statement of a test command truncates the log *after* the hook already
wrote that call's line and erases the evidence.

And a third, which is the one that actually cost time: **a log line proves the hook ran, not that
its decision was honored.** A `DECISION=ask` in the log alongside a command that ran to completion
looks like "the prompt was ignored." It is equally consistent with "the prompt appeared and the
human approved it" - and the agent cannot tell those apart, because a prompt never reaches the
tool result. Do not read a completed command as evidence the gate failed. Ask the human, or
instrument something the ask actually produces.

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
`./deploy.sh` publish with no prompt if the script pushes. The hook cannot close this, because it
only sees the command Claude Code runs, never the script's contents. Layer 5's CI scan
(`tests/publish-scan.sh`) catches the static half - a publish command written literally into a
tracked script - but a command the script constructs at runtime stays open, and only the
server-side layer covers that. Do not describe the hook as covering "all git push operations" - it
does not.

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

### When `ask` means nothing: escalating to `deny`

An `ask` is only a gate if someone is there to answer it. Two permission modes guarantee nobody is:

- **`bypassPermissions`** skips the prompt entirely. The hook's `ask` is discarded, and the publish
  runs - the gate does nothing at exactly the moment the session is least supervised.
- **`dontAsk`** answers the prompt without asking. The command is refused, but the refusal does not
  name the publish reason and reads like any other blocked call, so nobody learns a publish was
  attempted.

Both make `ask` a silent no-op. The hook therefore reads `permission_mode` from the same stdin
payload and escalates:

```sh
case "$mode" in
  bypassPermissions|dontAsk) decision=deny ;;
  *)                         decision=ask ;;
esac
```

The deny reason names the mode, so the block is self-explaining:
`git push publishes, and permission_mode='bypassPermissions' means no one is present to approve it,
so the publish is denied.`

Details that matter:

- **The vocabulary is open, so the default must be `ask`.** The five names in the docs
  (`default`, `plan`, `acceptEdits`, `dontAsk`, `bypassPermissions`) are not exhaustive: the live
  `PreToolUse` payload on this machine carries `"permission_mode":"auto"`, which is on no list.
  Anything not exactly one of the two no-human values - including an unknown value and an absent
  field - keeps the prompt.
- **`auto` is not a mode where `ask` is meaningless, and that is verified rather than assumed.**
  The distinction the whole section rests on is that `ask` is a no-op in exactly two modes, not in
  "any mode that isn't the traditional interactive default." Since `auto` is this machine's normal
  mode, getting that wrong would have made the gate useless in daily use. Confirmed by triggering
  a real publish-shaped command under `auto` and observing the prompt (see "Verified here vs. read
  from the docs" above) - not by reasoning from the mode's name.
- **`--mode bypassPermissions` on the command line is covered; `settings.json` alone is not
  enough.** The mode has to reach the payload, and it does so from the session's mode. A settings
  file that fails to parse still takes the hook registration with it, which no hook-side change can
  fix.
- **`jq` absence must not silently undo this.** Both payload reads (`tool_input.command` and
  `permission_mode`) fall back to `sed` when `jq` is missing, because a machine without `jq` would
  otherwise revert to `ask` in exactly the two modes the escalation exists for - indistinguishable
  from a genuine `default`. `tests/publish-gate-matrix.sh` asserts the escalation under a PATH with
  no `jq` for that reason.
- **This does not extend coverage to a shell tool the matcher does not name.** The mode is read
  inside the hook, so the escalation only applies where the hook already runs. A tool left out of
  the matcher is still not gated at all, in any mode. That gap is unchanged and orthogonal to this
  one; see "The gates are per-tool" above.
- **Verified:** the deny fires for `bypassPermissions` and `dontAsk` and the prompt is unchanged
  for `default`, `plan`, `acceptEdits`, `auto`, and an absent field, on both `git push` and
  `git push --no-verify` - 21 cases in the matrix, including negative controls that keep the
  classifier's exemptions (`git push --dry-run`, `git commit --no-verify`) silent under
  `bypassPermissions`, since the mode must not turn a read-only form into a prompt.
- **The escalation's premise is verified end to end, not just in the script, on both branches.**
  A real `ctx_shell` call carrying `git push /nonexistent-path main` produced two facts at once: the
  hook's log line recorded `MODE=auto` and `DECISION=ask`, and the human saw a prompt. And a
  mutation that made a real mode deny produced the hook's own reason in the tool result, with the
  command never running. The two branches are verified by different instruments - a prompt needs a
  person, a block does not - which is itself worth knowing; see below.

### Two traps when testing the gate live

Both were hit while verifying the `auto` case above, and both produce a *false negative*: the gate
looks like it failed when it actually worked.

- **A completed command is not evidence the prompt was skipped.** The agent cannot see permission
  dialogs, so an approved command and a never-prompted command look identical in the tool result -
  the `exit 128` from the bogus remote looked exactly like "the gate did nothing." It had prompted;
  the human approved it. This is the "the agent cannot see permission dialogs" fact restated,
  because it is very easy to re-derive the wrong conclusion from a clean tool result. Prompt text
  has to come from the human, or from instrumentation that the prompt itself produces.
- **The probe-removal recipe above deletes one line, so the probe must be one line.** A probe
  written as a `printf ... \` continuation is two lines. Deleting only the one holding
  `publish-gate.log` leaves a trailing backslash joining the *next* line of the script, and the
  script then prints its debug text to **stdout** - the decision channel - while `sh -n` still
  reports the file as fine. Confirm stdout carries exactly one JSON line after any removal:

  ```sh
  printf '{"permission_mode":"auto","tool_input":{"command":"git push"}}' |
    ~/.claude/hooks/publish-gate.sh | wc -l    # 1 if the hook emitted a decision, 0 if it did not
  ```

This is also why a probe belongs on the payload read only (one line, one `>>`), never spread across
a continuation.

There is a second reason to prefer `deny` over `ask` when testing: a `deny` reaches the agent as a
visible tool result, so it needs no human in the loop to be confirmed. An `ask` can only ever be
confirmed by a person.

### Testing the hook's classifier without triggering anything

Feed it synthetic payloads directly. This tests the script's logic with zero side effects and no
prompts:

```sh
printf '{"tool_input":{"command":"sudo git push origin main"}}' | ~/.claude/hooks/publish-gate.sh
```

`sh tests/publish-gate-matrix.sh` is the case table kept as a regression matrix - 94 cases, run it
after any edit to the hook, and after adding a tool to the matcher. It asserts both directions
(what must ask, what must stay silent), includes a case set carrying the MCP tool name in the
payload so a payload-shape change fails loudly, and never executes the commands it tests. The
`permission_mode` and no-`jq` blocks read the decision out of the JSON rather than inferring it
from whether anything was printed, so `ask` and `deny` stay distinguishable; unparseable output
counts as a failure there, since a payload Claude Code cannot parse is one it ignores. It takes an
optional path argument, so an edited copy can be tested before it is installed:
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

## Layer 5: the CI script-indirection scan

`tests/publish-scan.sh`, run by the `publish-scan` job in `.github/workflows/build.yml`. It
greps tracked script files (executable in the index, or `*.sh`) for the same six patterns the
hook's normalizer matches - `git push`, `git tag`, `gh release`, a `gh api` write, `gh repo
delete`, `gh pr merge` - with the same read-only and local exemptions, so it flags no more than
the hook does.

It exists because layer 3 can only ever see the command Claude Code runs. `bash deploy.sh` is a
clean line to every local gate no matter what `deploy.sh` contains. This layer looks inside the
file instead, which is why it is the one place in the harness that can.

```yaml
  publish-scan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v7
      - run: sh tests/publish-scan-test.sh
```

What it still misses, and cannot catch:

- **A command assembled at runtime.** `"git pu""sh"`, `printf`, a variable built from parts, a
  loop over an argument list - the pieces are not a publish command on any one line.
- **An encoded payload.** base64 or any other transform; the bytes on the line are noise.
- **A command fetched from a remote at runtime** and piped to a shell.
- **A publish in a file that is not a script** - `Makefile`, `package.json`, a workflow `run:`
  block, a `.py`. Widening the file set trades a real gap for a large false-positive rate.
- **Anything a script does with its input** rather than with a literal it contains.

So it catches a publish command *written literally into a tracked file*, and nothing more. It is a
review tripwire, not a boundary.

Details that matter:

- **The allowlist is a tracked file, not a list in the workflow.** `tests/publish-scan-allowlist.txt`
  holds one path per line; adding to it is a reviewed diff, which is the point. It currently holds
  only `tests/publish-gate-matrix.sh`, which contains the patterns as match targets and never runs
  them. The scanner skips itself for the same reason.
- **It normalizes the way the hook does, and for the same reason.** Quote-stripping plus the
  `bash -c` unwrap happen before matching, so `echo "git push"` stays a near miss while
  `bash -c "git push"` is caught. Matching the raw text instead flags the first and misses the
  second - both directions were wrong in the first draft, and the test file catches it.
- **The test is what CI runs, and the fixtures are temp files.** No throwaway publish script is
  ever committed, because the scanner would flag it. `tests/publish-scan-test.sh` writes its
  fixtures to a temp directory and passes them as explicit paths.

  ```sh
  # the scanner itself, whose fixtures are temp files passed as explicit paths
  sh tests/publish-scan-test.sh

  # the scratch-branch case, without a scratch branch: a temp script that pushes
  printf '#!/bin/sh\ngit push origin main\n' > /tmp/scratch-deploy.sh
  sh tests/publish-scan.sh /tmp/scratch-deploy.sh   # exit 1, names the file and line

  # the allowlisted and scanner-own files must stay quiet
  sh tests/publish-scan.sh tests/publish-gate-matrix.sh tests/publish-scan.sh
  ```

  The explicit-path argument is what makes this testable before it is wired up, and it is why a
  failing case can be reproduced outside CI without pushing anything anywhere.
- **This job does not replace layer 4.** It runs on the same triggers, independent of `release`,
  and does not touch its `environment:`. A tag push still reaches the environment gate whether or
  not this job passes; a required reviewer is still what stops the Release.

## Rebuilding this from scratch: the order that works

1. Add the `permissions.ask` rules and the `--no-verify` deny rule to `~/.claude/settings.json`.
   Verify: run `git push <nonexistent-path> main` and confirm a prompt appears.
2. Install `publish-gate.sh` and register it as a `PreToolUse` hook with a matcher listing **every
   shell-capable tool you have**, anchored (`^(Bash|mcp__<tool>)$`), and **no `if` field** (an `if` of
   `Bash(git *)` only skips non-matching calls, which the script already does).
3. **Start a new session** - hook config added mid-session will not load reliably - then prove
   liveness with the log-line probe above, **once per tool in the matcher**. A tool you did not
   probe is a tool you are assuming is covered.
4. Run the matrix (`sh tests/publish-gate-matrix.sh`) against the classifier. It covers the
   `permission_mode` escalation and the no-`jq` fallback as well as the classifier itself.
5. Configure the GitHub environment with required reviewers, `prevent_self_review=false`.
6. Add `environment: release` to the release job; merge it.
7. Verify with a throwaway `v0.0.0-*` tag that the job reaches `waiting` with no Release, then
   approve it and confirm publication, then delete the Release and the tag.
8. Add `tests/publish-scan.sh`, its allowlist, and its test file, then the `publish-scan` job that
   runs the test. Verify by pointing the scanner at a temp script containing `git push` and
   confirming it exits 1 naming the file and line, then by running it against the repo. Consider
   making `publish-scan` a required status check.
9. Document the known gaps - script indirection (static case partially covered by step 8,
   runtime-constructed commands not), and any shell tool left out of the matcher - wherever the
   gate is described.

## Testing without publishing anything

- `--dry-run` tags and release names that cannot resolve (`v0.0.0-does-not-exist`), and a remote
  path that does not exist. Approving a prompt by reflex then costs nothing.
- **Watch the positional-argument trap:** in `git push <remote> <ref>`, the remote is a positional
  argument. If it is autocompleted away or typo'd, the command becomes `git push main` against the
  real `origin` and publishes. Confirm the remote is not a real one before running any push test.
- The hook's own exempted read-only forms (`git push --dry-run`, `git tag -l`, `gh release list`)
  are worth testing as negative controls; they prove the gate is not matching too broadly.
- **Never point the hook at a real remote to test it.** Feed it synthetic payloads instead
  (`sh tests/publish-gate-matrix.sh`), or use a remote path that cannot resolve. The
  `permission_mode` cases in particular are pure script runs - no git, no gh, nothing executed.
- **To prove the gate is live in your own mode, mutate rather than wait for a real publish.** Point
  the hook's escalation at the mode your session reports, run one publish-shaped command against a
  nonexistent remote, and confirm the block comes back in the tool result - then restore. This is
  the only check that closes the loop without a human, and it is the reason the `deny` branch is
  easier to verify than the `ask` branch. Back the hook up before editing, and re-run the matrix
  after restoring:

  ```sh
  cp ~/.claude/hooks/publish-gate.sh /tmp/hook.bak
  # swap the case pattern's modes for the value your payload carries - check the log-line
  # probe first, since it is `auto` on this machine and `default` elsewhere
  git push /nonexistent-path main      # must come back as a deny naming that mode
  cp /tmp/hook.bak ~/.claude/hooks/publish-gate.sh
  sh tests/publish-gate-matrix.sh
  ```

  Two ways this goes wrong: mutating to a mode you are not in, so nothing matches and the test
  proves nothing; and leaving the mutation in place, which turns a prompt into a hard block for
  every publish. The restore is not optional.

## What does not work

- **Asking the agent to be careful.** Not a control.
- **Relying on a git pre-push hook alone.** `--no-verify` skips it, and the agent can pass that
  flag.
- **Relying on the agent's report of whether a prompt appeared.** The agent cannot see dialogs.
- **Assuming `env` or a wrapper evades a permission rule.** It does not.
- **Relying on an `ask` in a session with no human in it.** `bypassPermissions` skips the prompt and
  `dontAsk` answers it without asking, so an `ask` there is a no-op that reads like a gate in the
  code. Only `deny` does anything in those modes; see "When `ask` means nothing" above.
- **Assuming the hook covers everything.** It does not cover script indirection. The CI scan
  (`tests/publish-scan.sh`) catches only the static half - a publish command written literally into
  a tracked script - and nothing catches a command the script builds at runtime; see "Layer 5".
- **Assuming a text scan covers a script that publishes.** It greps lines. Concatenation, encoding,
  a fetched payload, and a publish in a non-script file all pass it cleanly.
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
- **Trusting either `jq`-or-`sed` payload read as if the shape were fixed.** Both are one-line
  patterns over the payload. They cover the shape observed today, but a change to that shape
  degrades to "no opinion" - a silent allow - with no signal from the hook. The matrix is what
  makes that loud, which is why the no-`jq` case set exists.
