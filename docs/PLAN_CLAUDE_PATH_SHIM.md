# Plan: a PATH shim so `claude` routes without an rc file

Working note, 2026-09-21. Bash first, by request. The question was why `ccbunshin
init bash` / `init zsh` appears to fall back to a plain `claude` when the current
directory has no profile. The answer is that it does not fall back - the shell
wrapper is simply absent in the shells where this shows up. This note records the
measurement, why the rc-file wrapper cannot close the gap, and the design that can.

## The gap, measured

Same command, same machine, same binary, two shells. The stub `claude` below is a
script that records its argv and env, placed first on `PATH`, so `ccbunshin launch`
finds it and its own invocation is visible.

| Invocation | What ran |
| --- | --- |
| `zsh -i` (sources `~/.zshrc`) | `ccbunshin resolve-provider` -> `commandcode` -> `ccbunshin launch commandcode -p hello` -> `claude --settings ~/.claude-profiles/commandcode.json -p hello` |
| `zsh -f` (no rc file) | plain `claude -p hello`, no `--settings` |

The wrapper is a shell function, defined by `eval "$(ccbunshin init zsh)"` at
`~/.zshrc:225`. A process that never sources that file has no function, so `claude`
resolves to the binary on `PATH` and no profile is consulted. This is not about
markers or the global fallback - both work correctly whenever the wrapper runs
(covered by `TestResolveProviderFallsBackToGlobal` and the `run_global_fallback`
scenario in `tests/shell-integration.sh`).

Processes with no rc file include `bash script.sh`, `zsh -f`, `sh -c`, ssh command
execution, cron, editor run-tasks, make, test runners, and anything launched from
the GUI. `README.md:169` documents the interactive contract only, so the gap is
undocumented rather than contradicted.

## Why the rc wrapper cannot close it

The wrapper is defined by the rc file. There is no way to define it in a shell that
does not read the rc file. Fixing this outside the shell is the only option.

One partial mitigation already exists and is worth stating: `ccbunshin launch` and
`ccbunshin run` work from any shell, because resolution happens in the binary
(`findProfile`, `main.go:868`). What is missing is a way for a bare `claude` to
reach them.

## Options

1. **Exported shell function** (`export -f claude`, `BASH_FUNC_claude%%` in the
   environment). Bash-only, stripped by `env -i` and most sudo configurations,
   invisible to every other shell, and still requires an rc file to set up - the
   same limitation it is meant to fix. Rejected.
2. **`CCBUNSHIN_*` env var checked by Claude Code.** Not available: Claude Code is
   not ours to modify, and decision 4 keeps credentials and config out of the
   environment.
3. **PATH shim** (chosen). A `claude` entry early on `PATH` that is the ccbunshin
   binary. Reached from any shell, rc file or not.

## Design

`<shim dir>/claude` is a symlink to the ccbunshin executable. The binary dispatches
on `filepath.Base(os.Args[0])`:

- `claude` (or `claude-shim`) -> resolve the profile for the cwd and exec the real
  `claude` with `--settings`, or exec the real `claude` unchanged when no profile
  resolves. This is `runProjectClaude` (`main.go:1402`) with the resolution it
  already performs.
- anything else -> today's CLI dispatch.

Self-location, which is the part that has to be right:

```go
self, _ := os.Executable()          // symlink path, not the target
if r, err := filepath.EvalSymlinks(self); err == nil { self = r }
```

Then walk `filepath.SplitList(os.Getenv("PATH"))` in order and take the first
`claude` that is executable, not a directory, and whose `EvalSymlinks` differs from
`self`. Skipping self rather than erroring is what makes a second shim dir, or a
shim dir that appears twice on `PATH`, harmless.

Verified today against a prototype: invoked as `/tmp/argv0/shimdir/claude` with
`PATH=/tmp/argv0/shimdir:/tmp/argv0:...`, `os.Executable` returned the symlink path,
`EvalSymlinks` returned the real binary, and the walk skipped the shim and selected
`/tmp/argv0/claude` (the stand-in for the real claude).

Note the tempting wrong implementation: `exec.LookPath` returns the shim itself
(confirmed in the same prototype: `LookPath` resolved to `/tmp/argv0/shimdir/claude`).
The walk must be manual.

### Recursion

The only loop risk is the shim selecting itself, which the skip handles. Belt and
braces, if wanted: refuse when a `CCBUNSHIN_SHIM_DEPTH` counter crosses a low
threshold. Not planned - the self-skip is sufficient and testable, and an env
counter adds a variable to the contract for no measured failure.

### Ordering inside `main`

The argv0 dispatch must come before the `--proxy-server` check at `main.go:2201`.
A process named `claude` is never the proxy daemon, so the order is only a
correctness guard for a hypothetical `claude --proxy-server`, but it is free.
Related: `proxyBinary()` returns `os.Args[0]` (`main.go:939`), so `proxy start`
re-execs whatever name it was invoked under - unchanged by this plan, worth not
breaking.

### Installed shim path

`~/.local/bin` already holds the `claude` symlink on this machine
(`~/.local/bin/claude` -> `~/.local/share/claude/versions/2.1.278`), so a symlink
cannot simply be written there without clobbering the real binary. The shim needs a
directory of its own, prepended to `PATH`, defaulting to `~/.local/share/ccbunshin/shim`.

PATH order on this machine already has `~/.local/bin` eighth, so prepending a
dedicated directory is the reliable route either way.

### Scope of the fix

The shim covers every process that inherits a `PATH` containing the shim dir.
That includes non-interactive bash, `sh`, zsh, tcsh, ssh sessions, and the children
of any shell that exported the line. It does not cover processes started with the
system default `PATH` - GUI apps, `launchd`, some cron entries - unless the
directory is also set there. This is worth documenting rather than implying the
shim is universal.

## Command surface

Extend `init` rather than adding a top-level command, since `init` already owns
"set this shell up" and `uninstall` already owns removing what `init` wrote:

```
init <shell>              install the rc hooks and the shim symlink
init <shell> --no-shim    rc hooks only, the current behavior
init --shim-dir           print the shim directory, creating it if needed
```

`init --shim-dir` is the composable form, so a user who manages `PATH` themselves
can write `export PATH="$(ccbunshin init --shim-dir):$PATH"`.

`uninstall` removes the symlink alongside the rc hooks it already removes. It must
only remove a symlink that points at the ccbunshin binary; a `claude` symlink
pointing anywhere else is left alone, matching the exact-match discipline
`uninstallCLI` already uses.

Adding `init <shell>` a flag means `topLevelCommands` and the completion scripts do
not change, but `commandHelp("init")` and `usageText` do.

## Files

| File | Change |
| --- | --- |
| `cmd/ccbunshin/main.go` | argv0 dispatch in `main`; shim install/print in `initCLI`; symlink removal in `uninstallCLI`; `commandHelp("init")` and `usageText` |
| `cmd/ccbunshin/shim.go` (new) | `realClaude()`, `runShim()`, shim path and symlink management, so `main.go` does not grow a fourth concern |
| `cmd/ccbunshin/shim_test.go` (new) | unit tests below |
| `cmd/ccbunshin/main_test.go` | `init` flag parsing, help coverage |
| `tests/shell-integration.sh` | shim scenarios across bash, zsh, tcsh |
| `README.md`, `cmd/ccbunshin/README.md` | the shim, the PATH line, and what it does not cover |
| `docs/DECISIONS.md` | new entry: why a PATH shim over an exported function, and why argv0 dispatch |

## Verification

Unit, in `shim_test.go`:

- `realClaude` skips a candidate that resolves to self and returns the next one.
- `realClaude` returns "" when every candidate is self, and the shim then errors
  rather than looping.
- `realClaude` ignores non-executable and directory entries named `claude`.
- argv0 dispatch: base `claude` takes the shim path, base `ccbunshin` takes the CLI
  path, including `resolve-provider` and `launch` unchanged.

End to end, in `tests/shell-integration.sh`, using the existing recording-stub
pattern:

- A shim dir prepended to `PATH`, no rc file, `claude -p hello` in a marker
  directory records `--settings <SET1>`; the same command in the plain directory
  records the bare argv.
- Same two cases with a global selection set, which is the reported symptom.
- `ccbunshin uninstall` removes the symlink and leaves a `claude` symlink pointing
  elsewhere untouched.

The last one needs care: the existing harness puts a fake `claude` script on `PATH`,
so the shim has to be symlinked to the test binary and `PATH` reordered to put the
shim dir first.

Commands:

```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
sh tests/shell-integration.sh
```

## Order of work

1. `realClaude` plus its unit tests. It is the whole risk; everything else is
   plumbing.
2. argv0 dispatch and the manual check that `ccbunshin resolve-provider`,
   `ccbunshin launch`, and `claude` all still behave from a real interactive shell.
3. Shim install and print in `init`, removal in `uninstall`.
4. Shell integration scenarios.
5. Docs: README, `cmd/ccbunshin/README.md`, and the `docs/DECISIONS.md` entry.

## Open questions

- **Does the interactive wrapper get removed for bash?** The shim supersedes it:
  one mechanism, rc file or not. Keeping both is harmless (the function wins in an
  interactive bash, the shim covers everything else), and it is the lower-risk
  rollout. Recommend keeping both for the release that adds the shim, then
  dropping the bash function in a follow-up with its own decision entry. zsh and
  tcsh keep the rc wrapper either way.
- **Should `ccbunshin launch` prefer the shim path?** No. `runProjectClaude`
  (`main.go:1410`) execs `"claude"` through `PATH`; if a shim is first, that exec
  re-enters the binary, which resolves again and skips self. Correct, one extra
  process. Calling `realClaude()` directly there would avoid the extra exec but
  adds a second code path with the same result. Deferred unless the exec cost is
  measurable.
- **`CLAUDE_CODE_AUTO_MODE_SERVER` and the port discrepancy.** Unrelated to this
  plan but found while measuring: on this machine the shell exports
  `ANTHROPIC_BASE_URL=http://127.0.0.1:4444` (`~/.zshrc:236`) while the
  `commandcode` profile sets `:3456`, and the profile's value lost. That matches
  decision 2 in `docs/DECISIONS.md:27`, but the two configs disagreeing is worth a
  look on its own.
