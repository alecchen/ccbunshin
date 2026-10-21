#!/bin/sh
# End-to-end test for the ccbunshin project-aware claude wrapper.
#
# Builds the real binary, puts a recording fake `claude` stub and the real
# ccbunshin on PATH, then drives bash, zsh, and tcsh through the wrappers
# emitted by `ccbunshin init <shell>` and asserts routing, argv forwarding,
# transparency outside projects (with and without a global selection),
# idempotency, and exit-status propagation.
#
# Usage: tests/shell-integration.sh
# Requires: bash, zsh, tcsh, and a Go toolchain.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/bin"
LOG="$WORK/claude.log"
mkdir -p "$BIN" "$WORK/profiles"

# Build the real binary.
go -C "$ROOT/cmd/ccbunshin" build -o "$BIN/ccbunshin" .

# Fake claude: appends its argv as one line per invocation and honors CLAUDE_EXIT.
# It also prints the content of the settings file it was handed, so the test asserts
# which profile arrived by what the file says rather than by a path that changes on
# every launch.
cat > "$BIN/claude" <<'EOF'
#!/bin/sh
{
  printf 'argv:'
  for a in "$@"; do
    case "$a" in
      */ccbunshin-settings-*) printf ' [ccbunshin-settings-*]' ;;
      *) printf ' [%s]' "$a" ;;
    esac
  done
  printf '\n'
  prev=""
  for a in "$@"; do
    if [ "$prev" = "--settings" ] && [ -f "$a" ]; then
      tr -d ' \n' < "$a"
      printf '\n'
    fi
    prev=$a
  done
} >> "$CCBUNSHIN_FAKE_LOG"
exit "${CLAUDE_EXIT:-0}"
EOF
chmod +x "$BIN/claude"

export CCBUNSHIN_PROFILES_DIR="$WORK/profiles"
export CCBUNSHIN_FAKE_LOG="$LOG"
export PATH="$BIN:$PATH"

# Fixtures. Paths travel through the environment so per-shell code needs no
# positional arguments. The outer project and the plain dir paths have spaces.
PLAIN="$WORK/plain dir"
PROJ1="$WORK/proj one"
PROJ2="$WORK/proj one/app"     # nested project inside PROJ1
SET1="$WORK/profiles/provider1.json"
SET2="$WORK/profiles/provider2.json"
export PLAIN PROJ1 PROJ2 SET1 SET2

# The settings marker lines the fake claude matches on, one per profile.
M1='{"marker":"provider1"}'
M2='{"marker":"provider2"}'
export M1 M2

mkdir -p "$PROJ1/sub/deep" "$PROJ2" "$PLAIN"
printf 'provider1\n' > "$PROJ1/.ccbunshin-profile"
printf 'provider2\n' > "$PROJ2/.ccbunshin-profile"
printf '{"marker":"provider1"}\n' > "$SET1"
printf '{"marker":"provider2"}\n' > "$SET2"

# Expected argv lines, one per claude invocation below, in order. The settings path is
# compared by name only, because every launch gets a fresh temporary copy; which
# profile arrived is asserted by its content.
expected_file() {
  {
    printf 'argv: [-p] [hello world]\n'
    printf 'argv: [--settings] [ccbunshin-settings-*] [--model] [sonnet] [--resume] [abc] [-p] [fix this]\n%s\n' "$M1"
    printf 'argv: [--settings] [ccbunshin-settings-*]\n%s\n' "$M2"
    printf 'argv: [--settings] [ccbunshin-settings-*] [--settings] [foo.json]\n%s\n' "$M2"
    printf 'argv: [--settings] [ccbunshin-settings-*] [-p] [again]\n%s\n' "$M1"
    printf 'argv: [--settings] [ccbunshin-settings-*] [-p] [x]\n%s\n' "$M1"
  } > "$WORK/expected"
}

# Scenario body is shell-neutral: cd into a fixture and run claude directly.
SCEN='cd "$PLAIN"
claude -p "hello world"
cd "$PROJ1/sub/deep"
claude --model sonnet --resume abc -p "fix this"
cd "$PROJ2"
claude
claude --settings foo.json'

TAIL_SH='cd "$PROJ1"
claude -p "again"
CLAUDE_EXIT=7 claude -p x
s=$?
[ "$s" -eq 7 ] || exit 1'

TAIL_TCSH='cd "$PROJ1"
claude -p "again"
setenv CLAUDE_EXIT 7
claude -p x
if ( $status != 7 ) exit 1'

run_shell() {
  shell=$1
  init=$(ccbunshin init "$shell")
  echo "== $shell =="
  case "$shell" in
    bash) tail=$TAIL_SH; flags='--noprofile --norc -c' ;;
    zsh)  tail=$TAIL_SH; flags='-f -c' ;;
    tcsh) tail=$TAIL_TCSH; flags='-f -c' ;;
  esac
  expected_file
  : > "$LOG"
  # The second eval of the same init must be a no-op (no nesting, no re-alias).
  # tcsh goes through backquote eval like a real .tcshrc hook; backquotes
  # flatten newlines, so this also guards against multi-line tcsh output
  # ("Badly placed ()'s").
  if [ "$shell" = tcsh ]; then
    code=$(printf 'eval `ccbunshin init tcsh`\n%s\neval `ccbunshin init tcsh`\n%s\n' "$SCEN" "$tail")
  else
    code=$(printf '%s\n%s\n%s\n%s\n' "$init" "$SCEN" "$init" "$tail")
  fi
  "$shell" $flags "$code" || { echo "FAILED: $shell scenario exited $?"; return 1; }
  if ! diff -u "$WORK/expected" "$LOG"; then
    echo "FAILED: $shell argv mismatch"
    return 1
  fi
  echo "ok: $shell"
}

# A `claude` alias defined before the eval must not stop the wrapper from
# installing: zsh parses the whole eval string at once, so an alias named claude
# turns the POSIX `claude() { ... }` definition into a parse error.
run_alias_shadow() {
  shell=$1
  case "$shell" in
    bash) flags='--noprofile --norc -c' ;;
    zsh)  flags='-f -c' ;;
    *) return 0 ;;
  esac
  echo "== $shell (pre-existing claude alias) =="
  : > "$LOG"
  code=$(printf 'alias claude="echo SHADOWED"
eval "$(ccbunshin init %s)"
cd "$PROJ1"
claude -p "again"
' "$shell")
  "$shell" $flags "$code" || { echo "FAILED: $shell alias-shadow scenario exited $?"; return 1; }
  printf 'argv: [--settings] [ccbunshin-settings-*] [-p] [again]\n%s\n' "$M1" > "$WORK/expected"
  if ! diff -u "$WORK/expected" "$LOG"; then
    echo "FAILED: $shell wrapper did not survive a pre-existing claude alias"
    return 1
  fi
  echo "ok: $shell alias-shadow"
}

# The two halves of the no-marker contract: with no global selection the wrapper
# must stay transparent and run the plain claude binary, and with one set it must
# route through that profile. Both use $PLAIN, which has no .ccbunshin-profile
# marker anywhere above it.
run_global_fallback() {
  shell=$1
  case "$shell" in
    bash) flags='--noprofile --norc -c' ;;
    zsh)  flags='-f -c' ;;
    tcsh) flags='-f -c' ;;
  esac
  echo "== $shell (global fallback) =="
  init=$(ccbunshin init "$shell")
  if [ "$shell" = tcsh ]; then
    code=$(printf 'eval `ccbunshin init tcsh`\ncd "$PLAIN"\nclaude -p "no global"\n')
  else
    code=$(printf '%s\ncd "$PLAIN"\nclaude -p "no global"\n' "$init")
  fi

  : > "$LOG"
  rm -f "$CCBUNSHIN_PROFILES_DIR/global"
  "$shell" $flags "$code" || { echo "FAILED: $shell unset-global scenario exited $?"; return 1; }
  printf 'argv: [-p] [no global]\n' > "$WORK/expected"
  if ! diff -u "$WORK/expected" "$LOG"; then
    echo "FAILED: $shell mishandled a marker-less dir with no global selection"
    return 1
  fi

  : > "$LOG"
  printf 'provider1\n' > "$CCBUNSHIN_PROFILES_DIR/global"
  "$shell" $flags "$code" || { echo "FAILED: $shell set-global scenario exited $?"; return 1; }
  printf 'argv: [--settings] [ccbunshin-settings-*] [-p] [no global]\n%s\n' "$M1" > "$WORK/expected"
  if ! diff -u "$WORK/expected" "$LOG"; then
    echo "FAILED: $shell ignored the global selection outside a project"
    return 1
  fi
  rm -f "$CCBUNSHIN_PROFILES_DIR/global"
  echo "ok: $shell global-fallback"
}

failed=0
for shell in bash zsh tcsh; do
  if ! command -v "$shell" >/dev/null 2>&1; then
    echo "skip $shell (not installed)"
    continue
  fi
  run_shell "$shell" || failed=1
  run_alias_shadow "$shell" || failed=1
  run_global_fallback "$shell" || failed=1
done

[ "$failed" -eq 0 ] || exit 1
echo
echo "ALL SHELL INTEGRATION TESTS PASSED"
