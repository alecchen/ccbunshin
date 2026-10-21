# ccbunshin verification map

This directory is the maintained source for verifying the user-facing behavior
of ccbunshin. Read the index before driving the app, then use the matching
feature file as the recipe.

## Baseline preconditions

- Build the real binary into `$VERIFY_DIR/bin` and put only that directory on
  `PATH` (see the skill's Launch section).
- Export `HOME=$VERIFY_DIR/home`, so the proxy's pid and log do not touch the
  real home directory.
- Export `CCBUNSHIN_PROFILES_DIR=$VERIFY_DIR/profiles`.
- Use ports 18456 (proxy) and 18457 (fake upstream) unless they are taken.
- Run `ccbunshin version` and require a `<short commit>-<build time>` identity.
- Never drive an instance that was not started by this verification run.

## Driving conventions

- Start every recipe from the baseline state unless its preconditions say otherwise.
- Treat every command as literal. Keep quoted names and flags unchanged.
- Run CLI actions through the sandbox `ccbunshin` shell function.
- Run wrapper actions through an `eval "$(ccbunshin init <shell>)"` in a
  subshell, with a recording `claude` stub first on `PATH`.
- Run proxy actions through `curl` against `127.0.0.1:$VERIFY_PROXY_PORT`.
- Restore scratch state after a mutation. Do not remove proof artifacts during cleanup.

## Proof and skip reporting

- Capture the user action and the resulting state, not only the final output.
- CLI proof includes the command, stdout, stderr, and exit code.
- Proxy proof includes `/ccbunshin/resolve`, the client response, the fake
  upstream's `upstream.log`, and the relevant `proxy.log` lines.
- Profile proof includes the file path and its mode.
- Record the feature ID and entry point used with every artifact.
- Report an unreachable path with the attempted command and the unmet precondition.
- Do not report a skipped entry point as verified through a different path.

## Feature entry contract

Each feature file starts with an H1 title and one paragraph describing the
user-visible behavior. It then uses exactly four H2 sections in this order.

1. `Sub-features` lists short IDs with one line for each behavior.
2. `How to get to it (user POV)` lists every user entry point.
3. `Driving it with the sandbox` starts with `Preconditions:` and uses labeled
   bullets that pair each user action with an exact command and observable result.
4. `Gotchas` lists traps that can waste or invalidate a verification run.

Keep implementation details out of the map. Name only user paths, stable
handles, required state, commands, and observable proof.

## Features

- [Profiles](./profiles.md) covers create, list, status, doctor, delete, and the
  rejected-shape failure path.
- [Project and global selection](./selection.md) covers the
  `.ccbunshin-profile` marker, the global fallback, and their precedence.
- [Launch and the claude wrapper](./wrapper.md) covers `launch`, argv
  forwarding, exit status, and the project-aware `claude` in bash, zsh, and tcsh.
- [Model-routed proxy](./proxy.md) covers `proxy init/start/status/restart/stop`,
  the route table, `resolve`, `/healthz`, and openai-chat translation.
- [Shell completion](./completion.md) covers the generated scripts and the
  profile names they read at completion time.
