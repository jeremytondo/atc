# ATC App Server

The TypeScript implementation of the ATC server and `atc` CLI, built on
[Effect](https://effect.website) and [Bun](https://bun.sh). It lives alongside
the existing Go server in [`../server/`](../server/) until cutover; the Go
server remains the installed product today.

Bun is pinned in [`mise.toml`](mise.toml) and installed automatically by
[mise](https://mise.jdx.dev). All workflows are mise tasks:

```sh
mise run install    # install dependencies from the committed bun.lock
mise run dev        # run `atc serve` in the foreground (http://127.0.0.1:7332)
mise run test       # run the vitest suite on the Bun runtime
mise run fmt        # format with Prettier
mise run fmt:check  # fail if formatting is needed
mise run typecheck  # strict TypeScript type check
mise run check      # everything CI runs: fmt:check + typecheck + test
```

The dev port 7332 is a temporary development default (the Go server uses
7331); production listener behavior is settled in later work.

## Structure

One Bun package, one `package.json`, one committed `bun.lock`. Runtime
dependencies are pinned exactly and kept minimal: `effect` (the application
spine — HTTP, CLI, Schema, concurrency) and `@effect/platform-bun` (the Bun
adapter). Effect is on the `4.0.0-beta.x` line, upgraded deliberately, never
floated. `mise run refs` (repo root) checks out the matching Effect source for
API reference.

| Path               | Responsibility                                                    |
| ------------------ | ----------------------------------------------------------------- |
| `src/main.ts`      | Entrypoint for the `atc` executable; the only `runMain`           |
| `src/cli.ts`       | CLI commands and flags (Effect CLI); friendly startup failures    |
| `src/api.ts`       | The `/api/v1` HttpApi contract: endpoints and response schemas    |
| `src/handlers.ts`  | Contract implementation (handler Layer, no listener)              |
| `src/server.ts`    | Server assembly: routes + loopback Bun listener Layer             |
| `src/buildInfo.ts` | Build metadata service (version, commit, builtAt)                 |
| `test/`            | `@effect/vitest` tests, including a black-box serve/shutdown test |

The contract module is structured so the server implementation, an OpenAPI
document, and generated typed clients all derive from the same `HttpApi`
definition; OpenAPI serving and clients are follow-up work.

Public HTTP routes are versioned under `/api/v1`:

- `GET /api/v1/health` → `{ "status": "ok" }`
- `GET /api/v1/version` → application version, `apiVersion`, and build metadata
