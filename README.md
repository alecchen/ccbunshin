# ccbunshin

**ccbunshin** (分身, *bunshin*, "shadow clone") runs multiple Claude Code configurations side by side. Each profile is a clone with isolated configuration and shared Claude Code state.

MIT licensed. See [LICENSE](LICENSE).

Each profile provides isolated settings such as the endpoint, model, hooks, and telemetry environment. Claude Code state remains in the normal `~/.claude/` location.

## Install

Clone the repository and build the unified Go CLI:

```sh
go -C cmd/ccbunshin build -o ccbunshin
export PATH="$PWD:$PATH"
```

Run initialization once to create the profile directory:

```sh
ccbunshin init
```

## Profiles

Create a profile from the built-in scaffold:

```sh
ccbunshin create provider1
```

Create one from an example file:

```sh
ccbunshin create provider2 --from examples/provider2.json
```

Profiles are stored in `~/.claude-profiles/<name>.json`. A profile may define authentication using Claude Code's native mechanisms, such as `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, or `apiKeyHelper`. Profile files contain secrets only when you intentionally configure per-profile authentication, so keep the directory private and permissions restricted to `700`/`600`.

Example using a token from the profile environment:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://localhost:3456",
    "ANTHROPIC_AUTH_TOKEN": "replace-with-provider-token"
  },
  "hooks": {},
  "model": "sonnet"
}
```

For better secret handling, use `apiKeyHelper` to call a local credential helper instead of writing the token directly into the profile. The ccbunshin proxy does not manage or store credentials; Claude Code supplies authentication using its normal settings and environment behavior.

## Commands

```text
ccbunshin init
ccbunshin create <name> [--from <file>] [--force]
ccbunshin launch <name> [claude args...]
ccbunshin model <name> <model>
ccbunshin list
ccbunshin status <name>
ccbunshin doctor [name]
ccbunshin delete <name>
ccbunshin proxy start
ccbunshin proxy status
ccbunshin proxy stop
```

Launch a profile:

```sh
ccbunshin launch provider1
```

Update its model:

```sh
ccbunshin model provider1 qwen3.8-27b
```

`launch` passes the profile settings file to Claude Code with `--settings`.

## Phase 2 proxy

The Go proxy exposes one endpoint for the LeanCTX-facing flow. Configure both profiles with the same proxy URL, then route requests by the model field using `examples/proxy.json`:

```text
LeanCTX :5000 or :4444 -> ccbunshin proxy :3456
                              claude-* -> provider1
                              qwen/deepseek/gpt-oss -> provider2
```

Set `CCBUNSHIN_PROXY_CONFIG` to the route config. Route patterns without `*` are exact matches; patterns such as `claude-*` use glob matching. Provider URLs and model mappings are configurable in JSON. Authentication remains in Claude Code settings or environment; it is not stored in the proxy config. See `proxy/README.md`.


Run the shell test suite and ShellCheck:

```sh
shellcheck ccbunshin tests/test_ccbunshin.sh
bash -n ccbunshin
tests/test_ccbunshin.sh
```

## Per-profile model mappings

Both profiles can use the same Claude Code alias, while resolving that alias to different concrete models before the request reaches the shared proxy. Define Claude Code's model override variables inside each profile's `env` block.

Provider1 profile:

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

Provider2 profile:

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

Start both sessions with the same alias:

```sh
# Terminal 1: sonnet resolves to claude-sonnet-4-5
ccbunshin launch provider1

# Terminal 2: sonnet resolves to qwen-3.8-27b
ccbunshin launch provider2
```

The shared proxy receives the resolved model IDs and routes them using `examples/proxy.json`:

```text
provider1: sonnet -> claude-sonnet-4-5 -> provider1
provider2: sonnet -> qwen-3.8-27b     -> provider2
```

This avoids needing a profile header or separate proxy port. The model override variables are Claude Code settings, not ccbunshin-specific variables. Verify the exact variable names against the Claude Code version being used.


The proxy is compiled for the target operating system and architecture. Go's cross-compilation variables let you build a Linux binary from macOS without running it locally:

```sh
# Build for the current machine
go -C cmd/ccbunshin build -o ccbunshin

# Linux x86_64
env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go -C cmd/ccbunshin build -o ccbunshin-linux-amd64

# Linux ARM64
env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go -C cmd/ccbunshin build -o ccbunshin-linux-arm64
```

Check the Linux host architecture with `uname -m`:

```text
x86_64  -> GOARCH=amd64
aarch64 -> GOARCH=arm64
```

Copy the matching binary to the Linux host, then configure and run it:

```sh
scp ccbunshin-proxy-linux-amd64 user@linux-host:/usr/local/bin/ccbunshin-proxy
export CCBUNSHIN_PROXY_CONFIG=/etc/ccbunshin/proxy.json
/usr/local/bin/ccbunshin-proxy
```

Run proxy validation from the repository root:

```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
```


- `cmd/ccbunshin/` - unified Go CLI and model-routed proxy
- `examples/` - sample profile settings
- `docs/` - architecture and implementation documents
- `tests/` - legacy shell test location (the Go test suite is under `cmd/ccbunshin/`)

See `docs/CCBUNSHIN_IMPLEMENTATION.md` for the full implementation specification.
