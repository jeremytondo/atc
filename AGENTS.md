# Atelier Code Agent Instructions

## Repository Layout

This is the Atelier Code monorepo. Every product surface lives here:

- `server/` — Atelier Code Server: the standalone Go service, HTTP/WebSocket
  API, `atc` CLI, and embedded admin web UI. Has its own toolchain (`mise`),
  tests (`mise run test`), and release pipeline.
- `macos/` — Atelier Code for macOS: the native SwiftUI client
  (`AtelierCode.xcodeproj`).
- `packages/` — shared cross-surface libraries. `AtelierCodeKit` is the Swift
  API client used by the macOS app (and future iOS app).
- `docs/` — product and architecture documentation for the whole project.
- `scripts/` — repo-level helper scripts.

## Source Control

Jujutsu (jj) Protocol: You are in a jj repository; strictly do not use git
add/commit/stash/checkout. When a logical step passes tests, checkpoint your
work by running `jj describe -m "<msg>"` followed by `jj new`. If you write
code that breaks the build, immediately run `jj undo` to revert before trying
again. To push a branch, use a jj bookmark and push it to the git remote. To
create a PR push a branch and then create a PR in GitHub. Follow JJ best
practices.

## Atelier Code Server

The macOS app is driven by Atelier Code Server, whose source lives in
`server/`. A running instance is installed on the remote workstation defined
in `~/.ssh/config`; you can ssh into that machine to control it if needed.
It is reachable via the tailscale address:

http://100.91.7.102:7331/

## Code Style

- Always strive for simplicity. This is not a complex enterprise app.
- Code readability is critical. Code should be easily understandable by
  developers coming into the project.
