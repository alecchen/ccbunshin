#!/bin/sh
# Layer 5: a publish command written literally into a tracked script.
#
# Layer 4 (the required-reviewer environment on the release job) is the only
# gate that always holds, because it is server-side. This is the repo-side half
# of the script-indirection gap that layer 3 cannot see: the PreToolUse hook
# only ever sees the command Claude Code itself runs, so `bash deploy.sh` looks
# clean to it no matter what deploy.sh does.
#
# What this catches: `git push`, `git tag`, `gh release`, a `gh api` write,
# `gh repo delete`, or `gh pr merge` sitting literally in a tracked script.
#
# What this does NOT catch, and cannot:
#   - a command assembled at runtime (concatenation, printf, variable expansion)
#   - an encoded payload, base64 or otherwise
#   - a command fetched from a remote source and piped to a shell
#   - a publish in a file that is not a script (Makefile, package.json, *.yml)
#   - anything a script does with its input rather than with a literal
# It is a review tripwire for the obvious case, not a boundary.
#
# Usage: sh tests/publish-scan.sh [path ...]
#   No arguments: scan the tracked script files (executable in the index, or *.sh).
#   With paths: scan exactly those instead. That is the test seam, and it keeps
#   the test fixtures out of the repo, where this scanner would flag them.
set -eu

root=$(git rev-parse --show-toplevel)
cd "$root"

self=tests/publish-scan.sh
allowlist=tests/publish-scan-allowlist.txt

# Same six patterns the hook's normalizer matches, applied to the same
# normalized text - see normalize() below. Applied raw, a line like
# `echo "git push"` would match on the closing quote while the hook correctly
# ignores it, so the normalization is not optional.
pre='(^|[^[:alnum:]_-])'
post='([^[:alnum:]_-]|$)'
pattern="$pre"'git[[:space:]]+(push|tag)'"$post"
pattern="$pattern|$pre"'gh[[:space:]]+(release|api)'"$post"
pattern="$pattern|$pre"'gh[[:space:]]+repo[[:space:]]+delete'"$post"
pattern="$pattern|$pre"'gh[[:space:]]+pr[[:space:]]+merge'"$post"
# A gh api read is a GET; only a write names a method or passes a field.
write_verb='(-X|--method)[[:space:]]*(POST|PUT|PATCH|DELETE)|(-f|-F|--field|--raw-field|--input)([[:space:]]|=|$)'
# Read-only and local forms publish nothing, exactly as in the hook. Flagging
# them would train reviewers to allowlist harmless lines, which is the failure
# mode the hook's own exemption list exists to avoid.
exempt="$pre"'git[[:space:]]+push[[:space:]]+(-n|--dry-run)'"$post"
exempt="$exempt|$pre"'git[[:space:]]+tag[[:space:]]+(-l|--list|-v|--verify|--contains)'"$post"
exempt="$exempt|$pre"'gh[[:space:]]+release[[:space:]]+(list|view)'"$post"
exempt="$exempt|$pre"'gh[[:space:]]+(pr[[:space:]]+(view|list|diff|checks|status)|repo[[:space:]]+(view|list))'"$post"

# The two hook stages that change what its matcher sees, in the hook's order:
# unwrap a shell's -c body (anchored, which is what keeps `bash -c "git push"`
# as the body rather than mangling it), then drop remaining quoted strings
# (which is what makes `echo "git push"` invisible). Line counts are preserved,
# so grep -n still reports the file's real line numbers.
normalize() {
  sed -E 's/^(bash|sh|zsh|dash)[[:space:]]+-c[[:space:]]+"([^"]*)"[[:space:]]*$/\2/' "$1" 2>/dev/null |
    sed -E 's/"[^"]*"//g' |
    sed -E "s/'[^']*'//g"
}

# Path list: the arguments, or every tracked file (filtered below).
if [ "$#" -gt 0 ]; then
  files=$(printf '%s\n' "$@")
else
  files=$(git ls-files)
fi

# Comments and blank lines allowed; one repo-relative path per line.
allowed=$(sed -e 's/#.*//' -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//' -e '/^$/d' "$allowlist" 2>/dev/null || true)

is_allowed() {
  [ -n "$allowed" ] || return 1
  printf '%s\n' "$allowed" | grep -qxF -- "$1"
}

found=0
while IFS= read -r file; do
  [ -n "$file" ] || continue
  [ -f "$file" ] || continue
  [ "$file" = "$self" ] && continue

  if [ "$#" -eq 0 ]; then
    # Default mode only: keep scripts, drop everything else. The mode is read
    # from the index, so a file made executable but not staged still counts.
    case "$file" in
      *.sh) : ;;
      *)
        [ "$(git ls-files -s -- "$file" | awk '{print $1}')" = "100755" ] || continue
        ;;
    esac
  fi

  is_allowed "$file" && continue

  hits=$(normalize "$file" | grep -nE "$pattern" | grep -vE "$exempt" || true)
  [ -n "$hits" ] || continue

  # A gh api line only publishes when a write verb shares the line with it, so
  # the filtering happens here rather than in the exempt regex above.
  detail=$(printf '%s\n' "$hits" | while IFS= read -r hit; do
    text=${hit#*:}
    if printf '%s\n' "$text" | grep -qE 'gh[[:space:]]+api([[:space:]]|$)'; then
      printf '%s\n' "$text" | grep -qE "$write_verb" || continue
    fi
    printf '  %s:%s\n      %s\n' "$file" "${hit%%:*}" "$text"
  done)
  [ -n "$detail" ] || continue

  printf '%s\n' "$detail"
  found=1
done <<EOF
$files
EOF

if [ "$found" -ne 0 ]; then
  cat <<'MSG'

A script that publishes bypasses every local gate: the PreToolUse hook sees the
interpreter invocation, not what the script does. Either route the publish
through a gated path, or add the file to tests/publish-scan-allowlist.txt. That
file is tracked and reviewed on purpose - adding an entry is where someone
decides this is acceptable, so do not add one silently.
MSG
  exit 1
fi

echo "publish-scan: no publish commands in tracked scripts"
