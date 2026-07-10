# atc

atc is a product and development workspace for working with remote
Terminal Sessions: a standalone server that owns Projects and Terminal
Sessions, plus native client apps that attach to it.

This repository is a monorepo holding every atc surface:

| Directory | Surface |
| --- | --- |
| [`server/`](server/) | atc server — the standalone Go service, HTTP/WebSocket API, `atc` CLI, and embedded admin web UI |
| [`macos/`](macos/) | atc for macOS — the native SwiftUI client |
| `ios/` | Reserved for a future atc for iOS client |
| [`packages/`](packages/) | Shared cross-surface libraries (currently `ATCKit`, the Swift API client) |
| [`docs/`](docs/) | Product, architecture, and planning documentation |
| [`scripts/`](scripts/) | Repo-level helper scripts |

## atc server

The server is independently installable and useful on its own — no native
client required. It runs on the workstation where your terminal sessions live.

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/server/install.sh | sh
```

`atc` is the server's command line interface:

```sh
atc start
atc status
atc projects list
atc sessions start --project <id>
atc sessions attach <id>
```

See [`server/README.md`](server/README.md) for full setup, development, and
API documentation.

## atc for macOS

The macOS app connects to one or more atc servers, browses their
Projects, and attaches to Terminal Sessions in a native terminal. Open
`macos/atc.xcodeproj` in Xcode to build and run it.

## Development

Tasks are run with [mise](https://mise.jdx.dev). From the repo root:

```sh
mise run check   # every gate: gofmt, go vet, Go tests, web type check,
                 # web tests, ATCKit tests, macOS app tests
mise run test    # all test suites
```

Per-surface tasks (`server:build`, `web:check`, `kit:test`, `macos:test`, …)
are listed by `mise tasks`; `server/mise.toml` has the server's own dev/build
tasks. CI (`.github/workflows/`) runs the same mise tasks on every push, so a
green local `check` means a green build.

The HTTP API's wire shapes are pinned by shared fixtures in
[`packages/contracts/`](packages/contracts/) that the Go server, Swift
client, and web client all test against. Platform and security decisions
(macOS floor, sandbox/ATS stance, token storage) live in
[`docs/platform-policy.md`](docs/platform-policy.md).

Each surface builds, tests, and releases independently; see the workflows in
`.github/workflows/`.
