# README corrections from a live proxy review

Findings from running the proxy against a 95-model Command Code account
(`api.commandcode.ai`) and reading `cmd/ccbunshin/` at `bdf511c` plus the
systemone dialect from `8befbca`. Each item states what the README claims now, what
the code does, the evidence, and the suggested text.

Line numbers are for the working tree at 708 README lines. They will drift;
grep the quoted phrase instead.

---

## 1. The model-list claim is false (highest priority)

**README:563 says**

> `/model/info` and `/v1/models` are forwarded unchanged.

**What the code does.** `ServeHTTP` has no GET branch. Every request that is not
`/healthz` or `/ccbunshin/resolve` runs `io.ReadAll` then `requestModel(body)`.
A GET has no body, so both paths return `400 request body must be valid JSON`.

`serveModelInfo` appears only inside the design sketch at
`docs/PLAN_OPENAI_CHAT_DIALECT.md:503` and was never implemented. The
pre-translation `ServeHTTP` at `e17b3c5^:119` had the same ordering, so this has
never worked and is not a regression.

**Evidence.** Against a proxy built from the working tree:

```
GET /v1/models?limit=1000      -> 400 request body must be valid JSON
GET /model/info                -> 400 request body must be valid JSON
GET /v1/models                 -> 400 request body must be valid JSON
```

Proxy log line from a real Claude Code start:

```
WARN proxy: GET /v1/models?limit=1000 from 127.0.0.1:50147 request_bytes=0B
  failed: request body must be valid JSON
```

**Why it matters.** This 400 is what a client with gateway model discovery
enabled (`CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY=1`) receives. Claude Code
falls back to its built-in list, shows `[claude-code:unrecognized_model]`, and
then sends the requested ID verbatim anyway, so a custom model id works while
appearing unrecognized. Nothing in the README connects those two facts.

**Suggested replacement for line 563**

```
There is no model-list endpoint. Every route needs a `model` field to resolve,
so `GET /v1/models` and `/model/info` return 400 like any other bodyless
request. A client that probes for a model list (Claude Code does when
`CLAUDE_CODE_ENABLE_GATEWAY_MODEL_DISCOVERY` is set) gets that 400, falls back
to its built-in list, and reports a custom model id as `unrecognized_model`
while still sending it. To see what an id resolves to, use `resolve`.
```

**Test gap.** No test covers `/v1/models` or `/model/info`.
`main_test.go:107` covers `/healthz` only. If the 400 is intended, a test
pinning it would stop a future GET branch from silently changing behavior; if a
model list is wanted, item 1 is the place to design it.

---

## 2. `default_model` silently captures exact-match routes

**README:304 says**

> Optional `default_model` is the rewrite target for any routed model with no
> explicit `models` entry, for a translating dialect only.

Literal and correct. The trap is the implication: an exact-match route

```json
{ "pattern": "stealth/cat-jam:free", "provider": "commandcode" }
```

is still "a routed model with no explicit `models` entry" and is still rewritten.

**What the code does.** `planFor` (`dialect.go:98`) resolves

```
route models -> provider models -> provider default_model -> requested id
```

so any id the map does not name falls to `default_model`. With
`default_model: "deepseek/deepseek-v4.1-flash"`, a newly added route answers as
DeepSeek with no error.

**Evidence.** `/ccbunshin/resolve` on a live proxy:

```
model=claude-opus-5                    -> target deepseek/deepseek-v4.1-flash
model=stealth/cat-jam:free (no map)    -> target deepseek/deepseek-v4.1-flash
model=stealth/cat-jam:free (identity)  -> target stealth/cat-jam:free
```

Proxy log during normal sessions:

```
proxy: claude-opus-5 -> deepseek/deepseek-v4.1-flash (stream) ... status=200
```

**Suggested addition after line 299**

```
The upstream ID is resolved as route `models`, then provider `models`, then
provider `default_model`, then the requested ID unchanged. A route whose
pattern names one exact model still inherits `default_model` unless that same
ID also appears in `models`. To pin a model without rewriting it, add an
identity entry: {"mimo-v2.6-flash": "mimo-v2.6-flash"}.
```

`resolve` already answers this per ID; the cross-reference belongs next to the
key that causes it.

---

## 3. Glob patterns do not cross `/`

**README:299 says**

> `*` matches any run of characters (`"claude-*"` matches `claude-sonnet-4-5`)

The code uses `path.Match` (`main.go:100`, `main.go:169`), which treats `/` as a
separator. Verified:

```
"*"        vs "claude-opus-5"           -> true
"*"        vs "xiaomi/mimo-v2.6-flash"  -> false
"*/*"      vs "xiaomi/mimo-v2.6-flash"  -> true
"xiaomi/*" vs "xiaomi/mimo-v2.6-flash"  -> true
```

Nearly every vendor-prefixed id (`deepseek/*`, `Qwen/*`, `meta/*`) needs
`vendor/*`, not `*`. Conversely a bare `*` does cover every unprefixed id,
including all the Claude ones, which is a useful shortcut nobody can discover
from the current text.

**Suggested replacement for line 299 (first clause only)**

```
A pattern with no wildcard matches one exact model (`"qwen-3.8-27b"`). `*`
matches any run of characters except `/`, so use `xiaomi/*` for a prefixed id
namespace and `*/*` for all prefixed ids; a bare `*` matches only ids without a
slash.
```

---

## 4. "A misspelled dialect key is ignored" (README:430) is ambiguous

**README:430 says**

> A misspelled dialect key is ignored rather than rejected, so the provider
> silently stays on the default. The config format intentionally allows unknown
> keys; check the spelling if a provider seems untranslated.

Two different failures are being described as one.

- A misspelled **key name** (`"dialcts"`) is dropped by `encoding/json`; the
  provider keeps its default. Silent.
- A misspelled **value** (`"dialect": "openai"`) is a startup error:
  `parseDialect` (`dialect.go:49`) returns
  `route "x" dialect must be "anthropic", "openai-chat", or "systemone"`.

As written, a reader can conclude any misspelling is tolerated.

**Suggested replacement**

```
An unknown key name (a misspelled "dialect") is dropped silently and the
provider stays on its default; a bad value for a key that is recognized fails
at startup and names the key. Check the spelling of the key name first when a
provider seems untranslated.
```

---

## 5. Credential forwarding for non-Anthropic dialects is undocumented

**README:494 documents** `auth_token_env` as sending the token in both
`Authorization: Bearer` and `X-Api-Key`. What is missing is the default path:

- `anthropic`: the caller's headers are copied as-is (`main.go:459`).
- `openai-chat` and `systemone`: `applyPassThroughHeaders`
  (`dialect.go:175`) copies everything except Anthropic-only headers, then
  promotes `x-api-key` to `Authorization: Bearer` **and deletes `X-Api-Key`**
  (`dialect.go:199`).

So a gateway that requires `X-Api-Key` specifically will fail unless the
provider sets `auth_token_env`, and `auth_token_env` is the only way to get both
headers. That distinction is exactly what a Command Code style upstream needs
and is currently only inferable from source.

**Suggested addition after the three credential bullets**

```
An `anthropic` provider receives the caller's headers unchanged. The
translating and pass-through dialects strip Anthropic protocol headers, promote
`x-api-key` to `Authorization: Bearer`, and drop `X-Api-Key`, so an upstream
that requires `X-Api-Key` needs `auth_token_env` (which sets both) or
`auth_headers`.
```

---

## 6. Promote `resolve` without `?model=` to its own paragraph

**README:559** mentions in passing:

> Without `?model=` the whole route table is returned, so a client can discover
> the routes without reading the config file.

Verified shape, one entry per route:

```json
{"routes":[{"pattern":"claude-*","provider":"commandcode",
  "dialect":"openai-chat","upstream":"https://api.commandcode.ai/provider/v1",
  "default_model":"deepseek/deepseek-v4.1-flash"}, ...]}
```

`models` is included only when the route or provider has a non-empty map
(`main.go:414`). Given item 1 removed the model-list endpoint, this is the only
introspection surface the proxy has and deserves a named subsection with the
real response shape, not a clause inside a sentence about `?model=`.

---

## Context for the next session

- The systemone dialect committed at `8befbca`: `dialectSystemOne`
  (`dialect.go:23`), `serveSystemOne`, `validateSystemOneModels`, and the
  README section at "### System One decisions". Items above were checked
  against `8befbca`, not against `bdf511c` alone.
- `rewritesModel()` now returns true for `openai-chat` only, so `default_model`
  no longer reaches `anthropic` or `systemone`. Line 304 already says "for a
  translating dialect only"; item 2 is about exact-match routes within that
  dialect, which still inherit it.
- `cmd/ccbunshin/README.md` no longer exists; commit `bdf511c` merged it into
  the root README. Any note calling it a pointer stub is stale.
- Reproduce the model list that items 2 and 3 depend on:

```sh
curl -fsSL https://api.commandcode.ai/provider/v1/models | jq -r '.data[].id'
```

- Reproduce the routing behavior without disturbing the running daemon: run a
  second instance with `CCBUNSHIN_PROXY_CONFIG=<copy>` and a non-default `port`,
  or `go run . --proxy-server` from `cmd/ccbunshin`, since `proxy start` refuses
  when a pid file already exists.
