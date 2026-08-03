# atc

atc is a product and development workspace for working with remote Terminal
Sessions: a standalone server that owns Projects and Terminal Sessions, plus
native client apps that attach to it.

The server is useful on its own — no native client required. It runs on the
workstation where your terminal sessions live.

Two directories hold servers, which their names do not make obvious:
[`app-server/`](app-server/) is the active implementation (TypeScript, Effect +
Bun) and owns the `atc` CLI, while [`server/`](server/) is the legacy Go server
being migrated away from.

## Development

Each surface builds, tests, and releases independently; see
[`.github/workflows/`](.github/workflows/).

Tasks run with [mise](https://mise.jdx.dev), which also installs the pinned
toolchains. `mise tasks` lists everything; from the repo root:

```sh
mise run check          # every gate: Go server, web, App Server, ATCKit, macOS app
mise run test           # every test suite
mise run refs           # fetch read-only reference source into repos/ (gitignored)
```

Working on one surface? Run only its gate:

```sh
mise run app-server:check   # fmt, typecheck, tests, OpenAPI drift
mise run server:check       # gofmt, go vet, Go tests, web type check
mise run kit:test           # ATCKit package tests
mise run macos:test         # macOS app tests
mise run contract:check     # after any API contract change
```

Tasks scoped to a surface can also be run from its own directory with
`mise run -C <dir> <task>` (for example `mise run -C app-server dev`).

CI runs these same mise tasks on every push, so a green local `check` means a
green build.

## Where to look next

- [`AGENTS.md`](AGENTS.md) — working conventions, code style, and the
  invariants that hold across surfaces. Read this before changing code.
- [`app-server/README.md`](app-server/README.md) — server and CLI setup,
  configuration, and data locations.
- [`macos/README.md`](macos/README.md) — macOS app configuration.
- [`server/README.md`](server/README.md) — the legacy Go server.

Subsystem architecture is documented in module header comments, next to the
code it describes, rather than in this file.
