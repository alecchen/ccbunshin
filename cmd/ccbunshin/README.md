# ccbunshin Go command

This directory contains the unified Go executable for profile management and the model-routed proxy.

## Build

Build from the repository root:

```sh
go -C cmd/ccbunshin build -o ccbunshin
```

Cross-compile a Linux binary from macOS:

```sh
env GOOS=linux GOARCH=amd64 CGO_ENABLED=0 \
  go -C cmd/ccbunshin build -o ccbunshin-linux-amd64

env GOOS=linux GOARCH=arm64 CGO_ENABLED=0 \
  go -C cmd/ccbunshin build -o ccbunshin-linux-arm64
```

## CLI commands

```sh
ccbunshin init
ccbunshin create <name> [--from <file>] [--force]
ccbunshin launch <name> [claude args...]
ccbunshin model <name> <model>
ccbunshin list
ccbunshin status <name>
ccbunshin doctor <name>
ccbunshin delete <name>
ccbunshin uninstall
```

`ccbunshin launch <name>` runs Claude Code with:

```sh
claude --settings ~/.claude-profiles/<name>.json
```

Profiles retain private permissions: the directory is `700` and profile files are `600`.

## Proxy lifecycle

The same binary manages the proxy process:

```sh
ccbunshin proxy start
ccbunshin proxy status
ccbunshin proxy stop
```

The proxy configuration path comes from `CCBUNSHIN_PROXY_CONFIG`, or defaults to:

```text
~/.config/ccbunshin/proxy.json
```

The lifecycle commands keep the PID and log files under:

```text
~/.cache/ccbunshin/proxy.pid
~/.cache/ccbunshin/proxy.log
```

`proxy start` runs the internal server mode. The HTTP server listens on the configured numeric port, extracts the request model, applies ordered Go glob routes, optionally rewrites the model, and forwards the request to the selected provider. Unknown models return HTTP 400. `/healthz` returns HTTP 200 without contacting an upstream.

Authentication is not stored in the proxy config. Claude Code's native `ANTHROPIC_AUTH_TOKEN`, `ANTHROPIC_API_KEY`, or `apiKeyHelper` mechanisms remain responsible for credentials.

## systemd alternative

On Linux, systemd can manage the proxy instead of `ccbunshin proxy start|stop|status`. Install the binary at `/usr/local/bin/ccbunshin`, copy `docs/systemd/ccbunshin-proxy.service` to `/etc/systemd/system/`, and adjust the config path if needed:

```sh
sudo install -m 755 ccbunshin /usr/local/bin/ccbunshin
sudo install -m 644 docs/systemd/ccbunshin-proxy.service /etc/systemd/system/ccbunshin-proxy.service
sudo systemctl daemon-reload
sudo systemctl enable --now ccbunshin-proxy
sudo systemctl status ccbunshin-proxy
```

The service runs the unified binary in internal server mode and reads `/etc/ccbunshin/proxy.json`. Use the built-in lifecycle commands for user-local deployments; use systemd for boot startup and crash restarts.


```sh
gofmt -w cmd/ccbunshin/*.go
go -C cmd/ccbunshin vet ./...
go -C cmd/ccbunshin test ./...
```
