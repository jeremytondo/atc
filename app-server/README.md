# ATC App Server

The TypeScript implementation of the ATC server and `atc` CLI, built on
[Effect](https://effect.website) and [Bun](https://bun.sh).

Bun is pinned in [`mise.toml`](mise.toml) and installed automatically by
[mise](https://mise.jdx.dev). All workflows are mise tasks:

```sh
mise run install       # install dependencies from the committed bun.lock
mise run dev           # run `atc serve` in the foreground (http://127.0.0.1:7332)
mise run test          # run the vitest suite on the Bun runtime
mise run fmt           # format with Prettier
mise run fmt:check     # fail if formatting is needed
mise run typecheck     # strict TypeScript type check
mise run openapi       # regenerate the checked-in OpenAPI artifact (openapi.json)
mise run openapi:check # fail if openapi.json is stale relative to the contract
mise run check         # standing CI gates: fmt:check + typecheck + test + openapi:check
mise run build         # compile the standalone atc executable (dist/atc-<os>-<arch>)
mise run build:all     # cross-compile all four release targets
mise run test:compiled # black-box tests of the compiled artifact (builds first)
mise run test:zmx      # opt-in real-zmx smoke + compiled restart-recovery tests
mise run test:smoke    # opt-in live provider smoke tests (Codex + Claude credentials)
```

## Configuration and data locations

One precedence rule everywhere: **command flags > environment > config file >
defaults**. Invalid or malformed configuration fails fast with one stderr line
naming the offending source — never a partial boot.

Paths follow one XDG rule on every platform (macOS included), honoring `XDG_*`
overrides:

| Location    | Default                     | Holds                                            |
| ----------- | --------------------------- | ------------------------------------------------ |
| Config file | `~/.config/atc/config.toml` | Settings (TOML, camelCase)                       |
| Data dir    | `~/.local/share/atc/`       | SQLite database (`atc.db`)                       |
| State dir   | `~/.local/state/atc/`       | JSON log (`atc.log`), zmx sockets (`terminals/`) |

Environment variables are flat `ATC_<KEY>`: `ATC_PORT`, `ATC_BIND`,
`ATC_LOG_LEVEL`, `ATC_DATA_DIR`, `ATC_ZMX_EXECUTABLE`, `ATC_CODEX_EXECUTABLE`,
`ATC_CLAUDE_EXECUTABLE`, and `ATC_CONFIG` (path to an alternate config file).
The config file may set `port`, `bind`, `logLevel` (case-insensitive),
`dataDir`, `zmxExecutable`, `codexExecutable`, and `claudeExecutable`; unknown
keys are rejected. `atc serve --port`/`--bind` override the configured values
for that server only.

atc bundles no third-party binaries — install them yourself. Terminals
require [zmx](https://github.com/neurosnap/zmx); the agent integrations use
the Codex CLI and Claude Code. Each resolves from its `ATC_*_EXECUTABLE`
variable or config key, else its bare name on PATH.

## CLI

`atc serve` runs the server (it creates and migrates the database on boot).
Almost everything else is a client of the HTTP API, which is the complete
canonical interface: `atc api <method> <path>` (GET, POST, PUT, PATCH, DELETE)
reaches every operation, and curated commands exist only where they add local
behavior (relative-path resolution, TTY attach, `--yes` delete guards). The
exceptions are `atc start`/`stop`/`status` (background process management) and
`atc token` (the remote-access credential is a local `0600` file, not an API
resource). Run `atc --help` for the command surface and
`atc capabilities --json` for the machine-readable summary.

API-backed commands take zero connection flags — `ATC_ENDPOINT` (a full base
URL, set automatically in the environment of ATC-launched terminal sessions)
wins when present; otherwise they derive `http://127.0.0.1:<port>` from the
same settled configuration the server reads, so port changes just work.
Relative directory arguments resolve against the caller's working directory
before the API (which takes absolute paths only) sees them. `atc --version`
reports this executable's own version; `atc version` reports the version of a
running server.

API-backed commands print the JSON payload on stdout and exit `0` (empty
responses print nothing; `atc context` and `atc capabilities` print text by
default and JSON with `--json`). Failures exit `1`: config/request failures
print one `atc <command>: …` diagnostic line on stderr (`atc api` also
relays the server's JSON error body there); invalid usage prints an `ERROR`
block on stderr (help goes to stdout). `atc terminal attach` and
`atc thread open` are the exceptions — they bridge the local TTY onto the
WebSocket attach endpoint in raw mode (detach with `Ctrl-]`).

## API

Public HTTP routes are versioned under `/api/v1`. The contract module
(`src/api/contract.ts`) is the single source of truth; the generated
[`openapi.json`](openapi.json) is the readable inventory of every endpoint and
schema, and a running server serves the identical document at
`GET /openapi.json`. Regenerate it with `mise run openapi` after any contract
change — never edit it by hand. It is symlinked into `packages/ATCKit` to generate the Swift
client, so the server, the TypeScript client (`src/api/client.ts`), and the Swift
client all derive from the same definition.

One trust rule guards every route (API, SSE, the attach WebSocket): a request
passes if it arrives on a loopback connection presenting a recognized loopback
`Host`/`Origin`, or if it carries `Authorization: Bearer <token>` matching the
server's token; everything else is an empty 403. Local loopback clients never
need the token.

### Remote access

The listener binds `127.0.0.1` by default. Setting `bind = "0.0.0.0"` (or
`ATC_BIND` / `--bind`; see the `bind` note in `platform/config.ts` for why a
single non-loopback address is the wrong choice) opens it, and the bearer
token gates every non-loopback request. The intended posture is tailnet-only
reachability (Tailscale) — the token is the just-in-case backstop, not an
invitation to expose the server publicly. The token is generated on first
server start (or by `atc token`) into a `0600` file in the data dir; `atc
token` prints it for pasting into a client, and `atc token rotate` reissues
it, taking effect immediately on a running server. Requests arriving through
a local reverse proxy (`tailscale serve`) also need the token: the proxy
preserves the incoming `Host`, which makes its requests indistinguishable
from DNS rebinding, so only direct loopback traffic is token-free (the trust
module header has the full reasoning). Remote browser access to the Web UI
stays unsupported — browsers cannot attach bearer headers to SSE or
WebSockets.

## Structure

One Bun package, one `package.json`, one committed `bun.lock`. Runtime
dependencies are pinned exactly and kept minimal: `effect` (the application
spine — HTTP, CLI, Schema, concurrency), `@effect/platform-bun` (the Bun
adapter), and `@anthropic-ai/claude-agent-sdk` (the Claude provider seam),
upgraded deliberately, never floated.

Each module in `src/` opens with a header comment describing its
responsibility and invariants — read those rather than a table here. The
repository [`AGENTS.md`](../AGENTS.md) has the conventions and the invariants
that span modules.
