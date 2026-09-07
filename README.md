# ccbunshin

**ccbunshin** (分身, *bunshin*, "shadow clone") runs several Claude Code profiles side by side. Each profile has its own settings, while Claude Code state stays shared.

MIT licensed. See [LICENSE](LICENSE).

## Install

Install the latest published release:

```sh
curl -fsSL https://raw.githubusercontent.com/alecchen/ccbunshin/main/install.sh | sh
```

The installer picks the right binary for Linux or macOS on amd64 or arm64. Set `CCBUNSHIN_VERSION` to install another release, or `CCBUNSHIN_INSTALL_DIR` to choose another directory.

Initialize the profile directory:

```sh
ccbunshin init
```

## Profiles

Create a profile:

```sh
ccbunshin create provider1
```

Create one from an example:

```sh
ccbunshin create provider2 --from examples/provider2.json
```

Profiles live in `~/.claude-profiles/<name>.json`. The directory uses mode `700`, and profile files use mode `600`.

A profile can use Claude Code's normal authentication settings, including `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, and `apiKeyHelper`. If you put a token in a profile, protect that file. A local `apiKeyHelper` is usually a better choice than storing the token directly in JSON.

Example profile:

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

## Commands

```text
ccbunshin init
ccbunshin create <name> [--from <file>] [--force]
ccbunshin launch <name> [claude args...]
ccbunshin model <name> <model>
ccbunshin list
ccbunshin status <name>
ccbunshin doctor <name>
ccbunshin delete <name>
ccbunshin proxy start
ccbunshin proxy status
ccbunshin proxy stop
```

Launch a profile:

```sh
ccbunshin launch provider1
```

This runs Claude Code with the profile settings file:

```sh
claude --settings ~/.claude-profiles/provider1.json
```

Change the default model for future sessions:

```sh
ccbunshin model provider1 claude-sonnet-4-5
```

## Model routing

The proxy exposes one endpoint for the LeanCTX flow. Both profiles can point to it. The proxy reads the model in each request and uses the route rules in `examples/proxy.json`.

```text
ccbunshin :3456
├── claude-*              -> provider1
└── qwen/deepseek/gpt-oss -> provider2
```

Start the proxy with:

```sh
export CCBUNSHIN_PROXY_CONFIG=/path/to/proxy.json
ccbunshin proxy start
```

A route without `*` matches one exact model. A route such as `claude-*` matches models with that prefix. Provider URLs and model rewrites belong in the JSON config. Authentication stays in Claude Code settings and environment variables, not in the proxy config.

### Different mappings per profile

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

## Build and verify

Build for the current machine:

```sh
go -C cmd/ccbunshin build -o ccbunshin
```

Build Linux binaries from macOS:

```sh
env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go -C cmd/ccbunshin build -trimpath -ldflags='-s -w' \
  -o ccbunshin-linux-amd64

env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go -C cmd/ccbunshin build -trimpath -ldflags='-s -w' \
  -o ccbunshin-linux-arm64
```

The architecture names are:

```text
x86_64  -> amd64
aarch64 -> arm64
```

Run the Go checks:

```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
```

On Linux, `docs/systemd/ccbunshin-proxy.service` shows how to run the proxy at boot with automatic restarts. For a user-local process, use `ccbunshin proxy start`, `status`, and `stop`.

## Repository layout

- `cmd/ccbunshin/` contains the Go CLI and model-routed proxy.
- `examples/` contains profile and proxy config examples.
- `docs/` contains architecture and deployment notes.

See [cmd/ccbunshin/README.md](cmd/ccbunshin/README.md) for command and proxy details.
