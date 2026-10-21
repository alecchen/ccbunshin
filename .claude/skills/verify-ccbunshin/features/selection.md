# Project and global selection

A user picks a profile per directory with a `.ccbunshin-profile` marker, or one
fallback profile for every directory without a marker, and the nearest marker
wins.

## Sub-features

- `local-set` writes a `.ccbunshin-profile` marker in the current directory.
- `local-show` reports the selected profile and the marker file it came from.
- `local-unset` removes the marker.
- `global-set` writes the fallback profile name to a single file.
- `global-show` reports the fallback profile and its source file.
- `precedence` makes a local marker win over the global fallback, and the nearest
  marker win over an ancestor's.

## How to get to it (user POV)

- Run `ccbunshin local <name>`, `ccbunshin local`, `ccbunshin local --unset`
  from the project directory.
- Run `ccbunshin global <name>`, `ccbunshin global`, `ccbunshin global --unset`
  from anywhere.

## Driving it with the sandbox

Preconditions:

- At least two profiles exist (`ccbunshin create provider1`, `ccbunshin create provider2`).
- A scratch project directory exists outside the repo:
  `mkdir -p "$VERIFY_DIR/proj"`.

- **Set locally.** From `$VERIFY_DIR/proj`, run `ccbunshin local provider1`.
  Exit code 0; `"$VERIFY_DIR/proj/.ccbunshin-profile"` contains exactly
  `provider1` (one line).
- **Show locally.** From the same directory, run `ccbunshin local`. Exit code 0
  and stdout names `provider1` and the absolute path of the marker.
- **Nearest marker wins.** Create `"$VERIFY_DIR/proj/app"` with its own marker
  containing `provider2`. From `"$VERIFY_DIR/proj/app"`, `ccbunshin local`
  reports `provider2`; from `"$VERIFY_DIR/proj"` it still reports `provider1`.
- **Unset.** From `"$VERIFY_DIR/proj/app"`, run `ccbunshin local --unset`. Exit
  code 0 and that `.ccbunshin-profile` is gone; `ccbunshin local` there now
  reports the ancestor's `provider1`.
- **Set globally.** From any directory, run `ccbunshin global provider2`. Exit
  code 0 and `$VERIFY_PROFILES/global` contains `provider2`.
- **Show globally.** Run `ccbunshin global`. Exit code 0 and stdout is
  `profile: provider2` followed by the source path.
- **Precedence.** With a global of `provider2` and a local marker of
  `provider1`, `ccbunshin local` reports `provider1` (the local marker exists and
  is what `launch` uses); a directory with no marker reports the global through
  `ccbunshin global`.
- **Unset globally.** Run `ccbunshin global --unset`. Exit code 0 and
  `$VERIFY_PROFILES/global` is gone; `ccbunshin global` reports no selection.
- **Proof.** Capture every marker file's contents, the `.ccbunshin-profile`
  removal, and the stdout of each command under `$VERIFY_DIR/artifacts/selection/`.

## Gotchas

- `local <name>` writes into the current directory, not the repo root. Run it
  from `$VERIFY_DIR`, or it leaves an untracked marker in the working tree.
- `global` is a reserved profile name in `ccbunshin launch`: it selects the
  fallback profile, never a file named `global.json`.
- The global selection is one file, not an environment variable, so it survives
  a new shell. A run that expects "no global" must unset it first.
- A marker above the repo root can leak into a run started inside the repo:
  check `ccbunshin local` before concluding that a wrapper chose a profile.
