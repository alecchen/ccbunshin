# ccbunshin

**ccbunshin** (分身, *bunshin*, "shadow clone") runs several Claude Code profiles side by side. Each profile has its own settings, while Claude Code state stays shared.

MIT licensed. See [LICENSE](LICENSE).

## Install

Install the latest published release:

```sh
curl -fsSL https://raw.githubusercontent.com/alecchen/ccbunshin/main/install.sh | sh
```

The installer downloads the newest GitHub release for Linux or macOS on amd64 or arm64, so publishing a release needs no installer change. Set `CCBUNSHIN_VERSION` to pin a specific release (for example `CCBUNSHIN_VERSION=v0.0.3 sh install.sh`), or `CCBUNSHIN_INSTALL_DIR` to choose another directory.

Update an installed binary later with `ccbunshin update`, which checks the latest release, prints the current and latest versions when an update is available, and replaces the binary in place.

Set up ccbunshin. This creates `~/.claude-profiles/` if needed and installs the project-aware `claude` wrapper into each shell rc file that exists:

```sh
ccbunshin init
# Initialized profile directory: /Users/you/.claude-profiles
# installed: /Users/you/.bashrc hooks bash
# installed: /Users/you/.zshrc hooks zsh
# skip: /Users/you/.tcshrc not found
# skip: /Users/you/.cshrc not found
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
ccbunshin init [bash|zsh|tcsh]
ccbunshin create <name> [--from <file>] [--force]
ccbunshin local [<name>|--unset]
ccbunshin launch [<name>] [claude args...]
ccbunshin model <name> <model>
ccbunshin list
ccbunshin status <name>
ccbunshin doctor <name>
ccbunshin delete <name>
ccbunshin proxy init [--force]
ccbunshin proxy start
ccbunshin proxy status
ccbunshin proxy stop
ccbunshin update
ccbunshin version
```

`ccbunshin --help` prints this list; `ccbunshin help <command>` and
`ccbunshin <command> --help` print details for one command. Missing or
wrong arguments also print the relevant help.

Launch a profile explicitly:

```sh
ccbunshin launch provider1
```

Or select a default profile for the current directory and its descendants:

```sh
ccbunshin local provider1
ccbunshin launch
```

This writes a `.ccbunshin-profile` marker in the current directory. `ccbunshin launch` searches the current directory and its parents, with the nearest marker taking precedence. An explicit profile takes precedence over the local file. This project-local selection model is inspired by [rbenv](https://github.com/rbenv/rbenv)/[pyenv](https://github.com/pyenv/pyenv)'s local version model. Inspect or clear the current local selection with:

```sh
ccbunshin local
ccbunshin local --unset
```

The lookup is performed by the executable, so this works unchanged from Bash, zsh, tcsh, and other shells.

This runs Claude Code with the profile settings file:

```sh
claude --settings ~/.claude-profiles/provider1.json
```

Change the default model for future sessions:

```sh
ccbunshin model provider1 claude-sonnet-4-5
```

## Project-aware claude

`ccbunshin init` (no args) detects `~/.bashrc`, `~/.zshrc`, `~/.tcshrc`/`~/.cshrc`
and appends the matching wrapper hook to each file that exists, printing
per-file status (`installed:` / `ok: already hooks` / `skip: not found`) to
stdout. It is idempotent: re-running never duplicates a hook.

Alternatively, add a wrapper to one shell manually:

```sh
eval "$(ccbunshin init bash)"   # Bash
eval "$(ccbunshin init zsh)"    # Zsh
eval `ccbunshin init tcsh`      # tcsh
```

A project is any directory (or ancestor of the current directory) that contains a `.ccbunshin-profile` marker. Inside one, `claude` behaves as `ccbunshin launch <provider>` and forwards every argument unchanged:

```sh
cd ~/projects/my-app            # .ccbunshin-profile says provider1
claude -p "hello world"          # ccbunshin launch provider1 -p "hello world"
claude --model sonnet --resume abc   # all arguments reach Claude unchanged
```

Outside a project, `claude` runs the original Claude Code command with the original arguments. The nearest marker wins, so nested projects work, and the exit status of Claude is returned.

The wrapper reuses the existing `.ccbunshin-profile` marker created by `ccbunshin local <name>`; it introduces no new project marker. Bash and zsh define a `claude` shell function, tcsh an alias, so re-running eval is safe but replaces any `claude` function or alias you defined yourself. The original Claude Code binary remains callable and is what runs outside projects.

## Model routing (optional)

The proxy is optional. Each profile can already set its own
`ANTHROPIC_BASE_URL`, so point profiles directly at different endpoints
when you can.

Use the proxy when you need a single endpoint. For example, a single
lean-ctx proxy instance only has one Anthropic upstream. Point all
profiles at the ccbunshin proxy, and it routes each request to a
different endpoint based on the model ID and the rules in `proxy.json`.

```text
ccbunshin :3456
├── claude-*              -> provider1
└── qwen/deepseek/gpt-oss -> provider2
```

Generate a starting config with:

```sh
ccbunshin proxy init
```

This writes a template to `~/.config/ccbunshin/proxy.json` (mode `600`),
or to `$CCBUNSHIN_PROXY_CONFIG` when set. Use `--force` to overwrite an
existing file. Edit the upstreams and routes, then start the proxy with:

```sh
export CCBUNSHIN_PROXY_CONFIG=/path/to/proxy.json
ccbunshin proxy start
```

The PID and log live in `~/.config/ccbunshin/`, whatever the config path is.
`proxy start` exits non-zero and points at the log when the proxy cannot start
(for example the port is already in use); `proxy status` and `proxy stop` need
no arguments and read that same state.

Routes select the provider; `models` rewrites the model ID after routing.

A pattern with no wildcard matches one exact model (`"qwen-3.8-27b"`), and `*` matches any run of characters (`"claude-*"` matches `claude-sonnet-4-5`). Routes are checked in order; the first match wins. Once routed, the provider's `models` map optionally replaces the request's model ID with the upstream's ID before forwarding. Provider URLs and model rewrites belong in the JSON config. Authentication stays in Claude Code settings and environment variables, not in the proxy config.

`proxy.json` keys:

- `port` (required, 1-65535): the port the proxy listens on.
- `providers` (required, at least one): each provider needs an `upstream` absolute URL. Optional `timeout` is a Go duration string (default `60s`); optional `models` maps a requested model ID to the ID sent upstream. Optional `default_model` is the rewrite target for any routed model with no explicit `models` entry. Optional `dialect` selects the wire format: `anthropic` (the default) forwards the request unchanged; `openai-chat` translates an Anthropic request onto an OpenAI-compatible `/chat/completions` call, and translates the response back.
- `routes` (required): ordered list of `{pattern, provider}`. `pattern` matches the request model: no wildcard means one exact model, `*` matches any run of characters (for example `"claude-*"`). `provider` must name a provider above. Duplicate exact patterns are rejected. A route may also carry `models` (overriding the provider's map), `dialect` (overriding the provider's), and `model_dialects` (a `{target model: dialect}` map overriding both).

### Translating providers

A gateway that speaks OpenAI's `/chat/completions` instead of Anthropic's `/v1/messages` needs `"dialect": "openai-chat"`. The proxy then converts `system`, content blocks, `tools`, and `tool_choice` to their chat-completions equivalents; returns upstream `reasoning` as Anthropic `thinking` blocks; answers `/v1/messages/count_tokens` locally with an estimate; and rewraps upstream errors in Anthropic's error envelope. It never sends `reasoning_effort`, which several such gateways reject. See `cmd/ccbunshin/README.md` for the full behavior and the resolution order.

See `examples/proxy.json` for the full example the `init` template is based on.

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

Run the Go checks and the shell integration tests:

```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
sh tests/shell-integration.sh
sh tests/install-test.sh
```

On Linux, `docs/systemd/ccbunshin-proxy.service` shows how to run the proxy at boot with automatic restarts. For a user-local process, use `ccbunshin proxy start`, `status`, and `stop`.

## Repository layout

- `cmd/ccbunshin/` contains the Go CLI and model-routed proxy.
- `examples/` contains profile and proxy config examples.
- `docs/` contains architecture and deployment notes.

See [cmd/ccbunshin/README.md](cmd/ccbunshin/README.md) for command and proxy details.
