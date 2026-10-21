# ccbunshin

**ccbunshin** (分身, *bunshin*, "shadow clone") runs several Claude Code profiles side by side. Each profile has its own settings, while Claude Code state stays shared.

The whole program is one dependency-free Go binary in `cmd/ccbunshin`. It manages profiles, launches Claude Code, and runs an optional model-routed proxy.

- [Install](#install)
- [Profiles](#profiles)
  - [Environment variables in a profile](#environment-variables-in-a-profile)
- [Commands](#commands)
- [Project-aware claude](#project-aware-claude)
- [Shell completion](#shell-completion)
- [Model routing (optional)](#model-routing-optional)
- [systemd](#systemd)
- [Build and verify](#build-and-verify)
- [Repository layout](#repository-layout)
- [License](#license)

The proxy has its own reference: [docs/PROXY.md](docs/PROXY.md).

## Install

Install the latest published release:

```sh
curl -fsSL https://raw.githubusercontent.com/alecchen/ccbunshin/main/install.sh | sh
```

The installer downloads the newest GitHub release for Linux or macOS on amd64 or arm64, so publishing a release needs no installer change. Set `CCBUNSHIN_VERSION` to pin a specific release (for example `CCBUNSHIN_VERSION=v0.0.3 sh install.sh`), or `CCBUNSHIN_INSTALL_DIR` to choose another directory.

Update an installed binary later with `ccbunshin update`, which checks the latest release, prints the current and latest versions when an update is available, and replaces the binary in place. A local source build carries no release tag, so `update` prints the latest tag and stops; `ccbunshin update --force` replaces it with the release binary anyway.

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

A profile is a settings file consumed by another program, so two properties are enforced rather than assumed. Its top level has to be a JSON object: `null`, an array, or a scalar is valid JSON that Claude Code refuses to start with, and it answers with its own "Settings Error" dialog instead of a message naming the file. And writes replace the file in one step, so a reader - including a login reading profiles seconds after a session starts - never sees a truncated write.

The checks live in one place each, `parseProfile` and `writeProfileFile`:

```sh
ccbunshin list          # reports a profile Claude Code would reject, and still lists the rest
ccbunshin doctor bad    # exits non-zero, naming the file and the reason
ccbunshin launch bad    # refuses before starting Claude Code, naming the file
```

`create`, `model`, and `launch` all go through the same shape check, so `ccbunshin create x --from f` rejects `f` if its top level is not an object, and `launch` re-reads the profile immediately before `exec` in case it changed in between. The check is shape-only: Claude Code still owns validation of the keys themselves, and unknown keys are not ccbunshin's business.

### Environment variables in a profile

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

## Commands

```text
ccbunshin init [bash|zsh|tcsh]
ccbunshin create <name> [--from <file>] [--force]
ccbunshin local [<name>|--unset]
ccbunshin global [<name>|--unset]
ccbunshin launch [<name>] [claude args...]
ccbunshin model <name> <model>
ccbunshin list
ccbunshin status <name>
ccbunshin doctor <name>
ccbunshin delete <name>
ccbunshin uninstall
ccbunshin proxy init [--force]
ccbunshin proxy start
ccbunshin proxy status
ccbunshin proxy restart
ccbunshin proxy stop
ccbunshin completion <bash|zsh>
ccbunshin update [--force]
ccbunshin version
ccbunshin help [<command>]
```

`ccbunshin --help` prints this list; `ccbunshin help <command>` and
`ccbunshin <command> --help` print details for one command. Missing or
wrong arguments also print the relevant help.

Launch a profile explicitly:

```sh
ccbunshin launch provider1
```

This runs Claude Code with the profile's settings:

```sh
claude --settings ~/.claude-profiles/provider1.json
```

The file named by `--settings` is an expanded temporary copy of the profile, not the profile itself. See [Environment variables in a profile](#environment-variables-in-a-profile).

Extra arguments are forwarded to Claude unchanged, at the argument-vector level, with no option allowlist:

```sh
ccbunshin launch provider1 -p "hello world"
# claude --settings ~/.claude-profiles/provider1.json -p "hello world"
```

`ccbunshin launch` returns Claude's exit status, so `ccbunshin launch provider1 ...; echo $?` behaves like running Claude directly.

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

Set a global profile as the fallback for every directory with no marker:

```sh
ccbunshin global provider1
ccbunshin launch           # outside any project: uses provider1
ccbunshin launch global    # same thing, named explicitly
ccbunshin global           # profile: provider1
ccbunshin global --unset
```

The global selection lives in `~/.claude-profiles/global` (or
`$CCBUNSHIN_PROFILES_DIR/global`), so it is one file and no environment
variable to keep exported. Local wins where a marker exists, global applies
everywhere else, exactly like `pyenv local` and `pyenv global`. The name
`global` is reserved in `ccbunshin launch`: it selects the global profile
rather than a profile file named `global.json`.

The lookup is performed by the executable, so this works unchanged from Bash, zsh, tcsh, and other shells.

Change the default model for future sessions:

```sh
ccbunshin model provider1 claude-sonnet-4-5
```

## Project-aware claude

`ccbunshin init` (no args) detects `~/.bashrc`, `~/.zshrc`, `~/.tcshrc`/`~/.cshrc`
and appends the matching wrapper hook to each file that exists, printing
per-file status (`installed:` / `ok: already hooks` / `skip: not found`) to
stdout. It is idempotent: re-running never duplicates a hook. `ccbunshin uninstall`
removes those hooks again, matching the exact line `init` wrote so a hook you edited
by hand is left alone; your profiles are not touched.

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

Outside a project, `claude` uses the global profile when one is set with `ccbunshin global <name>`; with no local marker and no global profile it runs the original Claude Code command with the original arguments. The nearest marker wins, so nested projects work, and the exit status of Claude is returned.

The wrapper reuses the existing `.ccbunshin-profile` marker created by `ccbunshin local <name>`; it introduces no new project marker. Bash and zsh define a `claude` shell function, tcsh an alias, so re-running eval is safe but replaces any `claude` function or alias you defined yourself. The original Claude Code binary remains callable and is what runs outside projects.

Bash and zsh install a `claude` shell function that resolves the provider by calling `ccbunshin resolve-provider`. The function is declared with the `function` keyword and clears any pre-existing `claude` alias first: zsh parses a whole `eval` string before running it, so an alias named `claude` (agent launchers commonly set one) would otherwise turn the definition into a parse error and leave the wrapper uninstalled. tcsh cannot express conditional aliases, so it installs a `claude` alias that delegates to `ccbunshin run`, the internal command that resolves the nearest project provider or falls back to the original `claude` binary. `ccbunshin resolve-provider` and `ccbunshin run` are internal but callable. Re-running eval is idempotent: no nested wrappers or duplicate aliases.

The wrapper delegates project discovery to the ccbunshin binary, so it never parses profile files itself.

## Shell completion

`ccbunshin completion <bash|zsh>` prints a completion script for the CLI, and for zsh
also for the project-aware `claude` wrapper. It is generated by the binary rather than
shipped as a file, so the command surface cannot drift from the CLI.

Nothing is installed for you, and no directory has to be created: write the script to a
file in your home directory and source it from the rc file your shell actually reads.

```sh
ccbunshin completion bash > ~/.ccbunshin-completion.bash
ccbunshin completion zsh  > ~/.ccbunshin-completion.zsh
```

| shell | rc file | note |
| --- | --- | --- |
| bash | `~/.bashrc` | Linux, and macOS when `~/.bash_profile` sources `~/.bashrc` |
| bash | `~/.bash_profile` | macOS default: a terminal bash is a login shell and reads no `~/.bashrc` - a line added there does nothing |
| zsh | `~/.zshrc` | after `compinit`; with oh-my-zsh, which sources `.zshrc` after its own `compinit` |

```sh
# in the rc file for your shell:
source ~/.ccbunshin-completion.bash   # bash
source ~/.ccbunshin-completion.zsh    # zsh
```

Start a new shell, or re-source the rc file, and the completions are live.

### If the shell already has a completions directory

Both shells have directories that load a completion script with no rc line. Use one if you
prefer - just note the two caveats, both of which are why home plus a `source` line is the
default above.

**bash** - the on-demand directory belongs to the separate
[bash-completion](https://github.com/scop/bash-completion) package, which many macOS
installs do not have:

```sh
ccbunshin completion bash > "${BASH_COMPLETION_USER_DIR:-${XDG_DATA_HOME:-$HOME/.local/share}/bash-completion}/completions/ccbunshin.bash"
```

The file must be named `ccbunshin.bash` there (the package loads `<command>.bash` on demand
and ignores other names), and nothing reads the directory until that package has itself been
sourced from an rc file - so this route needs an rc line too, just a different one. System
directories also work where the package is installed system-wide:
`/usr/share/bash-completion/completions` (Debian/Ubuntu),
`/opt/homebrew/share/bash-completion/completions` (macOS with the package), both normally
root-owned.

**zsh** - any directory already on `$fpath` (check with `print -l $fpath`), which typically
includes `/usr/share/zsh/site-functions`, `/opt/homebrew/share/zsh/site-functions` on
Homebrew, and `~/.oh-my-zsh/custom/completions` under oh-my-zsh. Save it there as
`_ccbunshin`. That completes `ccbunshin` but **not** `claude`: `compinit` reads the file's
leading `#compdef` line and never runs the rest, so the wrapper's completion still needs the
`source` line above. Since the `source` line is needed either way, putting the file on `fpath`
only buys a convention - which is why the default is the single dotfile.

Profile names are read from `ccbunshin list` at completion time, so profiles created later complete without regenerating the script.

Verify with `sh tests/completion-test.sh`, which builds the binary and drives both activation paths for both shells. At the prompt: subcommands complete for `ccbunshin`, `init` offers its shells, `proxy` its subcommands, and the profile-taking commands (`launch`, `model`, `status`, `doctor`, `delete`, `local`, `global`) offer the profiles that exist at that moment; inside a project, `claude` completes Claude Code's common options.

## Model routing (optional)

The proxy is optional. Each profile can already set its own `ANTHROPIC_BASE_URL`, so point profiles directly at different endpoints when you can.

Use the proxy when you need a single endpoint. For example, a single lean-ctx proxy instance only has one Anthropic upstream. Point all profiles at the ccbunshin proxy, and it routes each request to a different endpoint based on the model ID and the rules in `proxy.json`.

```text
ccbunshin :3456
├── claude-*              -> provider1
└── qwen/deepseek/gpt-oss -> provider2
```

Start with:

```sh
ccbunshin proxy init    # writes a template config to ~/.config/ccbunshin/proxy.json
ccbunshin proxy start
```

The proxy reference lives in [docs/PROXY.md](docs/PROXY.md): every `proxy.json` key, the route and model-rewrite rules, the lifecycle commands, log levels, the three dialects (`anthropic`, `openai-chat`, `systemone`), `effort`, per-provider credentials, inbound authentication, and `/ccbunshin/resolve`.
## systemd

On Linux, systemd can run the proxy instead of the user-local lifecycle commands:

```sh
sudo install -m 755 ccbunshin /usr/local/bin/ccbunshin
sudo install -m 644 docs/systemd/ccbunshin-proxy.service /etc/systemd/system/ccbunshin-proxy.service
sudo systemctl daemon-reload
sudo systemctl enable --now ccbunshin-proxy
sudo systemctl status ccbunshin-proxy
```

The service reads `/etc/ccbunshin/proxy.json` and starts the same binary in server mode. Use systemd when the proxy should start at boot and restart after a crash. A provider that sets `auth_token_env` or `auth_headers` reads its credential from the daemon's environment, so a systemd deployment wants `Environment=` or `EnvironmentFile=` in the unit; the shipped unit shows where.

## Build and verify

Build for the current machine:

```sh
go -C cmd/ccbunshin build -ldflags "-X main.buildTime=$(date +%Y%m%d-%H%M)" -o ccbunshin
```

A local build reports its own identity as `<short commit>-<build time>`:

```sh
ccbunshin version
# e17b3c5-20260912-1453
```

The commit comes from the Go toolchain's VCS stamping, so it is present
automatically when building from a checkout, shortened to 7 characters; the time
comes from the `-ldflags` stamp above. Omit the stamp for a commit-only identity.
Either part is dropped when unavailable, and a release build still reports its
release tag instead. Release binaries are stamped by the GitHub Actions workflow
via `-X main.version=<tag>`.

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
go -C cmd/ccbunshin test ./...   # includes the log-level and per-request-log tests
sh tests/shell-integration.sh
sh tests/completion-test.sh
sh tests/install-test.sh
```

`main_test.go` covers the log levels directly: `TestParseLogLevel` for
`CCBUNSHIN_LOG`, and `TestLogThresholdFiltersLines`, `TestRequestLogLineNamesModelProviderAndDialect`,
`TestRequestLogLineRedactsCredentials`, `TestPassThroughUpstreamErrorIsLogged`,
and `TestFailedRequestIsLoggedAtErrorLevel` for what each level writes. The
severity split and the diagnostic suffix have their own cases:
`TestClientAbortIsWarnedAndLabelled` and `TestCanceledRequestLogsAsWarnThroughServeHTTP`
for the abort demotion, `TestProviderTimeoutStaysAnError` for the case that must
not be demoted, and `TestDiagnosticSuffixCarriesRequestShape`,
`TestDiagnosticSuffixRedactsCredentials`, and `TestRequestLogLineCarriesRequestID`
for what the failure lines carry. `TestDefaultProviderTimeoutClearsSlowStreams`
pins the default timeout.

Profile shape and write atomicity are covered by `TestProfileRejectsNonObjectTopLevel`
(every entry point that writes or hands over a profile rejects `null`, `[]`, and
scalars), `TestProfileCreateLeavesNoPartialFile` (a rejected create leaves nothing
behind), `TestProfileWriteIsAtomic` (a concurrent reader never observes a partial
file), and `TestListWarnsOnRejectedProfile`. End to end, set
`CCBUNSHIN_PROFILES_DIR` to a scratch directory, write `null` into one profile, and
run `ccbunshin list`, `ccbunshin doctor broken`, and `ccbunshin launch broken`:
each names the file and exits non-zero instead of opening Claude Code's settings
dialog.

The proxy's own checks live in `docs/COMPATIBILITY_REVIEW.md`, which is what to re-check before a release.

## Repository layout

- `cmd/ccbunshin/` contains the Go CLI and model-routed proxy.
- `examples/` contains profile and proxy config examples.
- `docs/` contains the decision log, architecture and deployment notes, the proxy reference
  ([docs/PROXY.md](docs/PROXY.md)), and the two gateway references the proxy is tested against:
  [docs/OPENCODE_ZEN.md](docs/OPENCODE_ZEN.md) and [docs/OPENCODE_GO.md](docs/OPENCODE_GO.md),
  each listing which models need which dialect.

## License

MIT licensed. See [LICENSE](LICENSE).
