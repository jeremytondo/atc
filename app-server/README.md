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
mise run test:smoke    # opt-in live provider smoke tests (Codex + Claude credentials)
```

## Configuration and data locations

One precedence rule everywhere: **command flags > environment > config file >
defaults**. Invalid or malformed configuration fails fast with one stderr line
naming the offending source — never a partial boot.

Paths follow one XDG rule on every platform (macOS included), honoring `XDG_*`
overrides:

| Location    | Default                     | Holds                      |
| ----------- | --------------------------- | -------------------------- |
| Config file | `~/.config/atc/config.toml` | Settings (TOML, camelCase) |
| Data dir    | `~/.local/share/atc/`       | SQLite database (`atc.db`) |
| State dir   | `~/.local/state/atc/`       | JSON log file (`atc.log`)  |

Environment variables are flat `ATC_<KEY>`: `ATC_PORT`, `ATC_LOG_LEVEL`,
`ATC_DATA_DIR`, and `ATC_CONFIG` (path to an alternate config file). The
config file may set `port`, `logLevel` (case-insensitive), and `dataDir`;
unknown keys are rejected. `atc serve --port` overrides the configured port
for that server only.

## CLI

`atc serve` runs the server (it creates and migrates the database on boot).
API-backed commands take zero connection flags — they derive
`http://127.0.0.1:<port>` from the same settled configuration the server
reads, so port changes just work:

```sh
atc health                                    # { "status": "ok" }
atc version                                   # build metadata + apiVersion
atc project create --name Demo --directory .  # directory must exist; stored canonicalized
atc project list
atc project get <project-id>
atc project update <project-id> --name Renamed
atc project delete <project-id> --yes         # record only; never touches the filesystem
atc fs check <path>                           # tagged directory health, never persisted
```

`atc --version` reports this executable's own version; `atc version` reports
the version of a running server. Relative directory arguments resolve against
the caller's working directory before the API (which takes absolute paths
only) sees them.

Success prints the JSON payload on stdout and exits `0`. Failures exit `1`:
config/request failures print one `atc <command>: …` diagnostic line on
stderr; invalid usage prints an `ERROR` block on stderr (help goes to
stdout). The command surface is curated for agent and script access to the
app's functionality — it does not mirror the API operation-for-operation, but
API-backed commands always go through the contract-derived client.

## Structure

One Bun package, one `package.json`, one committed `bun.lock`. Runtime
dependencies are pinned exactly and kept minimal: `effect` (the application
spine — HTTP, CLI, Schema, concurrency), `@effect/platform-bun` (the Bun
adapter), and `@anthropic-ai/claude-agent-sdk` (the Claude provider seam),
upgraded deliberately, never floated. Provider executables are not shipped:
the user's installed `codex` and `claude` are resolved from an env override
or PATH. `mise run refs` (repo root) checks out the matching Effect source
for API reference.

| Path                       | Responsibility                                                                                                        |
| -------------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `src/main.ts`              | Entrypoint for the `atc` executable; the only `runMain`                                                               |
| `src/cli.ts`               | CLI commands and flags (Effect CLI); base-URL resolution seam; friendly startup failures                              |
| `src/config.ts`            | `AppConfig` service: settled paths + settings, precedence pipeline, TOML parsing                                      |
| `src/api.ts`               | The `/api/v1` HttpApi contract: endpoints, schemas, and tagged error classes                                          |
| `src/openapi.ts`           | The OpenAPI document derived from the contract (pure, no server)                                                      |
| `src/client.ts`            | Contract-derived typed client (`HttpApiClient`, no server imports)                                                    |
| `src/handlers.ts`          | Contract implementation (handler Layer, no listener)                                                                  |
| `src/server.ts`            | Server assembly: guarded routes + tracer + loopback Bun listener Layer                                                |
| `src/localTrust.ts`        | Listener hardening: loopback `Host`/`Origin` validation, log correlation                                              |
| `src/persistence.ts`       | SQLite `SqlClient` layer: settled location, documented pragmas, startup migrations                                    |
| `src/migrations.ts`        | The checked-in, append-only migration record (compiled into the binary)                                               |
| `src/projectRepository.ts` | Projects repository: the only SQL for projects; row types stay here                                                   |
| `src/directories.ts`       | Demand-driven directory validation/health with a bounded timeout                                                      |
| `src/logging.ts`           | Server logging: JSON file in the state dir, pretty stderr in dev, level from config                                   |
| `src/buildInfo.ts`         | Build metadata service (version, commit, builtAt)                                                                     |
| `src/subprocess.ts`        | Subprocess service: scoped child processes, bounded diagnostics                                                       |
| `src/smoke.ts`             | Hidden, unstable `atc smoke` provider round trips (ATC-88)                                                            |
| `scripts/build.ts`         | Standalone-executable compile (Bun `--compile`, metadata injection)                                                   |
| `scripts/openapi.ts`       | Writes/checks the checked-in `openapi.json` artifact                                                                  |
| `openapi.json`             | Generated OpenAPI 3.1 document — regenerate, never edit; symlinked into `packages/ATCKit` for Swift client generation |
| `test/`                    | `@effect/vitest` tests, including black-box and opt-in live suites                                                    |

The contract module is structured so the server implementation, the checked-in
OpenAPI document (`openapi.json`), the contract-derived TypeScript client
(`src/client.ts`), and the generated Swift client (`ATCAppServerAPI` in
`packages/ATCKit`) all derive from the same `HttpApi` definition. See "OpenAPI
Contract" and "Clients and the CLI" in the repository `AGENTS.md` for the
conventions; OpenAPI serving over HTTP is follow-up work.

Public HTTP routes are versioned under `/api/v1`:

- `GET /api/v1/health` → `{ "status": "ok" }`
- `GET /api/v1/version` → application version, `apiVersion`, and build metadata
- `GET/POST /api/v1/projects`, `GET/PATCH/DELETE /api/v1/projects/{projectId}` → Projects CRUD
- `GET /api/v1/fs/check?path=…` → demand-driven directory health (tagged states, bounded timeout)

The loopback listener validates `Host`/`Origin` on every request (403
otherwise) — the local half of the settled trust architecture; bearer-token
remote access is a later, purely additive layer.
