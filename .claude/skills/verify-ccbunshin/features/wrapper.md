# Launch and the claude wrapper

`ccbunshin launch` runs Claude Code with a profile's settings file and forwards
every argument unchanged; the shell wrapper makes bare `claude` do the same
inside a project.

## Sub-features

- `launch-explicit` runs `claude --settings <profile> <args...>` and forwards arguments
  at the argument-vector level.
- `launch-exit` returns Claude's exit status.
- `launch-refuse` refuses a profile Claude Code would reject, naming the file, before
  starting Claude Code.
- `wrapper-project` makes `claude` inside a project resolve to `ccbunshin launch <profile>`.
- `wrapper-outside` leaves `claude` unchanged outside any project and with no global.
- `wrapper-idempotent` re-running the emitted hook adds no nested wrapper.
- `wrapper-shells` works in bash and zsh (functions) and tcsh (alias).

## How to get to it (user POV)

- Run `ccbunshin launch [<name>] [claude args...]` directly.
- Run `claude ...` after `ccbunshin init` (or `eval "$(ccbunshin init bash|zsh)"`
  / `eval \`ccbunshin init tcsh\``) in a project directory.

## Driving it with the sandbox

Preconditions:

- `$VERIFY_BIN` is first on `PATH` and holds an executable recording stub named
  `claude` that appends its argv to `$VERIFY_DIR/claude.log` and exits
  `${CLAUDE_EXIT:-0}` (copy the stub from `tests/shell-integration.sh`).
- A project directory with a `.ccbunshin-profile` marker naming `provider1`, and
  a plain directory with no marker and an ancestor with none.
- No global profile set (`ccbunshin global --unset`).

- **Explicit launch.** Run `ccbunshin launch provider1 -p "hello world"`. Exit
  code 0 and `$VERIFY_DIR/claude.log`'s last line is
  `argv: [--settings] [$VERIFY_PROFILES/provider1.json] [-p] [hello world]`.
- **No profile named.** From the plain directory with a global of `provider2`,
  `ccbunshin launch` uses `provider2`'s settings file.
- **Exit status.** Run `CLAUDE_EXIT=7 ccbunshin launch provider1; echo $?`. The
  printed status is 7.
- **Refuse a broken profile.** Write `null` into a profile, then run
  `ccbunshin launch broken; echo $?`. Non-zero, the message names the file, and
  `$VERIFY_DIR/claude.log` gains no line (the stub never ran).
- **Wrapper inside a project.** In a bash subshell:
  `cd "$VERIFY_DIR/proj" && eval "$(ccbunshin init bash)" && claude -p "hi"`.
  Exit code 0 and the log line carries `--settings $VERIFY_PROFILES/provider1.json`.
- **Wrapper outside a project.** From the plain directory with no global:
  `eval "$(ccbunshin init bash)" && claude -p "hi"`. The log line is
  `argv: [-p] [hi]` with no `--settings`.
- **Idempotency.** Evaluate the init output twice, then call `claude` once. The
  log gains exactly one line and the shell reports no "command not found" or
  nested-wrapper error.
- **Other shells.** Repeat the project case with `eval "$(ccbunshin init zsh)"`
  in `zsh` and `eval \`ccbunshin init tcsh\`` in `tcsh`. `sh tests/shell-integration.sh`
  drives all three end to end; run it rather than re-deriving the fixtures.
- **Proof.** Capture `$VERIFY_DIR/claude.log` in full plus the exit codes, under
  `$VERIFY_DIR/artifacts/wrapper/`.

## Gotchas

- The wrapper replaces any `claude` shell function or alias the user defined;
  run wrapper recipes in a fresh subshell, never in the interactive session.
- In tcsh, `ccbunshin init tcsh` output must be eval'd with backticks; the alias
  delegates to the hidden `ccbunshin run` command.
- macOS ships bash 3.2 and zsh; tcsh may be absent on Linux images. Check
  `command -v tcsh` before running the tcsh recipe and report it as skipped
  rather than failed.
- A real launch starts the real Claude Code. Always put the recording stub
  first on `PATH`; a recipe that forgets it opens an interactive session.
- Hook install writes to `~/.bashrc` / `~/.zshrc`. Use `ccbunshin init bash`
  (printed to stdout, eval'd) rather than bare `ccbunshin init`, which edits the
  sandbox `HOME`'s rc files.
