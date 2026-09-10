# ATC

## Overview

ATC brings the tools that make up a coding environment together into a single platform. It starts and tracks conversations with the coding agents on your machine, manages persistent terminals, and organizes both around your projects, all through one stable, secure API.

ATC does not replace these tools. Each keeps owning what it owns: zmx owns terminal sessions, and Claude Code and Codex own conversations. ATC gives those things a stable identity, a normalized state, and relationships to each other, and exposes them to clients that never have to learn any tool's own interface.

## Domains

**Projects.** Organize work around a codebase. A Project has a root directory and gives shared context to the Threads inside it.

**Terminals.** Persistent terminal sessions. Clients create and inspect them, attach for interactive input and output, and detach without stopping the process.

**Threads.** A Thread is one conversation with an Agent, owned by its Provider and tracked by ATC. ATC gives it a stable identity, a normalized status, its latest Turn and that Turn's final response, and its relationships. Threads are discovered when a provider starts one, or created through ATC when the provider's Integration supports that. Everything else a person does with a conversation — answering, approving, stopping — happens in the Provider's own program.

**Artifacts.** Published documents: interactive explanations, comparisons, reports, and demonstrations that agents author on a shared React platform and publish through the API. An Artifact keeps a history of immutable Versions, each a complete static build plus its source, behind one stable link and a permanent link per Version. They are historical snapshots, organized by Project when useful, and served to browsers by the document origin.

**Environments (future).** Where and under what runtime context work happens: shell, installed software, environment variables. Today ATC uses the user's normal environment on the local machine, and nothing is modeled yet.

## Architecture

**Core.** Defines the domains, their relationships, and their capabilities. Owns ATC identity and state, coordinates Integrations, and serves the API and event stream. Cross-tool workflows go through the domains. Integrations never talk to each other.

**Integrations.** An Integration is ATC's built-in relationship with one external system. It can face either way. Most connect a Provider and implement capabilities for ATC's domains: T3 Code supports Threads with observe and create, Claude Code and Codex support Threads with observe, zmx supports Terminals with drive. Some connect a system that sends work into ATC and receives results, like Linear. Every Integration appears in the catalog with its availability and, where it keeps one, its connection state. An Integration may also expose the Apps it ships and the Agents it runs.

**API.** One secure API and event stream, the same for local and remote clients. Clients can discover which Integrations are present, whether each is available, and what it supports.

**Clients.** Anything that uses the API from outside ATC: the CLI, a desktop app, or an automation on another machine. Clients get no special access.

**Document origin.** The one deliberate exception to "everything behind the API": a second listener that serves published Artifacts to browsers with no credential, on its own port and therefore its own browser origin, so a page it serves carries no credential and cannot read the API as its reader. It serves only published content and the read-only metadata the reader header needs; publishing, source retrieval, and every mutation stay on the API.

## Glossary

Glossary of terms can be found in GLOSSARY.md

## Webhook ingress

ATC can receive webhooks from external systems on an always-on machine
without a separate receiver or tunnel. Set `webhooks = true` in
`config.toml` (or pass `--webhooks` to `atc server start`, `restart`, or
`run`) and the server runs a restricted receiver process behind
[Tailscale Funnel](https://tailscale.com/docs/features/tailscale-funnel) on
the machine's existing Tailscale identity, publicly, on `webhooks_port`
(443 by default). This is independent of `tailscale = true`, which exposes
the private API on the tailnet only.

The receiver is a child of the server confined with Linux Landlock and
seccomp; it proves it can reach nothing but the server's delivery channel
before anything is exposed, and exposure ends with the server process
however it ends. Every delivery is verified inside the server by the
Integration that owns its route; only authorized deliveries are stored,
acknowledged, and processed. Enabling Funnel is a one-time tailnet policy
step: the server keeps running and `atc server status` (or
`GET /v1/webhooks`) shows the approval link, the endpoint's readiness, and
the inbox. The public hostname becomes part of a public certificate log,
as with any Funnel. Unsupported platforms and kernels leave only webhook
intake unavailable, with the reason in status.

`atc server start --help` documents how `--tailscale` and `--webhooks`
behave across start, restart, and stop.

Terminals outlive the server. On Linux each terminal session runs in its
own systemd scope, outside the server's control group, so `atc server
stop`, `restart`, `uninstall`, an upgrade, or a crash leaves every
session running, and the next start finds each one under its original
identity. Deleting a terminal ends everything its session started,
background processes included. Surviving logout needs the systemd user
manager to keep running (`atc server start` enables lingering for this);
a reboot ends live processes and nothing restores them. Where systemd is
absent, sessions launch directly and persist only as far as the platform's
own session handling allows. ATC-issued start, stop, restart, and
uninstall operations are recorded in `lifecycle.log` under the state
directory.

The Linear Integration receives its deliveries here: an `@atc` mention on
an issue starts one T3 Code conversation and posts the answer back. Setup
is manual; see
[`internal/integrations/linear/README.md`](internal/integrations/linear/README.md).

## Publishing documents

Agents author Artifacts with the [`atc-artifact` skill](skills/atc-artifact/SKILL.md)
(install it through your skill manager; ATC does not distribute it) and the
`atc artifact` commands: `new` creates a working copy on the shared platform,
`check`, `build`, and `preview` work on it locally, and `publish` uploads the
build and its source as a new Version. The first authoring use installs a
private Node runtime and the platform's dependencies under
`~/.local/share/atc/authoring`; an ATC upgrade refreshes them on the next
use. Working copies persist there too, outside any repository, until
`atc artifact discard`.

Readers open the links `publish` prints. The document origin listens on
`documents_port` (7332 by default; it must differ from `port`) at the same
bind address as the API, and follows `tailscale = true` onto the tailnet.
`atc server status` reports it; a port conflict leaves the rest of ATC
running, with the reason in status, and publishing keeps working. Published
content lives under `~/.local/share/atc/artifacts`, one directory per
Artifact and Version; the metadata is in the database. `atc artifact --help`
lists the management commands (`versions`, `restore`, `update` for the
title and Project, `source`, `delete`).

## Using the picker

Bare `atc` opens the picker against the local server, starting it if it is
stopped: choose a Space, then a Terminal, and attach (ctrl-\ detaches back
to the picker). `atc --remote <target>` opens the same picker against the
machine an ordinary ssh target names. The remote's server is started if
needed and must expose the API on the tailnet (`tailscale = true` in its
`config.toml`); control traffic uses that HTTPS endpoint, and ssh carries
only the launch-time bootstrap and the interactive attach. Nothing is
cached locally. Terminal rows are numbered per space and labelled by the
program in their foreground (`1:zsh`, `2:nvim`) or by a name you set
(`3:api`); press a row's number to attach. `atc --help` and `?` inside the
picker list the keys.

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

To publish a dev build from a pushed branch without merging it:

```sh
mise run release:dev --ref <branch>
```

Omitting `--ref` builds from the repository's default branch. Each dev
publication replaces the shared rolling dev build.

## Building and testing

Tools and tasks are managed by [mise](https://mise.jdx.dev) via
[`mise.toml`](mise.toml); `mise install` provisions the toolchain.

- `mise run build` — build a static `atc` binary into `bin/`
- `mise run check` — build, lint (including vet), check sqlc output, build
  the artifact platform, and test (CI runs the same task)
- `mise run platform:check` — type-check and build the artifact platform and
  its examples with the pinned Node
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
