# ccbunshin

**ccbunshin** (分身, *bunshin*, "shadow clone") runs several Claude Code profiles side by side. Each profile has its own settings, while Claude Code state stays shared.

The whole program is one dependency-free Go binary in `cmd/ccbunshin`. It manages profiles, launches Claude Code, and runs an optional model-routed proxy.

- [Install](#install)
- [Profiles](#profiles)
- [Commands](#commands)
- [Project-aware claude](#project-aware-claude)
- [Shell completion](#shell-completion)
- [Model routing (optional)](#model-routing-optional)
  - [Proxy lifecycle](#proxy-lifecycle)
  - [Log severity](#log-severity)
  - [Translating providers](#translating-providers)
  - [Provider credentials](#provider-credentials)
  - [Resolving a model ID](#resolving-a-model-id)
  - [Different mappings per profile](#different-mappings-per-profile)
- [systemd](#systemd)
- [Build and verify](#build-and-verify)
- [Repository layout](#repository-layout)
- [License](#license)

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

This runs Claude Code with the profile settings file:

```sh
claude --settings ~/.claude-profiles/provider1.json
```

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

Routes select the provider; `models` rewrites the model ID after routing.

A pattern with no wildcard matches one exact model (`"qwen-3.8-27b"`). `*` matches any run of characters except `/`, so use `xiaomi/*` for a prefixed ID namespace and `*/*` for every prefixed ID; a bare `*` matches only IDs without a slash. Routes are checked in order; the first match wins.

The upstream ID resolves in this order: the route's `models`, the provider's `models`, the provider's `default_model` (a translating dialect only), then the requested ID unchanged. An exact-match route still inherits `default_model` unless that same ID appears in a `models` map, so a route can be answered by a model its pattern does not name. To pin an ID without rewriting it, give it an identity entry: `{"mimo-v2.6-flash": "mimo-v2.6-flash"}`. `resolve` reports what any ID becomes.

`proxy.json` keys:

- `port` (required, 1-65535): the port the proxy listens on.
- `providers` (required, at least one): each provider needs an `upstream` absolute URL. Optional `timeout` is a Go duration string (default `5m`); optional `models` maps a requested model ID to the ID sent upstream. Optional `default_model` is the rewrite target for any routed model with no explicit `models` entry, for a translating dialect only. Optional `effort` is the `reasoning_effort` applied when the caller states none - see below. Optional `auth_token_env`, `auth_headers`, and `no_auth` give a provider its own credential - see [Provider credentials](#provider-credentials). Optional `dialect` selects the wire format: `anthropic` (the default) forwards the request unchanged; `openai-chat` translates an Anthropic request onto an OpenAI-compatible `/chat/completions` call, and translates the response back; `systemone` forwards a System One decision request unchanged onto `/systemone`. Those are the only three values. A bad value is rejected at startup, so `proxy start` fails and names the key rather than forwarding requests the wrong way.
- `routes` (required): ordered list of `{pattern, provider}`. `pattern` matches the request model: no wildcard means one exact model, `*` matches any run of characters (for example `"claude-*"`). `provider` must name a provider above. Duplicate exact patterns are rejected. A route may also carry `models` (overriding the provider's map), `dialect` (overriding the provider's), `model_dialects` (a `{target model: dialect}` map overriding both, keyed on the ID sent upstream), and `effort` (overriding the provider's).

The proxy listens on the configured port, reads the request model, applies the ordered glob routes (first match wins), resolves the target model and dialect, and forwards the request. It returns HTTP 400 when no route matches. `/healthz` returns HTTP 200 without contacting an upstream.

`dialect` says which wire format a route's upstream speaks, and there are three:

- `"anthropic"` - the default, and what you get with no `dialect` key at all. The request goes upstream byte-for-byte, so nothing is translated and nothing can be lost in translation. Use it for anything that already accepts `/v1/messages`, including a real Anthropic endpoint, a gateway in front of one, or another Anthropic-shaped proxy.
- `"openai-chat"` - for a gateway that speaks OpenAI's `/chat/completions` instead. The proxy then converts `system`, content blocks, `tools`, and `tool_choice` to their chat-completions equivalents; returns upstream `reasoning` as Anthropic `thinking` blocks; answers `/v1/messages/count_tokens` locally with an estimate; reports upstream prompt-cache hits as `cache_read_input_tokens` with the hit count subtracted out of `input_tokens`; and rewraps upstream errors in Anthropic's error envelope. `reasoning_effort` is never derived from `thinking` - that translation is what several such gateways reject - but a caller that states an effort of its own gets it forwarded. See [Translating providers](#translating-providers) and [Effort](#effort).
- `"systemone"` - for a System One decision endpoint, where a model answers typed questions with probabilities instead of text. The caller's body is forwarded unchanged to the provider upstream plus `/systemone`; there is nothing to translate, because the request is not a chat message. The caller's path and query are replaced, Anthropic protocol headers are dropped, and the response comes back as it arrived, errors included. Jev does not stream and only answers for `typesafe/*` model IDs, which `proxy start` checks. See [System One decisions](#system-one-decisions).

Any other value is an error, not a silent fallback: `proxy start` refuses the config and names the offending key. Set `dialect` on a provider, on a route (overriding the provider), or per target model in a route's `model_dialects`.

See `examples/proxy.json` for the full example the `init` template is based on, and `docs/OPENCODE_ZEN.md` and `docs/OPENCODE_GO.md` for which models the two gateways the proxy is tested against need which dialect.

### Proxy lifecycle

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

### Log severity

`CCBUNSHIN_LOG` sets the threshold for `proxy.log`, lowest to highest:
`debug`, `info` (the default), `warn`, `error`. A line is written when its own
level is at or above the threshold, so `error` leaves only failures. Any other
value is an error: a typo fails `proxy start` instead of silently logging
nothing.

Each line carries its level after the timestamp:

```text
2026/09/12 23:25:40 INFO proxy: claude-opus-5 -> oss-model (buffered) req=a1b2c3d4 provider=provider2 dialect=openai-chat status=200 bytes=271 elapsed=1ms
2026/09/12 23:25:40 WARN proxy: POST /v1/messages from 127.0.0.1:62779 request_bytes=42B failed: no provider route for model nope (configured patterns: claude-*)
```

- **info** (default): one line per request with the requested and target model, a short per-request tag (`req=`), provider, dialect, status, response bytes, and elapsed time, plus one line at startup and one at shutdown.
- **warn**: requests the proxy itself rejects (no route, unreadable body), reasoning dropped mid-stream, and a request the calling client aborted. A cancellation is the client's decision rather than a failure of the proxy or the upstream, so it is reported here with a `[client aborted: ...]` note instead of at `error`; the provider's own timeout expiring still counts as an error.
- **error**: upstream failures (unreachable, non-2xx, untranslatable response), a stream cut short after its headers were already sent, and any request whose response was 5xx.
- **debug**: the routing decision and the upstream URL for each request, translated request size, and upstream usage per streamed message, including the cache hit count (`cached=`); this is the level that shows whether prompt caching is hitting. It is also the only level that writes a request or response body, and only when a translated request is rejected.

Every `warn` and `error` line ends with a bracketed diagnostic suffix naming the
provider, dialect, `buffered`/`stream` shape, request size, and upstream URL:

```text
2026/09/14 10:45:20 ERROR proxy: claude-opus-5 -> oss-model req=a1b2c3d4 stream failed after 1m0.005s with 1662212 bytes already delivered to the client: context deadline exceeded [provider=commandcode dialect=openai-chat shape=stream request=48731B upstream=https://api.example.invalid/provider/v1]
```

A stream failure happens after the status is already committed, so the `bytes already
delivered` count is the only way to tell a stream that died at once from one that died
after a megabyte. Credentials in the upstream URL are redacted in that suffix.

At `debug`, an upstream that rejects a translated request also dumps what was sent and
what came back, which is what makes a wrong dialect mapping diagnosable:

```text
DEBUG proxy: claude-opus-5 -> oss-model req=a1b2c3d4 request_body={...}
DEBUG proxy: claude-opus-5 -> oss-model req=a1b2c3d4 upstream_response status=400 headers=map[...] body={...}
```

Those two lines carry the conversation, so they are `debug` only and never written at
the default level.

Credentials are never logged: URLs in the log are redacted, and no request or
response body is written below `debug`.

To check the log end to end against a real request, start the proxy with
`CCBUNSHIN_LOG=debug` and read `~/.config/ccbunshin/proxy.log`.

### Translating providers

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
- An unknown key name (a misspelled `"dialcts"`) is dropped silently by the JSON decoder, so the provider stays on its default. A bad value for a recognized key (`"dialect": "openai"`) fails startup and names the key, so check the key name's spelling first when a provider seems untranslated.

To find out which endpoint a gateway expects for a given model, POST a one-token request to its `/messages` path and read which endpoint the error names. Model lists often do not say, and an ID's prefix is not a reliable signal.

#### Effort

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

### System One decisions

TypeSafe's System One models, Jev among them, answer typed questions with probabilities instead of text. The request is a shared `state` plus a `questions` object, and the answer is keyed by the same question names. It is not a chat message, so no dialect can translate a Claude Code turn into it: a caller has to send the System One body itself. What this dialect buys is everything around the body - the route table, the provider's own credential, and the per-request log line.

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

The caller's own path is replaced by `/systemone`, so the path above is a placeholder and works as well as any other. The server path in `upstream` is kept, at whatever depth the gateway publishes it.

`systemone` requests reach the same route table as everything else, and the model ID is passed through unchanged unless a `models` entry maps it, which rewrites the body's `model`. `default_model` does not apply: it exists to give a translated chat upstream an ID it accepts, and a decision caller already sends a decision model. The `effort` key does not apply either.

Jev never streams, and the proxy does not add a stream: a `stream` field in the body is the upstream's to interpret. Upstream errors are passed back as they arrived, at the upstream's status.

### Provider credentials

A profile carries one credential, so a session launched from it sends the same one everywhere. That is wrong as soon as the route table spans endpoints that authenticate differently, and no client-side setting can fix it: `apiKeyHelper` takes no arguments and is cached for five minutes, and `ANTHROPIC_CUSTOM_HEADERS` is fixed for the process, so neither can vary per model. The proxy can, because it resolves the provider per request.

By default, authentication is forwarded and never configured: an inbound `Authorization: Bearer` is passed through, and an inbound `x-api-key` is promoted to `Authorization: Bearer`. Three optional provider keys override that, describing one credential each:

- `auth_token_env` - the name of an environment variable holding this provider's token. It is sent in both `Authorization: Bearer` and `X-Api-Key`, since one of the two is what the upstream reads and the pair covers either.
- `auth_headers` - a `{header name: environment variable}` map, for a gateway that needs a second credential in a header of its own naming, or a value that does not belong in either standard header. It sets exactly the headers it names.
- `no_auth` - send no credential at all, for a local server that needs none.

The default forwarding path differs by dialect. An `anthropic` provider receives the caller's headers unchanged. Every other dialect, `openai-chat` and `systemone` included, strips the Anthropic protocol headers, promotes `x-api-key` to `Authorization: Bearer`, and drops `X-Api-Key`. An upstream that requires `X-Api-Key` specifically therefore needs `auth_token_env`, which sets both headers, or an `auth_headers` entry naming it.

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

### Resolving a model ID

`GET /ccbunshin/resolve?model=<id>` reports what a request model routes to, answered from the route table:

```sh
curl -s 'http://127.0.0.1:3456/ccbunshin/resolve?model=claude-opus-5'
# {"dialect":"openai-chat","requested":"claude-opus-5","target":"oss-model"}
```

`target` is the ID sent upstream, provider prefix included. An unrouted model returns 404 with `{"error": "no route matches this model"}`.

Without `?model=`, the whole route table is returned - the only way to see the routing rules without reading the config file, and the closest thing the proxy has to introspection:

```sh
curl -s 'http://127.0.0.1:3456/ccbunshin/resolve'
# {"routes":[{"pattern":"claude-*","provider":"gateway","dialect":"openai-chat","upstream":"https://api.example.invalid/provider/v1","default_model":"oss-model"}, ...]}
```

Each entry carries `pattern`, `provider`, `dialect`, and `upstream`, plus `default_model` when the provider sets one and `models` when the route or provider has a non-empty map. `dialect` here is the route's configured dialect, since without a model there is no target ID for a `model_dialects` entry to key on.

This endpoint exists because a client cannot otherwise learn what its model ID became. A model list cannot answer it: a route is a glob (`claude-*`) with a `default_model`, so the set of matching models is not enumerable. `resolve` answers the specific question for the specific ID. A statusline uses it to show the real model name behind a Claude Code alias.

There is no model-list endpoint. Every route resolves on the request body's `model` field, so `GET /v1/models` and `/model/info` return `400 request body must be valid JSON` like any other bodyless request. A client that probes for a model list - Claude Code does when `CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` is set - receives that 400, falls back to its built-in list, and reports a custom model ID as `unrecognized_model` while still sending it. The ID works; only its label is unrecognized. Use `resolve` to see what an ID becomes.

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
- `docs/` contains the decision log, architecture and deployment notes, and the two gateway
  references the proxy is tested against: [docs/OPENCODE_ZEN.md](docs/OPENCODE_ZEN.md) and
  [docs/OPENCODE_GO.md](docs/OPENCODE_GO.md), each listing which models need which dialect.

## License

MIT licensed. See [LICENSE](LICENSE).
