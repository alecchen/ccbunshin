# ccbunshin

Run multiple Claude Code configurations side by side while sharing Claude Code state.

Each profile provides isolated settings such as the endpoint, model, hooks, and telemetry environment. Claude Code state remains in the normal `~/.claude/` location.

## Install

Clone the repository, then make the CLI available on your `PATH`:

```sh
chmod +x ccbunshin
export PATH="$PWD:$PATH"
```

Run initialization once to create the profile directory and install shell wrappers:

```sh
ccbunshin init
```

The command adds an anchored wrapper block to `~/.bashrc` and `~/.zshrc`.

## Profiles

Create a profile from the built-in scaffold:

```sh
ccbunshin create provider1
```

Create one from an example file:

```sh
ccbunshin create provider2 --from examples/provider2.json
```

Profiles are stored in `~/.claude-profiles/<name>.json` and should not contain credentials.

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
ccbunshin uninstall
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

The Go proxy is implemented under `proxy/`. It runs both explicit loopback listeners in one process:

- provider1: `127.0.0.1:3456`
- provider2: `127.0.0.1:3457`

Configure both upstreams before starting it:

```sh
export CCBUNSHIN_PROVIDER1_UPSTREAM=https://provider1.example.invalid
export CCBUNSHIN_PROVIDER2_UPSTREAM=https://provider2.example.invalid
go -C proxy run .
```

The proxy keeps provider routing separate from LeanCTX. Each listener supports `/healthz`, forwards Anthropic-compatible requests and streams responses. See `proxy/README.md` for all configuration variables.


Run the shell test suite and ShellCheck:

```sh
shellcheck ccbunshin tests/test_ccbunshin.sh
bash -n ccbunshin
tests/test_ccbunshin.sh
```

## Repository layout

- `ccbunshin` - dependency-free bash CLI
- `examples/` - sample profile settings
- `docs/` - architecture and implementation documents
- `tests/` - shell tests

See `docs/CCBUNSHIN_IMPLEMENTATION.md` for the full implementation specification.
