---
name: verify-ccbunshin
description: Drive the real ccbunshin binary to prove a change works - profiles, the project-aware claude wrapper, shell completion, and the model-routed proxy against a fake OpenAI upstream. Use when a change touches cmd/ccbunshin and you need end-to-end proof beyond go test.
---

# Verify ccbunshin

ccbunshin is a single Go CLI plus an optional HTTP proxy. Every user-facing
behavior is reached through the binary, so verification always means: build
`cmd/ccbunshin`, then run the real binary inside a disposable `HOME` and drive
it the way a user would.

Two kinds of proof exist in this repo and both are in scope: the shell test
scripts in `tests/` (fast, deterministic, part of the normal check) and driving
the binary by hand (what proves a behavior the scripts do not cover). Prefer
extending `tests/` when you are adding coverage for a new behavior; use this
skill's recipes to prove a behavior end to end while developing.

## Launch

Build the binary into a disposable sandbox and put only it on `PATH`, so a
system-wide `ccbunshin` (likely an older release) can never answer instead:

```sh
. .claude/skills/verify-ccbunshin/helpers/sandbox.sh
mkdir -p "$VERIFY_BIN" "$VERIFY_HOME" "$VERIFY_PROFILES"
go -C "$VERIFY_REPO/cmd/ccbunshin" build \
  -ldflags "-X main.buildTime=$(date +%Y%m%d-%H%M)" -o "$VERIFY_BIN/ccbunshin" .
export HOME="$VERIFY_HOME" CCBUNSHIN_PROFILES_DIR="$VERIFY_PROFILES"
export PATH="$VERIFY_BIN:/usr/bin:/bin:/usr/sbin:/sbin"
ccbunshin version
# c5bce13-20261021-1338   <- <short commit>-<build time>, never a release tag
verify_preflight                       # ports free and HOME is the sandbox
```

The sandbox `HOME` is not optional. The proxy writes `proxy.pid` and
`proxy.log` to `$HOME/.config/ccbunshin` no matter what `CCBUNSHIN_PROXY_CONFIG`
says, so a run that inherits the real `HOME` fights the user's own proxy for
the pid file and the port.

Ready means `ccbunshin version` prints a local identity. There is no server to
wait for unless a recipe starts the proxy; that recipe waits on `/healthz`.

Teardown is `verify_down` (stops the proxy through its own pid file and the
fake upstream through the pid it saved), then remove `$VERIFY_DIR`.

## Doctor

One read-only check, run first whenever a result looks wrong:

```sh
verify_preflight                       # ports free, HOME inside the sandbox
ccbunshin version                      # right build? commit and time, not a tag
ccbunshin proxy status                 # stopped | running (pid ...)
```

`verify_preflight` fails rather than letting a run start on top of a listener it
did not create. If it reports a busy port, choose another one through
`VERIFY_PROXY_PORT` / `VERIFY_UPSTREAM_PORT`; do not kill an unknown process.

## Drive

All recipes start from a fresh `$VERIFY_DIR` unless they say otherwise. Treat
every command as literal.

- **Profile lifecycle.** `ccbunshin create provider1` writes
  `$CCBUNSHIN_PROFILES_DIR/provider1.json` (mode 600, dir 700);
  `ccbunshin create provider2 --from examples/provider2.json` copies an example;
  `ccbunshin list` prints `name<TAB>model=...` per profile; `ccbunshin status
  provider1` and `ccbunshin doctor provider1` print the resolved path and exit
  0. A second `create` of the same name fails without `--force`.
- **Project selection.** `ccbunshin local provider1` writes
  `.ccbunshin-profile` in the *current directory* and prints the profile and the
  marker path; `ccbunshin local` reports it, `ccbunshin local --unset` removes
  it. `ccbunshin global provider2` writes `$CCBUNSHIN_PROFILES_DIR/global`;
  `ccbunshin global` prints `profile: provider2` and the source file. Do this
  from a scratch directory, never from the repo root - it creates a file in the
  working tree.
- **Launch and argv forwarding.** Launching for real starts Claude Code, so
  drive it with a recording stub instead, exactly as `tests/shell-integration.sh`
  does: put an executable `claude` earlier on `PATH` that appends `$@` to a log
  and exits `$CLAUDE_EXIT`, then `ccbunshin launch provider1 -p "hello world"`
  must produce `--settings <profile> -p hello world` in that log, and
  `CLAUDE_EXIT=7 ccbunshin launch provider1; echo $?` must print 7.
- **Shell wrapper.** `eval "$(ccbunshin init zsh)"` (or bash, tcsh) then calling
  `claude` inside a directory whose `.ccbunshin-profile` names a profile must
  reach the stub with that profile's `--settings`; outside any project with no
  global profile it must call the original `claude` with the arguments
  unchanged. `tests/shell-integration.sh` drives all three shells; run it rather
  than re-deriving it.
- **Completion.** `sh tests/completion-test.sh` builds the binary and drives
  bash `compgen` and both zsh activation paths. To eyeball it,
  `ccbunshin completion bash > /tmp/c.bash`, then in bash
  `source /tmp/c.bash; COMP_WORDS=(ccbunshin ""); COMP_CWORD=1; _ccbunshin;
  printf '%s\n' "${COMPREPLY[@]}"`.
- **Proxy end to end.** Start the fake OpenAI-dialect upstream, then the proxy:

  ```sh
  . .claude/skills/verify-ccbunshin/helpers/sandbox.sh
  # proxy.json: port 18456, provider upstream http://127.0.0.1:18457,
  # dialect openai-chat, default_model oss-model, no_auth true;
  # route {pattern: "claude-*", provider: "upstream"}
  export CCBUNSHIN_PROXY_CONFIG="$VERIFY_DIR/proxy.json" CCBUNSHIN_LOG=debug
  verify_preflight
  verify_upstream_start
  verify_proxy_start                    # proxy started (pid ..., log ...)
  curl -s -o /dev/null -w 'healthz=%{http_code}\n' "http://127.0.0.1:$VERIFY_PROXY_PORT/healthz"
  curl -s "http://127.0.0.1:$VERIFY_PROXY_PORT/ccbunshin/resolve?model=claude-opus-5"
  # {"dialect":"openai-chat","requested":"claude-opus-5","target":"oss-model"}
  curl -s "http://127.0.0.1:$VERIFY_PROXY_PORT/v1/messages" \
    -H 'content-type: application/json' \
    -d '{"model":"claude-opus-5","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}'
  # {"content":[{"text":"pong","type":"text"}],...,"usage":{"cache_read_input_tokens":2,"input_tokens":5,...}}
  curl -s -N "http://127.0.0.1:$VERIFY_PROXY_PORT/v1/messages" \
    -H 'content-type: application/json' \
    -d '{"model":"claude-opus-5","max_tokens":16,"stream":true,"messages":[{"role":"user","content":"ping"}]}'
  # event: message_start ... message_stop, with usage in message_delta
  cat "$VERIFY_DIR/upstream.log"        # one JSON line per upstream request
  ```

  `upstream.log` is the proof that translation happened: the request the
  upstream saw must carry `"model": "oss-model"` (the rewritten ID) and
  `/chat/completions` (the translated path), while the client spoke Anthropic's
  `/v1/messages` with `claude-opus-5`.

## Evidence

Capture the command, its stdout, stderr, and exit code, plus the state that
proves it: the profile file's mode, the `.ccbunshin-profile` contents, the
stub's argv log line, `upstream.log`, `proxy.log`. Write artifacts under
`/tmp/ccbunshin-verify/artifacts/` and leave them there; teardown removes
processes and scratch state, never the evidence.

Proof standards for this repo:

- A routing claim is proved by `upstream.log` plus `/ccbunshin/resolve`, never
  by the client response alone - the response looks the same whichever model
  answered.
- A proxy behavior is proved with `CCBUNSHIN_LOG=debug` when the claim is about
  what was logged; check the level before claiming a log line exists.
- `go test` and the `tests/*.sh` scripts are a different kind of evidence: they
  pin unit behavior. A change to CLI surface, proxy routing, or shell output
  needs an end-to-end run as well.

## Cleanup

```sh
verify_down
rm -rf "$VERIFY_DIR"        # skip if you need the artifacts
```

Never kill by process name (`pkill ccbunshin`, `pkill python3`): the user may
have their own proxy and other Python processes running. Stop the proxy with
`ccbunshin proxy stop` (its own pid file) and the fake upstream by the pid
`sandbox.sh` saved.

## Gotchas

- `CCBUNSHIN_PROXY_CONFIG` moves the config only. The pid and log always live in
  `$HOME/.config/ccbunshin`, so without a sandbox `HOME` a verification proxy
  writes the user's real pid file and `proxy start` refuses to start beside the
  running one.
- Ports 3456/3457 are the README examples and are often already taken on a
  developer machine. The sandbox defaults to 18456/18457 for that reason.
- `ccbunshin local <name>` writes `.ccbunshin-profile` into the current
  directory. Run it from `$VERIFY_DIR`, or `git status` will show a stray
  marker in the repo.
- `proxy start` reads `CCBUNSHIN_LOG` from the shell that starts it and passes
  it to the daemon; changing it afterwards needs `ccbunshin proxy restart`.
- The proxy daemon binds `:port` on all interfaces, so an IPv6/IPv4 listener
  conflict shows up as `bind: address already in use` in `proxy.log` even when
  `curl` to `localhost` fails for a different reason. Read the log, not just the
  exit status.
- `go test` binaries carry no VCS stamping; a test that asserts on
  `ccbunshin version` must not expect a commit.
- `tests/shell-integration.sh` needs `bash`, `zsh`, and `tcsh` on `PATH`. On
  macOS all three ship with the OS; a Linux CI image may need them installed.
- The proxy's `anthropic` dialect forwards byte-for-byte; a fake upstream for
  that case must serve `/v1/messages`, not `/chat/completions`.
