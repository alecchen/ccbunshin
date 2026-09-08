# ccbunshin Go command

This directory contains the Go executable. It manages profiles, launches Claude Code, and runs the model-routed proxy.

## Build

From the repository root:

```sh
go -C cmd/ccbunshin build -o ccbunshin
```

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

Inside a project, `claude` behaves as `ccbunshin launch <provider>`; outside one it runs the original Claude Code command with the original arguments. The wrapper reuses the existing `.ccbunshin-profile` marker and delegates project discovery to the ccbunshin binary, so it never parses profile files itself.

Bash and zsh install a `claude` shell function that resolves the provider by calling `ccbunshin resolve-provider`. tcsh cannot express conditional aliases, so it installs a `claude` alias that delegates to `ccbunshin run`, the internal command that resolves the nearest project provider or falls back to the original `claude` binary. `ccbunshin resolve-provider` and `ccbunshin run` are internal but callable. Re-running eval is idempotent: no nested wrappers or duplicate aliases.

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

Routes select the provider; `models` rewrites the model ID after routing.

The proxy listens on the configured port, reads the request model, applies the ordered glob routes (first match wins), optionally rewrites the model via the provider's `models` map, and forwards the request. It returns HTTP 400 when no route matches. `/healthz` returns HTTP 200 without contacting an upstream.

`proxy.json` keys:

- `port` (required, 1-65535): the port the proxy listens on.
- `providers` (required, at least one): each provider needs an `upstream` absolute URL. Optional `timeout` is a Go duration string (default `60s`); optional `models` maps a requested model ID to the ID sent upstream.
- `routes` (required): ordered list of `{pattern, provider}`. `pattern` matches the request model: no wildcard means one exact model, `*` matches any run of characters (for example `"claude-*"`). `provider` must name a provider above. Duplicate exact patterns are rejected.

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
