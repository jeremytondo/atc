# ATC

## Overview

ATC brings the tools that make up a coding environment together into a single platform. It tracks and drives conversations with the coding agents on your machine, manages persistent terminals, and organizes both around your projects, all through one stable, secure API.

ATC does not replace these tools. Each keeps owning what it owns: zmx owns terminal sessions, and Claude Code and Codex own conversations. ATC gives those things a stable identity, a normalized state, and relationships to each other, and exposes them to clients that never have to learn any tool's own interface.

## Domains

**Projects.** Organize work around a codebase. A Project has a root directory and gives shared context to the Threads inside it.

**Terminals.** Persistent terminal sessions. Clients create and inspect them, attach for interactive input and output, and detach without stopping the process.

**Threads.** A Thread is one conversation with an Agent, owned by its Provider and tracked by ATC. ATC gives it a stable identity, a normalized status, its latest Turn, and its relationships. Threads are discovered when a provider starts one, or created through ATC when the provider's Integration supports that.

**Environments (future).** Where and under what runtime context work happens: shell, installed software, environment variables. Today ATC uses the user's normal environment on the local machine, and nothing is modeled yet.

## Architecture

**Core.** Defines the domains, their relationships, and their capabilities. Owns ATC identity and state, coordinates Integrations, and serves the API and event stream. Cross-tool workflows go through the domains. Integrations never talk to each other.

**Integrations.** An Integration is ATC's built-in relationship with one external system. It can face either way. Most connect a Provider and implement capabilities for ATC's domains: T3 Code supports Threads with observe and create, Claude Code and Codex support Threads with observe, zmx supports Terminals with drive. Some connect a system that sends work into ATC and receives results, like Linear. Every Integration appears in the catalog with its availability and, where it keeps one, its connection state. An Integration may also expose the Apps it ships and the Agents it runs.

**API.** One secure API and event stream, the same for local and remote clients. Clients can discover which Integrations are present, whether each is available, and what it supports.

**Clients.** Anything that uses the API from outside ATC: the CLI, a desktop app, or an automation on another machine. Clients get no special access.

## Glossary

Glossary of terms can be found in GLOSSARY.md

## Webhook ingress

ATC can receive webhooks from external systems on an always-on machine
without a separate receiver or tunnel. Set `webhooks = true` in
`config.toml` (or pass `--webhooks` to `atc server start`, `restart`, or
`run`) and the server runs a restricted receiver process behind
[Tailscale Funnel](https://tailscale.com/docs/features/tailscale-funnel) on
the machine's existing Tailscale identity, publicly, on `webhooks_port`
(443 by default; 8443 and 10000 are the other Funnel ports). This is
independent of `tailscale = true`, which exposes the private API on the
tailnet only.

The receiver is a child of the server sandboxed with Linux Landlock (ABI 4,
kernel 6.7 or newer): it cannot read files, bind ports, or connect anywhere
but the server's loopback delivery channel, and it proves each of those
restrictions before anything is exposed. Every request it relays is treated
as untrusted; the Integration owning the route verifies it inside the
server, and only an authorized delivery is stored, acknowledged, and
processed. Exposure lives exactly as long as the server process, including
when it is killed outright. Unsupported platforms and kernels leave only
webhook intake unavailable, with the reason in `atc server status` and
`GET /v1/webhooks`.

Enabling Funnel is a one-time tailnet policy step; when it is needed, the
server keeps running and status shows the approval link. The public
hostname becomes part of a public certificate log, as with any Funnel.

`--tailscale` and `--webhooks` follow one contract: a flag applies to the
launch it starts, `restart` keeps the running launch's flags unless
replaced, `stop` then `start` returns to `config.toml`, and `=false`
disables for that launch. Flags never modify `config.toml`. See
`atc server start --help`.

## Installing and upgrading

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/install.sh | sh
```

Installs the latest release into `~/.local/bin` (override with
`ATC_INSTALL_DIR`, select a tag with `ATC_VERSION`). To install the rolling
development build directly on a new machine:

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/install.sh | env ATC_VERSION=dev sh
```

Supported platforms: macOS arm64, Linux amd64/arm64. After the first install
the binary keeps itself current:

- `atc upgrade` — move to the latest production release
- `atc upgrade --dev` — install the current rolling dev build

Releases are cut by the [Release workflow](.github/workflows/release.yml):
`mise run release:patch|minor|major|dev`, `gh workflow run release.yml`, or
the Actions "Run workflow" button.

## Building and testing

Tools and tasks are managed by [mise](https://mise.jdx.dev) via
[`mise.toml`](mise.toml); `mise install` provisions the toolchain.

- `mise run build` — build a static `atc` binary into `bin/`
- `mise run check` — build, lint, vet, and test (CI runs the same task)
- `mise run refs` — fetch read-only T3 Code, Herdr, Agent Client Protocol,
  and zmx v0.6.0 source into `repos/`
- `mise tasks` — list all tasks

## Repository status

- The complete previous product is preserved at
  [`legacy-product-2026-08`](https://github.com/jeremytondo/atc/tree/legacy-product-2026-08).
  The prior TypeScript App Server, macOS app, shared packages, release
  tooling, and CI were removed so their assumptions do not constrain the Go
  rebuild.
- [`experiments/`](experiments/) contains research prototypes and findings
  that may inform the rebuild. They are evidence, not production code or
  settled architecture.

GitHub releases `v0.0.x` and older are artifacts of the archived product;
the rebuild's releases start at `v0.1.0`.
