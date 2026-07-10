> Archived: superseded by `docs/plans/sessions-actions-environments.md` and `docs/specs/sessions-actions-environments.md`.

# Backend Agent Sessions Specification

Status: Draft v2

Purpose: Turn Cockpit's terminal-session proof loop into a durable backend
contract for starting, tracking, attaching to, and sending input to coding-agent
sessions.

Source: [`docs/agent-sessions-brief.md`](../agent-sessions-brief.md), ADR
[0005](../adr/0005-sqlite-for-cockpit-state.md), and ADR
[0006](../adr/0006-cockpit-owned-session-identity.md). This spec supersedes
[`terminal-session-orchestration.md`](../archive/terminal-session-orchestration.md)
where the two conflict; the older document remains the MVP history.

## Normative Language

The key words `MUST`, `MUST NOT`, `REQUIRED`, `SHOULD`, `SHOULD NOT`,
`RECOMMENDED`, `MAY`, and `OPTIONAL` in this document are to be interpreted as
described in RFC 2119.

`Implementation-defined` means the behavior must be selected and documented by
the implementation. Once documented, the selected behavior is part of the
implementation contract.

## 1. Problem Statement

Cockpit can currently launch configured agents in `zmx`, list live
Cockpit-managed sessions, send text/keys, and attach over a WebSocket terminal
bridge. That proof loop is not yet a stable product backend: identity is encoded
in the multiplexer name, metadata is not durable, failed launches disappear,
agents are not discoverable, and clients have to depend on implementation
details.

The project exists to:

- Provide a stable session-centered API and CLI contract for coding-agent
  sessions.
- Persist Cockpit-owned session metadata that the multiplexer cannot recover.
- Keep `zmx` replaceable by confining its behavior and names to the multiplexer
  boundary.
- Give browser, native, CLI, and agent clients the same backend semantics for
  start, list, read, attach, input, terminate, and archive.
- Let clients discover configured agents instead of hard-coding defaults.

The project does not attempt to solve:

- Project/item workflow integration.
- A separate `Context`, assignment, or work-package model.
- Persisted terminal output, scrollback, transcripts, or summaries.
- Structured agent protocols such as ACP.
- Multi-user tenancy, hosted deployment, or shared remote-session coordination.

## 2. Goals and Non-Goals

### 2.1 Goals

- Introduce Cockpit-owned stable `Session.id` values as public session identity.
- Introduce a SQLite-backed Cockpit state store and session registry.
- Add automatic startup migrations using an established Go migration library.
- Replace legacy scope/name-derived session identity with persisted session
  properties.
- Add `GET /api/agents` as the source of truth for configured agents and launch
  parameters.
- Replace the stable session API with `Session.id`-based operations:
  `start`, `list`, `read`, `attach`, `send-text`, `send-key`, `terminate`, and
  `archive`.
- Mirror the same operations in the CLI with plural resource groups:
  `cockpit agents ...` and `cockpit sessions ...`.
- Preserve direct WebSocket attach and define its frame protocol.
- Keep terminal input ordering simple and best-effort for a single developer's
  workstation.

### 2.2 Non-Goals

- No project/item associations in this backend pass.
- No `Context` model in this backend pass.
- No editable rename/update endpoint; session `name` is set only at start. A
  future `PATCH /api/sessions/{id}` is the additive extension point if rename is
  added later (§4.6).
- No terminal-output persistence.
- No session events or SSE; clients poll `GET /api/sessions`.
- No generic diagnostics endpoint; diagnostics live on relevant resources.
- No custom migration runner.
- No compatibility requirement for the old flat RPC routes or singular
  `cockpit session ...` CLI.
- No UI redesign beyond using the new backend contract when UI code is touched.

## 3. Project Boundary

This specification defines the next backend agent-session pass inside the
existing Cockpit service.

Target users:

- A single local developer running Cockpit on a workstation or remote dev host.
- Browser, native Mac, CLI, and agent/script clients using the local API.

Primary workflows:

- Discover configured agents and their accepted launch parameters.
- Start a coding-agent session with a working directory, optional params,
  optional prompt, and optional user-facing name.
- List non-archived sessions, including live sessions, terminated history, and
  failed starts.
- Read full detail for one session.
- Attach to a live session through a WebSocket terminal stream.
- Send text and named keys to a live session.
- Terminate a live session.
- Archive terminated or failed sessions to remove them from default lists.

Supported platforms or environments:

- Local Unix-like hosts; Linux remains the primary remote-host target.
- Existing Cockpit TCP and Unix-socket service listeners.
- The external `zmx` binary as the current multiplexer implementation.

Initial deployment shape:

- One Go service/CLI binary, backed by a local SQLite database under the user's
  XDG state directory.

Explicit exclusions:

- Hosted, multi-user, or public-internet deployment semantics.
- Any durable storage outside SQLite for Cockpit-owned state.
- Any API that exposes multiplexer session names as public identity.

## 4. Implementation Profile

This section is normative. Agent-generated implementation work MUST follow this
profile unless this specification is amended.

### 4.1 Technical Context

- Language/version: Go version pinned by the repo toolchain; currently Go
  1.26.x.
- CLI framework: Cobra, already approved.
- HTTP/WebSocket: existing HTTP server and `github.com/coder/websocket`.
- Storage: SQLite through one Cockpit state-store boundary.
- Migrations: established Go migration library with embedded ordered SQL files.
- Testing: `go test ./...`; store and multiplexer behavior MUST be testable
  without a live `zmx` daemon.
- Target platform: local Unix-like hosts, Linux primary.
- Project type: local service, CLI, HTTP API, embedded web app.
- Performance goals: start/list/read operations MUST remain responsive for a
  single developer's local session history.
- Constraints: the backend service is the source of truth; CLI and web clients
  MUST use the API boundary for session behavior.
- Scale/scope: single-user, single-workstation state.

### 4.2 Additional Technology Decisions

- SQLite is the Cockpit-owned state store (ADR 0005).
- Session identity is Cockpit-owned and not derived from `zmx` names (ADR 0006).
- The SQLite driver and migration library are implementation-defined, but the
  implementation MUST use established Go libraries and MUST NOT build a custom
  migration runner.
- Migration SQL files MUST be committed to the repo, embedded in the binary, and
  applied automatically during service startup.
- The service MUST fail startup clearly if the database cannot be opened or
  migrated.
- The database path defaults to `$XDG_STATE_HOME/cockpit/cockpit.db`, falling
  back to `~/.local/state/cockpit/cockpit.db`.
- The database path MUST be overrideable through config and environment, using
  `store.db_path` and `COCKPIT_DB_PATH` unless implementation documents a
  different equivalent key before coding.
- No CLI flag is required for the database path.
- The existing service control directory remains runtime-only for sockets, PID
  files, and logs; persistent SQLite state MUST NOT live there by default.

### 4.3 Repository Layout

New and touched areas SHOULD follow this shape:

```txt
internal/
  store/       # SQLite connection, migrations, transactions, session store
  session/     # Cockpit session domain, agent registry, lifecycle rules
  zmx/         # zmx command behavior and attach process details
  api/         # HTTP JSON + WebSocket routes
  server/      # routing, auth, listener-aware middleware, store wiring
  config/      # store path, agent metadata, existing config
  paths/       # runtime control paths plus state path helpers
cli/           # agents and sessions commands over the API
docs/
  adr/
  specs/
```

Project-specific layout rules:

- `internal/store` MUST own the SQLite connection lifecycle, migration
  execution, transaction boundary, and persistent session metadata access.
- `internal/store` MUST NOT become a generic ORM or hide all SQL behind
  unnecessary abstraction.
- `internal/zmx` MUST own `zmx` command shapes, flags, output parsing, attach
  process management, and multiplexer-private names.
- Other packages MUST NOT depend on `zmx` package-specific types, `zmx` command
  output quirks, or `zmx` session names.
- `internal/session` MUST own Cockpit session validation, status transitions,
  launch command construction from the agent registry, and lifecycle rules.
- `internal/api` MUST translate HTTP/WebSocket requests into domain operations;
  it MUST NOT invoke `zmx` directly.
- `cli` MUST call the API over the Unix socket for session and agent commands.

### 4.4 Code Style and Agent Rules

Implementations MUST:

- Treat this document, `CONTEXT.md`, and ADR 0005/0006 as the source of truth.
- Keep names consistent with `Session`, `Agent`, `Multiplexer`, `Attach`, and
  `Session Input`.
- Preserve the API boundary for CLI and web clients.
- Keep generated ids opaque and URL-safe.
- Add or update tests with behavior changes.
- Surface material ambiguity as a spec update rather than silently choosing.

Implementations MUST NOT:

- Reintroduce `Scope` or `Context` into the public session contract.
- Encode project, item, agent, time, or display text in session ids or
  multiplexer names.
- Persist terminal output.
- Expose the launched shell command as the source of truth for agent metadata.
- Accept arbitrary command strings in `start`.
- Add a second persistence mechanism for session state.

### 4.5 Dependency Policy

- New production dependencies MUST be justified.
- The SQLite driver and migration library are approved categories by this spec;
  the exact libraries MUST be documented by the implementation.
- No ORM is approved by this spec.
- Large framework changes, replacement of Cobra, replacement of SvelteKit, or
  replacement of `zmx` as the multiplexer MUST require a spec or ADR update.

### 4.6 Compatibility and Versioning

- All API clients are first-party and shipped from this repository; they are
  upgraded in lockstep with the service. There is no `/api/v1` prefix in this
  pass.
- API responses MUST evolve additively: fields MUST NOT be removed or
  repurposed; new fields MAY be added.
- Clients MUST ignore unknown fields so additive server changes never break a
  deployed client.
- Introducing a version prefix or any non-additive change is an ADR-level
  decision, made when a non-first-party consumer first requires it.

## 5. System Overview

### 5.1 Main Components

1. `internal/store` — Cockpit state store
   - Opens the SQLite database at the resolved state path.
   - Applies embedded SQL migrations.
   - Exposes session persistence operations.
   - Owns transaction boundaries for session start and lifecycle updates.

2. `internal/session` — Session domain
   - Generates and validates Cockpit session ids.
   - Validates agent names and accepted params.
   - Builds launch commands from the agent registry.
   - Starts, lists, reads, sends input, terminates, and archives sessions.
   - Reconciles persisted session state with multiplexer liveness.

3. `internal/zmx` — Multiplexer implementation
   - Maps Cockpit session ids to private `zmx` names.
   - Starts, sends to, attaches to, resizes, terminates, and lists multiplexer
     sessions.
   - Contains all `zmx` command and output knowledge.

4. `internal/api` — HTTP and WebSocket surface
   - Exposes agent discovery and session routes under `/api`.
   - Produces stable JSON response and error shapes.
   - Bridges WebSocket attach frames to the session attach stream.

5. `cli` — Operator and automation surface
   - Exposes `cockpit agents ...` and `cockpit sessions ...`.
   - Calls the service API over the Unix socket.

6. `internal/server`
   - Opens the store before serving.
   - Wires config, store, session service, auth, API routes, and web routes.

### 5.2 Component Boundaries

- `store` owns durable Cockpit state; `zmx` owns live terminal process state.
- `session` joins persisted metadata with multiplexer-derived liveness and
  attachability.
- `api` owns protocol translation, not domain rules.
- `cli` owns command UX, not session behavior.
- Multiplexer names are private to the multiplexer boundary and MUST NOT appear
  in API or CLI responses.

### 5.3 External Dependencies

- SQLite database file under the XDG state directory.
- The `zmx` binary and daemon.
- The user's login shell for launching agent commands.
- Local filesystem working directories.
- Existing Cockpit TCP and Unix-socket listeners.

## 6. Core Domain Model

### 6.1 Entities

#### 6.1.1 Session

A persistent terminal instance created by Cockpit to run a configured Agent.

Fields:

- `id` (string): Stable Cockpit-owned id. Opaque, URL-safe, prefixed with
  `ses_`, and used for all public references.
- `name` (string, optional): User-facing display name set at start. Not identity
  and not used by the multiplexer.
- `agent` (string): Registered agent name used for launch.
- `params` (object): Accepted launch params after validation against the agent
  registry. Returned in detail responses, not list responses.
- `workingDir` (string): Working directory used for launch.
- `prompt` (string, optional): Starting prompt stored by Cockpit. Returned in
  detail responses, not list responses.
- `status` (enum): `starting`, `running`, `failed`, or `terminated`. Archive is
  not a status; see `archivedAt`.
- `failureReason` (string, optional): Safe user-facing reason for failed starts.
- `failureCode` (string, optional): Stable machine code for a failed start, drawn
  from the error-code vocabulary in §7.5.4. Present whenever `status` is
  `failed`.
- `createdAt` (timestamp): Session record creation time.
- `updatedAt` (timestamp): Last Cockpit metadata/status update time.
- `terminatedAt` (timestamp, optional): Time Cockpit determined no live terminal
  session remained.
- `archivedAt` (timestamp, optional): Time the session was archived. A session is
  archived iff this is set; archiving does not change `status`, so a failed or
  terminated session keeps that status after archiving.
- `attachable` (bool, derived): Best-effort hint, true when the multiplexer
  reports a live session and the session is `running` and not archived. It is
  advisory — `attach` is the authoritative gate (§7.2.6). When liveness cannot
  be determined, `attachable` is best-effort and stored status is not mutated
  (§8.5).

API timestamps MUST be UTC RFC 3339 strings. SQLite storage format is
implementation-defined.

#### 6.1.2 Agent

A configured launchable coding tool such as Claude Code or Codex.

Fields:

- `name` (string): Stable config key used in `start`.
- `label` (string, optional): Human-readable display label.
- `description` (string, optional): Short display description.
- `bin` (string): Operator-defined executable path or command name.
- `args` (array of strings): Operator-defined fixed base args.
- `params` (object): Closed parameter schema.
- `prompt` (object, optional): How an initial prompt is passed as a launch
  argument. Fields: `flag` (string, optional) — operator-defined flag emitted
  before the prompt value; an empty or absent `flag` passes the prompt as a
  positional argument. If `prompt` is absent the agent does not accept an initial
  prompt, and a `start` that supplies one MUST fail (§7.2.3). The prompt *value*
  is never part of this config; it is the caller's text, passed as a single
  safely-quoted argument (§8.2).
- `availability` (enum): `available`, `unavailable`, or `unknown`.
- `availabilityMessage` (string, optional): Safe diagnostic text.

The API MUST NOT expose secrets. It MAY omit `bin`/`args` from `GET /api/agents`
if the implementation treats them as operator details rather than client display
data; launch behavior MUST NOT depend on clients knowing them.

#### 6.1.3 Agent Parameter

A closed launch option accepted by an Agent.

Fields:

- `type` (enum): `enum` or `bool`.
- `values` (array of strings, enum only): Accepted enum values.
- `default` (string or bool, optional): Default value for client display and
  omitted params. For an `enum` param, `default` MUST be one of `values`.
- `flag` (string): Operator-defined command-line flag emitted for the param.
- `label` (string, optional): Human-readable display label.
- `description` (string, optional): Short display description.

Free-form string params are deliberately unsupported.

### 6.2 Relationships

- A Session references one Agent by registry name.
- A Session stores accepted launch params, not raw caller input.
- A Session's live terminal process is owned by the Multiplexer.
- The Store persists Cockpit-owned metadata; the Multiplexer supplies liveness
  and terminal streams.

### 6.3 Identifiers and Normalization Rules

- `Session.id` MUST be generated by Cockpit, not supplied by clients.
- `Session.id` MUST be URL-safe and MUST NOT encode semantic data.
- Session ids SHOULD use at least 128 bits of randomness or an equivalent
  collision-resistant scheme.
- Agent names are config keys and MUST be stable strings accepted by the agent
  registry.
- Working directories MUST be non-empty for `start`. Path canonicalization is
  implementation-defined, but errors MUST be clear and safe to display.

## 7. Component Specifications

### 7.1 `internal/store`

#### 7.1.1 Purpose

Own SQLite-backed Cockpit state.

#### 7.1.2 Responsibilities

This component MUST:

- Resolve and open the configured SQLite database path.
- Ensure the state directory exists with owner-only permissions where possible.
- Apply embedded SQL migrations on service startup.
- Persist session records and lifecycle updates.
- Provide query methods for list/read/archive behavior.
- Provide a transaction boundary for creating a start record and updating its
  launch result.

This component MUST NOT:

- Invoke the multiplexer.
- Build agent commands.
- Implement HTTP or CLI behavior.
- Persist terminal output.
- Become a generic ORM.

#### 7.1.3 Validation and Failure Handling

- Startup MUST fail clearly if the database cannot be opened.
- Startup MUST fail clearly if migrations fail.
- Store errors returned to API/domain code MUST be wrapped with operation
  context but MUST NOT include secrets.

### 7.2 `internal/session`

#### 7.2.1 Purpose

Own Cockpit session semantics on top of persisted metadata and the multiplexer
boundary.

#### 7.2.2 Responsibilities

This component MUST:

- Implement `Start(agent, params, workingDir, prompt, name)`.
- Implement `List(includeArchived)`.
- Implement `Read(id)`.
- Implement `Attach(id)`.
- Implement `SendText(id, text)`.
- Implement `SendKey(id, key)`.
- Implement `Terminate(id)`.
- Implement `Archive(id)`.
- Validate agent names and params before constructing a command.
- Store accepted launch params, not raw unvalidated params.
- Reconcile running/starting records with multiplexer liveness on reads/lists.

This component MUST NOT:

- Accept arbitrary command text from callers.
- Expose multiplexer names.
- Construct or invoke `zmx` commands directly.
- Persist terminal output.
- Model project/item relationships.

#### 7.2.3 Start Behavior

- `Start` MUST create a durable session record before launching the multiplexer
  session.
- The new record MUST start with status `starting`.
- `Start` MUST build the launch command only from operator-defined registry
  config and accepted closed params.
- If `prompt` is present, Cockpit MUST store it and pass it to the agent as a
  launch argument per the agent's `prompt` placement (§6.1.2), as a single
  safely-quoted argument (§8.2). The agent receives the prompt at startup and
  acts on it; there is no separate post-launch inject-or-submit step for the
  initial prompt. Follow-up input after launch uses `SendText`/`SendKey`.
- If `prompt` is present for an agent with no `prompt` placement configured,
  `Start` MUST fail before launch with a clear error.
- Every `start` creates a distinct session. There is no scope or uniqueness
  constraint and no start deduplication, so a repeated `start` (e.g. a
  double-submit) creates multiple sessions.
- `Start` is synchronous from the client perspective: it MUST return only after
  immediate launch success or failure.
- On launch success, the session MUST transition to `running` and the operation
  MUST return the full session object.
- On launch failure after a record exists, the session MUST transition to
  `failed`, store a safe `failureReason` and a `failureCode` (§7.5.4), and the
  API MUST return a non-2xx error whose `error` equals that `failureCode` and
  that includes `sessionId`.
- Raw internal launch errors MUST be logged and MUST NOT be stored as
  `failureReason`.

If the service crashes while a session is `starting`, reconciliation (§8.5)
resolves the record on next startup by liveness: a `starting` record whose
multiplexer session is live MUST become `running`; one with no live session MUST
become `failed` with a safe reason. A `starting` record MUST NOT be failed
without checking liveness, since a crash between a successful launch and the
`running` write leaves a live session.

#### 7.2.4 List and Read Behavior

- Default list MUST return non-archived sessions.
- Default list MUST include `running`, `terminated`, and `failed` sessions.
- Default list MUST be ordered by `createdAt` descending (newest first).
- Lists MUST accept an optional `status` filter (e.g. `GET /api/sessions?status=running`)
  that restricts results to the named status.
- Pagination is not required in this pass. If added later it MUST be additive
  (§4.6) — e.g. an optional `limit`/cursor that leaves the default envelope and
  ordering unchanged.
- Default list MUST omit prompt text and accepted params.
- Default list items MUST include: `id`, optional `name`, `agent`,
  `workingDir`, `status`, `attachable`, timestamps, and `failureReason`/
  `failureCode` when present.
- Detail reads MUST include accepted params and the stored starting prompt.
- Reads/lists MUST derive `attachable` from multiplexer liveness (§8.5). When
  liveness cannot be determined, the read/list MUST NOT mutate stored status,
  and `attachable` is a best-effort value; `attach` remains the authoritative
  gate.
- If a `running` session is positively observed to be no longer live, reads/
  lists SHOULD mark it `terminated` and set `terminatedAt`.

#### 7.2.5 Input Behavior

- `SendText` MUST inject text bytes without appending Enter.
- `SendKey` MUST inject bytes from the named key registry.
- Required key names: `enter`, `ctrl-c`, and `escape`.
- Input operations MUST fail for missing, failed, terminated, or archived
  sessions.
- Input operations remain available while WebSocket clients are attached.
- Cockpit does not coordinate concurrent writers beyond best-effort arrival at
  the multiplexer boundary.

#### 7.2.6 Attach Behavior

- `Attach` MUST fail for missing, failed, terminated, or archived sessions.
- Multiple simultaneous attaches MUST be allowed when the multiplexer supports
  them.
- Cockpit MUST NOT add an exclusive attach lock in this pass.

#### 7.2.7 Terminate Behavior

- `Terminate` MUST request the multiplexer boundary to stop the live terminal
  session for the `Session.id`.
- Once the terminal session is no longer reachable, Cockpit MUST set
  `terminatedAt` and status `terminated`. Terminating an already-archived session
  MUST NOT change `archivedAt`.
- `Terminate` MUST be idempotent for already terminated, failed, archived, or
  otherwise non-live sessions.
- After termination, attach and input operations MUST fail.

#### 7.2.8 Archive Behavior

- `Archive` MUST be metadata-only.
- `Archive` MUST reject live sessions.
- `Archive` MUST succeed for terminated sessions and failed starts.
- `Archive` SHOULD be idempotent for already archived sessions.
- `Archive` MUST set `archivedAt` and remove the session from default list
  responses. `Archive` MUST NOT change `status`: the session keeps its
  `terminated` or `failed` status, and archived state is expressed solely by
  `archivedAt`.

### 7.3 `internal/zmx`

#### 7.3.1 Purpose

Own the current multiplexer implementation.

#### 7.3.2 Responsibilities

This component MUST:

- Map `Session.id` to private `zmx` session names.
- Start a detached terminal session.
- Send byte payloads to a terminal session.
- Attach to a live PTY stream.
- Resize attach PTYs.
- Terminate a live multiplexer session if supported by `zmx`.
- List live multiplexer sessions for liveness reconciliation.

This component MUST NOT:

- Know about project/item concepts.
- Know about user-facing session names.
- Know about HTTP, CLI, or SQLite.
- Expose `zmx` names as public API identity.

The exact private `zmx` name format is implementation-defined, but it MUST be
*deterministically* derived from `Session.id` so that it is recomputable from the
id at any time, MUST NOT be separately persisted as identity, and MUST NOT encode
user-facing session meaning.

### 7.4 Agent Registry

#### 7.4.1 Purpose

Define the closed set of launchable agents and their accepted launch params.

#### 7.4.2 Responsibilities

The registry MUST:

- Preserve the existing default agents `claude` and `codex` when no config
  agents are defined.
- Treat a configured `[agents]` table as replacing defaults, consistent with
  existing behavior.
- Support `bin`, fixed `args`, closed typed `params`, agent `label`, optional
  agent `description`, and optional parameter labels/descriptions.
- Support `enum` and `bool` params.
- Support an optional per-agent `prompt` placement (§6.1.2) describing how an
  initial prompt is passed as a launch argument: positional by default, or behind
  an operator-defined flag.
- Reject unknown params, wrong param types, and invalid enum values before any
  multiplexer launch.
- Reject an `enum` param whose `default` is not one of its `values`.

The registry MUST NOT:

- Accept free-form string params.
- Let client-supplied text become shell command tokens except through closed
  enum values, bool flags, or the prompt value passed as a single safely-quoted
  argument (§8.2).
- Hide configured agents from discovery because their binary is unavailable.

### 7.5 `internal/api`

#### 7.5.1 Purpose

Expose agent and session operations over HTTP and WebSocket under `/api`.

#### 7.5.2 Required Routes

- `GET /api/agents`
- `POST /api/sessions/start`
- `GET /api/sessions`
- `GET /api/sessions/{id}`
- `GET /api/sessions/{id}/attach`
- `POST /api/sessions/{id}/send-text`
- `POST /api/sessions/{id}/send-key`
- `POST /api/sessions/{id}/terminate`
- `POST /api/sessions/{id}/archive`

The older routes `/api/sessions/send`, `/api/sessions/key`, and
`/api/sessions/attach?name=...` MUST be removed or made unreachable when the new
contract is implemented. The old `/api/sessions/start` request/response shape
does not require compatibility even though the canonical start path remains.

#### 7.5.3 JSON Shapes

`GET /api/agents` response:

```json
{
  "agents": [
    {
      "name": "codex",
      "label": "Codex",
      "description": "OpenAI Codex CLI",
      "availability": "available",
      "params": {
        "model": {
          "type": "enum",
          "values": ["gpt-5-codex"],
          "label": "Model"
        },
        "full-auto": {
          "type": "bool",
          "label": "Full Auto"
        }
      }
    }
  ]
}
```

`POST /api/sessions/start` request:

```json
{
  "agent": "codex",
  "params": { "model": "gpt-5-codex" },
  "workingDir": "/home/user/project",
  "prompt": "Review this change",
  "name": "Review session"
}
```

Successful start response: full session detail.

`GET /api/sessions` response:

```json
{
  "sessions": [
    {
      "id": "ses_abc123",
      "name": "Review session",
      "agent": "codex",
      "workingDir": "/home/user/project",
      "status": "running",
      "attachable": true,
      "createdAt": "2026-06-25T15:04:05Z",
      "updatedAt": "2026-06-25T15:04:06Z"
    }
  ]
}
```

List responses MUST NOT include `prompt` or `params`.

`GET /api/sessions/{id}` response:

```json
{
  "id": "ses_abc123",
  "name": "Review session",
  "agent": "codex",
  "params": { "model": "gpt-5-codex" },
  "workingDir": "/home/user/project",
  "prompt": "Review this change",
  "status": "running",
  "attachable": true,
  "createdAt": "2026-06-25T15:04:05Z",
  "updatedAt": "2026-06-25T15:04:06Z"
}
```

Error response:

```json
{
  "error": "launch_failed",
  "message": "agent binary is not available",
  "sessionId": "ses_abc123"
}
```

`sessionId` is OPTIONAL and MUST be present when a failed start record was
created. For a launch failure, `error` equals the failed session's stored
`failureCode`, and that session (via `GET /api/sessions/{id}`) carries both
`failureCode` and the human-readable `failureReason`. The `error` value is one
of the codes enumerated in §7.5.4.

#### 7.5.4 Validation and Status Codes

- Invalid JSON MUST return HTTP 400.
- Missing required fields MUST return HTTP 400.
- Unknown agent MUST return HTTP 400 and MUST NOT create a session record.
- Invalid params MUST return HTTP 400 and MUST NOT create a session record.
- Missing session id MUST return HTTP 404.
- Input/attach to non-live sessions SHOULD return HTTP 409 unless 404 is more
  accurate because the session id is unknown.
- Archive of a live session MUST return HTTP 409.
- Launch failure after record creation MUST return non-2xx with `sessionId`.

The `error` field MUST be one of the following stable codes. The set is closed
and evolves additively (§4.6); clients MAY switch on these values. A failed
session's `failureCode` is drawn from this same vocabulary.

- `invalid_request` (400) — malformed JSON, or a missing/invalid required field.
- `unknown_agent` (400) — agent not in the registry.
- `invalid_params` (400) — unknown param, wrong type, or invalid enum value.
- `session_not_found` (404) — unknown session id.
- `session_not_live` (409) — attach or input to a non-live session.
- `session_live` (409) — archive of a live session.
- `launch_failed` (non-2xx) — multiplexer launch failed after the record was
  created.
- `agent_misconfigured` (500) — the agent's registry config cannot produce a
  launch (operator fault).
- `internal_error` (500) — an unexpected internal fault.

### 7.6 WebSocket Attach Protocol

#### 7.6.1 Route

`GET /api/sessions/{id}/attach` upgrades to WebSocket.

#### 7.6.2 Frames

- Server-to-client terminal output MUST use binary frames.
- Client-to-server terminal input MUST use binary frames.
- Client-to-server control messages MUST use text JSON frames.
- Resize control message:

```json
{ "type": "resize", "cols": 120, "rows": 40 }
```

Invalid control messages MAY be ignored or MAY close the socket with a clear
protocol error; the selected behavior is implementation-defined.

The server MUST raise the WebSocket read limit high enough to accept terminal
pastes. The default frame limit is too small, and a large paste MUST NOT tear
down the bridge.

When the session ends while a client is attached — `terminate`, the agent
exiting, or reconciliation — the server MUST close the WebSocket with a coded
reason from this closed set so all clients render disconnects consistently:

- normal closure (1000) with reason `session_ended` — the session is no longer
  live.
- internal-error closure (1011) with reason `internal_error` — a server-side
  failure ended the bridge.

Clients use the close code/reason to distinguish a finished session from a
transient connection drop. There are no other server-to-client control frames;
terminal output is the only non-close server-to-client traffic.

#### 7.6.3 Auth

Attach MUST use the same backend authentication boundary as the rest of the API
(§8.2). Native clients (CLI, native apps) SHOULD authenticate the WebSocket
handshake with the normal `Authorization` header.

Browsers cannot set arbitrary handshake headers, so browser attach MUST present
the token via the `Sec-WebSocket-Protocol` handshake header (the WebSocket
subprotocol), and the server MUST read the token from there. The token MUST NOT
be placed in the WebSocket URL. This makes WebSocket auth exactly as strong as
REST auth — the same token, no new endpoint, and no token in logs or history.

A short-lived, single-use attach-ticket endpoint is the documented escalation if
a future hosted or multi-user mode ever needs a credential weaker than the
long-lived token. It is not part of this pass.

### 7.7 CLI

#### 7.7.1 Required Commands

- `cockpit agents list`
- `cockpit sessions start --agent <name> [--param key=value]... [--dir <path>] [--prompt <text>] [--name <name>]`
- `cockpit sessions list`
- `cockpit sessions show <id>`
- `cockpit sessions attach <id>`
- `cockpit sessions send-text <id> <text>`
- `cockpit sessions send-key <id> <key>`
- `cockpit sessions terminate <id>`
- `cockpit sessions archive <id>`

The singular `cockpit session ...` command group MAY be removed. Compatibility
is not required.

#### 7.7.2 Behavior

- CLI commands MUST call the service API over the Unix socket.
- CLI output SHOULD be concise and script-friendly.
- The data commands (`agents list`, `sessions list`, `sessions show`,
  `sessions start`) MUST support `-o, --output text|json`, with `text` as the
  default. The `json` output MUST match the corresponding API response shape, so
  the same structure is parsed whether scripting against the CLI or the API.
- Action commands (`sessions terminate`, `sessions archive`, `sessions
  send-text`, `sessions send-key`) MUST signal success/failure through exit
  status and print the affected session id.
- `sessions start` MUST print the created session id and enough status
  information for scripts to continue.
- `sessions list` MUST omit prompt and params, matching the API list contract.
- `sessions show` MUST include full detail.
- `sessions attach` SHOULD bridge the current terminal to the WebSocket attach
  stream. If raw-terminal support requires a new dependency, that dependency
  MUST be justified under this spec's dependency policy.

## 8. Cross-Cutting Requirements

### 8.1 Persistence

The system MUST persist:

- Session records.
- Accepted launch params.
- Starting prompts.
- User-facing session names.
- Safe failure reasons and failure codes.
- Lifecycle timestamps.
- Migration state.

The system MUST NOT persist:

- Terminal output, scrollback, transcripts, or summaries.
- Multiplexer names as public session properties.
- Raw internal launch errors in session records.
- Project/item associations.

### 8.2 Security and Permissions

- Existing Unix socket trust and TCP bearer-token behavior remain the API auth
  boundary.
- Safe user-facing error messages MUST avoid secrets.
- Agent launch commands MUST be built only from operator config and accepted
  closed params.
- Prompt text is the one free-form launch value. It MUST be passed as a single
  safely-quoted launch argument — one shell token, through the same quoting
  boundary as every other token — and MUST NOT be concatenated as raw shell.
  Closed params remain the only other caller-influenced tokens. No caller text
  may form unquoted shell.
- Database files and state directories SHOULD use owner-only permissions where
  the platform supports them.

### 8.3 Observability

The service SHOULD log:

- Database open and migration failures.
- Session start attempts and outcomes.
- Multiplexer launch/terminate failures.
- Attach failures.
- Reconciliation that marks a session failed or terminated.

Logs MAY include raw internal errors subject to existing secret-handling
requirements. Session records MUST store only safe failure reasons.

### 8.4 Diagnostics

- `GET /api/agents` MUST include per-agent availability diagnostics.
- Session list/read responses MUST include status and attachability.
- Existing `/api/health` remains the service-health endpoint.
- No generic diagnostics endpoint is required in this pass.

### 8.5 Reconciliation

The service SHOULD reconcile persisted session records with multiplexer liveness
on startup and on session list/read operations.

Reconciliation MUST act only on positive evidence from the multiplexer. If the
liveness query itself fails (e.g. the multiplexer is briefly unreachable),
reconciliation MUST NOT mutate any stored status — a transient query failure MUST
NEVER mark live sessions terminated. Reconciliation writes MUST be safe under
concurrent reads.

Reconciliation MUST NOT delete session records. On positive evidence it MAY
transition:

- a `starting` record whose multiplexer session is live to `running`;
- a `starting` record with no live session (e.g. after a crash) to `failed` with
  a safe reason;
- a non-archived `running` record positively observed as no longer live to
  `terminated`, setting `terminatedAt`.

Pre-existing sessions from the prior `cockpit:<kind>:<id>` scheme are not
adopted: they are absent from the store and do not match the new derived names,
so they become invisible to the new backend on upgrade. No migration of running
sessions is performed.

## 9. Interfaces and Integration Contracts

### 9.1 Agent Discovery

`GET /api/agents` MUST return configured agents even when unavailable.

Availability:

- `available`: the executable is currently discoverable enough for diagnostics.
- `unavailable`: the executable is known unavailable or misconfigured.
- `unknown`: availability cannot be determined cheaply or safely.

Availability is diagnostic metadata and MUST NOT be the only launch gate.
`start` still owns launch-time success/failure.

### 9.2 Session Start

Start validation order SHOULD be:

1. Decode request.
2. Validate required fields.
3. Validate agent exists.
4. Validate params against the agent schema.
5. If a prompt is supplied, validate the agent has a `prompt` placement.
6. Validate working directory is present.
7. Create `starting` record.
8. Launch through the multiplexer boundary, passing the prompt (if present) as a
   safely-quoted launch argument.
9. Transition to `running` or `failed`.
10. Return the full session, or a launch-failure error with `sessionId` and
    `failureCode`.

### 9.3 Session Archive and History

`GET /api/sessions` defaults to non-archived sessions, ordered `createdAt`
descending, with an optional `status` filter (§7.2.4). Archived sessions require
an explicit archive/history request. The exact query parameter or route for
including archived sessions is implementation-defined and MUST be documented
before implementation if added in this pass.

## 10. Acceptance Criteria

- Starting a valid configured agent creates a SQLite session record, launches
  the multiplexer session, returns a full `running` session object, and makes the
  session visible in `GET /api/sessions`.
- Starting with an optional prompt stores the prompt and passes it as a launch
  argument, so the agent starts working on it with no separate submit step.
- Starting a prompt for an agent with no `prompt` placement fails before launch.
- Starting with invalid agent/params fails before creating a session record.
- Launch failure after record creation leaves a visible `failed` session with a
  safe `failureReason` and a `failureCode`, and returns a non-2xx error whose
  `error` equals that `failureCode` and includes `sessionId`.
- `GET /api/agents` returns configured agents and does not require frontend
  hard-coding of `claude` or `codex`.
- `GET /api/sessions` omits prompt and params.
- `GET /api/sessions/{id}` includes prompt and accepted params.
- WebSocket attach streams terminal bytes as binary frames and accepts resize
  control JSON.
- `send-text` does not submit; `send-key enter` submits.
- `terminate` is idempotent and disables attach/input.
- `archive` rejects live sessions and succeeds for terminated or failed
  sessions.
- Archived sessions disappear from the default list and retain their underlying
  `terminated`/`failed` status (archive sets only `archivedAt`).
- `GET /api/sessions` is ordered newest-first and honors a `status` filter.
- A failed multiplexer liveness query does not mutate stored session status.
- Browser attach authenticates via `Sec-WebSocket-Protocol`; no token appears in
  the WebSocket URL.
- A session ending while attached closes the WebSocket with `session_ended`.
- Multiplexer names do not appear in API or CLI responses.
- `go test ./...` passes.

## 11. Verification Expectations

Unit tests SHOULD cover:

- Session id generation format and collision-resistance boundaries.
- Deterministic, recomputable id→name derivation in the multiplexer boundary.
- Agent param validation, defaults (including `enum` `default` ∈ `values`), and
  command construction, including the prompt passed as a single safely-quoted
  argument and the prompt-for-unsupported-agent error.
- Unknown agent and invalid params creating no session record.
- Store migrations and session persistence.
- Start success; launch failure recording `failureReason`/`failureCode`;
  `starting` reconciliation (live→running, dead→failed); and a liveness-query
  failure leaving stored status unmutated.
- List/detail response shaping, newest-first ordering, and the `status` filter.
- Terminate/archive idempotency and conflict behavior, including archive
  preserving the underlying status.
- Multiplexer boundary hiding private names.
- Attach WebSocket frame handling, resize control, and the `session_ended`/
  `internal_error` close codes.
- CLI command request/response behavior (including `-o json`) using a fake API
  server or socket.

Integration or smoke tests SHOULD cover:

- Starting a real session against a fake or real `zmx` wrapper in a temporary
  working directory.
- Web attach to a live session.
- Service startup migration against an empty temporary SQLite database.

## 12. Suggested Implementation Sequence

This section is advisory (non-normative). It SHOULD guide the build order so each
phase is an independently testable checkpoint that leaves `go test ./...` green;
an implementation that finds a better order is not in violation. The §4 profile
remains the contract.

1. **Store foundation** — `internal/store`: SQLite open, embedded migrations, the
   session table, CRUD, and the start transaction boundary, wired into startup
   (open-before-serve, fail-clear). Testable in isolation; no route changes yet.
2. **Identity + domain refactor** — opaque `ses_` `Session.id`, deterministic
   id→name derivation in the `zmx` boundary, persist records via the store, and
   drop scope-from-name identity.
3. **Agent discovery** — `GET /api/agents` and availability. Nearly independent;
   unblocks frontend de-hardcoding and can land early.
4. **New session routes** — resource-oriented `/api/sessions/{id}/…`, the new JSON
   shapes, the error-code vocabulary, and status codes; remove the old flat RPC
   routes.
5. **Reconciliation** — startup and on-read liveness reconciliation with the
   non-mutating-on-failure rules (§8.5).
6. **Attach protocol** — `/api/sessions/{id}/attach` with the frame protocol,
   `Sec-WebSocket-Protocol` auth, close codes, and the read-limit.
7. **CLI** — plural command groups over the API with `-o json`; remove the
   singular commands.

## 13. Definition of Done

- The new spec is implemented without relying on the old scoped `zmx` name as
  public identity.
- SQLite state store and migrations are wired into service startup.
- Agent discovery and session APIs match this spec.
- CLI command groups are plural and call the API.
- Old session RPC routes and singular CLI commands are removed or clearly
  unreachable.
- Tests cover the new persistence, API, CLI, and multiplexer-boundary behavior.
- Documentation or help text reflects `Session.id`, `Session Input`, and
  `sessions start`.

## 14. Open Questions

- Exact SQLite driver and migration library: implementation-defined within this
  spec's dependency constraints.
- Exact archived-history route/query shape: deferrable unless archived browsing
  is implemented in this pass.
