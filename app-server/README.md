# ATC App Server

The ATC server and `atc` CLI, built on [Effect](https://effect.website) and
[Bun](https://bun.sh).

Installing a release needs no toolchain — see [Install](../README.md#install)
at the repo root. Everything below is for working on the server itself.

Bun is pinned in [`mise.toml`](mise.toml) and installed automatically by
[mise](https://mise.jdx.dev). All workflows are mise tasks:

```sh
mise run deps          # install dependencies from the committed bun.lock
mise run dev           # run `atc serve` in the foreground (http://127.0.0.1:7331)
mise run test          # run the vitest suite on the Bun runtime
mise run fmt           # format with Prettier
mise run fmt:check     # fail if formatting is needed
mise run typecheck     # strict TypeScript type check
mise run openapi       # regenerate the checked-in OpenAPI artifact (openapi.json)
mise run openapi:check # fail if openapi.json is stale relative to the contract
mise run check         # standing CI gates: fmt:check + typecheck + test + openapi:check + web:assets:check
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

| Location    | Default                     | Holds                                                                 |
| ----------- | --------------------------- | --------------------------------------------------------------------- |
| Config file | `~/.config/atc/config.toml` | Settings (TOML, camelCase)                                            |
| Data dir    | `~/.local/share/atc/`       | SQLite database (`atc.db`), prompt images (`attachments/<threadId>/`) |
| State dir   | `~/.local/state/atc/`       | JSON log (`atc.log`), zmx sockets (`terminals/`)                      |

Environment variables are flat `ATC_<KEY>`: `ATC_PORT`, `ATC_BIND`,
`ATC_TAILSCALE`, `ATC_LOG_LEVEL`, `ATC_DATA_DIR`, `ATC_ZMX_EXECUTABLE`,
`ATC_TAILSCALE_EXECUTABLE`, `ATC_CODEX_EXECUTABLE`, `ATC_CLAUDE_EXECUTABLE`, and
`ATC_CONFIG` (path to an alternate config file). The config file may set
`port`, `bind`, `tailscale`, `logLevel` (case-insensitive), `dataDir`,
`zmxExecutable`, `tailscaleExecutable`, `codexExecutable`, and
`claudeExecutable`; unknown keys are rejected. `atc serve --port`/`--bind`/
`--tailscale` override the configured values for that server only.

Upgrading from the retired Go server: its data is not migrated and nothing
deletes it automatically. Remove the leftovers by hand if you had one —
`~/.local/state/atc/atc.db`, `~/.config/atc/server/`, and the socket
directory (`$XDG_RUNTIME_DIR/atc`, or `$TMPDIR/atc` on macOS — do not remove
`~/.local/state/atc/` wholesale; the App Server keeps its log there).

atc bundles no third-party binaries — install them yourself. Terminals require
[zmx](https://github.com/neurosnap/zmx), integrated tailnet exposure requires
Tailscale, and the agent integrations use the Codex CLI and Claude Code. Each
resolves from its `ATC_*_EXECUTABLE` variable or config key, else its bare name
on PATH.

## CLI

`atc serve` runs the server (it creates and migrates the database on boot).
Almost everything else is a client of the HTTP API, which is the complete
canonical interface: `atc api <method> <path>` (GET, POST, PUT, PATCH, DELETE)
reaches every operation, and curated commands exist only where they add local
behavior (relative-path resolution, TTY attach, `--yes` delete guards). The
exceptions are `atc start`/`stop`/`status` (self-managed background process),
`atc service` (user-scope launchd/systemd units for running the server as a
login service — separate from `start`, which supervises nothing), and
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
passes if it is verified local or integrated-Tailscale traffic, or if it
carries `Authorization: Bearer <token>` matching the server's token;
everything else is an empty 403. Local loopback clients never need the token.

### Remote access

The listener binds `127.0.0.1` by default. Set `tailscale = true`
(`ATC_TAILSCALE` / `--tailscale`) to keep it loopback-only while ATC owns a
foreground Tailscale Serve route on the same port. Verified tailnet requests
are token-free, including the Web UI; tailnet reachability and Tailscale ACLs
are the authorization boundary, and the route disappears with the server.

For other remote-access arrangements, setting `bind = "0.0.0.0"` (`ATC_BIND` /
`--bind`) opens the listener and the bearer token gates every non-loopback
request. The token is generated on first server start (or by `atc token`) into
a `0600` file in the data dir; `atc token` prints it and `atc token rotate`
reissues it live. The trust module header documents both paths.

## Admin UI

A running server serves a small read-only console at its base URL
(`http://127.0.0.1:7331/`): a health/build overview at `/`, and the full API
reference (Scalar over `openapi.json`) at `/docs`. It is an admin surface for
observing the server and reading the docs, not an API client. Like every route
it sits behind the trust guard: it is available directly on loopback and
through a verified integrated Tailscale exposure. Other remote browser paths
cannot attach the bearer header and answer 403.

The UI is a prerendered SvelteKit app in [`web/`](web). Its build output
(`web/build/`) and the embed manifest that compiles it into the executable are
committed, so building or running the server never needs the web toolchain.
After changing `web/src`, run:

```sh
mise run web:dev          # UI dev server with /api proxied to a running App Server
mise run web:check        # svelte-check over the UI source
mise run web:build        # rebuild web/build and the embed manifest — commit the result
```

## Structure

One Bun package, one `package.json`, one committed `bun.lock` — plus the
deliberately separate admin UI toolchain under `web/` (its own lockfile, only
needed when changing the UI). Runtime
dependencies are pinned exactly and kept minimal: `effect` (the application
spine — HTTP, CLI, Schema, concurrency), `@effect/platform-bun` (the Bun
adapter), and `@anthropic-ai/claude-agent-sdk` (the Claude provider seam),
upgraded deliberately, never floated.

Each module in `src/` opens with a header comment describing its
responsibility and invariants — read those rather than a table here. The
repository [`AGENTS.md`](../AGENTS.md) has the conventions and the invariants
that span modules.
