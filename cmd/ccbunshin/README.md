# ccbunshin Go command

This directory contains the Go executable. It manages profiles, launches Claude Code, and runs the model-routed proxy.

## Build

From the repository root:

```sh
go -C cmd/ccbunshin build -ldflags "-X main.buildTime=$(date +%Y%m%d-%H%M)" -o ccbunshin
```

`ccbunshin version` then reports `<short commit>-<build time>`, for example
`e17b3c5-20260912-1453`. The commit comes from the toolchain's VCS stamping and
needs no flag, shortened to 7 characters; the time comes from the `-ldflags`
stamp. Omit the stamp for a commit-only identity. Release builds are stamped
`-X main.version=<tag>` by the release workflow and report the tag.

Cross-compile for Linux:

```sh
env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go -C cmd/ccbunshin build -trimpath -ldflags='-s -w' \
  -o ccbunshin-linux-amd64

env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go -C cmd/ccbunshin build -trimpath -ldflags='-s -w' \
  -o ccbunshin-linux-arm64
```

## Profiles

```sh
ccbunshin init [bash|zsh|tcsh]
ccbunshin create <name> [--from <file>] [--force]
ccbunshin launch [<name>] [claude args...]
ccbunshin local [<name>|--unset]
ccbunshin global [<name>|--unset]
ccbunshin model <name> <model>
ccbunshin list
ccbunshin status <name>
ccbunshin doctor <name>
ccbunshin delete <name>
ccbunshin version
ccbunshin help [<command>]
```

`ccbunshin --help` prints the command list; `ccbunshin help <command>` and
`ccbunshin <command> --help` print details for one command. Missing or
wrong arguments also print the relevant help.

`ccbunshin launch provider1` runs:

```sh
claude --settings ~/.claude-profiles/provider1.json
```

Extra arguments are forwarded to Claude unchanged, at the argument-vector level, with no option allowlist:

```sh
ccbunshin launch provider1 -p "hello world"
# claude --settings ~/.claude-profiles/provider1.json -p "hello world"
```

`ccbunshin launch` returns Claude's exit status, so `ccbunshin launch provider1 ...; echo $?` behaves like running Claude directly.

## Shell integration

`ccbunshin init` (no args) detects `~/.bashrc`, `~/.zshrc`,
`~/.tcshrc`/`~/.cshrc` and appends the matching wrapper hook to each file that
exists, printing per-file status (`installed:` / `ok: already hooks` /
`skip: not found`) to stdout. Re-running is idempotent.

The manual alternative - `ccbunshin init bash`, `ccbunshin init zsh`, and
`ccbunshin init tcsh` - print a wrapper that routes `claude` through the nearest `.ccbunshin-profile` project:

```sh
eval "$(ccbunshin init bash)"
eval "$(ccbunshin init zsh)"
eval `ccbunshin init tcsh`
```

Inside a project, `claude` behaves as `ccbunshin launch <provider>`; outside one it uses the global profile from `ccbunshin global <name>`, or the original Claude Code command with the original arguments when no global profile is set. The wrapper reuses the existing `.ccbunshin-profile` marker and delegates project discovery to the ccbunshin binary, so it never parses profile files itself.

Bash and zsh install a `claude` shell function that resolves the provider by calling `ccbunshin resolve-provider`. The function is declared with the `function` keyword and clears any pre-existing `claude` alias first: zsh parses a whole `eval` string before running it, so an alias named `claude` (agent launchers commonly set one) would otherwise turn the definition into a parse error and leave the wrapper uninstalled. tcsh cannot express conditional aliases, so it installs a `claude` alias that delegates to `ccbunshin run`, the internal command that resolves the nearest project provider or falls back to the original `claude` binary. `ccbunshin resolve-provider` and `ccbunshin run` are internal but callable. Re-running eval is idempotent: no nested wrappers or duplicate aliases.

The profile directory has mode `700`; profile files have mode `600`. Authentication comes from Claude Code settings and environment variables. The proxy does not store credentials.

## Proxy lifecycle

```sh
ccbunshin proxy init [--force]
ccbunshin proxy start
ccbunshin proxy status
ccbunshin proxy stop
```

`proxy init` writes a template config (mode `600`); `--force` overwrites an existing file. Set `CCBUNSHIN_PROXY_CONFIG` to choose the JSON config. If unset, the command uses:

```text
~/.config/ccbunshin/proxy.json
```

The process stores its PID and log at:

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

Routes select the provider; `models` rewrites the model ID after routing.

The proxy listens on the configured port, reads the request model, applies the ordered glob routes (first match wins), resolves the target model and dialect, and forwards the request. It returns HTTP 400 when no route matches. `/healthz` returns HTTP 200 without contacting an upstream.

`proxy.json` keys:

- `port` (required, 1-65535): the port the proxy listens on.
- `providers` (required, at least one): each provider needs an `upstream` absolute URL. Optional `timeout` is a Go duration string (default `60s`); optional `models` maps a requested model ID to the ID sent upstream. Optional `default_model` is the rewrite target for any routed model with no explicit `models` entry. Optional `dialect` selects the wire format: `anthropic` (the default) forwards the request unchanged, `openai-chat` translates it (see below).
- `routes` (required): ordered list of `{pattern, provider}`. `pattern` matches the request model: no wildcard means one exact model, `*` matches any run of characters (for example `"claude-*"`). `provider` must name a provider above. Duplicate exact patterns are rejected. A route may also carry `models` (overriding the provider's map), `dialect` (overriding the provider's), and `model_dialects` (a `{target model: dialect}` map overriding both, keyed on the ID sent upstream).

## Translating providers

A provider that speaks OpenAI's `/chat/completions` rather than Anthropic's `/v1/messages` needs `"dialect": "openai-chat"`. The proxy then translates in both directions:

- Anthropic `system`, content blocks, `tools`, and `tool_choice` become their chat-completions equivalents. `tool_result` becomes a `role: "tool"` message placed before the turn it answers, and `tool_use` arguments become a JSON string.
- Upstream `reasoning` becomes Anthropic `thinking` blocks, streaming and non-streaming. **`reasoning_effort` is never sent**: deriving it from `thinking` is what broke requests against the upstream this was built for, so reasoning is recovered from the response instead. `thinking` itself is not forwarded either.
- `POST /v1/messages/count_tokens` is answered locally with a character-based estimate, since such upstreams often do not implement it. The estimate is approximate by design.
- Upstream errors are rewrapped in Anthropic's `{"type": "error", ...}` envelope at the same status, keeping the upstream's message, so a client that classifies errors on that shape still works and a wrong mapping is legible.
- Prompt-cache breakpoints (`cache_control`) are dropped, since chat-completions has no equivalent. Every turn is a cold prefill.

Dialect resolution is most-specific-first: the route's `model_dialects` entry for the target model, then the route's `dialect`, then the provider's `dialect`, then `anthropic`. Because the dialect is per model, one upstream can serve both kinds at once, as long as its routes live in different namespaces: `claude-*` and, say, `vertex-claude-*` do not collide, since a pattern matches the whole model string.

Two things to know when configuring it:

- A route with dialect `openai-chat` needs somewhere to get an acceptable model ID, or `proxy start` refuses the config. Set `default_model` on the provider, or `models` on the provider or route.
- A misspelled dialect key is ignored rather than rejected, so the provider silently stays on the default. The config format intentionally allows unknown keys; check the spelling if a provider seems untranslated.

To find out which endpoint a gateway expects for a given model, POST a one-token request to its `/messages` path and read which endpoint the error names. Model lists often do not say, and an ID's prefix is not a reliable signal.

## Resolving a model ID

`GET /ccbunshin/resolve?model=<id>` reports what a request model routes to, answered from the route table:

```sh
curl -s 'http://127.0.0.1:3456/ccbunshin/resolve?model=claude-opus-5'
# {"dialect":"openai-chat","requested":"claude-opus-5","target":"oss-model"}
```

`target` is the ID sent upstream, provider prefix included. An unrouted model returns 404 with `{"error": "no route matches this model"}`. Without `?model=` the whole route table is returned, so a client can discover the routes without reading the config file.

This endpoint exists because a client cannot otherwise learn what its model ID became. A model list cannot answer it: a route is a glob (`claude-*`) with a `default_model`, so the set of matching models is not enumerable. `resolve` answers the specific question for the specific ID. A statusline uses it to show the real model name behind a Claude Code alias.

`/model/info` and `/v1/models` are forwarded unchanged. `/model/info` carries no `model` field, so it cannot be dialect-dispatched or resolved, and a gateway that does not implement it returns its own 404.

Authentication is forwarded, never configured: an inbound `Authorization: Bearer` is passed through, and an inbound `x-api-key` is promoted to `Authorization: Bearer`. No credential is read from or written to `proxy.json`.

## systemd

On Linux, systemd can run the proxy instead of the user-local lifecycle commands:

```sh
sudo install -m 755 ccbunshin /usr/local/bin/ccbunshin
sudo install -m 644 docs/systemd/ccbunshin-proxy.service /etc/systemd/system/ccbunshin-proxy.service
sudo systemctl daemon-reload
sudo systemctl enable --now ccbunshin-proxy
sudo systemctl status ccbunshin-proxy
```

The service reads `/etc/ccbunshin/proxy.json` and starts the same binary in server mode. Use systemd when the proxy should start at boot and restart after a crash.

## Verification

```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
sh tests/shell-integration.sh
sh tests/install-test.sh
```

## Compatibility review

The Anthropic API is versioned such that new *optional* request fields and new streaming
event types can appear without a version bump. Both are additive, so the translator is
written to ignore unknown request fields and unknown stream events rather than fail on
them. Before a release, re-check that this still holds:

- API versioning policy - https://platform.claude.com/docs/en/api/versioning
- Anthropic API release notes - https://platform.claude.com/docs/en/release-notes/api
- Claude Code release notes - https://platform.claude.com/docs/en/release-notes/claude-code
- Claude Code changelog (raw, diffable) - https://raw.githubusercontent.com/anthropics/claude-code/main/CHANGELOG.md
- Messages and headers reference - https://platform.claude.com/docs/en/api/messages and https://platform.claude.com/docs/en/api/beta-headers
- Streaming event reference - https://platform.claude.com/docs/en/build-with-claude/streaming

The Claude Code changelog is worth watching specifically, because it documents environment
variables and endpoint behavior that affect a gateway rather than the model API.
