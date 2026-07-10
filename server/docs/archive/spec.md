# atc Framework Specification

Status: Framework slice complete.

> **Archived:** This framework spec records the initial application skeleton.
> Current product work is specified in `docs/specs/`.
>
> **Scope note:** This document specifies the initial hello-world *framework*
> slice, which is now built. Its non-goals (no product features, no agent
> orchestration, no persistence) describe that slice — they are **not** ongoing
> prohibitions on the product. Product features are specified per-feature (see
> Linear issues and `docs/adr/`). Treat this document as the historical record
> of the application skeleton, not as the governing scope for product work.

Purpose: Define the initial atc application framework: a Go service, Cobra CLI, basic HTTP API, embedded SvelteKit web app, and mise development/build tasks.

## Normative Language

The key words `MUST`, `MUST NOT`, `REQUIRED`, `SHOULD`, `SHOULD NOT`, `RECOMMENDED`, `MAY`, and `OPTIONAL` in this document are to be interpreted as described in RFC 2119.

`Implementation-defined` means the behavior must be selected and documented by the implementation. Once documented, the selected behavior is part of the implementation contract.

## Terminology

- atc service: the long-running atc process that owns the local control surface and runs the HTTP server.
- Server: the HTTP serving implementation inside the service, including routing, TCP listener setup, Unix socket listener setup, and listener-aware middleware.
- Foreground mode: `atc serve`, which runs the service attached to the current terminal until interrupted.
- Background mode: `atc start`, which launches the same service detached from the current terminal and manages it with local lifecycle files. This may also be described as daemon mode.
- Service control directory: the per-user directory containing local service coordination files such as `atc.sock` and `atc.pid`.

## 1. Problem Statement

atc is intended to become an ideation, planning, workflow, and AI coding orchestration platform. The initial implementation exists only to establish the application skeleton that later product behavior can safely build on.

The project exists to:

- Provide a single local Go service as the source of truth for future atc behavior.
- Provide a CLI command surface for service lifecycle and API-backed local automation.
- Provide a browser-based web UI that can run during development and ship inside the Go binary.
- Establish repeatable development and build commands through mise.

The project does not attempt to solve:

- Project, Item, Environment, workflow, agent orchestration, or code review functionality.
- Database schema, persistence, migrations, or data import/export.
- iOS application behavior.
- Production-grade TCP authentication, service installation, auto-start, or release automation.

## 2. Goals and Non-Goals

### 2.1 Goals

- The implementation MUST create a working Go application at the repository root.
- The implementation MUST provide a Cobra-based CLI.
- The implementation MUST provide service lifecycle commands: `serve`, `start`, `stop`, and `status`.
- The implementation MUST provide at least one API-backed CLI command: `health`.
- The implementation MUST provide a minimal HTTP API with health and version endpoints.
- The implementation MUST provide a SvelteKit web app that can run locally for development.
- The implementation MUST package the built web app into the Go binary with `go:embed`.
- The implementation MUST provide mise tasks for common development and build workflows.

### 2.2 Non-Goals

- The implementation MUST NOT add a database.
- The implementation MUST NOT implement real atc domain behavior beyond framework diagnostics.
- The implementation MUST NOT implement Project, Item, Environment, workflow, agent, or review models.
- The implementation MUST NOT install OS services, launch agents, systemd units, or auto-start hooks.
- The implementation MUST NOT expose a CLI `version` command until version metadata is backed by CI/CD or another intentional release process.
- The implementation MUST NOT require TCP token authentication in this framework slice.

## 3. Initial Project Boundary

This specification defines the hello-world framework for atc.

Target users:

- A local developer running atc on a workstation or remote development host.
- AI agents or scripts that need a stable local CLI/API surface.

Primary workflows:

- Start the service in the foreground for development.
- Start and stop the service in the background for local use.
- Check service process status.
- Check service API health through the CLI.
- Open or remotely test the web UI over an explicitly configured TCP bind.
- Build one self-contained Go binary containing the web app.

Supported platforms or environments:

- Local Unix-like development environments.
- Remote Linux hosts accessed over SSH.
- Browser access from another device on the user's tailnet when explicitly configured.

Initial deployment shape:

- A local CLI plus service shipped as one Go binary.
- A SvelteKit frontend source tree built separately and embedded into the Go binary.

Explicit exclusions:

- Windows-specific background-mode lifecycle behavior.
- iOS client implementation.
- Tailscale integration beyond allowing an operator-selected non-loopback bind address.
- Public internet exposure.
- Multi-user access control.

## 4. Implementation Profile

This section is normative. Agent-generated implementation work MUST follow this profile unless this specification is amended.

### 4.1 Technical Context

- Language/version: latest patch release in the Go 1.26.x line.
- CLI framework: Cobra.
- Backend dependencies: Go standard library SHOULD be preferred; Cobra is an approved production dependency for the CLI.
- Frontend framework: SvelteKit.
- Frontend package manager: pnpm.
- Frontend output: static build compatible with embedding in the Go binary.
- Storage: none.
- Testing: `go test ./...` for Go packages; frontend checks are implementation-defined but MUST be wired into mise if present.
- Target platform: local Unix-like development environments, with Linux as the primary remote-host target.
- Project type: local service, CLI, HTTP API, and embedded web app.
- Performance goals: none beyond fast local startup and responsive health checks.
- Constraints: the backend service is the source of truth; clients MUST communicate through the API boundary.
- Scale/scope: single-user local development framework.

### 4.2 Additional Technology Decisions

- Cobra MUST be used from the initial CLI implementation, even though the first command set is small, because atc is expected to grow a larger command surface.
- mise MUST define the common development and build task surface.
- The Go module MUST live at the repository root.
- The web source MUST live under `web/` and use pnpm as its frontend package manager.
- `web/package.json` MUST declare the selected pnpm package manager version through the `packageManager` field.
- Built web assets MUST be staged inside the Go module before `go build` so `go:embed` can include them.
- Staged web assets MUST be treated as build artifacts and MUST NOT be committed.
- TCP token auth is deferred, but listener-aware middleware boundaries MUST exist so Unix socket and TCP behavior can diverge later.
- The initial integrated Go service MUST serve embedded web assets only; serving frontend assets directly from disk is deferred.

### 4.3 Repository Layout

The project SHOULD use this structure:

```txt
atc/
  go.mod
  mise.toml
  cmd/
    atc/
      main.go
  cli/
  internal/
    api/
    assets/
      web.go
      web/
    core/
    paths/
    server/
  web/
  docs/
    idea.md
    spec.md
```

Project-specific layout rules:

- `cmd/atc/main.go` MUST be a thin entrypoint.
- `cli/` MUST own Cobra command definitions.
- `internal/core/` MUST own framework-level application logic.
- `internal/api/` MUST expose HTTP handlers and MUST NOT own listeners.
- `internal/assets/` MUST expose the embedded web asset filesystem and MUST NOT own HTTP routing or listeners.
- `internal/paths/` MUST own service control path resolution and local service coordination file locations (the Unix socket and PID paths).
- `internal/server/` MUST own HTTP server setup, routing, listener creation including stale Unix socket preparation, and listener-aware middleware.
- Foreground mode (`atc serve`) MUST start the service by invoking `internal/server/` directly from `cli/`. There is no separate foreground wrapper package; `atc serve` is the canonical way to start the server in-process.
- `internal/daemon/` is PLANNED and not yet implemented. When background mode (`start`/`stop`/`status`) is built, it MUST own background run-mode lifecycle only: launching `atc serve` as a detached child process and managing the PID file. It MUST NOT own foreground execution.
- `internal/assets/web/` MUST contain staged built frontend assets only.
- `web/` MUST contain frontend source and MUST NOT be imported by Go directly.

### 4.4 Code Style and Agent Rules

Implementations MUST:

- Treat this document as the source of truth.
- Preserve the boundaries and contracts defined here unless the spec is amended.
- Keep names consistent with the component model.
- Prefer small, focused modules.
- Avoid hidden global state.
- Update tests when behavior changes.
- Surface material ambiguities as open questions.

Implementations MUST NOT:

- Add real product features listed as non-goals.
- Replace Cobra, Go, SvelteKit, or mise without a spec amendment.
- Introduce database, queue, container, or service-manager requirements.
- Let the CLI bypass the API for API-backed commands.

### 4.5 Dependency Policy

- New production dependencies MUST be justified before introduction.
- Approved dependencies SHOULD be preferred over new alternatives.
- Cobra is approved for CLI command structure.
- SvelteKit and its required frontend dependencies are approved for the web app.
- Large framework changes MUST be reflected in this specification before implementation.

## 5. System Overview

### 5.1 Main Components

1. Go service
   - Owns the application process.
   - Owns the local control paths and lifecycle coordination files.
   - Hosts one HTTP router containing API routes and embedded frontend routes.

2. CLI
   - Owns operator commands and local automation commands.
   - Runs the service in foreground mode for `serve`.
   - Launches and manages the service in background mode for `start`, `stop`, and `status`.
   - Calls the service API over a Unix domain socket for API-backed commands.

3. API
   - Exposes framework diagnostic endpoints.
   - Wraps core behavior without owning transport concerns.

4. Web app
   - Provides a SvelteKit hello-world UI.
   - Calls the API over HTTP in development when needed.
   - Builds to static assets for embedding.

5. Server
   - Owns TCP and Unix socket listeners.
   - Mounts API and web handlers onto one router.
   - Applies listener-aware middleware.

6. Assets
   - Embeds staged frontend output into the Go binary.
   - Exposes embedded web assets as a filesystem for the server to serve in packaged mode.

### 5.2 Component Boundaries

- `core` owns framework diagnostic data and future business logic boundaries.
- `api` MUST translate HTTP requests into core calls and JSON responses.
- `api` MUST NOT create network listeners.
- `paths` MUST own local service control path resolution and coordination file locations and MUST NOT import other internal components.
- `server` MUST be the only component that creates listeners, and MUST own stale Unix socket preparation before binding the Unix listener.
- `cli` MUST run foreground mode (`serve`) by invoking `server` directly, and MAY import `paths` to resolve coordination file locations.
- `daemon` (PLANNED, not yet implemented) MUST own background run-mode lifecycle only — `start`/`stop`/`status` — launching `atc serve` as a detached child rather than serving in-process, and managing the PID file. It MUST NOT own foreground execution. It MAY import `paths`.
- `assets` MUST expose embedded web asset filesystems and MUST NOT know about HTTP routing, listeners, or service lifecycle.
- API-backed CLI commands MUST dial the service through the Unix socket instead of importing `core` directly.

### 5.3 External Dependencies

- Local filesystem for service PID file, Unix socket file, and staged web assets.
- Browser for web UI access.
- Node-compatible frontend toolchain for SvelteKit development and builds.
- mise for task orchestration.

## 6. Core Domain Model

The initial framework has no persistent atc domain model.

### 6.1 Framework Data Shapes

#### 6.1.1 Health

Represents service API health.

Fields:

- `status` (string): MUST be `ok` when the service can serve API requests.

#### 6.1.2 Version

Represents diagnostic build metadata.

Fields:

- `name` (string): MUST be `atc`.
- `version` (string): MAY be `dev` until release automation exists.
- `commit` (string): MAY be `unknown` until release automation exists.

### 6.2 Relationships

- Health and Version are diagnostic API resources only.
- Health and Version MUST NOT imply persistence, release automation, or product domain behavior.

## 7. Component Specifications

### 7.1 CLI

#### 7.1.1 Purpose

The CLI provides local service lifecycle control and agent-friendly access to atc's API.

#### 7.1.2 Responsibilities

The CLI MUST:

- Use Cobra for command definitions.
- Provide `atc serve`.
- Provide `atc start`.
- Provide `atc stop`.
- Provide `atc status`.
- Provide `atc health`.
- Provide help text for every command.

The CLI MUST NOT:

- Expose `atc version` in this framework slice.
- Implement real product workflows.
- Install OS-level services or auto-start hooks.

#### 7.1.3 Behavior

- `atc serve` MUST run the service in foreground mode.
- `atc serve` and `atc start` MUST bind TCP to `127.0.0.1:7331` by default.
- `atc serve` and `atc start` MUST accept `--http-addr`.
- `ATC_HTTP_ADDR` MUST provide a default HTTP address when `--http-addr` is not set.
- `atc start` SHOULD launch the service in background mode using local PID and socket files.
- `atc stop` MUST stop a running background service when possible.
- `atc status` MUST report whether the service appears to be running.
- `atc health` MUST call `GET /api/health` through the Unix socket and report success or failure.

#### 7.1.4 Validation and Failure Handling

- Invalid listen addresses MUST fail with a clear error.
- `atc start` MUST fail clearly when the service is already running.
- `atc stop` MUST fail clearly when no service is running.
- `atc health` MUST fail clearly when the Unix socket cannot be reached or the API returns a non-2xx response.
- PID and socket paths MUST follow the service control directory defaults in this specification and MUST be documented by command help or developer documentation.

### 7.2 Server

#### 7.2.1 Purpose

The server owns transport setup and routes all client traffic to the same API and frontend handlers.

#### 7.2.2 Responsibilities

The server MUST:

- Create one HTTP router.
- Mount API handlers under `/api/`.
- Mount embedded web assets for non-API routes.
- Support a TCP listener.
- Support a Unix domain socket listener.
- Apply listener-aware middleware.

The server MUST NOT:

- Implement core business behavior.
- Require TCP auth in the initial framework slice.
- Create separate API and web servers.

#### 7.2.3 Behavior

- The service MUST bind TCP to `127.0.0.1:7331` by default.
- Non-loopback TCP binding MUST be opt-in via `--http-addr` or `ATC_HTTP_ADDR`.
- When non-loopback TCP binding is used without TCP auth, the service SHOULD log a warning.
- Unix socket requests MAY be treated as trusted local requests.
- TCP requests MUST pass through a distinct middleware path so token auth can be added later.
- The router MUST serve API routes before frontend fallback routes.

#### 7.2.4 Validation and Failure Handling

- Listener startup failures MUST fail service startup.
- Stale socket files SHOULD be handled when it is safe to do so.
- Ambiguous or unsafe socket cleanup MUST fail clearly rather than deleting unknown files.
- Shutdown SHOULD be graceful for foreground and background modes.

### 7.3 API

#### 7.3.1 Purpose

The API proves that atc clients can communicate with the service through stable HTTP contracts.

#### 7.3.2 Required Endpoints

- `GET /api/health`
- `GET /api/version`

#### 7.3.3 Output Contracts

`GET /api/health` MUST return HTTP 200 with:

```json
{
  "status": "ok"
}
```

`GET /api/version` MUST return HTTP 200 with:

```json
{
  "name": "atc",
  "version": "dev",
  "commit": "unknown"
}
```

The `version` and `commit` values MAY become build-time injected values later.

#### 7.3.4 Error Handling Contract

- Unknown API routes MUST return HTTP 404.
- API errors MUST return JSON error responses.
- API handlers MUST NOT panic for normal bad input.

### 7.4 Web App

#### 7.4.1 Purpose

The web app proves that atc can provide a browser UI during development and ship it inside the Go binary.

#### 7.4.2 Responsibilities

The web app MUST:

- Use SvelteKit.
- Provide a hello-world atc page.
- Be runnable through a frontend development task.
- Build to static output suitable for Go embedding.
- Use a configurable API base URL in development when it calls the API.

The web app MUST NOT:

- Implement product workflows.
- Require a database.
- Require direct access to backend internals.

#### 7.4.3 Behavior

- In packaged mode, the Go service MUST serve the built web app from embedded assets.
- In frontend development mode, the SvelteKit dev server MAY run separately from the Go service.
- The frontend MUST treat the Go API as an HTTP API.
- The frontend SHOULD include a simple visible indication that the app is running.

### 7.5 Build And Tasks

#### 7.5.1 Purpose

mise provides the repeatable command surface for development, verification, and packaging.

#### 7.5.2 Required Tasks

The project MUST provide mise tasks for:

- `mise run dev`: run the Go service for integrated development.
- `mise run web:dev`: run the SvelteKit dev server.
- `mise run web:build`: build the SvelteKit web app.
- `mise run assets:stage`: stage built web assets into `internal/assets/web/`.
- `mise run build`: build the SvelteKit app, stage assets, and build the Go binary.
- `mise run test`: run Go tests.

Frontend tasks MUST run pnpm commands from inside `web/`. mise task names are the stable project interface; direct pnpm commands are an implementation detail for frontend contributors.

#### 7.5.3 Build Ordering

- `mise run build` MUST build the web app before running `go build`.
- `mise run build` MUST stage static web output into `internal/assets/web/` before running `go build`.
- `mise run build` MUST depend on or otherwise invoke `web:build` and `assets:stage` in serial order.
- The build MUST avoid embedding stale or partial frontend assets.
- The staged asset directory MUST exist before Go embedding is evaluated.

## 8. Interfaces and Integration Contracts

### 8.1 HTTP API

- Base API path: `/api/`
- Response format: JSON for API endpoints.
- Initial health endpoint: `GET /api/health`
- Initial version endpoint: `GET /api/version`

### 8.2 CLI To Service API

- API-backed CLI commands MUST communicate through the Unix domain socket.
- `atc health` MUST map to `GET /api/health`.
- CLI output SHOULD be concise and script-friendly.
- CLI failures MUST use non-zero exit codes.

### 8.3 Web To API

- The packaged web app MUST call the API on the same origin when needed.
- The development web app MUST use a configurable API base URL.
- The initial web app MAY avoid API calls if the hello-world page does not need them.

## 9. Cross-Cutting Requirements

### 9.1 Data Persistence

The system MUST persist:

- No application data.

The system MAY create:

- A PID file for background-mode service lifecycle.
- A Unix socket file for local service API access.
- Build artifacts under `internal/assets/web/`.

The system MUST NOT persist:

- Project records.
- Item records.
- Environment records.
- Workflow state.
- Agent state.

### 9.2 Error Handling and Recovery

- Errors shown to users MUST be actionable and safe to display.
- Recoverable failures SHOULD preserve current process state where possible.
- Irrecoverable failures MUST fail clearly.
- Retried health checks MUST be safe to retry.

### 9.3 Security and Permissions

The system MUST:

- Validate untrusted HTTP input.
- Avoid logging secrets.
- Default TCP binding to loopback.
- Require explicit configuration for non-loopback TCP binding.

Permission rules:

- Local Unix socket access MAY rely on filesystem permissions.
- TCP token auth is deferred.
- Non-loopback unauthenticated TCP access MUST remain opt-in.

### 9.4 Observability

The system SHOULD expose:

- Startup logs including TCP bind address and Unix socket path.
- Warning logs when unauthenticated non-loopback TCP binding is enabled.
- Shutdown logs.
- Clear CLI error messages for service lifecycle failures.

### 9.5 Configuration

The implementation MUST support:

- `--http-addr` for the TCP bind address.
- `ATC_HTTP_ADDR` as the environment default for TCP bind address.

Configuration precedence:

1. CLI flags.
2. Environment variables.
3. Built-in defaults.

Defaults:

- TCP bind address: `127.0.0.1:7331`.
- Service control directory: `$XDG_RUNTIME_DIR/atc` when `XDG_RUNTIME_DIR` is set and non-empty.
- Service control directory fallback: `$TMPDIR/atc-$UID` when `TMPDIR` is set and non-empty, otherwise `/tmp/atc-$UID`.
- Service control directory permissions: the implementation MUST create the directory with `0700` permissions when possible.
- Unix socket path: `<service-control-dir>/atc.sock`.
- PID file path: `<service-control-dir>/atc.pid`.

## 10. Acceptance Criteria

The implementation is considered complete when:

- `go.mod` exists at the repository root and targets the latest patch release in the Go 1.26.x line.
- The CLI is implemented with Cobra.
- `atc serve` runs the service in foreground mode.
- `atc start`, `atc stop`, and `atc status` provide basic local service lifecycle management.
- `atc serve` and `atc start` accept `--http-addr` and respect `ATC_HTTP_ADDR`.
- `atc health` calls `GET /api/health` over the Unix socket.
- `GET /api/health` returns the required JSON health response.
- `GET /api/version` returns the required JSON version response.
- The service serves one router containing `/api/` routes and embedded web assets.
- The default TCP bind is `127.0.0.1:7331`.
- Non-loopback TCP binding is possible only through explicit configuration.
- A SvelteKit hello-world web app exists under `web/`.
- The web app can run locally for frontend development.
- The web app can be built, staged, embedded, and served by the Go binary.
- mise tasks cover development, testing, web build, asset staging, and binary build.

The implementation is not acceptable if:

- It requires a database.
- It implements real product workflows.
- It bypasses the API for `atc health`.
- It exposes unauthenticated non-loopback TCP binding by default.
- It requires OS service installation.
- It requires committed staged web build artifacts.

## 11. Verification Plan

Automated tests:

- Go tests MUST cover API health behavior.
- Go tests SHOULD cover API version behavior.
- Go tests SHOULD cover configuration precedence for HTTP bind address.
- CLI command tests SHOULD cover command construction and validation where practical.

Manual checks:

- Run `atc serve` and confirm `GET /api/health` works over TCP.
- Run `atc start`, `atc status`, `atc health`, and `atc stop`.
- Run the web development task and confirm the SvelteKit hello-world page loads.
- Run the integrated service and confirm the embedded web page loads.
- Run with `--http-addr 0.0.0.0:7331` or a tailnet address and confirm remote browser access is possible.

Integration checks:

- `mise run build` MUST produce a Go binary with embedded web assets.
- The built binary MUST serve API and web routes from the same service.
- `mise run test` or the documented Go test task MUST pass.

Regression checks:

- A missing staged asset directory MUST not result in silently stale embedded assets.
- API routes MUST continue to take precedence over frontend fallback routing.
- `atc status` MUST remain a lifecycle check, not an alias for API health.

## 12. Definition of Done

Required for completion:

- Go application skeleton implemented.
- Cobra CLI implemented with required commands.
- Server package implemented with TCP and Unix socket listener support.
- API package implemented with required endpoints.
- SvelteKit app created under `web/`.
- Embedded assets package implemented.
- mise tasks implemented.
- Go tests added for the framework contracts.
- This archived framework spec remains consistent with the implemented behavior.

Recommended but not required:

- Frontend typecheck or lint task.
- Build-time injection for `/api/version`.
- Development mode for serving web assets directly from disk.

Out of scope for initial implementation:

- Database and migrations.
- Real Project, Item, Environment, workflow, agent, or code review behavior.
- TCP token authentication.
- iOS app.
- CI/CD release versioning.
- OS service installation.

## 13. Resolved Implementation Decisions

- `web/` MUST use pnpm as its frontend package manager.
- Runtime files MUST live under `$XDG_RUNTIME_DIR/atc` when available, falling back to `$TMPDIR/atc-$UID` or `/tmp/atc-$UID`.
- The Unix socket path MUST be `<service-control-dir>/atc.sock`.
- The PID file path MUST be `<service-control-dir>/atc.pid`.
- The initial integrated Go service MUST serve embedded web assets only.
- A later development mode MAY serve web assets directly from disk, but that behavior is deferred until frontend iteration against the Go service needs it.
