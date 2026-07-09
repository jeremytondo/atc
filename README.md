# Atelier Code

Atelier Code is a product and development workspace for working with remote
Terminal Sessions: a standalone server that owns Projects and Terminal
Sessions, plus native client apps that attach to it.

This repository is a monorepo holding every Atelier Code surface:

| Directory | Surface |
| --- | --- |
| [`server/`](server/) | Atelier Code Server — the standalone Go service, HTTP/WebSocket API, `atc` CLI, and embedded admin web UI |
| [`macos/`](macos/) | Atelier Code for macOS — the native SwiftUI client |
| `ios/` | Reserved for a future Atelier Code for iOS client |
| [`packages/`](packages/) | Shared cross-surface libraries (currently `AtelierCodeKit`, the Swift API client) |
| [`docs/`](docs/) | Product, architecture, and planning documentation |
| [`scripts/`](scripts/) | Repo-level helper scripts |

## Atelier Code Server

The server is independently installable and useful on its own — no native
client required. It runs on the workstation where your terminal sessions live.

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atelier-code/main/server/install.sh | sh
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

## Atelier Code for macOS

The macOS app connects to one or more Atelier Code Servers, browses their
Projects, and attaches to Terminal Sessions in a native terminal. Open
`macos/AtelierCode.xcodeproj` in Xcode to build and run it.

Each surface builds, tests, and releases independently; see the workflows in
`.github/workflows/`.
