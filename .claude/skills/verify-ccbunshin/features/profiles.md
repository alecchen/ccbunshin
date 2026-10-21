# Profiles

A profile is a Claude Code settings file that ccbunshin creates, lists, inspects,
and deletes, with a shape check that stops a file Claude Code would reject from
ever being handed over.

## Sub-features

- `profile-create` creates `<name>.json` with mode 600 in a mode 700 directory,
  blank or copied from an example.
- `profile-create-refuse` refuses to overwrite an existing profile without `--force`
  and refuses a source file whose top level is not a JSON object.
- `profile-list` lists every profile with its configured model and survives one
  profile Claude Code would reject.
- `profile-status` shows the resolved path and model of one profile.
- `profile-doctor` exits non-zero, naming the file and the reason, for a rejected profile.
- `profile-delete` removes one profile file.

## How to get to it (user POV)

- Run `ccbunshin create <name> [--from <file>] [--force]`.
- Run `ccbunshin list`.
- Run `ccbunshin status <name>`.
- Run `ccbunshin doctor <name>`.
- Run `ccbunshin delete <name>`.

## Driving it with the sandbox

Preconditions:

- The sandbox is sourced, `HOME` and `CCBUNSHIN_PROFILES_DIR` point into
  `$VERIFY_DIR`, and `$VERIFY_PROFILES` is empty.
- `ccbunshin version` prints a local build identity.

- **Blank create.** Run `ccbunshin create provider1`. Exit code 0 and
  `$VERIFY_PROFILES/provider1.json` exists with mode 600; `ls -ld $VERIFY_PROFILES`
  shows 700.
- **Create from an example.** Run
  `ccbunshin create provider2 --from "$VERIFY_REPO/examples/provider2.json"`.
  Exit code 0 and `$VERIFY_PROFILES/provider2.json` has the same content as the
  example (`diff` exits 0).
- **Refuse to overwrite.** Run `ccbunshin create provider1`. Exit code is
  non-zero, stderr or stdout names `provider1`, and the existing file is unchanged.
- **Force overwrite.** Run `ccbunshin create provider1 --force`. Exit code 0.
- **Reject a non-object profile.** Write `null` into a file and create from it:
  `printf 'null\n' > "$VERIFY_DIR/bad.json"`;
  `ccbunshin create broken --from "$VERIFY_DIR/bad.json"`. Exit code is
  non-zero, the message names `bad.json`, and `$VERIFY_PROFILES/broken.json`
  does not exist (no partial file).
- **List.** Run `ccbunshin list`. Exit code 0, stdout has one line per profile
  beginning with the profile name and a tab, and `provider2`'s line reports its
  model.
- **Multiplication.** Write `null` into an existing profile;
  `printf 'null\n' > "$VERIFY_PROFILES/broken.json"; ccbunshin list`. Exit code
  0, the other profiles still list, and the bad one is reported instead of
  aborting the listing.
- **Status.** Run `ccbunshin status provider1`. Exit code 0 and stdout names
  `$VERIFY_PROFILES/provider1.json`. `status` does not apply the shape check -
  the caller that must refuse a broken profile is `doctor` or `launch`.
- **Doctor pass and fail.** `ccbunshin doctor provider1` exits 0.
  `ccbunshin doctor broken` exits non-zero and the message names the file.
- **Delete.** Run `ccbunshin delete provider2`. Exit code 0 and
  `$VERIFY_PROFILES/provider2.json` is gone; `ccbunshin list` no longer shows it.
- **Proof.** Capture `ls -l "$VERIFY_PROFILES"` and the captured stdout of
  each command above under `$VERIFY_DIR/artifacts/profiles/`.

## Gotchas

- `CCBUNSHIN_PROFILES_DIR` decides where profiles live; a run that forgets it
  writes into the user's real `~/.claude-profiles`.
- `create` with no `--from` writes a blank template, so `status` then reports
  `model=` with nothing after it. That is success, not a failure.
- The shape check is shape-only: unknown keys are left for Claude Code to judge,
  so `doctor` on a well-formed object with nonsense keys exits 0.
- `delete` has no confirmation prompt; point it only at the sandbox.
