# atc

atc is a product and development workspace for working with remote Terminal
Sessions: a standalone server that owns Projects and Terminal Sessions, plus
native client apps that attach to it.

The server is useful on its own — no native client required. It runs on the
workstation where your terminal sessions live.

The server and `atc` CLI live in [`app-server/`](app-server/) (TypeScript,
Effect + Bun).

## Install

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/install.sh | sh
```

This downloads the latest stable GitHub Release for your platform, verifies
its checksum, and installs `atc` to `~/.local/bin` (`ATC_INSTALL_DIR`
overrides). Append `-s -- --channel dev` to ride the dev channel — a rolling
prerelease rebuilt from every merge to `main`. Run `atc service install` to
start it as a login service, or `atc serve` for the foreground. A running
server serves its console at `http://127.0.0.1:7331/` and the API reference
at `/docs`. To update, run `atc upgrade` — it reinstalls from the binary's
own channel and restarts the service.

## Development

Each surface builds, tests, and releases independently; see
[`.github/workflows/`](.github/workflows/).

Tasks run with [mise](https://mise.jdx.dev), which also installs the pinned
toolchains. `mise tasks` lists everything; from the repo root:

```sh
mise run dev            # run the App Server in the foreground (http://127.0.0.1:7331)
mise run install        # build the App Server and install it as ~/.local/bin/atc
mise run check          # every gate: App Server, ATCKit, macOS app
mise run test           # every test suite
mise run refs           # fetch read-only reference source into repos/ (gitignored)
```

Working on one surface? Run only its gate:

```sh
mise run app-server:check   # fmt, typecheck, tests, OpenAPI drift
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

Subsystem architecture is documented in module header comments, next to the
code it describes, rather than in this file.
