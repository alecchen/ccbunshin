#!/bin/sh
# Tests for tests/publish-scan.sh (layer 5).
#
# Fixtures are written to a temp directory and passed to the scanner as explicit
# paths, so nothing throwaway is ever added to the repo - where the scanner
# would then flag its own fixtures.
#
# Usage: sh tests/publish-scan-test.sh [path-to-scanner]
set -eu

here=$(cd "$(dirname "$0")" && pwd)
root=$(cd "$here/.." && pwd)
scanner=${1:-$here/publish-scan.sh}
case "$scanner" in
  /*) : ;;
  *) scanner="$root/$scanner" ;;
esac
# Tracked as mode 644 like every other script here, so the file need only be
# readable - the scanner is invoked with sh, not exec'd.
[ -r "$scanner" ] || { echo "not readable: $scanner" >&2; exit 2; }

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
cd "$root"

pass=0
fail=0

# want is "fail" (scanner must exit non-zero and name the file) or "pass".
# The scanner is run with sh rather than exec'd, matching how CI and the repo's
# other tests invoke it, and how it is tracked (mode 644).
check() {
  want=$1
  label=$2
  shift 2
  out=$(sh "$scanner" "$@" 2>&1) && got=pass || got=fail
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL [want %s, got %s] %s\n' "$want" "$got" "$label"
    printf '%s\n' "$out" | sed 's/^/    /'
  fi
}

# The failure has to name the file and the line, or it is not actionable.
check_names() {
  file=$1
  line=$2
  out=$(sh "$scanner" "$file" 2>&1) || true
  case "$out" in
    *"$file:$line"*) pass=$((pass + 1)) ;;
    *) fail=$((fail + 1)); printf 'FAIL [output omits %s:%s]\n%s\n' "$file" "$line" "$out" ;;
  esac
}

printf '#!/bin/sh\ngit push origin main\n' > "$work/push.sh"
printf '#!/bin/sh\nset -eu\necho deploying\n\ngit tag -a v1.0.0 -m release\n' > "$work/tag.sh"
printf '#!/bin/sh\ngh release create v1.0.0\n' > "$work/release.sh"
printf '#!/bin/sh\ngh api -X POST repos/o/r/releases\n' > "$work/apiwrite.sh"
printf '#!/bin/sh\ngh repo delete owner/repo\ngh pr merge 42\n' > "$work/ghdestructive.sh"
printf '#!/bin/sh\nbash -c "git push"\n' > "$work/compound.sh"

# Negative controls: forms the hook also exempts, and local commands.
printf '#!/bin/sh\ngit push --dry-run\n' > "$work/dryrun.sh"
printf '#!/bin/sh\ngit tag -l\n' > "$work/taglist.sh"
printf '#!/bin/sh\ngh release list\ngh api repos/o/r/releases\n' > "$work/reads.sh"
printf '#!/bin/sh\ngit commit --no-verify -m x\ngit status\n' > "$work/local.sh"
printf '#!/bin/sh\necho "git push"\nigx push\n' > "$work/nearmiss.sh"
printf '#!/bin/sh\necho deploying\n' > "$work/clean.sh"

echo "scanner: $scanner"

# --- catches a literal publish -------------------------------------------
check fail 'git push'                  "$work/push.sh"
check fail 'git tag'                   "$work/tag.sh"
check fail 'gh release create'         "$work/release.sh"
check fail 'gh api write'              "$work/apiwrite.sh"
check fail 'gh repo delete + pr merge' "$work/ghdestructive.sh"
check fail 'compound bash -c'          "$work/compound.sh"
check_names "$work/push.sh" 2
check_names "$work/tag.sh" 5

# --- stays quiet on the exempt and local forms ---------------------------
check pass 'git push --dry-run'        "$work/dryrun.sh"
check pass 'git tag -l'                "$work/taglist.sh"
check pass 'gh release list / api GET' "$work/reads.sh"
check pass 'local commit/status'       "$work/local.sh"
check pass 'near misses'               "$work/nearmiss.sh"
check pass 'no publish at all'         "$work/clean.sh"

# --- the real tree -------------------------------------------------------
# Must pass with the ask rules, the hook, and the matrix file all present.
# publish-scan-allowlist.txt is what keeps the fixture-bearing files from
# failing this - both the matrix and this file's own name.
check pass 'the repo itself'
out=$(cd "$root" && sh "$scanner" 2>&1) || true
case "$out" in
  *publish-gate-matrix.sh*|*publish-scan-test.sh*)
    fail=$((fail + 1)); printf 'FAIL [allowlist did not cover a fixture-bearing file]\n%s\n' "$out" ;;
  *) pass=$((pass + 1)) ;;
esac

echo "pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
