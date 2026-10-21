# The model-routed proxy

ccbunshin can run one local endpoint and route each request to a different upstream by
model ID. The whole program is one binary, so this is the same `ccbunshin` you already
installed; nothing extra to build or run.

The proxy is optional. Each profile can already set its own `ANTHROPIC_BASE_URL`, so point
profiles directly at different endpoints when you can. Use the proxy when you need a single
endpoint: for example, a lean-ctx instance has one Anthropic upstream, and pointing all
profiles at the ccbunshin proxy lets it route per model.

This is the reference for `proxy.json`, the lifecycle commands, logging, the three dialects,
credentials, and the token gate. For install, profiles, and the `claude` wrapper, see the
[project README](../README.md).

```text
ccbunshin :3456
├── claude-*              -> provider1
└── qwen/deepseek/gpt-oss -> provider2
```

Generate a starting config with:

```sh
ccbunshin proxy init
```

Routes select the provider; `models` rewrites the model ID after routing.

A pattern with no wildcard matches one exact model (`"qwen-3.8-27b"`). `*` does not cross `/`: it matches any run of characters except a slash. A prefixed ID such as `xiaomi/mimo-v2.6-flash` needs `xiaomi/*`, every prefixed ID needs `*/*`, and a bare `*` only ever matches IDs without a slash. Routes are checked in order; the first match wins.

The upstream ID resolves in this order: the route's `models`, the provider's `models`, the provider's `default_model` (a translating dialect only), then the requested ID unchanged. That means an exact-match route still inherits `default_model` unless the same ID appears in a `models` map, so a route can quietly be answered by a model its pattern never names. To pin an ID without rewriting it, give it an identity entry: `{"mimo-v2.6-flash": "mimo-v2.6-flash"}`. `resolve` reports what any ID becomes.

`proxy.json` keys:

- `port` (required, 1-65535): the port the proxy listens on.
- `bind` (optional, default `127.0.0.1`): the address the proxy listens on. Loopback is the default because a proxy that forwards a stored credential should not be reachable from the network unless someone says so. Set `"0.0.0.0"` (or `"::"`, or `""`) to listen on every interface, which is what a config written before this key existed got.
- `client_token_env` (optional): the name of an environment variable holding a token every inbound request must present, in `Authorization: Bearer` or `x-api-key`. See [Inbound authentication](#inbound-authentication).
- `providers` (required, at least one): each needs an `upstream` absolute URL. Optional keys: `timeout` (Go duration, default `5m`), `models` (requested ID to upstream ID), `default_model` (rewrite target for a routed model with no `models` entry, translating dialects only), `effort` (default `reasoning_effort`, see [Effort](#effort)), `auth_token_env`, `auth_headers`, and `no_auth` (the provider's own credential, see [Provider credentials](#provider-credentials)), and `dialect`. A bad `dialect` is rejected at startup, so `proxy start` names the key instead of forwarding requests the wrong way.
- `routes` (required): ordered list of `{pattern, provider}`. `pattern` matches the request model: no wildcard means one exact model, `*` matches any run of characters (for example `"claude-*"`). `provider` must name a provider above. Duplicate exact patterns are rejected. A route may also carry `models`, `dialect`, `model_dialects` (a `{target model: dialect}` map, keyed on the ID sent upstream), and `effort`, each overriding the provider's value.

The proxy listens on `bind:port`, reads the request model, applies the ordered glob routes (first match wins), resolves the target model and dialect, and forwards the request. It returns HTTP 400 when no route matches. `/healthz` returns HTTP 200 without contacting an upstream, and without a token even when `client_token_env` is set.

`dialect` says which wire format a route's upstream speaks, and there are three:

- `"anthropic"` - the default, and what you get with no `dialect` key at all. The request goes upstream byte-for-byte, so nothing is translated and nothing can be lost in translation. Use it for anything that already accepts `/v1/messages`, including a real Anthropic endpoint, a gateway in front of one, or another Anthropic-shaped proxy.
- `"openai-chat"` - for a gateway that speaks OpenAI's `/chat/completions` instead. The proxy then converts `system`, content blocks, `tools`, and `tool_choice` to their chat-completions equivalents; returns upstream `reasoning` as Anthropic `thinking` blocks; answers `/v1/messages/count_tokens` locally with an estimate; reports upstream prompt-cache hits as `cache_read_input_tokens` with the hit count subtracted out of `input_tokens`; and rewraps upstream errors in Anthropic's error envelope. `reasoning_effort` is never derived from `thinking` - that translation is what several such gateways reject - but a caller that states an effort of its own gets it forwarded. See [Translating providers](#translating-providers) and [Effort](#effort).
- `"systemone"` - for a System One decision endpoint, where a model answers typed questions with probabilities instead of text. The caller's body is forwarded unchanged to the provider upstream plus `/systemone`; there is nothing to translate, because the request is not a chat message. The caller's path and query are replaced, Anthropic protocol headers are dropped, and the response comes back as it arrived, errors included. Jev does not stream and only answers for `typesafe/*` model IDs, which `proxy start` checks. See [System One decisions](#system-one-decisions).

Any other value is an error, not a silent fallback: `proxy start` refuses the config and names the offending key. Set `dialect` on a provider, on a route (overriding the provider), or per target model in a route's `model_dialects`.

See `../examples/proxy.json` for the full example the `init` template is based on, and `OPENCODE_ZEN.md` and `OPENCODE_GO.md` for which models the two gateways the proxy is tested against need which dialect.

## Proxy lifecycle

```sh
ccbunshin proxy init [--force]
ccbunshin proxy start
ccbunshin proxy status
ccbunshin proxy restart
ccbunshin proxy stop
```

`proxy init` writes a template config (mode `600`) to
`~/.config/ccbunshin/proxy.json`, or to `$CCBUNSHIN_PROXY_CONFIG` when set;
`--force` overwrites an existing file. The process stores its PID and log at:

```text
~/.config/ccbunshin/proxy.pid
~/.config/ccbunshin/proxy.log
```

The PID and log always live there, whatever `CCBUNSHIN_PROXY_CONFIG` points at:
the config directory is not assumed to be writable, and a relative config path
does not make the state paths relative.

`proxy start` launches the daemon detached and returns once the daemon has stayed
up past startup, so a proxy that cannot bind exits non-zero with the log path
instead of reporting success. It refuses to start a second daemon while one is
running, and clears a stale PID file left by a crash or reboot rather than
refusing to start. `status` and `stop` also find a proxy started by a release
that wrote its PID under `~/.cache/ccbunshin`.

Every lifecycle command reports what it did: `start` and `stop` name the PID,
`stop` also reports how long the daemon had been up, and `status` adds the log
path. `stop` waits for the daemon to exit and reports a process that would not
stop instead of removing the PID file and calling it stopped. `restart` is `stop`
followed by `start`, which is what picking up an edited config takes; it starts a
proxy when none was running rather than refusing.

```text
$ ccbunshin proxy start
proxy started (pid 51234, log ~/.config/ccbunshin/proxy.log)
$ ccbunshin proxy status
proxy running (pid 51234, up 2m0s, log ~/.config/ccbunshin/proxy.log)
$ ccbunshin proxy restart
proxy stopped (pid 51234, was up 2m4s)
proxy started (pid 51290, log ~/.config/ccbunshin/proxy.log)
```

## Log severity

`CCBUNSHIN_LOG` sets the threshold for `proxy.log`, lowest to highest:
`debug`, `info` (the default), `warn`, `error`. A line is written when its own
level is at or above the threshold, so `error` leaves only failures. Any other
value is an error: a typo fails `proxy start` instead of silently logging
nothing.

```text
2026/09/12 23:25:40 INFO proxy: claude-opus-5 -> oss-model (buffered) req=a1b2c3d4 provider=provider2 dialect=openai-chat status=200 bytes=271 elapsed=1ms
2026/09/12 23:25:40 WARN proxy: POST /v1/messages from 127.0.0.1:62779 request_bytes=42B failed: no provider route for model nope (configured patterns: claude-*)
2026/09/14 10:45:20 ERROR proxy: claude-opus-5 -> oss-model req=a1b2c3d4 stream failed after 1m0.005s with 1662212 bytes already delivered to the client: context deadline exceeded [provider=commandcode dialect=openai-chat shape=stream request=48731B upstream=https://api.example.invalid/provider/v1]
```

- **info** (default): one line per request with the requested and target model, a short per-request tag (`req=`), provider, dialect, status, response bytes, and elapsed time, plus one line at startup and one at shutdown.
- **warn**: requests the proxy itself rejects (no route, unreadable body), reasoning dropped mid-stream, and a request the calling client aborted. A cancellation is the client's decision rather than a failure of the proxy or the upstream, so it is reported here with a `[client aborted: ...]` note instead of at `error`; the provider's own timeout expiring still counts as an error.
- **error**: upstream failures (unreachable, non-2xx, untranslatable response), a stream cut short after its headers were already sent, and any request whose response was 5xx.
- **debug**: the routing decision and the upstream URL for each request, translated request size, and upstream usage per streamed message, including the cache hit count (`cached=`); this is the level that shows whether prompt caching is hitting. It is also the only level that writes a request or response body, and only when a translated request is rejected.

Four things the lines share. Every `warn` and `error` line ends with a bracketed
suffix naming provider, dialect, `buffered`/`stream` shape, request size, and
upstream URL, with credentials in the URL redacted. A stream failure happens
after the status is committed, so its `bytes already delivered` count is the only
way to tell a stream that died at once from one that died after a megabyte.
Credentials are never logged, and no body is written below `debug`. The two
`debug` body dumps carry the conversation, which is why they are `debug` only.

To check the log end to end against a real request, start the proxy with
`CCBUNSHIN_LOG=debug` and read `~/.config/ccbunshin/proxy.log`.

## Translating providers

A provider that speaks OpenAI's `/chat/completions` rather than Anthropic's `/v1/messages` needs `"dialect": "openai-chat"`. The proxy then translates in both directions:

- Anthropic `system`, content blocks, `tools`, and `tool_choice` become their chat-completions equivalents. `tool_result` becomes a `role: "tool"` message placed before the turn it answers, and `tool_use` arguments become a JSON string.
- Upstream `reasoning` becomes Anthropic `thinking` blocks, streaming and non-streaming. **`reasoning_effort` is never derived from `thinking`**: doing so is what broke requests against the upstream this was built for, so reasoning is recovered from the response instead. `thinking` itself is not forwarded either. An effort the caller states is a different matter and is forwarded - see `effort` under Request translation.
- The caller's `output_config.effort` becomes `reasoning_effort`. Everything else `output_config` carries is still dropped, `format` among it, so a caller asking for a JSON schema does not get one upstream.
- `POST /v1/messages/count_tokens` is answered locally with a character-based estimate, since such upstreams often do not implement it. The estimate is approximate by design.
- Upstream errors are rewrapped in Anthropic's `{"type": "error", ...}` envelope at the same status, keeping the upstream's message, so a client that classifies errors on that shape still works and a wrong mapping is legible.
- Prompt-cache breakpoints (`cache_control`) are dropped, since chat-completions has no equivalent. Prefix caching still happens: such upstreams cache a repeated prefix automatically, without a marker. Its hit count comes back as `prompt_tokens_details.cached_tokens` and is reported as Anthropic's `cache_read_input_tokens`, with the hit count subtracted out of `input_tokens` so the two still sum to the upstream's `prompt_tokens`. `cache_creation_input_tokens` has no upstream counterpart and stays `0`, so a first turn with a cold prefix reads as `0%`. `input_tokens` is the local estimate until the upstream reports usage, which supersedes it in the `message_delta` - so that field can change between the start of a stream and its end.

Dialect resolution is most-specific-first: the route's `model_dialects` entry for the target model, then the route's `dialect`, then the provider's `dialect`, then `anthropic`. Because the dialect is per model, one upstream can serve both kinds at once, as long as its routes live in different namespaces: `claude-*` and, say, `vertex-claude-*` do not collide, since a pattern matches the whole model string.

When configuring it:

- A route with dialect `openai-chat` needs somewhere to get an acceptable model ID, or `proxy start` refuses the config. Set `default_model` on the provider, or `models` on the provider or route.
- A misspelled key name (`"dialcts"`) is dropped by the JSON decoder, and the provider keeps its default without a warning. A bad value for a key that is recognized (`"dialect": "openai"`) fails startup and names the key. When a provider seems untranslated, check the spelling of the key name first.

To find out which endpoint a gateway expects for a given model, POST a one-token request to its `/messages` path and read which endpoint the error names. Model lists often do not say, and an ID's prefix is not a reliable signal.

## Effort

`effort` sets the `reasoning_effort` sent to an `openai-chat` upstream when the request itself carries no effort. Its values are `low`, `medium`, `high`, `xhigh`, and `max`; anything else is rejected at startup.

It is a default, never an override. A value the caller stated always wins, because Claude Code sends an effort on every effort-capable request and re-sends the session's setting on each turn - so a configured value that displaced it would silently turn the `/effort` control into a no-op. What `effort` reaches is the traffic that states nothing, which is the tier that has no effort of its own: a Haiku-class request carries `output_config.format` and no effort at all, and it is what a subagent run on that tier inherits.

Set it on a provider, or on a route to override the provider's. A route pattern matches the whole model string, so give the tier its own route ahead of the catch-all:

```json
"routes": [
  {"pattern": "claude-haiku-4-5*", "provider": "gateway", "effort": "low"},
  {"pattern": "claude-*", "provider": "gateway"}
]
```

A route's `effort` overrides the provider's, and an empty value means no default at all.

An `anthropic`-dialect provider is unaffected: nothing is translated, so the caller's own `output_config` goes upstream untouched and `effort` is not consulted.

## System One decisions

TypeSafe's System One models, Jev among them, answer typed questions with probabilities instead of text. The request is a shared `state` plus a `questions` object, and the answer comes back keyed by the same question names. It is not a chat message, so no dialect can turn a Claude Code turn into it: the caller has to send the System One body itself. The route table, the provider's own credential, and the per-request log line still apply.

```json
{
  "providers": {
    "decisions": {
      "upstream": "https://gateway.example.invalid/v1",
      "auth_token_env": "DECISIONS_API_KEY"
    }
  },
  "routes": [
    { "pattern": "typesafe/*", "provider": "decisions", "dialect": "systemone" }
  ]
}
```

```sh
curl -s http://127.0.0.1:3456/v1/messages -H 'content-type: application/json' -d '{
  "model": "typesafe/jev",
  "state": "My payments have failed for three days and I am losing sales.",
  "questions": {
    "is_urgent": {"type": "noul", "instructions": "Does this need urgent attention?"},
    "department": {"type": "choice", "instructions": "Which team?", "criteria": {"billing": "charges", "shipping": "delivery"}}
  }
}'
```

The caller's own path is replaced by `/systemone`, so the path in the example is a placeholder and any other works the same. The path in `upstream` is kept at whatever depth the gateway publishes it.

`systemone` requests go through the same route table as everything else. The model ID passes through unless a `models` entry maps it, in which case the body's `model` is rewritten. `default_model` does not apply: it gives a translated chat upstream an ID it accepts, and a decision caller already sends a decision model. `effort` does not apply either.

Jev never streams, and the proxy does not add a stream: a `stream` field in the body is the upstream's to interpret. Upstream errors are passed back as they arrived, at the upstream's status.

## Inbound authentication

Without `client_token_env`, the listen address is the only boundary: anything that can reach the port can spend whatever credential a provider holds. That is fine on a single-user machine bound to loopback, and wrong on a host other people can log into. Behind ssh, loopback is no boundary at all, because every user on the box can reach `127.0.0.1` too.

`client_token_env` names an environment variable holding a token that every inbound request must present:

```json
{
  "port": 3456,
  "bind": "127.0.0.1",
  "client_token_env": "CCBUNSHIN_PROXY_TOKEN",
  "providers": {
    "gateway": {
      "upstream": "https://gateway.example.invalid",
      "auth_token_env": "GATEWAY_TOKEN"
    }
  },
  "routes": [{"pattern": "claude-*", "provider": "gateway"}]
}
```

The client sends the token in `Authorization: Bearer` or `x-api-key`, so a profile whose `ANTHROPIC_AUTH_TOKEN` is the **value** of `CCBUNSHIN_PROXY_TOKEN` works unchanged. `/healthz` stays open, because a liveness check should not need a secret. Everything else is gated, `/ccbunshin/resolve` included, since it publishes the route table.

Four rules follow:

- The caller's credential authenticated it to the proxy, not to any upstream, so it is removed before every forwarded request. Otherwise the proxy token would be handed to every gateway the routes name.
- Because of that, every provider must carry its own credential (`auth_token_env` or `auth_headers`) or explicitly want none (`no_auth`). A config that leaves one provider with no auth key is refused at startup, with the provider named.
- The token is compared in constant time and read per request, so a rotated value needs no restart.
- An empty or unexported variable rejects every request rather than opening the gate, and the daemon logs that at `error`.

The token is the value, never the variable name. `"ANTHROPIC_AUTH_TOKEN": "CCBUNSHIN_PROXY_TOKEN"` sends the letters, and every request is rejected.

Any non-empty string works. There is no required format and no length rule, and nothing to hash on your side: the daemon hashes both sides itself. The token has to survive `Authorization: Bearer` plus shell and JSON quoting, so generate it from letters, digits, `-` and `_`:

```sh
export CCBUNSHIN_PROXY_TOKEN=$(openssl rand -hex 32)
```

Put that same value in the client's `ANTHROPIC_AUTH_TOKEN`, or reference it as `${CCBUNSHIN_PROXY_TOKEN}` from a profile, which [expands it at launch](#environment-variables-in-a-profile).

## Environment variables in a profile

Claude Code treats a settings `env` value as a literal, so `"ANTHROPIC_AUTH_TOKEN": "$TOKEN"` sends that text on the wire. `ccbunshin launch` and the project-aware wrapper expand the profile's `env` block before handing it to Claude Code, so a profile can keep its secrets out of the file:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:3456",
    "ANTHROPIC_AUTH_TOKEN": "${CCBUNSHIN_PROXY_TOKEN}"
  }
}
```

Three rules:

- Only `env` is expanded. Other top-level keys, including `hooks` and `permissions`, are passed through untouched, because a dollar sign there belongs to Claude Code or to a shell.
- An unset variable fails the launch and names both the variable and the key. Expanding to an empty string would send a blank credential, which reads as a gateway fault instead of a profile mistake.
- `$$` is a literal dollar sign. A dollar sign that starts no valid name, as in `cost is $100`, is left alone.

The profile file is never rewritten. Expansion happens on a private temporary copy, created with mode 0600 and removed when the launch returns, including when claude exits non-zero or the launch is interrupted. Until it is removed it is the only plaintext copy of the secret, since the profile holds placeholders. A rotated secret therefore takes effect on the next launch with no edit, and a profile that names variables stays safe to copy and share.

## Provider credentials

A profile carries one credential, so a session launched from it sends the same one everywhere. That is wrong as soon as the route table spans endpoints that authenticate differently, and no client-side setting can fix it: `apiKeyHelper` takes no arguments and is cached for five minutes, and `ANTHROPIC_CUSTOM_HEADERS` is fixed for the process, so neither can vary per model. The proxy can, because it resolves the provider per request.

By default, authentication is forwarded and never configured: an inbound `Authorization: Bearer` is passed through, and an inbound `x-api-key` is promoted to `Authorization: Bearer`. Three optional provider keys override that, describing one credential each:

- `auth_token_env` - the name of an environment variable holding this provider's token. It is sent in both `Authorization: Bearer` and `X-Api-Key`, since one of the two is what the upstream reads and the pair covers either.
- `auth_headers` - a `{header name: environment variable}` map, for a gateway that needs a second credential in a header of its own naming, or a value that does not belong in either standard header. It sets exactly the headers it names.
- `no_auth` - send no credential at all, for a local server that needs none.

What the proxy forwards by default depends on the dialect. An `anthropic` provider gets the caller's headers unchanged. Every other dialect, `openai-chat` and `systemone` included, drops the Anthropic protocol headers, moves `x-api-key` into `Authorization: Bearer`, and removes `X-Api-Key`. So an upstream that only reads `X-Api-Key` needs `auth_token_env`, which sets both headers, or an `auth_headers` entry naming it.

With no auth key set, the caller's credential is forwarded exactly as before, which is what every config written before these keys expects. `auth_headers` cannot be combined with either other key, and `auth_token_env` cannot be combined with `no_auth`; a config that does both is refused at startup.

Every value is the name of an environment variable, never the secret, so `proxy.json` stays safe to copy and share. The daemon reads those variables from its own environment, which is the shell that ran `ccbunshin proxy start`:

```sh
export GATEWAY_TOKEN=...
export GATEWAY_TENANT_KEY=...
ccbunshin proxy restart
```

One session, three upstreams, each with its own credential:

```json
{
  "port": 3456,
  "providers": {
    "gateway1": {
      "upstream": "https://gateway1.example.invalid",
      "auth_token_env": "GATEWAY1_TOKEN"
    },
    "gateway2": {
      "upstream": "https://gateway2.example.invalid",
      "auth_headers": {
        "authorization": "GATEWAY2_TOKEN",
        "x-api-key": "GATEWAY2_TOKEN",
        "x-tenant-key": "GATEWAY2_TENANT_KEY"
      }
    },
    "local": {
      "upstream": "http://192.0.2.10:8080",
      "no_auth": true
    }
  },
  "routes": [
    {"pattern": "claude-*", "provider": "gateway1"},
    {"pattern": "gpt-*", "provider": "gateway2"},
    {"pattern": "local-*", "provider": "local"}
  ]
}
```

`gateway1` gets `Authorization: Bearer <GATEWAY1_TOKEN>` and `X-Api-Key: <GATEWAY1_TOKEN>`. `gateway2` gets the same pair from `GATEWAY2_TOKEN` plus `X-Tenant-Key: <GATEWAY2_TENANT_KEY>`, which is what `auth_token_env` alone cannot express: its single value goes to both standard headers, so a second, different value has nowhere to go.

Four rules for `auth_headers`:

- It sets only what it names, so a gateway that reads a custom header and no `Authorization` receives none, the caller's included.
- `Authorization` is the one name that gets the `Bearer ` scheme added, so put the bare token in the variable; every other name is sent raw.
- Header names are validated at load as HTTP header field names and canonicalized to one spelling, so a lowercase key still lands correctly, and two keys differing only in case are rejected instead of resolving to whichever one set the header last.
- An entry whose environment variable is empty is skipped, and is never filled from another entry.

An `anthropic`-dialect provider is unaffected by any of these keys unless it sets one: with none set, nothing changes.

## Resolving a model ID

`GET /ccbunshin/resolve?model=<id>` reports what a request model routes to, answered from the route table:

```sh
curl -s 'http://127.0.0.1:3456/ccbunshin/resolve?model=claude-opus-5'
# {"dialect":"openai-chat","requested":"claude-opus-5","target":"oss-model"}
```

`target` is the ID sent upstream, provider prefix included. An unrouted model returns 404 with `{"error": "no route matches this model"}`.

Without `?model=`, `resolve` returns the whole route table. Apart from reading the config file, it is the only way to see the routing rules:

```sh
curl -s 'http://127.0.0.1:3456/ccbunshin/resolve'
# {"routes":[{"pattern":"claude-*","provider":"gateway","dialect":"openai-chat","upstream":"https://api.example.invalid/provider/v1","default_model":"oss-model"}, ...]}
```

Each entry has `pattern`, `provider`, `dialect`, and `upstream`, plus `default_model` when the provider sets one and `models` when the route or provider has a non-empty map. The `dialect` shown is the route's configured dialect rather than the request-time resolution, because with no model in hand there is no target ID for a `model_dialects` entry to key on.

This endpoint exists because a client cannot otherwise learn what its model ID became. A model list cannot answer it: a route is a glob (`claude-*`) with a `default_model`, so the set of matching models is not enumerable. `resolve` answers the specific question for the specific ID. A statusline uses it to show the real model name behind a Claude Code alias.

There is no model-list endpoint. Every route resolves on the request body's `model` field, so `GET /v1/models` and `/model/info` return `400 request body must be valid JSON` like any other bodyless request. A client that probes for a model list - Claude Code does when `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` is set - receives that 400, falls back to its built-in list, and reports a custom model ID as `unrecognized_model` while still sending it. The ID works; only its label is unrecognized. Use `resolve` to see what an ID becomes.

## Different mappings per profile

Claude Code can resolve the same alias to different concrete model IDs in each profile.

Provider1:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://localhost:3456",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-4-1",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-4-5",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "claude-haiku-4-5"
  },
  "hooks": {},
  "model": "sonnet"
}
```

Provider2:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://localhost:3456",
    "ANTHROPIC_DEFAULT_OPUS_MODEL": "deepseek-v4-flash",
    "ANTHROPIC_DEFAULT_SONNET_MODEL": "qwen-3.8-27b",
    "ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-oss-120b"
  },
  "hooks": {},
  "model": "sonnet"
}
```

Run both sessions with the same alias:

```sh
ccbunshin launch provider1
ccbunshin launch provider2
```

The first session sends `claude-sonnet-4-5`. The second sends `qwen-3.8-27b`. The proxy routes those concrete model IDs to different providers.

