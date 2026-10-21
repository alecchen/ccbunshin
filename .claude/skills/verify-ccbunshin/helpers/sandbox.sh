#!/bin/sh
# Disposable sandbox for a ccbunshin verification run.
#
# A run needs its own HOME because the proxy's pid and log live under
# $HOME/.config/ccbunshin and are NOT redirected by CCBUNSHIN_PROXY_CONFIG.
# Without the sandbox HOME a verification proxy would collide with the real one.
#
# Usage (source it, it exports into the calling shell):
#   . .claude/skills/verify-ccbunshin/helpers/sandbox.sh
#
# Provides: $VERIFY_DIR, $VERIFY_BIN, $VERIFY_HOME, $VERIFY_PROFILES,
# ccbunshin (shell function), verify_start_proxy, verify_stop_proxy,
# verify_upstream_start, verify_upstream_stop, verify_down.
set -eu

VERIFY_REPO=${VERIFY_REPO:-$(git rev-parse --show-toplevel 2>/dev/null || true)}
if [ -z "$VERIFY_REPO" ]; then
  echo "sandbox.sh: set VERIFY_REPO to the ccbunshin checkout before sourcing" >&2
  return 1
fi
VERIFY_SKILL_DIR=$VERIFY_REPO/.claude/skills/verify-ccbunshin
VERIFY_DIR=${VERIFY_DIR:-/tmp/ccbunshin-verify}
VERIFY_BIN=$VERIFY_DIR/bin
VERIFY_HOME=$VERIFY_DIR/home
VERIFY_PROFILES=$VERIFY_DIR/profiles
VERIFY_PROXY_PORT=${VERIFY_PROXY_PORT:-18456}
VERIFY_UPSTREAM_PORT=${VERIFY_UPSTREAM_PORT:-18457}
export VERIFY_DIR VERIFY_BIN VERIFY_HOME VERIFY_PROFILES
export VERIFY_PROXY_PORT VERIFY_UPSTREAM_PORT

ccbunshin() {
  "$VERIFY_BIN/ccbunshin" "$@"
}

# Refuse to start on top of a listener this run did not create. A port already
# held by the user's real proxy makes `proxy start` fail with a bind error that
# looks like a config problem, so catch it here instead.
verify_preflight() {
  for port in "$VERIFY_PROXY_PORT" "$VERIFY_UPSTREAM_PORT"; do
    if lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
      echo "port $port is already in use by another process:" >&2
      lsof -nP -iTCP:"$port" -sTCP:LISTEN >&2
      echo "pick free ports: VERIFY_PROXY_PORT=... VERIFY_UPSTREAM_PORT=... . $0" >&2
      return 1
    fi
  done
  case "$HOME" in
    "$VERIFY_DIR"*) ;;
    *)
      echo "HOME is $HOME, not the sandbox $VERIFY_HOME; the proxy would write the real pid file" >&2
      return 1
      ;;
  esac
}

verify_upstream_start() {
  python3 -I "$VERIFY_SKILL_DIR/helpers/fake_upstream.py" \
    "$VERIFY_UPSTREAM_PORT" "$VERIFY_DIR/upstream.log" \
    >"$VERIFY_DIR/upstream.out" 2>&1 &
  echo $! >"$VERIFY_DIR/upstream.pid"
  i=0
  while [ $i -lt 50 ]; do
    if curl -sf -o /dev/null -X POST "http://127.0.0.1:$VERIFY_UPSTREAM_PORT/chat/completions" \
      -H 'content-type: application/json' -d '{"model":"probe"}'; then
      return 0
    fi
    i=$((i + 1))
    sleep 0.1
  done
  echo "fake upstream did not start, see $VERIFY_DIR/upstream.out" >&2
  return 1
}

verify_proxy_start() {
  ccbunshin proxy start
  ccbunshin proxy status
}

verify_stop_proxy() {
  ccbunshin proxy stop || true
}

verify_upstream_stop() {
  if [ -f "$VERIFY_DIR/upstream.pid" ]; then
    pid=$(cat "$VERIFY_DIR/upstream.pid")
    kill "$pid" 2>/dev/null || true
    wait "$pid" 2>/dev/null || true   # reap it, so no "Terminated" line is printed
    rm -f "$VERIFY_DIR/upstream.pid"
  fi
}

# Kill only what this run started: the proxy by its own pid file, the fake
# upstream by the pid saved at start. Never kill by process name.
verify_down() {
  verify_stop_proxy
  verify_upstream_stop
}
