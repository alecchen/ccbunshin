#!/bin/sh
# End-to-end test for the shell completion scripts.
#
# Builds the real binary, then loads the generated script the way its shell
# loads it and drives it the way the shell would: bash through a real compgen
# call, and zsh through compinit, in both of the ways a user can activate it
# (the file on fpath, and a plain source from an rc file). The scripts are also
# checked for the command names and profile names they must offer, so the
# hand-kept lists inside them cannot drift from the CLI unnoticed.
#
# Usage: tests/completion-test.sh
# Requires: bash, zsh, and a Go toolchain.
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/bin"
mkdir -p "$BIN" "$WORK/profiles" "$WORK/zfunc"
go -C "$ROOT/cmd/ccbunshin" build -o "$BIN/ccbunshin" .

export CCBUNSHIN_PROFILES_DIR="$WORK/profiles"
export PATH="$BIN:$PATH"
printf '{}\n' > "$WORK/profiles/provider1.json"
printf '{}\n' > "$WORK/profiles/provider2.json"

BASH_SCRIPT="$WORK/ccbunshin.bash"
ZSH_SCRIPT="$WORK/_ccbunshin"
ccbunshin completion bash > "$BASH_SCRIPT"
ccbunshin completion zsh > "$ZSH_SCRIPT"
cp "$ZSH_SCRIPT" "$WORK/zfunc/_ccbunshin"

fail() { echo "FAIL: $1" >&2; exit 1; }

check_words() {
  got=$1
  shift
  for want in "$@"; do
    printf '%s\n' "$got" | grep -qx "$want" || fail "completion does not offer $want"
  done
}

# --- bash -------------------------------------------------------------------

bash -n "$BASH_SCRIPT" || fail "bash completion does not parse"

# A real completion call: the words offered for an empty first argument.
words=$(bash --noprofile --norc -c '
source "$1"
COMP_WORDS=(ccbunshin "")
COMP_CWORD=1
_ccbunshin
printf "%s\n" "${COMPREPLY[@]}"
' _ "$BASH_SCRIPT")
check_words "$words" init launch proxy completion uninstall version help

# The proxy subcommands, and a profile name read from "ccbunshin list".
words=$(bash --noprofile --norc -c '
source "$1"
COMP_WORDS=(ccbunshin proxy "")
COMP_CWORD=2
_ccbunshin
printf "%s\n" "${COMPREPLY[@]}"
' _ "$BASH_SCRIPT")
check_words "$words" init start stop restart status

words=$(bash --noprofile --norc -c '
source "$1"
COMP_WORDS=(ccbunshin launch "")
COMP_CWORD=2
_ccbunshin
printf "%s\n" "${COMPREPLY[@]}"
' _ "$BASH_SCRIPT")
check_words "$words" provider1 provider2

echo "ok: bash"

# --- zsh --------------------------------------------------------------------

zsh -n "$ZSH_SCRIPT" || fail "zsh completion does not parse"

cat > "$WORK/check-fpath.zsh" <<'EOF'
# Activation path 1: the file sits on fpath and compinit finds it. Nothing is
# sourced, so _ccbunshin is autoloaded from the file name.
fpath=("$1/zfunc" $fpath)
autoload -Uz compinit
compinit -u -d "$1/zcompdump"
print -r -- "ccbunshin=${_comps[ccbunshin]}"
print -r -- "claude=${_comps[claude]}"
EOF

cat > "$WORK/check-source.zsh" <<'EOF'
# Activation path 2: an rc file sources the script, after compinit has already
# run. The completion function has to be defined by the script itself.
autoload -Uz compinit
compinit -u -d "$1/zcompdump-source"
source "$1/_ccbunshin"
print -r -- "ccbunshin=${_comps[ccbunshin]}"
print -r -- "claude=${_comps[claude]}"
profiles=(${(f)"$(command ccbunshin list 2>/dev/null | cut -f1 -s)"})
print -r -- "profiles=${(j:,:)profiles}"
EOF

# Activation path 1 (fpath + compinit): compinit reads the #compdef line and
# never runs the rest of the file, so only ccbunshin is registered. Path 2
# (sourced from an rc file) registers both.
cat > "$WORK/expected-fpath" <<'EOF'
ccbunshin=_ccbunshin
claude=
EOF

cat > "$WORK/expected-source" <<'EOF'
ccbunshin=_ccbunshin
claude=_claude
profiles=provider1,provider2
EOF

if zsh -f "$WORK/check-fpath.zsh" "$WORK" > "$WORK/got-fpath" 2>"$WORK/err-fpath"; then
  if ! diff -u "$WORK/expected-fpath" "$WORK/got-fpath"; then
    fail "zsh fpath activation registered the wrong completions"
  fi
  echo "ok: zsh (fpath + compinit, ccbunshin only)"
else
  cat "$WORK/err-fpath" >&2
  fail "zsh fpath activation failed"
fi

if zsh -f "$WORK/check-source.zsh" "$WORK" > "$WORK/got-source" 2>"$WORK/err-source"; then
  if ! diff -u "$WORK/expected-source" "$WORK/got-source"; then
    fail "zsh source activation did not define the completions or parse profile names"
  fi
  echo "ok: zsh (source from an rc file)"
else
  cat "$WORK/err-source" >&2
  fail "zsh source activation failed"
fi

echo
echo "ALL COMPLETION TESTS PASSED"
