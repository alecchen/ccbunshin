# Model-routed proxy

The proxy is one local endpoint that routes each request to a provider by model
ID, translating the wire format when the provider speaks OpenAI's
`/chat/completions` instead of Anthropic's `/v1/messages`.

## Sub-features

- `proxy-init` writes a template config at mode 600 and refuses to overwrite
  without `--force`.
- `proxy-lifecycle` starts a detached daemon, reports status, restarts, and stops
  only after the process is gone.
- `proxy-start-refuse` fails on a bad config or a port that cannot be bound,
  naming the log, instead of reporting success.
- `proxy-healthz` answers 200 without contacting an upstream.
- `proxy-route` sends each model to the first matching route and returns 400
  when none match.
- `proxy-resolve` reports what a model ID becomes, for one model or the whole table.
- `proxy-translate` converts an Anthropic request to `/chat/completions` and the
  response back, buffered and streaming.
- `proxy-usage` reports upstream cache hits as `cache_read_input_tokens` and
  subtracts them out of `input_tokens`.
- `proxy-log` writes one INFO line per request and demotes a client abort to WARN.

## How to get to it (user POV)

- Run `ccbunshin proxy init`, `start`, `status`, `restart`, `stop`.
- Point a client at `http://127.0.0.1:<port>` (`ANTHROPIC_BASE_URL` in a profile).
- Run `curl` against `/healthz`, `/ccbunshin/resolve`, and `/v1/messages`.

## Driving it with the sandbox

Preconditions:

- The sandbox is sourced and `verify_upstream_start` has succeeded.
- `$VERIFY_DIR/proxy.json` uses port `$VERIFY_PROXY_PORT`, one provider
  `upstream` at `http://127.0.0.1:$VERIFY_UPSTREAM_PORT` with
  `"dialect": "openai-chat"`, `"default_model": "oss-model"`, `"no_auth": true`,
  and one route `{"pattern": "claude-*", "provider": "upstream"}`.
- `CCBUNSHIN_PROXY_CONFIG` points at that file and `CCBUNSHIN_LOG=debug`.

- **Init.** In a fresh sandbox, run `ccbunshin proxy init`. Exit code 0,
  `$CCBUNSHIN_PROXY_CONFIG` exists with mode 600, and it is valid JSON. Running
  it again fails; `ccbunshin proxy init --force` succeeds.
- **Start and status.** Run `verify_proxy_start`. `proxy started (pid ..., log ...)`
  then `proxy running (pid ..., up ..., log ...)`, and the log path is under
  `$VERIFY_HOME/.config/ccbunshin/`, not the real home.
- **Bind failure.** With the proxy already running, start a second one against
  the same port from another config. Exit code non-zero, the message names the
  log, and the log contains `address already in use`.
- **Refuse a bad dialect.** Set `"dialect": "openai"` on the provider and run
  `ccbunshin proxy start`. Exit code non-zero, the message names the key, and no
  daemon starts.
- **healthz.** `curl -s -o /dev/null -w '%{http_code}' http://127.0.0.1:$VERIFY_PROXY_PORT/healthz`
  prints `200`.
- **resolve.** `curl -s 'http://127.0.0.1:$VERIFY_PROXY_PORT/ccbunshin/resolve?model=claude-opus-5'`
  prints exactly `{"dialect":"openai-chat","requested":"claude-opus-5","target":"oss-model"}`.
  Without `?model=` the whole route table comes back with `pattern`, `provider`,
  `dialect`, and `upstream` per entry.
- **No route.** `curl -s -o /dev/null -w '%{http_code}' -X POST
  http://127.0.0.1:$VERIFY_PROXY_PORT/v1/messages -H 'content-type: application/json'
  -d '{"model":"nope","messages":[]}'` prints `400`.
- **Translate, buffered.** POST a `claude-opus-5` message with
  `"max_tokens": 16` and one user turn `ping`. The response is Anthropic-shaped
  (`"type":"message"`, a `text` block reading `pong`), and the last line of
  `$VERIFY_DIR/upstream.log` shows `"path": "/chat/completions"` with
  `"model": "oss-model"` and an OpenAI `messages` array.
- **Translate, streaming.** Add `"stream": true`. The response is an Anthropic
  SSE stream: `message_start`, `content_block_start`, `content_block_delta`
  with `pong`, `message_delta` carrying usage, `message_stop`.
- **Usage split.** In both shapes, `usage` reports `cache_read_input_tokens: 2`
  and `input_tokens: 5` (7 upstream prompt tokens with 2 cache hits), so the two
  sum to the upstream's `prompt_tokens`.
- **Log.** `grep 'INFO proxy:' "$VERIFY_HOME/.config/ccbunshin/proxy.log"` shows
  one line per request naming the requested model, the target model, provider,
  dialect, and status. At `debug` the routing decision and upstream URL appear too.
- **Restart and stop.** `ccbunshin proxy restart` prints a stop line with the old
  pid and a start line with a new one. `ccbunshin proxy stop` reports the pid and
  uptime, and `lsof -nP -iTCP:$VERIFY_PROXY_PORT -sTCP:LISTEN` finds nothing.
- **Proof.** Capture the client responses, `upstream.log`, `proxy.log`, and the
  status output under `$VERIFY_DIR/artifacts/proxy/`.

## Gotchas

- The proxy binds `:port` on all interfaces. A port already held by the user's
  real proxy fails with `bind: address already in use`, which is a different
  problem from a bad config; read `proxy.log` before changing anything.
- `CCBUNSHIN_PROXY_CONFIG` moves the config only; pid and log stay in
  `$HOME/.config/ccbunshin`. Without the sandbox `HOME`, `proxy start` refuses
  to start beside the user's running daemon.
- The `openai-chat` translation appends `/chat/completions` to the provider's
  upstream path. A fake upstream that only knows `/v1/chat/completions` must be
  reached through an upstream URL ending in `/v1`.
- `cache_creation_input_tokens` is always `0` for `openai-chat`, because the
  upstream has no equivalent. A first turn with a cold prefix legitimately reads
  as `0%`.
- A misspelled config key (`"dialcts"`) is dropped without a warning; only a bad
  value for a recognized key fails startup. Check key spelling first when a route
  behaves as if untranslated.
- `reasoning_effort` is never derived from `thinking`. A caller that states an
  effort gets it forwarded; a test that expects `reasoning_effort` without one is
  testing behavior the design forbids.
- The fake upstream's usage is fixed at 7/2 tokens, so `input_tokens` in a
  stream starts as a local estimate and changes at `message_delta`. Assert on the
  final `message_delta`, not `message_start`.
