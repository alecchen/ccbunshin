# Plan: tcsh completion for the CLI

Working note, 2026-09-14. The question was whether `ccbunshin completion tcsh`
should exist, after `ccbunshin init tcsh` already ships. Short answer: it can be
built, and the mechanism is different in kind from the bash and zsh scripts - not
just a third dialect of the same idea.

Two findings drive everything below. **TAB in tcsh does not list candidates**, so
a completion script that works is invisible to a user who does not know to press
`^D`. And **a backquoted completion command receives the whole command line in
`$COMMAND_LINE`**, which is the only way to vary position 2 by subcommand. The
second makes the feature worthwhile; the first makes its documentation load-bearing.

`ccbunshin completion tcsh` currently errors (`completion.go` `completionScript`,
"unsupported shell ... (use bash or zsh)"), so this is new work, not a fix.

## What was verified, and how

| Claim | Evidence |
| --- | --- |
| `complete` spec grammar, the word/pattern/list/suffix fields | `man tcsh`, `complete` builtin section |
| The available completion lists, including `(...)`, `$var`, `` `cmd` `` | Same, `list, the list of possible completions` |
| `$COMMAND_LINE` is set for a backquoted completion command | Same, and the shipped `svn` completion it cites as the worked example |
| Specs evaluate left to right, so a broad first spec shadows the rest | Same, called "a common mistake" in the man page |
| TAB is `complete-word`; `M-^D` is `list-choices`; `^D` at end of line is `delete-char-or-list-or-eof` | `bindkey` output, default bindings |
| `autolist` is unset by default | `$?autolist` is `0` under both `tcsh -c` and `tcsh -f -c`, and it is not in `/etc/csh.cshrc` |
| A backquoted spec registers and survives a round trip | `complete ccbunshin 'p/1/(`helper`)/'` then `complete ccbunshin` echoes it back, exit 0 |
| Backquote substitution flattens newlines | `docs/DECISIONS.md` and the comment above `tcshInit` in `main.go` |

**What was not verified: any of it end to end in a real interactive tcsh.** The
line editor could not be driven through the sandbox pty used here. A control case
(`cd /us` then `^D`, which cannot produce silence in a working tcsh) produced
silence, which means the harness never reached the editor, not that completion is
broken. Every claim above is therefore documentation or a non-interactive
observation. Confirm the interactive behaviour by hand before building - the
first item under "Order of work" is exactly that.

## How tcsh completion works

`complete` is not programmable in the bash sense. A completion is a list of
`word/pattern/list/suffix` specs, and there are no conditionals:

```
complete ccbunshin 'p/1/(init create launch ... )/' \
                   'n/init/p/2/(bash zsh tcsh)/' \
                   'n/proxy/p/2/(init start stop restart status)/'
```

- `p/N/` is positional; `n/pattern/` gates on the **previous** word, which is what
  lets position 2 differ per subcommand.
- The list can be a literal `(...)`, a `$var`, or `` `command` `` output.
- All specs go in **one** `complete` invocation. They evaluate left to right, so a
  broad `n/*` placed first would shadow every later spec.

That covers the static surface: the 16 commands, and the subcommand lists for
`init`, `completion`, and `proxy`. It does not cover profiles, which are read at
completion time by the bash and zsh scripts.

For those, the escape hatch:

> command invoked from `` `...` `` version has additional environment variable
> set, the variable name is `COMMAND_LINE` and contains ... contents of the
> current (already typed in) command line.

So a hidden subcommand - a sibling to `resolve-provider` and `run` - can read
`$COMMAND_LINE`, find the subcommand and position, and print candidate words one
per line. The man page's own `kill` example (`` `ps | awk \{print\ \$1\}` ``)
confirms newline-separated output is the expected shape.

This is reusable: bash and zsh would not need it, but any future shell without an
indexable words array would.

## The list-only problem

This is the part that needs a decision, not code.

`set autolist` makes tcsh list on an ambiguous completion, which is what zsh does
out of the box (`AUTO_LIST` and `LIST_AMBIGUOUS` are both on, even under `zsh -f`).
bash reaches the same place on the **second** TAB. tcsh, unconfigured, does
neither: TAB completes to the longest common prefix and rings the bell. For
`ccbunshin <TAB>` all 16 commands share no prefix, so nothing at all happens.

Three routes, in increasing intrusiveness:

1. **`^D`** - end-of-line listing, already bound. `ccbunshin <TAB><^D>` works with
   no configuration at all. `M-^D` lists anywhere on the line.
2. **`bindkey '^D' list-choices`** - makes `^D` unconditional rather than
   position-dependent. Changes one key, not the completion model.
3. **`set autolist`** - TAB itself lists. Correct, and the only route that gives
   parity with zsh, but it is a **shell-wide** variable: it changes completion for
   every command in that shell, not just this one.

Note that `set autolist` is a different category of change from sourcing a
completion file. Options 1 and 2 are additive to the shell; option 3 alters global
behaviour. A generated completion script that set `autolist` on source would
silently change the user's completion everywhere else, which is why the plan is to
**emit the completions and document the prerequisite**, never to have the script
set the variable itself.

## Open questions to settle before building

**Does a spec that lists nothing break `ccbunshin <TAB>` for an unrelated word?**
The specs are anchored to `ccbunshin`, so they should not fire elsewhere. Unverified.

**Does the backquoted helper run on every TAB press?** If so it spawns a process per
keystroke. The bash and zsh scripts only reach for `ccbunshin list` in the same
position, so the cost is comparable, but tcsh offers no way to cache it. Worth
measuring before shipping a helper that does more than `cut -f1 -s`.

**Does trailing-space completion behave?** The `suffix` field defaults to a space
for words and `/` for directories. `ccbunshin init bash ` is a complete command, so
the default is right, but a list that mixes both would need the field set explicitly.

## The tcsh-specific blocker

**Backquote substitution flattens newlines.** `tcshInit`'s comment in `main.go`
documents this for the alias and is why the wrapper must stay one line with no
leading comment. The install line is `eval \`ccbunshin init tcsh\``, and the same
flattening applies to a multi-line completion emission.

Two ways out, and they are not equivalent:

- **One logical line.** Feasible for the static specs; awkward once a backquoted
  helper and several `n/` specs are involved, and the result is unreadable in an
  rc file.
- **Write to a file and `source` it.** This is what the bash and zsh docs already
  recommend, and it sidesteps the flattening entirely. It also fits a tcsh quirk:
  `complete` state is per-shell, so a sourced file re-registers cleanly.

The recommendation is the file route, which makes the generated script's header
the same shape as the existing two: "write it here, source it from there". The
`eval` form stays reserved for the wrapper alias, which has no alternative.

## Feasibility

| Piece | Verdict |
| --- | --- |
| Static command and subcommand specs | Straightforward. Same surface as `topLevelCommands`. |
| A `$COMMAND_LINE` helper | Feasible, and the mechanism is documented. Needs a measurement first (above). |
| A `claude` completion for the tcsh wrapper | Feasible but low value. See below. |
| Parity with bash/zsh on TAB alone | Not achievable without changing a shell-wide variable. |
| Test coverage equal to bash/zsh | Not achievable. See below. |

The `claude` wrapper in tcsh is `alias claude 'ccbunshin run \!*'`, so
`complete claude ...` would register against the alias. The candidates, though, are
`claude`'s own flags, and the wrapper resolves its provider per directory at runtime
- the same limitation the zsh script documents for `_claude`, where the answer is to
hardcode a flag list rather than read anything dynamic. Do this only if the flag list
is worth maintaining in a third place.

## Verification, and its honest ceiling

`tests/completion-test.sh` drives bash through a real `compgen` call and zsh through
`compinit` in both activation paths. **Neither technique has a tcsh equivalent.** The
line editor is not scriptable, so a tcsh branch cannot assert that TAB inserts the
right text - the part most likely to break.

What a tcsh branch *can* assert:

1. The emitted script parses: `tcsh -f -c 'source ...'` exits 0.
2. The specs are registered: `complete ccbunshin` echoes back the spec list.
3. The helper classifies a given `$COMMAND_LINE` correctly, invoked directly:
   `COMMAND_LINE='ccbunshin launch ' ccbunshin complete-word` prints the profile names.
4. Drift: the command names in the script match `topLevelCommands`, extending
   `TestCompletionCoversCommands` (`main_test.go`) to the tcsh script.

That is a weaker guarantee than the other two shells get, and the gap is permanent
rather than a matter of effort. Assertions 1-3 are worth having; none of them would
catch a spec whose fields are ordered wrong, which is the failure mode the man page
specifically warns about. This is the strongest argument for treating the whole
feature as optional, and it should be stated in the doc rather than discovered later.

## Interaction with existing decisions

**Decision 12** (`docs/DECISIONS.md`: completion scripts are generated, and `init`
does not install them) rests on the shells not agreeing where a script goes. tcsh
makes that worse rather than better: it adds a third location convention, and
uniquely, a *required* shell variable for the feature to be usable at all. Nothing
in decision 12 needs reversing - generation stays, installation stays manual - but
the reasoning should gain the tcsh case if this ships, because tcsh is now the
clearest example of why `init` does not install completions.

**Decision 7** (the project-aware `claude` wrapper is a thin router over the
`.ccbunshin-profile` marker) is unaffected: completion reads the marker's *result*
and never sets it.

**Hidden commands.** `resolve-provider` and `run` are dispatchable but absent from
`usageText`, `topLevelCommands`, and the completion scripts, and
`TestCompletionCoversCommands` enforces that surface. A completion helper belongs in
the same category: dispatchable, hidden, and used by generated scripts. If the helper
is added, that test's contract is unchanged, but the helper needs its own help text
alongside the other two in `commandHelp`.

## Order of work

1. **Confirm the interactive behaviour by hand.** Open a real `tcsh`, register a
   throwaway spec with `complete`, and press TAB and `^D`. Everything above is
   documented rather than observed, and this is a five-minute check that decides
   whether the rest is worth doing.
2. **Settle the list-only question.** Decide whether the documented route is `^D`,
   `bindkey`, or `set autolist`. This is a documentation decision as much as a
   technical one, since the feature is invisible without it.
3. **Measure the backquoted helper's cost** per TAB press, and only then decide
   whether profiles are completed dynamically or not at all in v1. A static-only
   v1 (commands and subcommands, no profiles) is small, has no helper, and has no
   per-keystroke cost.
4. **Implement**, extending `completionScript` with a `tcsh` case and
   `topLevelCommands` as the single source of the command surface.
5. **Extend `tests/completion-test.sh`** with the four assertions above, and say
   plainly in its header what it cannot cover and why.
6. **Document the prerequisite** in both READMEs' shell-completion sections: a
   tcsh row in the existing table, plus the `autolist` / `^D` note the other two
   shells do not need.
