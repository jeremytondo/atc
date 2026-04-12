# Atelier Code: Technical Design
## Mission

Atelier Code delivers an uncompromising, AI-first modern coding experience. It combines the power of AI coding agents with the speed, polish, and ergonomics of a native SwiftUI interface.

## Product Domains
Atelier Code is organized around three product domains:

- **Agentic Coding**: Write code with AI agents.
- **Context Management (Future State)**: Create, organize, and manage context using a beautiful native markdown editor. 
- **Planning (Future State)**: Plan and manage product work, tasks, and execution.

This document is centered on the **Agentic Coding** domain. The other domains are important to the long-term vision, but their internal models remain intentionally high level for now.

___
# Agentic Coding
The Agentic Coding domain defines the system and UI for working with AI agents. It governs how users do things like create workspaces, conduct conversations, run turns, receive streaming updates, respond to approval requests, and continue work over time.

## Core System Components
The Agentic Coding domain is composed of four primary system components:

- **Client Application**: A native macOS application, with future support for iOS, that delivers the primary end-user experience. 
- **App Server**: The backend control plane responsible for managing sessions, persistence, approvals, real-time thread updates, and the stable client-facing protocol. It coordinates communication between the client application and supported agent runtimes and owns the Atelier product contract.
- **Agent Adapters**: A provider-specific integration layer that maps the App Server's Codex-shaped contract to the native interface exposed by an agent runtime. The Codex adapter should be near pass-through. Other adapters, such as Claude Agent SDK, translate agent-specific behavior into the Codex-shaped model used by the App Server. Adapters normalize agent differences upward but do not redefine the platform model.
- **Agent Runtimes**: The external agent systems that perform execution on behalf of the user, including maintaining the agent loop, invoking tools, and producing outputs. Examples include [Codex App Server](https://developers.openai.com/codex/app-server) and [Claude Agent SDK](https://platform.claude.com/docs/en/agent-sdk/overview).

### Note on the Client Application
The client application is out of scope for this document, however this is some context in case it affects decisions made for the App Server and Agent Adapters.

The client is responsible for connecting to the App Server and ensuring that the App Server is installed and running on the local machine or remote development host. On macOS, this likely means local installation when needed, background process launch, and local WebSocket connection. In future remote scenarios, such as iOS clients connecting to a remote development machine, the client may connect to a remote App Server over a secure network such as Tailscale.

The exact installation, startup, restart, and reconnection mechanism is intentionally out of scope for this document. The App Server contract should not depend on those client lifecycle mechanics beyond requiring a long-lived connection model and explicit initialization on each connection.

## Scope
This document is focused on the Atelier App Server and Agent Adapters for the Agentic Coding domain.

It defines the server contract, adapter responsibilities, and protocol model. Native client architecture and client-specific behaviors are intentionally out of scope and should be defined separately.

## Design Stance
The **[Codex App Server](https://developers.openai.com/codex/app-server)** is the reference model for the Atelier Code App Server.

The **Atelier Code App Server** should mirror Codex as closely as practical. Atelier Code is not defining a separate orchestration model; it is adopting a Codex-shaped contract and adding only Atelier-owned platform concerns such as workspace metadata, persistence, capability gating, and agent selection.

The **Codex adapter** should be mostly pass-through. Other adapters, such as **Claude Agent SDK**, should conform to the same Codex-shaped contract. When an agent runtime cannot fully support that contract, the limitation should be exposed through explicit capabilities rather than weakening the model.

### Codex App Server References 
* [Documentation](https://developers.openai.com/codex/app-server/)
* [Source Code](https://github.com/openai/codex/tree/main/codex-rs/app-server)

## Protocol
The Atelier **App Server** exposes a long-lived bidirectional **WebSocket** interface to the **Client Application** built around three protocol message types:
* **Requests**
* **Responses**
* **Notifications**

The protocol should remain JSON-RPC-shaped. Requests use `{ id, method, params }`. Responses echo `id` and return either `{ result }` or `{ error }`. Notifications use `{ method, params }` and do not include an `id`.

Method and notification names should remain slash-shaped where practical, using forms such as `thread/start`, `turn/start`, and `turn/started`.

In compact form:
* Request: `{ id, method, params }`
* Response: `{ id, result }` or `{ id, error }`
* Notification: `{ method, params }`

Atelier-specific methods should be limited to Atelier-owned concerns such as `Workspace` and related platform metadata.

## Core Primitives
The **Agentic Coding** domain is built around one Atelier-owned primitive, **Workspace**, layered on top of a Codex-shaped model built around **Thread**, **Turn**, and **Item**.
* **Workspace**: The top-level container for agent work within a specific code environment. It defines where work takes place, including the local or remote environment and working directory, and stores workspace metadata and associated threads.
* **Thread**: A conversation between the user and an agent. Threads contain turns.
* **Turn**: A single user request and the agent work that follows. Turns contain items and stream incremental updates.
* **Item**: A unit of input or output within a turn, such as a user message, agent message, command execution, file change, or tool call.

## Lifecycle Overview
* **Initialize once per connection**: After opening a WebSocket connection to the **Atelier App Server**, the client establishes the session and begins receiving notifications.
* **Open a workspace**: The client opens or creates a **Workspace** to establish Atelier-owned product context for execution.
* **Start or resume a thread**: The client starts, resumes, or forks a **Thread** within that workspace.
* **Begin a turn**: The client starts a **Turn** with user input.
* **Steer an active turn**: The client may append input to the active turn without creating a new one.
* **Stream updates**: While a turn is active, the server streams status changes, item updates, and requests for user action.
* **Read point-in-time state**: `thread/read` and `thread/list` return point-in-time state. Live execution state is surfaced through the notification stream.
* **Finish the turn**: The server emits the final turn state when execution completes, fails, is interrupted, or pauses for user action.

## Initialization
After opening a WebSocket connection to the **Atelier App Server**, the **Client Application** initializes the session before issuing other requests.

The Atelier App Server keeps the same general initialize-then-proceed shape as Codex, but uses initialization only for lightweight client/server compatibility, session setup, and readiness rather than broad protocol negotiation.

## API Overview
The Atelier **App Server** mirrors the core thread and turn operations of the **Codex App Server**. The API surface is centered on:
* thread/start
* thread/resume
* thread/fork
* thread/read
* thread/list
* thread/name/set
* thread/archive
* thread/unarchive
* turn/start
* turn/steer
* turn/interrupt
* model/list

The App Server does not attempt to mirror the full Codex API surface. Codex-specific admin, configuration, plugin, and auxiliary execution APIs remain outside the core Atelier protocol.

## Models
The **Atelier App Server** exposes the model catalog through a Codex-aligned model/list method.

The client only needs visible models, enough metadata to render the model picker, and the reasoning options supported by each model. The response should include the model identifier, display name, supported reasoning efforts, default reasoning effort, and whether the model is the agent runtime's recommended default.

New threads use the agent runtime default model. The client may change the model and reasoning effort for a thread, and that selection persists for subsequent turns until changed again.

The current model and reasoning effort for a thread should be exposed through thread data, not through model/list.

## Threads
A **Thread** is the conversation container for agent work. Threads are started with `thread/start`, resumed for active interaction with `thread/resume`, forked with `thread/fork`, read with `thread/read`, and listed with `thread/list`.

`thread/read` should remain distinct from `thread/resume`. `thread/read` returns stored thread detail only. It does not load the thread for active interaction and does not establish a live notification stream.

Adapter-facing contracts should model `thread/read` as a thread-detail response rather than a summary shape. When turn history is requested, the response includes typed `Turn` detail records alongside the thread metadata.

`thread/resume` loads the thread for active interaction. After resume, live execution updates should arrive through notifications rather than through repeated point-in-time reads.

`thread/list` and `thread/read` support history, navigation, and point-in-time inspection. They should not be treated as the canonical surface for active execution. 

Loaded-thread behavior should be represented through thread execution status and notifications, not as a separate platform primitive. Thread execution status should remain Codex-shaped, with states such as `notLoaded`, `idle`, `active`, and `systemError`, while live loaded-thread changes are surfaced through notifications.

## Turns
A **Turn** is a single unit of work within a thread. It begins with user input and includes the agent activity that follows.

Turns are started with turn/start, may be guided further with turn/steer, and may be interrupted with turn/interrupt.

A turn belongs to exactly one thread and is ordered within that thread. Only one turn may be active at a time per thread. A `Turn` is the core execution primitive. An active turn is not a separate concept; it is a turn whose status remains in progress or is awaiting user input.

### Turn Lifecycle
* A turn begins when the client issues turn/start.
* The server forwards the request to the selected agent runtime and begins streaming updates.
* While the turn is active, the server streams lifecycle changes, item events, and any requests for user action such as approvals.
* The client may send turn/steer to provide additional guidance to the active turn when supported by the agent runtime.
* A turn remains active while execution is in progress or awaiting required user input.
* A turn ends when it completes, fails, or is interrupted.
* The final turn state is emitted through the notification stream.

### Turn State
Turn status represents overall execution progress and is distinct from item-level updates. The App Server should stay aligned with the agent runtime's turn model where practical. An active turn is a turn whose status is still in progress or awaiting input. Any normalization needed for cross-agent consistency should be handled by the adapter layer.

## Events
Events are the live notification stream for thread lifecycle, turn lifecycle, item activity, and user-action requests. Read and list methods return point-in-time state; notifications are the canonical surface for active execution. After a thread is started or resumed, the client receives notifications as agent runtime state changes.

At a minimum, the event stream should cover:
* **Thread events** such as status changes, archive state changes, and closure.
* **Turn events** such as turn start, completion, plan updates, and diff updates.
* **Item events** such as item start, item completion, and item-specific delta streams.
* **Request resolution events** used to close the loop on approvals and other user-action requests.

Notifications are the canonical surface for loaded-thread and in-progress turn execution.

`turn/plan/updated` and `turn/diff/updated` are intentionally live-only in the current App Server phase. They should be streamed to active subscribers, but they are not yet part of `thread/read`, reconnect snapshots, or recovery replay state until the App Server has a durable active-turn recovery model for them.

`thread/status/changed` should carry thread execution state for loaded threads. `turn/started` and `turn/completed` should mark turn lifecycle boundaries. `item/started` and `item/completed` should mark item lifecycle boundaries.

When agent runtime activity pauses for approval or other user input, the App Server should surface the request through the notification stream and emit a resolution event when the request is answered or cleared.

Example happy-path flow:
* `initialize`
* `workspace/open`
* `thread/start`
* `thread/started`
* `turn/start`
* `turn/started`
* item message and item delta notifications
* `turn/completed`

### Items
Items are the units of input and output within a turn. They should remain Codex-shaped in the App Server and be surfaced through item lifecycle notifications rather than through a separate platform-specific model.

An item should have a stable identity within the turn, a concrete item type, and a final completed shape that the client can treat as authoritative. Some item types may also produce incremental delta events while work is in progress.

Item deltas may be streamed while work is in progress when the agent runtime supports them. These deltas improve responsiveness, but the final item/completed payload should remain the authoritative final state for that item.

The client should render live progress from deltas when available, but should reconcile to the final completed item state. The App Server should not require the client to reconstruct the durable item record solely from deltas.

## Approvals
Approvals let the **Atelier App Server** pause execution when the selected agent runtime requires explicit user consent before continuing.

Approval requests are surfaced by the server during an active turn and resolved by the client. They are scoped to the relevant **Thread**, **Turn**, and item or request identity, and should be presented as part of the active conversation rather than as a separate workflow.

At minimum, the system should support:
* **Command execution approvals** when the agent runtime requires permission before running a command.
* **File change approvals** when the agent runtime requires permission before applying edits.
* **Tool or connector approvals** when a tool call has side effects or otherwise requires explicit user confirmation.

The App Server should treat approvals as server-initiated requests. The client responds with a user decision, and the server then resumes, declines, or clears the pending work.

The approval lifecycle should remain Codex-shaped:
* The agent runtime emits an item that represents the pending action.
* The server sends an approval request to the client for that item.
* The client returns a decision.
* The server emits a resolution event.
* The item completes with its final outcome.

Example approval flow:
* item begins
* approval request is emitted
* client returns decision
* resolution event is emitted
* item completes

The App Server should preserve enough approval state to support active UI rendering, recovery, and auditability, but it should not invent a separate approval system when the agent runtime already provides the authoritative flow.

## Architecture & Standards

This section defines the module architecture, coding standards, and tooling decisions for the Atelier Code App Server.


### Module Architecture (App / Core / Features)

The App Server strictly separates infrastructure plumbing from domain business logic using a pragmatic **App / Core / Features** architecture. The primary organizational axis is **feature cohesion**: code that changes together lives together. Infrastructure remains generic and stable while domain logic scales with feature growth.

#### Feature Directories — Domain Logic

Feature directories live at the `src/` root alongside `app/` and `core/` (e.g., `src/workspaces/`, `src/threads/`, `src/turns/`, `src/agents/`). This layer contains the business value of Atelier Code. Code here represents features and entities.

**Contents:** TypeBox validation schemas, business logic services, protocol entry points (`*.handlers.ts`), and persistence logic (`store.ts` with Drizzle table definitions and query functions). Each feature owns its full persistence vertical.

**Rule:** Feature directories are entirely blind to the transport layer. They do not know if a request came from a WebSocket, an HTTP endpoint, or a local CLI. They receive parsed objects, execute logic, and return results.

When a feature directory exposes a composed runtime unit for `src/app/` to wire up, that exported unit may be named a **module** to distinguish the instantiated composition surface from the broader architectural concept of a feature.

**Agent adapters** live under `src/agents/`. Adapter logic is domain-meaningful — it defines how Atelier talks to a specific agent like Codex or Claude — not generic plumbing. Adapter interfaces are defined here, with concrete implementations per agent as sub-modules.

**Interdependencies:** Features may depend on each other's public interfaces (turns depend on threads), but only through explicit imports of typed contracts exported from the feature's `index.ts`. Features must not reach into each other's internals.

Each feature exposes a single barrel `index.ts` that exports its public API: handlers, service functions, and types. Internal helpers remain unexported. Barrel files should not be chained across directory levels — one per feature is the limit.

#### `src/core/` — Infrastructure Engine

This layer is generic, reusable plumbing that makes the server run. It is strictly isolated from domain logic.

- **`transport/`** — Manages raw socket connections (e.g., `websocket-server.ts`). It only emits and receives raw strings. It does not parse JSON or handle business logic.
- **`protocol/`** — The translation layer. It parses raw strings from the transport layer into JSON-RPC objects and uses a generic `Dispatcher` to route methods to registered feature handlers.
- **`store/`** — Database primitives: the connection handle, migration runner, and shared Drizzle configuration. It does not contain feature-specific table definitions or query logic — those live in each feature's `store.ts`.
- **`shared/`** — Truly generic utilities used across the application (e.g., custom error classes, ID generators).

#### `src/app/` — Orchestrator

This is the bootstrap layer and the **only** place in the codebase where `core/` and feature directories are allowed to interact.

- **App layer lifecycle** — The app layer owns process-level startup, shutdown, and healthcheck coordination. It initializes the core transport and protocol engines, registers feature handlers with the dispatcher, wires parsed events to the transport layer, and starts the listener. It contains zero per-request business logic.
- **Feature-owned runtime state** — Loaded threads, active turns, pending approvals, and other execution state live in the relevant feature directories, not in the app lifecycle surface.

#### Import Rules

- `core/transport/`, `core/protocol/`, and `core/store/` are peers. None imports from another. All three may import from `core/shared/`.
- Feature directories may import the database connection type from `core/store/` and utilities from `core/shared/`, but never import from `core/transport/` or any WebSocket-specific code. Within a feature, only `store.ts` should touch the database connection or Drizzle APIs directly. Feature services should depend on store functions or store-shaped capability objects, not on the database handle itself. `core/store/` never imports from features.
- `src/app/` is the only place that connects transport, protocol, store, and features. It creates the database connection from `core/store/` and passes it to each feature's store at startup.


### Server Framework

Use **raw `Bun.serve`** for the App Server shell. No HTTP framework (Hono, Elysia, etc.) is needed.

The App Server exposes a single long-lived WebSocket connection, not a REST API with routing and middleware. The JSON-RPC method dispatcher is the router. This is a small, protocol-critical surface that should be owned directly rather than abstracted behind a framework that provides no value for this use case.


### Schema & Validation

Use **TypeBox** as the schema and runtime validation library.

TypeBox produces JSON Schema natively and supports compiled runtime validation, which fits a protocol-heavy WebSocket server well. Drizzle has first-class TypeBox schema generation support, keeping the persistence and validation layers aligned without a mapping step.

Validation follows three layers:

1. **Envelope validation** — Parse and validate JSON-RPC-shaped message envelopes at ingress.
2. **Method payload validation** — Validate `params` and event payloads against method-specific TypeBox schemas.
3. **Execution rule validation** — Enforce lifecycle and state rules in domain services.

Inbound requests and outbound notifications should both be validated against canonical schemas. Validation failures map to stable, machine-readable error codes and message shapes. Execution rule violations (e.g., steering a non-active turn) must be distinct from schema parse failures.


### Error Handling

Domain services return typed `Result` values (see Coding Standards). The protocol layer is responsible for mapping those results to well-formed JSON-RPC responses — either `{ id, result }` for success or `{ id, error }` for failure.

The `error` object follows JSON-RPC spec: `code` is always numeric, `message` is a human-readable string, and `data` is an optional object for structured context. Standard JSON-RPC error codes (`-32700` parse error, `-32600` invalid request, `-32601` method not found, `-32602` invalid params) cover envelope and schema validation failures. Atelier-specific errors use a dedicated numeric range starting at `-33000`. Stable, machine-readable symbolic codes such as `TURN_NOT_ACTIVE` or `THREAD_NOT_FOUND` go in `error.data.code`, not in the top-level numeric `error.code` field.

The protocol layer should have a catch-all that ensures the client always receives a well-formed error response, even for unhandled failures. No request should result in a silent drop or malformed payload. Malformed outbound notifications have no response path in JSON-RPC — these should be logged and dropped.


### Logging & Observability

Use structured logging from day one. Log entries should be JSON-formatted with consistent fields: `timestamp`, `level`, `message`, and contextual correlation IDs (`connectionId`, `threadId`, `turnId`) where applicable.

The logger should be provided through feature-level context created at composition time, not imported as a process-wide global. This keeps feature code testable (swap in a silent or capturing logger in tests) and avoids hidden side effects.

Log levels: `debug` for internal tracing, `info` for lifecycle events (connection opened, thread started, turn completed), `warn` for recoverable issues (validation failure, stale approval), `error` for infrastructure failures (database write failed, WebSocket send failed).


### Configuration

Configuration is loaded once at startup from a configuration file, with environment variable overrides for values that vary by deployment context (e.g., port, database path in CI).

The configuration file is the primary source for settings such as server port, database location, available agents, and feature flags. Environment variables may override specific values when needed but should not be the default mechanism for most settings.

Configuration should be read into a typed, readonly config object during bootstrap in the app layer and passed explicitly to the subsystems that need it. Feature code should never read environment variables or configuration files directly.


### Graceful Shutdown

The server process should handle `SIGINT` and `SIGTERM` signals and shut down cleanly. Graceful shutdown means: stop accepting new WebSocket connections, interrupt or complete any active turns, flush pending store writes, close the database connection, and then exit.

The shutdown sequence should be coordinated from the app layer, calling into features and core subsystems in the correct order. If shutdown takes longer than a reasonable timeout, force exit to avoid hanging indefinitely.


### Path Aliases

Use a single TypeScript path alias configured in `tsconfig.json` to keep imports clean across the project:

```json
{
  "compilerOptions": {
    "paths": {
      "@/*": ["./src/*"]
    }
  }
}
```

This covers all directories without requiring a new alias each time a feature is added, and it works without a separate `baseUrl` setting. Relative imports are fine within a single feature directory; use the `@/` alias for cross-feature and cross-layer imports.


### Coding Standards

#### Functions and Data Over Classes

The default unit of composition is a function that takes data and returns data. Services are modules that export functions, not class instances. Classes are reserved for genuinely stateful things — a WebSocket connection, a database handle — not used as namespaces for grouping methods.

#### Colocation Over Premature Abstraction

Keep code close to where it's used until duplication actually causes pain. A schema, its handler, and its service logic living in the same feature directory is the intended outcome of this architecture. Do not extract shared abstractions preemptively.

#### Types as Documentation

With strict TypeScript and TypeBox schemas at boundaries, types replace much of what comments and naming conventions traditionally provided. Discriminated unions for events, branded types for IDs, and `readonly` by default communicate constraints more reliably than prose. If a comment is explaining what a value can be, it should be a better type instead. Comments still have a role for non-obvious intent — protocol design decisions, lifecycle invariants, or "why" context that types alone cannot express.

#### Explicit Over Clever

Prefer `if`/`else` or `switch` over complex ternary chains. Prefer named intermediate variables over long method chains when readability suffers. Prefer early returns over nested conditionals. Code should be traceable linearly without jumping around.

#### Errors as Values

For domain logic, return result types rather than throwing:

```typescript
type Result<T, E = AppError> =
  | { ok: true; data: T }
  | { ok: false; error: E }
```

This makes error paths visible in the type system and forces callers to handle them. Reserve `throw` for genuinely exceptional infrastructure failures (database unreachable, WebSocket write failure). Domain-level failures such as "turn is not active" or "thread not found" are expected outcomes, not exceptions.

#### Immutability by Default

Use `readonly` on types, `as const` on literals, and avoid mutation in domain logic. When state needs to change, return a new value. Mutable state lives only at the edges — the store, the WebSocket connection — not in domain functions transforming data.

#### Dependency Injection Without Magic

Pass dependencies as function arguments or use a context object. The `app/server.ts` composition root wires things together at startup. No DI containers, decorators, or reflection.

#### Small Files, Explicit Exports

Each file has a single responsibility and explicitly exports its public surface. Each feature exposes one `index.ts` barrel file exporting its public API. Internal helpers stay unexported. Do not chain barrel files across directory levels.

#### Naming Conventions

**Qualify "runtime."** The bare word "runtime" is ambiguous — it can mean the server process, an external agent system, or an operational status. Always qualify it: **agent runtime** for external agent systems (Codex, Claude), **agent adapter** for agent-specific integration code, **execution status** or **thread status** for operational state, and **server process** for the running App Server. In code (type names, variable names, module names), prefer **agent** as the short form when context is clear.

**Keep "Codex" out of shared code.** Use "Codex" only in adapter-specific code under `src/agents/` or for literal provider and model values. Everywhere else in the App Server, use generic names. The App Server contract is Codex-shaped, but shared code should not be coupled to a specific agent's name.


### Testing Strategy

The App Server should be covered by five complementary test layers:

**Protocol harness tests** are the primary executable check for the public Atelier contract. These are end-to-end WebSocket tests that connect to the running server process, send JSON-RPC-shaped requests, and assert response shapes, protocol errors, and notification ordering for both happy-path and invalid-sequencing cases.

**Domain service tests** are focused unit tests for thread, turn, and approval lifecycle rules using fake stores and fake agent adapters. These verify invariants such as initialize-before-use, one active turn per thread, approval scoping, and state transition correctness without requiring a live socket. Domain tests should cover execution-rule failures separately from schema validation failures.

**Store tests** verify each feature's persistence logic against both its Drizzle implementation and its in-memory test fake. SQLite coverage should include migration application, restart reload, duplicate mapping protection, and failure behavior for missing or stale linkage.

**Agent adapter contract tests** validate request mapping, response normalization, notification translation, and error handling against pinned Codex contract fixtures where possible. Adapter tests should prefer pinned fixtures for determinism.

**Live smoke tests** are environment-gated tests against a real local Codex setup for the minimal lifecycle once the real adapter exists. These should stay small and verify integration seams rather than replace deterministic contract coverage.


### Tooling

| Concern         | Tool              | Notes                                                                 |
|-----------------|-------------------|-----------------------------------------------------------------------|
| Execution       | Bun               | Server process, test runner, script execution.                        |
| Language        | TypeScript (strict) | No `any`. Prefer `unknown`, explicit narrowing, discriminated unions. |
| Server          | `Bun.serve`       | Raw WebSocket server. No HTTP framework.                              |
| Validation      | TypeBox           | Runtime validation with native JSON Schema output.                    |
| Persistence     | Drizzle ORM + SQLite | Feature-owned stores. In-memory fakes for testing.                 |
| Formatting      | Biome             | Single formatter and linter for the App Server.                       |
| Testing         | `bun:test`        | Bun's built-in test runner.                                          |


### Persistence Strategy

Each feature owns its persistence logic. A feature's `store.ts` contains its Drizzle table definitions and query functions, and it is the only place in that feature allowed to touch the database handle or Drizzle APIs directly. Feature services consume store functions or store-shaped capability objects so they remain transport-agnostic and testable. For testing, in-memory fakes are plain objects that satisfy the same structural type — no separate interface file needed. `core/store/` provides only the database connection, migration runner, and shared Drizzle configuration.

Use **Drizzle ORM with SQLite** for the first durable implementation. Drizzle's migration tooling can scan table definitions across multiple feature directories.

Generate and apply schema migrations using the Drizzle migration workflow. Keep migration files checked into the repository under the App Server module so schema evolution is reviewable and reproducible. Apply migrations at process startup before accepting WebSocket traffic. Treat failed or partial migrations as startup failures rather than attempting best-effort recovery.

Limit the persisted schema in early phases to Atelier-owned metadata and reattachment state, not mirrored thread/turn/item history.


### Deferred Decisions

The following decisions are intentionally deferred and should be captured as separate follow-up work:

- Client-managed App Server installation, startup, restart, and reconnection behavior across local macOS and future remote-host scenarios.
- Secure remote connection and discovery details for non-local App Server deployments.
- Broader multi-agent selection and capability-gating behavior beyond the initial Codex-only rollout.


### Readiness Criteria

The implementation is ready for broader integration when:

- The App Server can accept a WebSocket connection, initialize, and route a minimal method set.
- Thread and turn lifecycle state transitions can be exercised end-to-end in tests.
- Approval request and resolution flow can be simulated through notifications and decisions.
- Feature services depend on store functions or store-shaped capability objects rather than direct database access, and can be tested with in-memory fakes.
- Protocol harness tests can assert contract shape and lifecycle sequencing.
