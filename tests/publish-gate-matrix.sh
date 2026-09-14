#!/bin/sh
# Regression matrix for the publish-gate PreToolUse hook classifier.
#
# Feeds synthetic payloads to the hook and asserts the decision that comes back.
# No git or gh command is ever executed, so this publishes nothing and triggers
# no prompt.
#
# Run this after ANY edit to ~/.claude/hooks/publish-gate.sh. A hook that matches
# nothing looks exactly like a hook that is not loaded, so the matrix is the only
# cheap way to tell those two apart.
#
# Usage: sh tests/publish-gate-matrix.sh [path-to-hook]
set -eu

hook=${1:-$HOME/.claude/hooks/publish-gate.sh}
# A bare name like ".tmp-gate.sh" is not on PATH, so exec needs a slash.
case "$hook" in
  */*) : ;;
  *) hook="./$hook" ;;
esac
# Absolute, because the jq-less case below replaces PATH and still has to exec it.
hook_dir=$(cd "$(dirname "$hook")" && pwd)
hook=$hook_dir/$(basename "$hook")
[ -x "$hook" ] || { echo "not executable: $hook" >&2; exit 2; }
command -v jq >/dev/null 2>&1 || { echo "jq is required" >&2; exit 2; }

pass=0
fail=0

# ask   - the command publishes; the hook must print a decision
# allow - no opinion; the hook must print nothing
check() {
  want=$1
  cmd=$2
  payload=$(jq -nc --arg c "$cmd" '{tool_input:{command:$c}}')
  out=$(printf '%s' "$payload" | "$hook" || true)
  if [ -n "$out" ]; then got=ask; else got=allow; fi
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL [want %s, got %s] %s\n' "$want" "$got" "$cmd"
  fi
}

# The decision is read out of the JSON rather than inferred from "did it print
# anything", so ask and deny are distinguishable. Malformed output is a failure,
# not a crash: the hook returning unparseable JSON means Claude Code ignores it,
# which is the silent-allow failure this whole file exists to catch.
# want=allow means "no output at all".
check_payload() {
  want=$1
  payload=$2
  label=$3
  out=$(printf '%s' "$payload" | "$hook" || true)
  if [ -z "$out" ]; then
    got=allow
  else
    got=$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecision // "MALFORMED"' 2>/dev/null || echo MALFORMED)
  fi
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL [want %s, got %s] %s\n' "$want" "$got" "$label"
  fi
}

# permission_mode is a top-level field of the PreToolUse payload. The live value
# observed on this machine is "auto", which is not one of the five the docs list,
# so the vocabulary is open: the hook must treat exactly two values as
# no-human-present and everything else - known, unknown, or absent - as a prompt.
check_mode() {
  want=$1
  mode=$2
  cmd=$3
  payload=$(jq -nc --arg c "$cmd" --arg m "$mode" '{permission_mode:$m,tool_input:{command:$c}}')
  check_payload "$want" "$payload" "mode=$mode $cmd"
}

# The denials have to name the mode, or the reason Claude gets back reads as an
# unexplained block.
check_deny_names_mode() {
  mode=$1
  cmd=$2
  payload=$(jq -nc --arg c "$cmd" --arg m "$mode" '{permission_mode:$m,tool_input:{command:$c}}')
  out=$(printf '%s' "$payload" | "$hook" || true)
  reason=$(printf '%s' "$out" | jq -r '.hookSpecificOutput.permissionDecisionReason // ""' 2>/dev/null || echo "")
  case "$reason" in
    *permission_mode=\'$mode\'*) pass=$((pass + 1)) ;;
    *) fail=$((fail + 1)); printf 'FAIL [deny reason omits mode=%s] %s\n' "$mode" "$reason" ;;
  esac
}

# The MCP shell tool's payload is flat: tool_input is {"command": "..."}, the
# same shape Bash uses. Verified against a real transcript, so the hook's
# existing jq path needs no change to cover it. Kept as a separate case set so a
# future lean-ctx release that changes the shape fails loudly here.
check_mcp() {
  want=$1
  cmd=$2
  payload=$(jq -nc --arg c "$cmd" '{tool_name:"mcp__lean-ctx__ctx_shell",tool_input:{command:$c}}')
  out=$(printf '%s' "$payload" | "$hook" || true)
  if [ -n "$out" ]; then got=ask; else got=allow; fi
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL [want %s, got %s] %s (mcp payload)\n' "$want" "$got" "$cmd"
  fi
}

echo "hook: $hook"

# --- publishes: plain spellings -------------------------------------------
check ask 'git push'
check ask 'git push origin main'
check ask 'git tag v1.0.0'
check ask 'git tag -a v1.0.0 -m release'
check ask 'gh release create v1.0.0'
check ask 'gh repo delete owner/repo'
check ask 'gh pr merge 42'

# --- publishes: git global flags ------------------------------------------
check ask 'git --no-pager push'
check ask 'git -C /repo push'
check ask 'git -c k=v push'
check ask 'git --git-dir=/x push'

# --- publishes: wrappers --------------------------------------------------
check ask 'env FOO=1 git push'
check ask 'sudo git push'
check ask 'nohup git push'
check ask 'time git push'
check ask 'timeout 5 git push'
check ask 'nice git push'
check ask 'command git push'
check ask 'env FOO=1 git tag v1'

# --- publishes: compound commands ----------------------------------------
check ask 'echo hi && git push'
check ask 'true; git push'
check ask 'git status || git push'
check ask 'git push &'
check ask 'bash -c "git push"'
check ask '(git push)'
check ask 'if true; then git push; fi'
check ask '! git push'
check ask 'for r in a b; do git push $r; done'
check ask 'find . -exec git push \;'
check ask 'xargs -n1 git push'

# --- publishes: gh api writes --------------------------------------------
check ask 'gh api -X POST repos/o/r/releases'
check ask 'gh api -f tag_name=v1 repos/o/r/releases'

# --- publishes: through an MCP shell (flat command payload) --------------
check_mcp ask 'git push'
check_mcp ask 'git push origin main'
check_mcp ask 'sudo git push'
check_mcp ask 'echo hi && git tag v1'
check_mcp ask 'gh release create v1.0.0'
check_mcp allow 'git status'

# --- no-verify: only the remote-publishing forms are gated ---------------
# --no-verify on a local command (commit, rebase, merge) is normal use and
# must NOT prompt: it touches nothing remote, and gating it makes auto mode
# useless. The flag only matters for `git push`, where it is the escape hatch
# out of a pre-push hook. The hook's publishing clause already catches every
# push spelling, so these assert that the hook stays out of the way of local
# commands while still covering push.
check ask 'git push --no-verify'
check ask 'git push --no-verify origin main'
check ask 'git -C /repo push --no-verify'
check ask 'git --no-pager push --no-verify'
check ask 'echo hi && git push --no-verify'
check ask 'git push --no-verify --force'
check_mcp ask 'git push --no-verify'
# Local use of the flag: silent, so auto mode keeps working.
check allow 'git commit --no-verify'
check allow 'git commit --no-verify -m x'
check allow 'git commit -m x --no-verify'
check allow 'git rebase --no-verify'
check allow 'git merge --no-verify'
check allow 'git am --no-verify'
check allow 'git cherry-pick --no-verify'
check allow 'git commit -m x'
check allow 'git log --oneline'
check allow 'curl --no-verify'
check allow 'echo --no-verify'
# gh does not take --no-verify; the flag must not make it look like a publish.
check allow 'gh release list --no-verify'

# --- no opinion: read-only forms -----------------------------------------
check allow 'git push --dry-run'
check allow 'git push -n'
check allow 'git tag -l'
check allow 'git tag --list'
check allow 'git tag -v v1'
check allow 'git status'
check allow 'gh release list'
check allow 'gh release view v1'
check allow 'gh api repos/o/r/releases'
check allow 'gh pr view 42'
check allow 'gh pr checks'
check allow 'gh repo view o/r'
check allow 'gh api my-F-org'

# --- no opinion: near misses ---------------------------------------------
check allow 'echo "git push"'
check allow 'igit push'
check allow 'git pushover'

# --- permission_mode: ask escalates to deny where nobody can answer -------
# An "ask" in bypassPermissions or dontAsk is a silent no-op: the first skips the
# prompt, the second answers it without asking. Both would let the publish
# through with no approval, so the hook must block instead. The classifier is
# untouched by the mode - only the decision changes - which is what the plain
# push / --no-verify pair below and the negative controls after it assert.
check_mode ask   default           'git push'
check_mode ask   plan              'git push'
check_mode ask   acceptEdits       'git push'
check_mode deny  bypassPermissions 'git push'
check_mode deny  dontAsk           'git push'
check_mode ask   default           'git push --no-verify'
check_mode ask   plan              'git push --no-verify'
check_mode ask   acceptEdits       'git push --no-verify'
check_mode deny  bypassPermissions 'git push --no-verify'
check_mode deny  dontAsk           'git push --no-verify'
check_deny_names_mode bypassPermissions 'git push'
check_deny_names_mode dontAsk 'git push --no-verify'
# "auto" is the value this machine's PreToolUse payload actually carries, and it
# is not one of the five names the docs list. It must keep prompting.
check_mode ask   auto              'git push'
check_mode ask   auto              'git push --no-verify'
# Mode is not a classifier input: forms the classifier ignores stay ignored no
# matter what the mode says, or a bypassPermissions session would prompt on
# every read-only command.
check_mode allow bypassPermissions 'git push --dry-run'
check_mode allow bypassPermissions 'git commit --no-verify'
check_mode allow bypassPermissions 'git status'
check_mode allow dontAsk           'git status'

# --- the jq-less path must escalate too -----------------------------------
# The mode read has a sed fallback alongside the jq one, the same way the
# command read does. Without it, a machine lacking jq would silently fall back
# to ask in the two modes this whole block exists to cover. PATH is replaced
# with a directory of symlinks to the three tools the hook needs, so `command -v
# jq` fails while sed/awk/cat still work. The payload is built before the
# subshell, since jq is not reachable inside it.
no_jq_dir=$(mktemp -d)
trap 'rm -rf "$no_jq_dir"' EXIT
# Enumerated by hand from the hook's own tool use. If the hook grows a new
# dependency this list goes stale and these cases fail loudly, which is the
# intended direction - a silent pass here would be the gate quietly not running.
for tool in sed awk cat tail; do
  tool_path=$(command -v "$tool")
  ln -s "$tool_path" "$no_jq_dir/$tool"
done

check_no_jq() {
  want=$1
  mode=$2
  cmd=$3
  payload=$(jq -nc --arg c "$cmd" --arg m "$mode" '{permission_mode:$m,tool_input:{command:$c}}')
  # Exec the hook itself: its #!/bin/sh shebang is an absolute path and so
  # survives the emptied PATH, while `sh` would not be findable.
  out=$(printf '%s' "$payload" | env -i PATH="$no_jq_dir" HOME="$HOME" "$hook" || true)
  if [ -z "$out" ]; then
    got=allow
  else
    # jq is unreachable here too, so read the decision with a pattern.
    got=$(printf '%s' "$out" | sed -n 's/.*"permissionDecision"[[:space:]]*:[[:space:]]*"\([^"]*\)".*/\1/p')
    [ -n "$got" ] || got=MALFORMED
  fi
  if [ "$got" = "$want" ]; then
    pass=$((pass + 1))
  else
    fail=$((fail + 1))
    printf 'FAIL [want %s, got %s] no-jq mode=%s %s\n' "$want" "$got" "$mode" "$cmd"
  fi
}
check_no_jq deny bypassPermissions 'git push'
check_no_jq ask  default           'git push'
check_no_jq allow bypassPermissions 'git push --dry-run'

echo "pass=$pass fail=$fail"
[ "$fail" -eq 0 ]
