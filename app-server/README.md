# ATC App Server

The TypeScript implementation of the ATC server and `atc` CLI, built on
[Bun](https://bun.sh). It lives alongside the existing Go server in
[`../server/`](../server/) until cutover; the Go server remains the installed
product today.

Bun is pinned in [`mise.toml`](mise.toml) and installed automatically by
[mise](https://mise.jdx.dev). All workflows are mise tasks:

```sh
mise run install    # install dependencies from the committed bun.lock
mise run dev        # run `atc serve` in the foreground (http://127.0.0.1:7332)
mise run test       # run the Bun test suite
mise run fmt        # format with Prettier
mise run fmt:check  # fail if formatting is needed
mise run typecheck  # strict TypeScript type check
mise run check      # everything CI runs: fmt:check + typecheck + test
```

The dev port 7332 is a temporary development default (the Go server uses
7331); production listener behavior is settled in later work.

## Structure

One Bun package, one `package.json`, one committed `bun.lock`. Runtime
dependencies are pinned exactly and kept minimal: Hono (HTTP) and Commander
(CLI parsing).

| Path                      | Responsibility                                                        |
| ------------------------- | --------------------------------------------------------------------- |
| `src/main.ts`             | Source entrypoint for the `atc` executable                            |
| `src/cli/program.ts`      | CLI command registration and parsing (Commander)                      |
| `src/server/serve.ts`     | Foreground serve lifecycle: bind, wait for SIGINT/SIGTERM, shut down  |
| `src/server/lifecycle.ts` | Listener start/stop and shutdown signals (`Bun.serve`)                |
| `src/server/app.ts`       | HTTP application factory (constructible without a listener)           |
| `src/server/routes.ts`    | Public `/api/v1` routes and their typed responses                     |
| `src/buildInfo.ts`        | Version and build metadata provider                                   |
| `tests/`                  | Bun tests, including a black-box test of the real entrypoint over TCP |

Public HTTP routes are versioned under `/api/v1`:

- `GET /api/v1/health` → `{ "status": "ok" }`
- `GET /api/v1/version` → application version, `apiVersion`, and build metadata
