# Pinning ANTHROPIC_BASE_URL against cc-switch rewrites

> Date: 2026-09-05
> Problem: cc-switch keeps rewriting `ANTHROPIC_BASE_URL` in `~/.claude/settings.json` to its
> own proxy, but the intended chain is `Claude Code -> lean-ctx proxy -> cc-switch proxy ->
> provider`. The base URL must stay pinned to lean-ctx; cc-switch's switch action must not
> change it.

---

## 1. Why `export ANTHROPIC_BASE_URL` does not work

Claude Code settings precedence (highest first):

1. Managed settings
2. **Command line (`claude --settings`)**
3. Project local (`.claude/settings.local.json`)
4. Shared project (`.claude/settings.json`)
5. **User (`~/.claude/settings.json`)**

The `env` block in `~/.claude/settings.json` (user scope) **overrides process environment
variables**. That is why cc-switch can rewrite the base URL and why exporting the variable in
the shell does not stick.

The fix is to set the value at a scope above user settings: the `--settings` flag.

## 2. How `--settings` behaves

Per the official docs:

> "Claude Code merges JSON you pass with `--settings` with your settings files by the same
> rules as the other levels: it takes a key you set here over the same key in local, project,
> or user settings, and keeps the lower-level value for a key you omit."

So a `--settings` file that only defines `env` overrides the base URL / token, while hooks,
permissions, plugins, and statusLine from `~/.claude/settings.json` still apply.

## 3. The fix: pin the base URL with `claude --settings`

### 3.1 Create the pin file

`~/.claude/lean-ctx-settings.json`:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "http://127.0.0.1:<lean-ctx-port>",
    "ANTHROPIC_AUTH_TOKEN": "<token>"
  }
}
```

### 3.2 Wrap `claude` in the shell rc

Add to `~/.bashrc` (or `~/.zshrc`):

```bash
claude() {
  command claude --settings "$HOME/.claude/lean-ctx-settings.json" "$@"
}
```

### 3.3 Result

cc-switch can keep rewriting `~/.claude/settings.json` - every `claude` process launched
through the wrapper uses the `--settings` env block instead:

```text
Claude Code -> lean-ctx proxy -> cc-switch proxy -> provider
```

Provider switching still happens in the cc-switch GUI (it changes where cc-switch's proxy
routes); the base URL never changes.

## 4. Caveats

- **Token.** If the auth token cc-switch's proxy expects is provider-specific (changes per
  switch), a single static pin file is not enough - use per-provider settings files instead.
  This is what `luckybilly/cc-switch-helper` (`ccs`) and `guyskk/claude-code-config-switcher`
  (`ccc`) automate: they regenerate `--settings` per provider. If the token is fixed (usual
  when a proxy sits in front), the static file is sufficient.
- **CLI only.** `--settings` applies to the `claude` command. IDE/desktop launches do not get
  it. Fine for a terminal workflow on the Linux VM.
- **Wrapper flag conflicts.** The shell function intercepts all `claude` invocations. If a
  command passes its own `--settings`, the function would pass two. Rename the function
  (e.g. `claude-cc`) or handle it explicitly if this matters.

## 5. Alternatives

- **Configure cc-switch providers to use lean-ctx directly.** Edit each provider's
  `settings_config` inside cc-switch so `env.ANTHROPIC_BASE_URL` points at lean-ctx. If
  cc-switch's relay mode force-injects its own address, this will not stick; the `--settings`
  pin is robust against that.
- **`CLAUDE_CONFIG_DIR` profile.** Point `CLAUDE_CONFIG_DIR` at a separate dir whose
  `settings.json` pins lean-ctx, and symlink the state subdirs back to `~/.claude`. Heavier:
  `CLAUDE_CONFIG_DIR` relocates settings, session history, plugins, and `.claude.json`
  together (all-or-nothing), so shared-state symlinks are required.

## 6. Source evidence

| Claim | Location |
|---|---|
| Settings precedence: `--settings` (Command line) above User | code.claude.com/docs/en/settings |
| `--settings` merges per-key; keeps lower-level value for omitted keys | code.claude.com/docs/en/settings |
| `env` block follows the same precedence levels | code.claude.com/docs/en/settings |
| `CLAUDE_CONFIG_DIR` relocates the whole `~/.claude` tree | code.claude.com/docs/en/settings, /docs/en/claude-directory |
| `claude --settings` accepts a file path or JSON string | github.com/luckybilly/cc-switch-helper (`src/launcher.js`) |