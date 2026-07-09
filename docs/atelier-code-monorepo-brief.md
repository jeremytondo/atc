# Atelier Code Monorepo Brief

Status: Draft

## Purpose

Atelier Code should become a single product and development workspace for the
server, macOS app, and future client apps built around remote Terminal Sessions.
The monorepo should make cross-cutting product work easier while keeping the
server independently installable and useful for developers who want to build on
its API and CLI.

## Idea Definition

Atelier Code is the overall project. The existing Cockpit server becomes
Atelier Code Server, and the existing macOS app becomes the Atelier Code macOS
client. Future clients, such as iOS, mobile, or web surfaces, should live in the
same repository and share server contracts, client libraries, fixtures, and
documentation where useful.

The server remains a standalone runtime. It owns Project and Terminal Session
records, exposes the HTTP and WebSocket APIs, ships the CLI, and can still be
installed without installing any native client app.

## Recommended Direction

Move toward a product/platform monorepo with top-level runtime directories
rather than hiding each surface under an `apps/` folder. This keeps the
polyglot shape obvious and avoids unnecessary nesting around large standalone
components.

Recommended repository shape:

```text
atelier-code/
  server/
  macos/
  ios/
  packages/
  docs/
  scripts/
```

Use `server/`, `macos/`, and `ios/` as repo-local names. Use product language
in public-facing docs and UI:

- Atelier Code
- Atelier Code Server
- Atelier Code for macOS
- Atelier Code for iOS

Retire the Cockpit name from product language, documentation, new code, and
new install paths. Treat the rename as a clean break rather than a compatibility
migration: old `cockpit` paths, config files, sockets, database files,
environment variables, install scripts, and binary names do not need to keep
working.

Use `atc` as the primary command name. It is faster to type, avoids collision
with an existing `atelier` CLI, and follows the same practical pattern as the
GitHub CLI using `gh`.

Preferred command shape:

```sh
atc start
atc status
atc stop
atc health
atc projects list
atc sessions start --project <id>
atc sessions attach <id>
```

Do not add an extra `server` namespace to normal service commands. Since the
CLI ships with and administers Atelier Code Server, `atc start` is clearer than
`atc server start`.

Keep the Go module path short as `github.com/jeremytondo/atelier-code`. The
repository can still keep the server source under `server/`; the module name
should reflect the umbrella project rather than a nested server-only identity.

## Key Features

- One repository for server, macOS client, future clients, shared packages,
  docs, and scripts.
- Top-level `server/` directory for the standalone Go service, API, embedded
  admin web UI, and `atc` CLI.
- Top-level `macos/` directory for the native SwiftUI app.
- Future top-level client directories such as `ios/` when those surfaces become
  real.
- Shared `packages/` directory for API clients, schemas, fixtures, generated
  contracts, or other cross-surface libraries.
- Independent build, test, and release workflows per major surface.
- Server install path that does not require the macOS app.
- Clear removal of Cockpit naming from new product surfaces.

## Non-Goals / Deferred Ideas

- Do not make the server a private implementation detail of the macOS app.
- Do not require native clients to be installed before using the server CLI or
  API.
- Do not introduce an `apps/` wrapper unless the repo later has enough small
  app surfaces to justify it.
- Do not rename API resource concepts such as Projects or Terminal Sessions as
  part of the monorepo move unless a separate product-language decision calls
  for it.
- Defer a broader package manager or build orchestration decision until the
  repo shape and CI pain are concrete.
- Do not preserve backward compatibility for old `cockpit` config, binary,
  database, socket, install script, or environment-variable names.
- Defer generated API schemas or contract tooling until after the migration and
  until multiple clients make the need concrete.

## System Shape

- **`server/`**: Owns the standalone service, API, CLI, daemon lifecycle,
  Project and Terminal Session persistence, and server-side admin web UI.
- **`macos/`**: Owns the native macOS client experience, settings, Connections,
  project-first navigation, terminal attachment, and app-specific UI policy.
- **`ios/`**: Reserved for a future iOS client when mobile work begins.
- **`packages/`**: Owns shared API clients, schemas, generated models, fixtures,
  and contract tests that keep clients aligned with the server.
- **`docs/`**: Owns product, architecture, ADR, setup, migration, and release
  documentation for the whole Atelier Code project.
- **`scripts/`**: Owns repo-level helper scripts that coordinate multiple
  surfaces without replacing each surface's native tooling.

## Core Concepts

- **Atelier Code**: The umbrella product and repository.
- **Atelier Code Server**: The standalone server/API/CLI runtime currently
  known as Cockpit.
- **Server Admin UI**: The web UI bundled with Atelier Code Server for
  administration and control-panel workflows, distinct from future full client
  apps.
- **`atc`**: The primary command-line interface for administering Atelier Code
  Server and working with Projects and Terminal Sessions.
- **Atelier Code for macOS**: The native macOS app surface currently known as
  AtelierCode.
- **Project**: A server-owned working area that groups related Terminal
  Sessions.
- **Terminal Session**: A server-owned ZMX-backed terminal process.
- **Connection**: A macOS-app-local relationship to one Atelier Code Server.

## Confirmed Decisions

- Move toward a monorepo for Atelier Code, Atelier Code Server, and future
  client apps.
- Use top-level directories such as `server/`, `macos/`, and `ios/` rather than
  `apps/server`, `apps/macos`, and `apps/ios`.
- Remove Cockpit as the long-term product and component name.
- Treat the rename as a clean break with no backward compatibility requirement
  for old Cockpit paths, environment variables, config files, sockets,
  databases, binaries, or install scripts.
- Keep the server standalone even after it moves into the monorepo.
- Treat the CLI as part of the server.
- Prefer `atc` as the CLI command name instead of `atelier` because it is faster
  to type and avoids confusion with an existing `atelier` CLI.
- Prefer commands like `atc start` over `atc server start`.
- Use `github.com/jeremytondo/atelier-code` as the Go module path.
- Keep the existing embedded web UI inside `server/` as a server admin control
  panel, not as a top-level client app.
- Let the migration PR breakdown be decided pragmatically during
  implementation rather than fixed in the brief.
