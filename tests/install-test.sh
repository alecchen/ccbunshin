#!/bin/sh
# Tests for install.sh release download selection.
#
# install.sh must default to GitHub's stable latest-release endpoint, pin to a
# specific version via CCBUNSHIN_VERSION, honor CCBUNSHIN_REPO and
# CCBUNSHIN_INSTALL_DIR, and leave platform detection untouched. A stub curl
# records the requested URL without touching the network.
#
# Usage: sh tests/install-test.sh
set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/bin"
PREFIX="$WORK/prefix"
mkdir -p "$BIN" "$PREFIX"

cat > "$BIN/curl" <<'EOF'
#!/bin/sh
printf '%s\n' "$*" >> "$CCBUNSHIN_CURL_LOG"
out=""
prev=""
for a in "$@"; do
  if [ "$prev" = "-o" ]; then out="$a"; fi
  prev="$a"
done
[ -n "$out" ] && : > "$out"
exit 0
EOF
chmod +x "$BIN/curl"

export PATH="$BIN:$PATH"
export CCBUNSHIN_CURL_LOG="$WORK/curl.log"

fail() { echo "FAIL: $1" >&2; exit 1; }

# 1. Default download uses /releases/latest/download/<asset>.
: > "$CCBUNSHIN_CURL_LOG"
CCBUNSHIN_INSTALL_DIR="$PREFIX" sh "$ROOT/install.sh" >/dev/null
grep -q '/releases/latest/download/' "$CCBUNSHIN_CURL_LOG" \
  || fail "default URL is not /releases/latest/download/<asset>"
grep -q 'ccbunshin-linux-amd64\|ccbunshin-linux-arm64\|ccbunshin-darwin-amd64\|ccbunshin-darwin-arm64' "$CCBUNSHIN_CURL_LOG" \
  || fail "default URL has no stable asset name"

# 2. Pinned CCBUNSHIN_VERSION uses /releases/download/<version>/<asset>.
: > "$CCBUNSHIN_CURL_LOG"
CCBUNSHIN_VERSION=v0.0.3 CCBUNSHIN_INSTALL_DIR="$PREFIX" sh "$ROOT/install.sh" >/dev/null
grep -q '/releases/download/v0.0.3/' "$CCBUNSHIN_CURL_LOG" \
  || fail "pinned URL does not use /releases/download/v0.0.3/"

# 3. CCBUNSHIN_REPO overrides the repository.
: > "$CCBUNSHIN_CURL_LOG"
CCBUNSHIN_REPO=acme/ccbunshin CCBUNSHIN_INSTALL_DIR="$PREFIX" sh "$ROOT/install.sh" >/dev/null
grep -q 'https://github.com/acme/ccbunshin/releases/latest/download/' "$CCBUNSHIN_CURL_LOG" \
  || fail "CCBUNSHIN_REPO override not honored"

# 4. CCBUNSHIN_INSTALL_DIR places the binary there.
[ -x "$PREFIX/ccbunshin" ] || fail "binary not installed into CCBUNSHIN_INSTALL_DIR"

# 5. install.sh contains no hard-coded release version.
if grep -q 'CCBUNSHIN_VERSION:-v' "$ROOT/install.sh"; then
  fail "install.sh hard-codes a default release version"
fi

echo "install tests passed"
