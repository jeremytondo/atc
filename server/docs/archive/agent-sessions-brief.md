> Archived: superseded by `docs/plans/sessions-actions-environments.md` and `docs/specs/sessions-actions-environments.md`.

# Cockpit Backend Agent Sessions Brief

Status: Draft

## Purpose

Build a solid backend for starting, tracking, sending input to, and attaching
to terminal-based agentic coding sessions. The backend should be reliable
enough for future web and native Mac interfaces to build on without each UI
inventing its own session semantics.

## Idea Definition

Cockpit should expose a local service API for working with coding agents such as
Claude Code and Codex through their real CLI/TUI processes. The service should
launch configured agents, keep enough metadata to explain what is running, expose
session lifecycle and attach operations, and keep the terminal multiplexer
implementation behind a narrow internal boundary.

The current backend proves the core loop: launch a registered agent in `zmx`,
list Cockpit-managed sessions, inject text/keys, and attach over a WebSocket
terminal bridge. The next backend iteration should turn that proof loop into a
stable product contract.

## Recommended Direction

- Focus on backend correctness and API shape before investing in UI design.
- Treat the current session API as a starting point, not the final domain model.
- Keep `Session` as the top-level API concept. Add agent discovery beside the
  session lifecycle API rather than introducing agent-owned sessions.
- Decouple session identity from projects and items. Do not model project/item
  links in this backend pass; leave that integration deferred.
- Introduce a Cockpit-owned stable session id as the public identifier. Clients
  and higher-level backend packages should not know or persist multiplexer
  handles.
- Session ids should be opaque, generated, URL-safe strings with a short prefix
  such as `ses_`. They should be stable and readable in logs, but not encode
  project, item, agent, creation time, or multiplexer details.
- Sessions may have an optional user-facing `name` for display in lists,
  titles, and CLI output. `id` remains the stable API identity; multiplexer
  names remain private.
- Introduce a small Cockpit-owned session registry keyed by the stable Cockpit
  session id. Use `zmx` for liveness and PTY ownership, but store Cockpit
  metadata that `zmx` cannot recover.
- Let the multiplexer boundary map Cockpit session ids to its private session
  names. Do not encode project, item, agent, or other human-facing details in
  those names.
- Promote WebSocket attach behavior into an explicit backend contract before
  native or browser clients depend on it.
- Keep `zmx` replaceable by preserving the `internal/zmx` boundary and avoiding
  API contracts that leak `zmx` implementation details.
- Keep `zmx`-specific behavior inside `internal/zmx`. Other packages may speak
  in generic session ids and PTY streams, but should not depend on `zmx` command
  shapes, flags, output quirks, names, or package-specific types.

## Exploration Tasks

- Define the session-centered API organization: sessions own lifecycle, attach,
  input, and status; agents are discoverable launch configuration.
- Define stable HTTP and CLI operation names around `Session.id`, using `start`
  consistently for launching a session.
- Define the Cockpit-owned session identity contract and make multiplexer handle
  mapping private to the multiplexer boundary.
- Audit the current package boundary so the session domain depends on generic
  multiplexer and PTY contracts rather than `zmx`-specific package types.
- Define the session properties that replace the legacy project/item/free
  scoping model.
- Define the backend session-manager contract: session list shape, attach
  protocol, lifecycle operations, events, diagnostics, and auth behavior.
- Define the first persistent session registry around Cockpit-owned metadata:
  `id`, `agent`, validated `params`, `workingDir`, optional `prompt`, optional
  user-facing `name`, persisted start status, optional failure reason,
  `createdAt`, `updatedAt`, and lifecycle timestamps for archive/termination
  when those operations exist.
- Back the session registry with a local SQLite database. Keep the initial
  schema small and session-focused, but treat SQLite as the likely foundation
  for Cockpit-owned application state rather than a one-off session file.
- Introduce a single Cockpit state-store boundary for SQLite ownership:
  connection lifecycle, migrations, and transactions live there, even if the
  only initial table is for sessions.
- Use an established Go migration library for SQLite migrations. Commit ordered
  SQL migration files, embed them into the binary, and apply them through the
  store boundary at startup and in tests. Do not build a custom migration runner
  unless the chosen tool cannot meet the local embedded use case.
- Apply pending SQLite migrations automatically during service startup. Startup
  should fail clearly if the database cannot be opened or migrated.
- Store the SQLite database under Cockpit's XDG state directory, such as
  `$XDG_STATE_HOME/cockpit/cockpit.db` with a fallback to
  `~/.local/state/cockpit/cockpit.db`. Do not place persistent state under the
  runtime/control directory used for sockets and PID files.
- Support a config/env override for the SQLite database path for tests,
  alternate profiles, and portable setups. Do not add a CLI flag unless a
  concrete workflow needs it.

## Key Features

- Agent discovery: require `GET /agents` to list configured agents, their
  parameter schemas, defaults, display metadata, and diagnostic availability
  before more UI work depends on agent choices.
  Display metadata should stay small: agent `label`, optional agent
  `description`, and optional parameter labels/descriptions.
- Session start: launch a configured agent with validated parameters and a
  working directory. `start` may include an optional `prompt`; the backend
  stores it as session metadata and passes it to the agent as a single
  safely-quoted launch argument (per the agent's `prompt` placement), so the
  agent receives it at startup and begins working — there is no separate
  inject-or-submit step for the initial prompt. `start` may also include an
  optional user-facing `name`. Follow-up input after launch uses `send text`/
  `send key`.
- Session start should create a durable session record before launching the
  multiplexer session, mark it `starting`, then mark it `running` on launch
  success or `failed` with a safe user-facing failure reason on launch failure.
- `POST /sessions/start` is synchronous for clients in this pass: it returns
  after launch has either reached `running` or `failed`.
- If launch fails after a session record is created, `POST /sessions/start`
  should return a non-2xx error with a safe message and include the failed
  `sessionId` in the error body so clients can show the recorded failure.
- On success, `POST /sessions/start` should return the full session object, not
  only the id, so clients do not need an immediate follow-up read.
- Session list: return running and recently known sessions with status,
  reachability, created time, command/agent metadata, working directory, and
  attach availability.
- Session list defaults to non-archived sessions, including both live and
  terminated history and failed starts. Each item should expose session status
  and derived attach availability. Archived sessions require an explicit
  archive/history request. List responses should stay scannable: id, optional
  user-facing name, agent, working directory, status, attachability, timestamps,
  and failure reason for failed starts when present. Do not include prompt text
  or accepted params.
- Session attach: define the bidirectional terminal stream contract, resize
  messages, close behavior, auth expectations, and error reporting.
- Session attach should use a direct WebSocket upgrade at
  `GET /sessions/{id}/attach`; do not introduce a separate attach setup/ticket
  endpoint in this pass. Browser attach auth must be handled through the normal
  app auth boundary without casually exposing long-lived API tokens in URLs.
- Attach frame protocol: server-to-client terminal output uses binary frames;
  client-to-server terminal input uses binary frames; client-to-server control
  messages use text JSON frames, starting with
  `{ "type": "resize", "cols": 120, "rows": 40 }`.
- Multiple clients may attach to the same live session when the multiplexer
  supports it. Cockpit should not add an exclusive attach lock in this pass;
  input arbitration is deferred to multiplexer behavior and user expectations.
- `send-text` and `send-key` remain available while WebSocket clients are
  attached. Input ordering is best-effort by arrival at the multiplexer boundary;
  Cockpit does not coordinate concurrent writers in this single-developer
  backend pass.
- Session input: support `send text` and `send key` operations through a stable
  API. `send text` injects bytes without submitting; `send key` injects a named
  key such as `enter`, `ctrl-c`, or `escape`.
- Session lifecycle: add explicit terminate and archive semantics rather than
  relying only on multiplexer state.
- Session terminate: request the multiplexer boundary to stop the running
  terminal session for a `Session.id`; mark `terminatedAt` when the terminal
  session is no longer reachable. After termination, attach and session input
  operations fail, but the session record remains visible in history until
  archived. Terminate is idempotent and succeeds as a no-op for sessions with no
  live terminal session, including failed starts.
- Session archive: set `archivedAt` for a session with no live terminal session
  and remove it from default active/history views. Archive is metadata-only and
  must reject live sessions; clients may compose terminate-then-archive as a
  convenience.
- Runtime diagnostics: expose diagnostics where clients need them: per-agent
  availability in `GET /agents`, per-session status and attachability in
  session reads/lists, and existing service health through `/health`. Avoid a
  new generic diagnostics endpoint in this pass.

## Non-Goals / Deferred Ideas

- UI design is deferred until the backend contract is clearer.
- Native Mac implementation is deferred, except for a future focused spike to
  validate terminal embedding and attach behavior.
- Project and item integration is deferred as a relationship layer, not removed
  from the long-term product.
- Structured agent protocols such as ACP are not part of this backend pass.
- Persisting terminal output, scrollback, transcripts, or generated summaries is
  not part of this backend pass.
- Session events/SSE are deferred. Clients can poll `GET /sessions` until the
  list/status model is stable and a real UI proves events are needed.
- Multi-user tenancy, hosted deployment, and shared remote sessions are outside
  the current local-workstation scope.
- Concurrent input arbitration beyond the multiplexer behavior is deferred; the
  app is scoped to a single developer's workstation.

## System Shape

- **Agent Registry**: Defines launchable agents, binaries, fixed args, typed
  parameters, and client-facing metadata.
- **Agent/Session API**: Public HTTP and CLI contract for discovery, start,
  list, read, attach, session input, lifecycle, and diagnostics.
- **Session Domain**: Owns Cockpit session identity, validation, metadata,
  lifecycle rules, and mapping between API concepts and multiplexer operations.
- **Session Registry Store**: Persists Cockpit-owned metadata that cannot be
  derived from the multiplexer, such as agent name, validated params, working
  directory, originating prompt, optional user-facing session name, and
  timestamps. The first implementation should use a local SQLite database.
- **Cockpit Store**: Owns the SQLite connection, migrations, and transaction
  boundary for Cockpit-owned application state. The first table can be
  session-focused; the boundary should not become a generic ORM. Migrations
  should use an established Go migration library with embedded SQL files.
- **Session Properties**: Describe the intrinsic facts of a session, such as
  id, optional user-facing name, agent, validated params, working directory,
  originating prompt, status, optional failure reason, and timestamps.
- **Multiplexer Boundary**: Owns all `zmx` process, list, send, attach, resize,
  and lifecycle integration. `zmx` command behavior and package-specific types
  should not leak past this boundary.
- **Attach Bridge**: Translates backend PTY streams into a stable client
  protocol for web and native clients.
- **Auth and Transport Layer**: Keeps Unix socket trust, TCP bearer auth, and
  WebSocket attach auth behavior explicit.

## Core Concepts

- **Agent**: A configured launchable coding tool, such as Claude Code or Codex.
- **Session**: A persistent terminal instance created to run an agent or other
  registered command.
- **Session Properties**: Intrinsic facts about a session, such as agent,
  params, working directory, prompt, status, optional failure reason,
  timestamps, and optional user-facing name.
- **Multiplexer**: The external terminal/session owner, currently `zmx`.
- **Attach**: A live terminal connection to an existing session.
- **Session Input**: Text or named keys injected into a session terminal.

## Confirmed Decisions

- Backend hardening comes before UI design.
- The public backend API should be session-centered. Agents should be exposed
  through discovery and launch configuration, not as owners of session lifecycle.
- The stable HTTP API should use consistent domain operation names around
  `Session.id`: `GET /agents`, `POST /sessions/start`, `GET /sessions`,
  `GET /sessions/{id}`, `GET /sessions/{id}/attach`,
  `POST /sessions/{id}/send-text`, `POST /sessions/{id}/send-key`,
  `POST /sessions/{id}/terminate`, and `POST /sessions/{id}/archive`.
- The stable CLI should mirror those operation names where useful:
  `cockpit agents list`, `cockpit sessions start`, `cockpit sessions list`,
  `cockpit sessions show <id>`, `cockpit sessions attach <id>`,
  `cockpit sessions send-text <id> ...`, `cockpit sessions send-key <id> ...`,
  `cockpit sessions terminate <id>`, and `cockpit sessions archive <id>`.
- `start`, not `create`, is the canonical operation name because it launches a
  real terminal session, not just a persisted record.
- The older routes (`/sessions/send`, `/sessions/key`) and singular
  `cockpit session ...` CLI do not need compatibility support and can be removed
  when the new contract is implemented.
- Sessions should expose a stable Cockpit-owned id as their public identity.
  Multiplexer handles are private to the multiplexer boundary.
- Session ids should be opaque, generated, URL-safe, prefixed strings, and
  should not encode project, item, agent, creation time, or multiplexer details.
- Sessions may have an optional user-facing `name`; this is display metadata,
  not identity. `Session.id` remains the stable API reference.
- Session `name` can be set at start and returned by session reads/lists.
  Rename/update endpoints are deferred.
- Multiplexer names should not encode project, item, agent, or other
  human-facing details; those details belong in Cockpit metadata.
- `zmx`-specific behavior belongs inside `internal/zmx`. The rest of the
  backend should depend on generic multiplexer and PTY contracts.
- The new public contract should model session properties only. It should not
  expose `Scope`, introduce `Context` as a separate concept, or include
  project/item relationships in this backend pass.
- The first persistent session registry should store only Cockpit-owned session
  metadata: `id`, `agent`, validated `params`, `workingDir`, optional `prompt`,
  optional user-facing `name`, persisted start status, optional failure reason,
  `createdAt`, `updatedAt`, and lifecycle timestamps when those lifecycle
  operations exist.
- The session registry should be backed by SQLite. Keep the first schema tiny
  and session-focused, while leaving room for SQLite to become the app-wide
  Cockpit state store.
- SQLite should enter through a single Cockpit state-store boundary that owns
  connection lifecycle, migrations, and transactions. Avoid separate ad hoc
  files or persistence mechanisms per subsystem.
- SQLite schema changes should use an established migration library with
  embedded ordered SQL migration files. Avoid growing a custom migration runner.
- The service should apply pending migrations automatically on startup and fail
  clearly when migration fails.
- The SQLite database should live under the XDG state directory, not the
  runtime/control directory. The existing socket/PID directory remains runtime
  state only.
- The SQLite database path should be overrideable through config/env for tests
  and alternate profiles, but should not get a CLI flag by default.
- The public operation language should use `Session Input`, `send text`, and
  `send key`; avoid exposing `Drive` as a backend operation name.
- `terminate` should stop the running terminal session through the multiplexer
  boundary, set `terminatedAt` once the terminal is no longer reachable, and be
  idempotent for already terminated or failed sessions. After termination,
  attach and session input operations fail.
- `archive` should be metadata-only, require no live terminal session, set
  `archivedAt`, and remove the session from default active/history views. Failed
  starts are archiveable immediately.
- Attach should use the same backend authentication boundary as the rest of the
  API, including WebSocket upgrade requests. Do not introduce short-lived attach
  tickets in this backend pass.
- Attach should be a direct WebSocket upgrade at `GET /sessions/{id}/attach`.
  A separate attach-ticket/details call is deferred unless browser/native auth
  constraints force it.
- Attach frames should stay simple: terminal bytes use binary frames in both
  directions, and client control messages use text JSON frames such as resize.
- Multiple simultaneous attaches are allowed when supported by the multiplexer;
  Cockpit should not add an exclusive attach lock in this pass.
- Session input remains available while clients are attached. Cockpit does not
  coordinate concurrent writers beyond best-effort arrival order at the
  multiplexer boundary.
- `start` should create a durable record before launching, then transition
  `starting` to `running` or `failed`. Failed starts remain visible with a safe
  failure reason so users can see what happened.
- `POST /sessions/start` should wait for immediate launch success/failure before
  returning; asynchronous start semantics are deferred.
- Failed starts should return non-2xx responses while still preserving the
  failed session record. Include the failed `sessionId` in the error body when
  one exists.
- Failed session records should store only safe user-facing failure reasons.
  Raw internal errors belong in logs, not long-lived session state.
- Successful starts should return the full session object.
- `GET /sessions` should not include prompt text. `GET /sessions/{id}` may
  return the stored starting prompt.
- `GET /sessions` should omit accepted launch params; use `GET /sessions/{id}`
  for full launch detail.
- `GET /sessions/{id}` should include the full session detail: agent, accepted
  launch params, working directory, optional user-facing name, stored starting
  prompt, timestamps, status, failure reason when present, and attachability.
- Cockpit should not persist terminal output in this backend pass. The live
  attach stream is the only terminal-output path; the registry may store the
  starting prompt but not ongoing scrollback, transcripts, or summaries.
- Default session listing should return non-archived sessions, including live
  sessions, terminated history, and failed starts. Attach availability is
  derived from the multiplexer; persisted status records start outcomes and
  lifecycle transitions.
- Session events/SSE are deferred; clients should poll `GET /sessions` in this
  backend pass.
- Agent discovery is required in this backend pass. `GET /agents` should be the
  client source of truth for configured agents and their launch parameters, so
  UIs do not hard-code default agents such as `claude` and `codex`.
- `GET /agents` should report binary availability as diagnostic metadata, such
  as `availability: "available"|"unavailable"|"unknown"`, without hiding
  configured agents or turning discovery into a launch preflight gate. Start
  should still fail clearly if the actual launch cannot run.
- Diagnostics should be embedded in the relevant resources for this pass:
  agent availability on agents, session status/attachability on sessions, and
  service health on `/health`. Do not add a generic diagnostics endpoint yet.
- The agent registry should support small display metadata: agent `label`,
  optional agent `description`, and optional parameter labels/descriptions.
  Icons, categories, rich UI hints, and agent-specific behavior flags are
  deferred.
- `start` should accept an optional `prompt`. Cockpit stores the prompt as
  session metadata and passes it to the agent as a single safely-quoted launch
  argument per the agent's `prompt` placement; the agent acts on it at startup
  with no separate inject-or-submit step. Follow-up input after launch uses
  `send text`/`send key`.
- The first real feature should test starting, tracking, and connecting to
  agentic coding sessions using Claude Code and Codex CLIs.

## Open Questions
