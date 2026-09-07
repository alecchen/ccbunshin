#!/usr/bin/env bash
set -euo pipefail

ROOT=$(cd "$(dirname "$0")/.." && pwd)
CLI="$ROOT/ccbunshin"
TMP=$(mktemp -d)
trap 'rm -rf "$TMP"' EXIT

run() { HOME="$TMP" CCBUNSHIN_PROFILES_DIR="$TMP/profiles" "$CLI" "$@"; }

run create provider1 --from "$ROOT/examples/provider1.json" >/dev/null
test -f "$TMP/profiles/provider1.json"
test "$(stat -f '%Lp' "$TMP/profiles/provider1.json" 2>/dev/null || stat -c '%a' "$TMP/profiles/provider1.json")" = 600
run model provider1 test-model >/dev/null
run list | grep -F 'provider1	model=test-model' >/dev/null
run doctor provider1 >/dev/null
run delete provider1 >/dev/null
test ! -e "$TMP/profiles/provider1.json"

printf 'all ccbunshin tests passed\n'
