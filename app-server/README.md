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

The server listens on port 7332 by default; pass `--port` to override.

## CLI

`atc serve` runs the server. API-backed commands use the contract-derived
client and an explicit `--url` (connection profiles are later work):

```sh
atc health --url http://127.0.0.1:7332    # prints the JSON payload, e.g. { "status": "ok" }
atc version --url http://127.0.0.1:7332   # prints build metadata + apiVersion as JSON
```

`atc --version` reports this executable's own version; `atc version --url …`
reports the version of a running server.

Success prints the JSON payload on stdout and exits `0`. Failures exit `1`:
request failures print one `atc <command>: …` diagnostic line on stderr;
invalid usage prints an `ERROR` block on stderr (help goes to stdout). Every
public API operation maps to exactly one CLI command via the parity registry
(`src/parity.ts`), enforced against the contract by `test/parity.test.ts`.

## Structure

One Bun package, one `package.json`, one committed `bun.lock`. Runtime
dependencies are pinned exactly and kept minimal: `effect` (the application
spine — HTTP, CLI, Schema, concurrency), `@effect/platform-bun` (the Bun
adapter), and `@anthropic-ai/claude-agent-sdk` (the Claude provider seam),
upgraded deliberately, never floated. Provider executables are not shipped:
the user's installed `codex` and `claude` are resolved from an env override
or PATH. `mise run refs` (repo root) checks out the matching Effect source
for API reference.

| Path                 | Responsibility                                                                                                        |
| -------------------- | --------------------------------------------------------------------------------------------------------------------- |
| `src/main.ts`        | Entrypoint for the `atc` executable; the only `runMain`                                                               |
| `src/cli.ts`         | CLI commands and flags (Effect CLI); friendly startup failures                                                        |
| `src/api.ts`         | The `/api/v1` HttpApi contract: endpoints and response schemas                                                        |
| `src/openapi.ts`     | The OpenAPI document derived from the contract (pure, no server)                                                      |
| `src/client.ts`      | Contract-derived typed client (`HttpApiClient`, no server imports)                                                    |
| `src/parity.ts`      | API-to-CLI parity registry and its contract validation                                                                |
| `src/handlers.ts`    | Contract implementation (handler Layer, no listener)                                                                  |
| `src/server.ts`      | Server assembly: routes + loopback Bun listener Layer                                                                 |
| `src/buildInfo.ts`   | Build metadata service (version, commit, builtAt)                                                                     |
| `src/subprocess.ts`  | Subprocess service: scoped child processes, bounded diagnostics                                                       |
| `src/smoke.ts`       | Hidden, unstable `atc smoke` provider round trips (ATC-88)                                                            |
| `scripts/build.ts`   | Standalone-executable compile (Bun `--compile`, metadata injection)                                                   |
| `scripts/openapi.ts` | Writes/checks the checked-in `openapi.json` artifact                                                                  |
| `openapi.json`       | Generated OpenAPI 3.1 document — regenerate, never edit; symlinked into `packages/ATCKit` for Swift client generation |
| `test/`              | `@effect/vitest` tests, including black-box and opt-in live suites                                                    |

The contract module is structured so the server implementation, the checked-in
OpenAPI document (`openapi.json`), the contract-derived TypeScript client
(`src/client.ts`), and the generated Swift client (`ATCAppServerAPI` in
`packages/ATCKit`) all derive from the same `HttpApi` definition. See "OpenAPI
Contract" and "Clients and CLI Parity" in the repository `AGENTS.md` for the
conventions; OpenAPI serving over HTTP is follow-up work.

Public HTTP routes are versioned under `/api/v1`:

- `GET /api/v1/health` → `{ "status": "ok" }`
- `GET /api/v1/version` → application version, `apiVersion`, and build metadata
