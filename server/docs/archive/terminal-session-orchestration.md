# Terminal Session Orchestration — MVP Specification

Status: Draft v1

Purpose: Let atc spawn a command in a persistent terminal session that lives independently of the atc service, inject text and keys into it, and list those sessions — over both the API and CLI.

Source: Linear DEV-22. Decisions in this spec were settled in a design session and are recorded in [`CONTEXT.md`](../../CONTEXT.md) (glossary) and [`docs/adr/0001`–`0004`](../adr/). This spec MUST NOT silently re-open those decisions.

> **Revision v2 (security):** Following the MVP security review, [ADR 0004](../adr/0004-sessions-launch-agents-from-a-registry.md) supersedes ADR 0003. `start` now launches a named **agent** resolved against a server-side **agent registry** (with typed parameters) instead of an arbitrary command string, and the TCP listener gains optional bearer-token authentication. Sections below are updated to v2; passages describing an arbitrary `command` reflect the superseded v1 design and are marked where they remain for context.

## Normative Language

The key words `MUST`, `MUST NOT`, `REQUIRED`, `SHOULD`, `SHOULD NOT`, `RECOMMENDED`, `MAY`, and `OPTIONAL` in this document are to be interpreted as described in RFC 2119.

`Implementation-defined` means the behavior must be selected and documented by the implementation. Once documented, the selected behavior is part of the implementation contract.

## 1. Problem Statement

atc must perform actions on the workstation on behalf of API and CLI clients. The first such action is starting and interacting with a persistent, terminal-based process — primarily an AI coding agent (`claude`, `codex`), but generalized to any command.

The project exists to:

- Spawn a command in a terminal session that **persists independently of the atc service**, so the session survives a service restart and can be attached to later.
- Inject text into a running session without submitting it, and inject control/special keys (including the key that submits).
- List atc-managed sessions so clients can answer "what is running?" without atc holding its own session state.
- Expose all of the above identically over the API and the CLI.

The project does not attempt to solve:

- Reading agent/process output back programmatically (output streaming).
- A web-based or in-app terminal.
- Remote access (SSH, Tailscale).
- Persisting session metadata in atc (a registry/database).
- Input arbitration when a human terminal and the API drive the same session concurrently (`zmx`'s "leader" concept).

## 2. Goals and Non-Goals

### 2.1 Goals

- Provide a `start` operation that launches a registered **agent** in a detached, persistent, scoped session and returns the session name immediately (fire-and-register; never blocks on the process). The command is built from the agent registry, not supplied by the caller (ADR 0004).
- Provide a `send` operation that injects text verbatim, without submitting.
- Provide a `key` operation that injects a friendly-named control/special key as raw bytes.
- Provide a `list` operation that returns atc-managed sessions discovered from the multiplexer.
- Mirror all operations across HTTP API and CLI.
- Keep the multiplexer (`zmx`) behind a single, narrow internal wrapper so it is swappable.

### 2.2 Non-Goals

- No reading/streaming of session output.
- No web terminal, no remote access.
- No session registry, database, or persisted session metadata (see ADR 0002).
- No pre-flight check that the agent binary exists.
- No control keys beyond `enter`, `ctrl-c`, `escape`.
- No driving of agents via ACP (see ADR 0001).

## 3. Initial Project Boundary

This specification defines the MVP for terminal session orchestration within the existing atc service.

Target users:

- A local developer driving sessions via the CLI.
- AI agents or scripts driving sessions via the CLI or API.

Primary workflows:

- Start a session running a command (e.g. `claude`) scoped to an item, a project, or nothing.
- Send a prompt into a running session, then submit it with an `enter` key.
- Interrupt a session with `ctrl-c`.
- List running atc sessions.
- Attach to a session from a local terminal (via `zmx attach`, outside atc) to observe it.

Supported platforms or environments:

- Local Unix-like development environments; Linux is the primary target. `zmx` MUST be present and runnable headless on the host.

Initial deployment shape:

- New operations inside the existing single Go service/CLI binary; the multiplexer (`zmx`) is an external runtime dependency invoked as a child process.

Explicit exclusions:

- Output streaming, web terminal, remote access, session registry/persistence, agent enums, command pre-flight validation, keys beyond the three named.

## 4. Implementation Profile

This section is normative. Agent-generated implementation work MUST follow this profile unless this specification is amended. It inherits the constraints of the archived framework spec (`docs/archive/spec.md`) except where that spec's non-goals describe the now-complete framework slice.

### 4.1 Technical Context

- Language/version: Go (the version pinned in `mise.toml`; currently the Go 1.26.x line).
- Primary dependencies: Go standard library; Cobra for CLI (already approved). No new production dependencies required.
- Storage: none. Session identity and liveness come from `zmx`.
- Testing: `go test ./...`. The `zmx` wrapper MUST be testable without a live `zmx` daemon (see §11).
- Target platform: local Unix-like hosts, Linux primary.
- Project type: local service + CLI + HTTP API (extends the existing framework).
- Performance goals: `start` MUST return promptly and MUST NOT block on the spawned process.
- Constraints: the backend service is the source of truth; CLI MUST reach session operations through the API over the Unix socket. `zmx` is invoked only through the wrapper in §5.
- Scale/scope: single-user, single workstation.

### 4.2 Additional Technology Decisions

- Multiplexer: `zmx` (libghostty-based). Invoked as a child process; not linked. Rationale and swap-seam: ADR 0001 / `CONTEXT.md`.
- The `zmx` wrapper MUST be the only code that constructs or runs `zmx` commands. No Go interface/abstraction layer is required for the MVP; swappability comes from confining `zmx` to one package.
- Launch MUST use a login-interactive shell so the command inherits the full environment: `zmx run <name> -d <shell> -l -i -c "<command>"`, where `<shell>` is the user's `$SHELL`. The `<command>` is built by `internal/session` from the agent registry (ADR 0004), not received from the caller; `internal/zmx` still deals only in an opaque command string.
- Input MUST be delivered to `zmx send` via the child process's stdin (piped), not as shell-interpolated arguments, to avoid quoting/escaping defects.
- `zmx` is located from `PATH` by default, overridable via configuration (see §9.5).

### 4.3 Repository Layout

New and touched packages within the existing layout:

```txt
internal/
  zmx/        # NEW: thin wrapper around the zmx binary (Start, Send, SendKey, List)
  session/    # NEW: session domain logic (naming, scope, key registry, operations)
  api/        # MODIFIED: session routes
  server/     # MODIFIED: transport-aware auth middleware (token on TCP)
  config/     # MODIFIED: zmx binary location, agent registry, API token
cli/          # MODIFIED: `atc session …` commands
docs/
  specs/terminal-session-orchestration.md  # this document
```

Project-specific layout rules:

- `internal/zmx/` MUST deal only in `zmx` session names and byte payloads. It MUST NOT know about items, projects, scopes, the `atc:` naming scheme, or key registries.
- `internal/session/` MUST own the `atc:<kind>:<id>` naming scheme, scope parsing/encoding, the key registry, the agent registry and command construction, and the start/send/key/list operations. It depends on `internal/zmx`.
- `internal/api/` MUST translate HTTP requests into `internal/session` calls. It MUST NOT invoke `zmx` directly.
- `cli/` session commands MUST call the API over the Unix socket, consistent with the existing API-backed command pattern. They MUST NOT call `internal/session` or `zmx` directly.

### 4.4 Code Style and Agent Rules

Implementations MUST:

- Treat this document and the referenced ADRs as the source of truth.
- Keep names consistent with `CONTEXT.md` (Session, Agent, Command, Scope, Multiplexer, Drive).
- Prefer small, focused modules; avoid hidden global state.
- Confine all `zmx` invocation to `internal/zmx`.
- Update tests when behavior changes.

Implementations MUST NOT:

- Add a session registry, database, or persisted session metadata.
- Accept an arbitrary command string in `start`; `start` launches a registered agent (ADR 0004).
- Interpolate caller-supplied free text into the launched command.
- Add a "start-or-attach" combined operation.
- Add output reading, streaming, web terminal, or remote access.
- Let the CLI bypass the API for session operations.

### 4.5 Dependency Policy

- No new production Go dependencies are expected. Any new dependency MUST be justified before introduction.
- `zmx` is a required external runtime dependency, not a Go module dependency.

## 5. System Overview

### 5.1 Main Components

1. `internal/zmx` — Multiplexer wrapper
   - Builds and runs `zmx` child-process commands.
   - Exposes: `Start(name, dir, command)`, `Send(name, payload)`, `List()`.
   - Pipes byte payloads to `zmx send` via stdin.

2. `internal/session` — Session domain
   - Encodes/decodes the `atc:<kind>:<id>` name.
   - Generates collision-resistant free-session ids.
   - Translates friendly key names to bytes (key registry).
   - Resolves an agent name + typed params into a command via the agent registry.
   - Implements `start`, `send`, `key`, `list`, including validation and the reject-if-exists rule.

3. `internal/api` — HTTP surface
   - Flat RPC routes under `/api/sessions/`.
   - Translates requests to `internal/session` calls and JSON responses.

4. `cli` — Operator/agent surface
   - `atc session start|send|key|list`, calling the API over the Unix socket.

### 5.2 Component Boundaries

- `internal/zmx` owns `zmx` command construction and execution; it MUST NOT own naming, scope, or key semantics.
- `internal/session` owns all atc-specific session semantics; it MUST NOT construct `zmx` commands itself.
- `internal/api` owns HTTP translation; it MUST NOT own `zmx` or core listener concerns.
- `cli` owns command UX; it MUST reach the service through the API over the Unix socket.

### 5.3 External Dependencies

- The `zmx` binary (multiplexer) and its running daemon.
- The user's login shell (`$SHELL`).
- The local filesystem (working directories for spawned commands; the existing Unix socket for CLI↔API).

## 6. Core Domain Model

No data is persisted. The model below describes in-memory/derived shapes; identity lives in the `zmx` session name (ADR 0002).

### 6.1 Entities

#### 6.1.1 Session

A persistent terminal running a command, independent of the atc service.

Fields:

- `name` (string): the full `zmx` session name, `atc:<kind>:<id>`. The sole identity.
- `scope` (Scope): derived by parsing `name`.
- `command` (string): the command the session runs. On `list`, derived from `zmx`'s reported `cmd`. Not stored by atc.
- `dir` (string): the working directory the session started in. On `list`, derived from `zmx`'s reported `start_dir`.
- `created` (timestamp): derived from `zmx`'s reported `created`.

#### 6.1.2 Scope

What a session belongs to.

Fields:

- `kind` (enum): `item` | `project` | `free`.
- `id` (string): item id, project slug, or a generated free id. For `free`, generated; for `item`/`project`, supplied by the caller.

#### 6.1.3 Key (registry, not persisted)

A friendly key name mapped to the raw bytes injected into a session.

MVP registry (exact, closed set):

- `enter` → `\r` (`0x0D`)
- `ctrl-c` → `0x03`
- `escape` → `0x1B`

### 6.2 Relationships

- A `Session` has exactly one `Scope`, encoded in its `name`.
- `Scope` is an atc concept; `zmx` knows only the opaque `name`.

### 6.3 Identifiers and Normalization Rules

- Session name format: `atc:<kind>:<id>`, colon-separated.
- `kind` MUST be one of `item`, `project`, `free`.
- Parse-back MUST use `SplitN(name, ":", 3)`; the third segment is the id. This relies on item ids and project slugs never containing a colon (confirmed).
- "atc-managed" MUST be defined as: name matches the 3-part pattern AND `kind` is a valid kind. A naive `atc:` prefix match MUST NOT be used (so hand-made sessions like `atc:claude-code` are excluded).
- Free-session ids MUST be collision-resistant, generated from `crypto/rand` (RECOMMENDED: ~8 lowercase base32 chars). Two concurrent free `start` calls MUST NOT collide.

## 7. Component Specifications

### 7.1 `internal/zmx` (Multiplexer wrapper)

#### 7.1.1 Purpose

Provide the single, narrow seam through which atc talks to `zmx`.

#### 7.1.2 Responsibilities

This component MUST:

- Resolve the `zmx` binary (§9.5) and run it as a child process.
- `Start(name, dir, command)`: run `zmx run <name> -d <shell> -l -i -c "<command>"` with the child process's working directory set to `dir`. Return immediately after `zmx` returns; MUST NOT wait on the spawned command.
- `Send(name, payload []byte)`: run `zmx send <name>`, writing `payload` to its stdin.
- `List()`: run `zmx list`, returning one record per session including at least name and any reported `pid`, `created`, `start_dir`, `cmd`, and reachability/status.

This component MUST NOT:

- Apply or interpret the `atc:` naming scheme, scope, or key semantics.
- Maintain state between calls.

#### 7.1.3 Inputs and Outputs

Inputs: a `zmx` session name, a working directory, a command string, or a raw byte payload.

Outputs: success/error from the underlying `zmx` invocation; for `List`, parsed session records.

#### 7.1.4 Behavior

- `<shell>` is the user's `$SHELL`.
- The `-d` flag MUST follow the name (`run <name> -d …`); leading `-d` is parsed by `zmx` as the session name.
- All payloads MUST be delivered via stdin, not as interpolated arguments.

#### 7.1.5 Validation and Failure Handling

- If the `zmx` binary cannot be found or executed, calls MUST return a clear error identifying `zmx` and how to configure it.
- A non-zero `zmx` exit MUST surface as an error including `zmx`'s stderr where available.
- `List` MUST tolerate malformed or `unreachable` entries: it MUST NOT fail the whole call because one entry is broken.

### 7.2 `internal/session` (Session domain)

#### 7.2.1 Purpose

Own all atc-specific session semantics on top of the wrapper.

#### 7.2.2 Responsibilities

This component MUST:

- `Start(scope, dir, agent, params)`:
  - Resolve `agent` against the registry, building the command from its `bin`, base `args`, and validated `params`. Unknown agent or invalid param is a caller error; no command text comes from the caller.
  - Determine the session name: for `item`/`project`, `atc:<kind>:<id>`; for `free`, `atc:free:<generated-id>`.
  - For `item`/`project` scope, reject if an atc-managed session with that name already exists (check via `List`).
  - Call `zmx.Start` with the built command and return the name.
- `Send(name, text)`: inject `text` bytes verbatim via `zmx.Send` (no appended carriage return).
- `Key(name, keyName)`: look up `keyName` in the registry and inject its bytes via `zmx.Send`.
- `List()`: call `zmx.List`, keep only atc-managed sessions (§6.3), and return them with parsed scope.

This component MUST NOT:

- Persist anything.
- Provide a combined start-or-attach operation.
- Pre-flight whether the agent binary exists.
- Accept or interpolate a caller-supplied command string.

#### 7.2.3 Inputs and Outputs

Inputs: scope (kind + optional id), working directory, agent name, agent params, session name, text, key name.

Outputs: session name (from `Start`); session list (from `List`); success/error otherwise.

#### 7.2.4 Behavior

- `start` is strict create; `send`/`key` require an existing session.
- Submission is the caller's responsibility: `send` never submits; a separate `key enter` submits.

#### 7.2.5 Validation and Failure Handling

- Unknown `agent` MUST fail with an error listing valid agents (HTTP 400).
- An invalid `param` (unknown key, wrong type, or out-of-set enum value) MUST fail with a clear error (HTTP 400).
- `item`/`project` `start` against an existing managed name MUST fail clearly (do not inject into the existing session).
- Unknown key name MUST fail with an error listing valid keys.
- `send`/`key` to a non-existent session MUST fail clearly (surface `zmx`'s "session does not exist").

#### 7.2.6 Observability

SHOULD log, on `start`: the session name, the working directory, and the command. SHOULD log session-operation errors with the session name.

### 7.3 `internal/api` (HTTP surface)

See §8. Translates requests to `internal/session`; owns no `zmx` or listener concerns.

### 7.4 `cli` (`atc session …`)

#### 7.4.1 Purpose

Give humans and agents the same operations as the API.

#### 7.4.2 Responsibilities

This component MUST provide:

- `atc session start --agent <name> [--param key=value]… [--item <id> | --project <slug>] [--dir <path>]` — prints the session name to stdout on success.
- `atc session send <name> <text>` — injects text, no submit.
- `atc session key <name> <key>` — injects a registry key.
- `atc session list` — lists atc-managed sessions.

#### 7.4.3 Behavior

- `--agent` is REQUIRED and names a registered agent.
- `--item` and `--project` are mutually exclusive; neither means a `free` session.
- `--dir` defaults to the current working directory.
- `--param key=value` is repeatable; values are sent as strings and validated/typed by the service against the agent's spec.
- Output SHOULD be concise and script-friendly; failures MUST use non-zero exit codes.

#### 7.4.4 Validation and Failure Handling

- Specifying both `--item` and `--project` MUST fail with a clear error.
- A missing `--agent`, or a `--param` not in `key=value` form, MUST fail with a clear error.
- API/socket errors MUST be reported clearly with a non-zero exit.

## 8. Interfaces and Integration Contracts

### 8.1 Session HTTP API

#### 8.1.1 Purpose

Expose session operations as the source of truth for all clients.

#### 8.1.2 Required Operations

- `POST /api/sessions/start`: start a session; returns its name.
- `POST /api/sessions/send`: inject text without submitting.
- `POST /api/sessions/key`: inject a registry key.
- `GET /api/sessions`: list atc-managed sessions.

Routing style is flat RPC (operation in the path, identifiers in the body), consistent with the existing switch-based router. Path parameters are not required.

#### 8.1.3 Input Contracts

`POST /api/sessions/start`:

```json
{
  "agent": "claude",
  "params": { "model": "opus", "resume": true },
  "dir": "/home/user/project",
  "item": "DEV-22"
}
```

- `agent` (string, REQUIRED) — a key in the agent registry.
- `params` (object, OPTIONAL) — per-agent typed parameters; each is validated against the agent's spec (`enum` value from a closed set, or `bool`). Unknown keys, wrong types, or out-of-set values are rejected. No free-form command text is accepted.
- `dir` (string, REQUIRED).
- Scope is OPTIONAL: at most one of `item` (string) or `project` (string). Neither ⇒ `free`. Both ⇒ error.

Requests on the TCP listener MUST carry `Authorization: Bearer <token>` when a token is configured; the Unix socket is trusted (§9.3).

`POST /api/sessions/send`:

```json
{ "name": "atc:item:DEV-22", "text": "implement the parser" }
```

`POST /api/sessions/key`:

```json
{ "name": "atc:item:DEV-22", "key": "enter" }
```

#### 8.1.4 Output Contracts

`POST /api/sessions/start` → HTTP 200:

```json
{ "name": "atc:item:DEV-22" }
```

`POST /api/sessions/send` and `/key` → HTTP 200 with an empty JSON object `{}` on success.

`GET /api/sessions` → HTTP 200:

```json
{
  "sessions": [
    {
      "name": "atc:item:DEV-22",
      "scope": { "kind": "item", "id": "DEV-22" },
      "command": "/usr/bin/zsh -l -i -c claude",
      "dir": "/home/user/project",
      "created": 1781982140
    }
  ]
}
```

#### 8.1.5 Error Handling Contract

- Validation failures (missing/unknown agent, invalid param, both `item` and `project`, unknown key) MUST return HTTP 400 with a JSON error.
- A request on the TCP listener with a missing or invalid bearer token (when a token is configured) MUST return HTTP 401.
- `item`/`project` `start` against an existing managed session MUST return HTTP 409 (conflict).
- `send`/`key` to a non-existent session MUST return HTTP 404.
- `zmx` unavailable or failing, or a misconfigured agent, MUST return HTTP 500 with a JSON error identifying the cause.
- Error bodies MUST follow the existing API error shape (`{ "error": …, "message": … }`).

## 9. Cross-Cutting Requirements

### 9.1 Data Persistence

The system MUST persist:

- Nothing. No session registry, no database.

The system MAY create:

- Working-directory side effects produced by the spawned command itself (outside atc's control).

Identity and liveness MUST be derived from `zmx list` on demand.

### 9.2 Error Handling and Recovery

- Errors MUST be actionable and safe to display.
- `start` MUST be fire-and-register: a failure to launch the command inside the session is NOT detected (no pre-flight); it is observable only on attach. This is intended.
- `send`/`key` MUST be safe to call repeatedly; they are not idempotent in effect (each injects bytes) and this is expected.
- `list` MUST degrade gracefully around unreachable/malformed `zmx` entries.

### 9.3 Security and Permissions

The system MUST:

- Validate untrusted HTTP input (presence/shape of `agent`, `params`, `name`, `key`, scope).
- Build the launched command only from operator-defined registry config and values validated against a closed set; caller-supplied free text MUST NOT be interpolated into the command (ADR 0004).
- Avoid logging secrets; note that injected `text` MAY contain sensitive data and SHOULD NOT be logged verbatim beyond what §7.2.6 requires. The configured API token MUST NOT be logged.
- Apply transport-aware authentication: requests on the owner-only Unix socket are trusted; requests on the TCP listener MUST present the configured bearer token. When no token is configured the TCP guard is a no-op seam (preserving current local behavior).

Permission rules:

- `start` launches a registered agent, removing arbitrary-command execution from the API surface. The registry is blast-radius reduction; authentication is the primary control for who may call the API.
- The Unix socket remains the trusted local path (owner-only via the `0o700` control dir). The TCP listener is the seam for remote use (e.g. an iOS client over Tailscale) and MUST be gated by the bearer token before broader exposure.
- Token source/precedence follows the standard config precedence (see §9.5). A future iteration MAY add per-device/rotating tokens; the single configured token is the MVP seam.

### 9.4 Observability

The system SHOULD expose:

- A startup/log line per `start` with session name, working directory, and command.
- Operator-visible, actionable errors for `zmx` unavailability and session-operation failures.

### 9.5 Configuration

- `zmx` binary location: resolved from `PATH` by default; overridable via configuration.
  - Sources/precedence MUST follow the existing config precedence (CLI/flag, then environment, then config file, then built-in default), consistent with `internal/config`.
  - RECOMMENDED keys/vars: a config key (e.g. `zmx_bin`) and/or `ATC_ZMX_BIN`.
  - Default: `zmx` (resolved via `PATH`).
  - Validation: if the resolved binary cannot be executed, session operations that need it MUST fail clearly.
- Agent registry: an `[agents]` table mapping each agent name to its `bin`, base `args`, and typed `params` (`enum`/`bool`). When the config file defines `[agents]` it fully replaces the built-in defaults; otherwise a default registry (`claude`, `codex` as bare binaries) applies. The registry is a service-level setting (no CLI flag); the CLI reaches it through the running service.
- TCP API token: the bearer token required on the TCP listener.
  - Precedence follows the standard config order; RECOMMENDED key `auth.token` and/or `ATC_API_TOKEN`.
  - Default: empty (TCP authentication disabled; Unix socket remains trusted).

## 10. Acceptance Criteria

The implementation is considered complete when:

- `atc session start --agent claude` (item, project, and free variants) returns a `atc:<kind>:<id>` name and the session appears in `zmx list`, with `start` returning promptly (not blocking on the agent).
- `atc session start --agent <unknown>` fails with a 400 / clear CLI error; `--param` values are validated against the agent's spec.
- `atc session send <name> "<text>"` places text in the running command without submitting.
- `atc session key <name> enter` submits the pending text.
- `atc session key <name> ctrl-c` interrupts the running command.
- `atc session list` returns only atc-managed sessions, with parsed scope, tolerating unreachable entries.
- The same four operations work over `POST /api/sessions/start|send|key` and `GET /api/sessions`.
- After `start`, attaching with `zmx attach <name>` from a separate terminal shows the command alive and working on what was sent (the DEV-22 proof loop: spawn → inject → submit → persist → attach).
- A second `item`/`project` `start` for an existing session fails (409 / clear CLI error) rather than injecting into it.
- All `zmx` invocation is confined to `internal/zmx`.

The implementation is not acceptable if:

- It persists session state or adds a database/registry.
- It accepts an arbitrary command string in `start` or interpolates caller free text into the launched command.
- It blocks `start` on the spawned process.
- It lets the CLI invoke `zmx` or `internal/session` directly instead of going through the API.
- It constructs `zmx` commands outside `internal/zmx`.
- It introduces output streaming, a web terminal, or remote access.

## 11. Verification Plan

Automated tests:

- `internal/session`: name encode/decode round-trips, including ids/slugs containing hyphens; rejection of non-managed names (`atc:claude-code`); free-id generation is non-colliding and well-formed; key registry maps the three keys to correct bytes; unknown key errors.
- `internal/session`: `start` resolves the agent registry into the expected command and enforces unknown-agent, invalid-param, and reject-if-exists rules, using a faked `zmx` wrapper (no live daemon). Agent command construction covers enum/bool params, deterministic ordering, and shell-quoting.
- `internal/zmx`: command construction (argument order, `-d` after name, working-directory wiring, stdin payload delivery) verified without a live daemon — e.g. via an injected runner/exec seam or a stub `zmx` on `PATH`.
- `internal/api`: request validation, status-code mapping (400/401/404/409/500), and response shapes.
- `internal/server`: transport-aware auth — Unix trusted, TCP requires the token, no-op when unset.
- Config: `zmx` location precedence, agent-registry defaults vs file override, and API-token precedence.

Manual checks:

- Run the full DEV-22 proof loop against a real `zmx` and a real agent: start, send a prompt, `key enter`, then `zmx attach` and confirm the agent is working.
- Confirm `start` returns immediately for a long-running command.
- Confirm `ctrl-c` interrupts.

Integration checks:

- CLI `session` commands reach the running service over the Unix socket and produce identical results to direct API calls.
- `list` reflects sessions created by `start` and excludes non-atc `zmx` sessions.

Regression checks:

- Existing framework endpoints/commands continue to work.
- API routes continue to take precedence over frontend fallback routing.

## 12. Definition of Done

Required for completion:

- `internal/zmx` wrapper implemented (Start/Send/List) with `zmx` invocation confined to it.
- `internal/session` implemented (naming, scope, free-id generation, key registry, start/send/key/list, validation).
- `internal/api` session routes implemented with the contracts in §8.
- `internal/server` transport-aware auth middleware implemented (token on TCP, Unix trusted).
- `cli` `session start|send|key|list` implemented over the Unix socket.
- `internal/config` extended with `zmx` location, the agent registry, and the TCP API token.
- Tests per §11 added and passing; `go test ./...` green.
- `CONTEXT.md` and ADRs remain consistent with the implementation.

Recommended but not required:

- A `--dir` shorthand and shell completion for `session` commands.

Out of scope for initial implementation:

- Output reading/streaming, web terminal, remote access.
- Session registry / persisted metadata (added later, keyed by session name; first trigger: capturing originating prompt — ADR 0002).
- Keys beyond `enter`/`ctrl-c`/`escape`.
- Command pre-flight validation.
- `zmx` "leader"/input-arbitration handling.

## 13. Open Questions

- Free-id length/alphabet: ~8 base32 chars is RECOMMENDED; confirm before lock-in if a shorter/longer id is preferred. (Deferrable)
- `GET /api/sessions` field set: name, scope, command, dir, created are proposed from `zmx list`; confirm whether `pid`/status should also be surfaced. (Deferrable)
- Whether `atc session send` should accept text via stdin (in addition to an argument) for large prompts. (Deferrable)
- `zmx` portability confirmation on every intended target OS beyond the current Linux workstation (DEV-22 dependency note). (Spec-shaping for non-Linux targets only)

## Appendices

### Appendix A. zmx command mapping (reference)

- Start: `zmx run <name> -d $SHELL -l -i -c "<command>"`, child cwd = `dir`, where `<command>` is built by `internal/session` from the agent registry (`bin` + base `args` + validated param tokens, shell-quoted) — not received from the caller. `run` appends a completion marker to the launch line; this is harmless because it only fires after the command exits.
- Send text: pipe `text` bytes to `zmx send <name>` stdin (verbatim; no carriage return).
- Send key: pipe the registry bytes to `zmx send <name>` stdin (`enter`=`\r`, `ctrl-c`=`0x03`, `escape`=`0x1B`).
- List: `zmx list`, then filter to names matching `atc:(item|project|free):<id>`.
- Attach (human, outside atc): `zmx attach <name>`.
