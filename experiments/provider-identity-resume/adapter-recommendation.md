# Agent Provider Adapter recommendation

Status: final recommendation for
[ATC-68](https://linear.app/elevenideas/issue/ATC-68/poc-provider-identity-and-resume-with-codex-app-server-and-claude)

## Decision

Use provider-native drivers for ATC's two first-class agents:

- Codex uses one supervised, long-lived `codex app-server` process per ATC
  server profile. It may multiplex multiple threads, with every event
  correlated by `threadId`.
- Claude uses the Claude Agent SDK behind a small supervised TypeScript helper.
  The helper owns one streaming `query()` writer per connected Claude session
  and exposes a provider-neutral local control protocol to the Go server.

Do not put provider transports, event shapes, IDs, or version details in the
frontend contract. The Go server owns durable Agent Sessions and calls both
drivers through one internal Agent Provider Adapter boundary.

The Claude SDK helper is recommended over direct CLI JSON parsing because the
tested SDK surface provides typed permission callbacks, request correlation,
lifecycle events, interruption, and exact-session resume. The helper is an
internal implementation detail, not a client-facing service.

ACP remains a possible future adapter for other agents. It is not the
lowest-common-denominator contract for Codex or Claude.

## Ownership boundaries

ATC owns:

- the public Agent Session ID;
- the association with a Workspace and canonical working directory;
- lifecycle, activity, pending-request, and active-driver state;
- the single-writer rule;
- normalized events and server-side fan-out to front ends;
- linked Terminal Session identity; and
- safe errors and retry policy.

The provider owns:

- its conversation or thread ID;
- provider turns and request IDs;
- native lifecycle and input events;
- provider history; and
- its TUI resume behavior.

The persisted provider ID is private integration metadata. Front ends address
only the ATC Agent Session ID. Provider IDs and raw events may appear in
operator diagnostics, but client correctness must not depend on them.

An Agent Session and a Terminal Session remain separate records. Ending a
Terminal Session does not end or delete the Agent Session or provider
conversation.

## Minimal internal interface

The production interface should be small and behavior-oriented. The following
Go-shaped pseudocode defines the boundary; concrete wire types may differ:

```go
type Adapter interface {
    Create(context.Context, CreateRequest) (Connection, error)
    Resume(context.Context, ResumeRequest) (Connection, error)
}

type Connection interface {
    Session() ProviderSession
    Send(context.Context, SendRequest) (TurnRef, error)
    Interrupt(context.Context, InterruptRequest) (InterruptResult, error)
    Respond(context.Context, RespondRequest) error
    Events() <-chan Event
    Close(context.Context) error
}

type ProviderSession struct {
    ProviderID       string
    WorkingDirectory string
    ResumeReady      bool
}
```

`CreateRequest` contains the canonical working directory, provider
configuration, and the initial input when the provider cannot durably create
an empty conversation. `ResumeRequest` contains a required nonblank expected
provider ID and the expected canonical working directory.

`Send` starts exactly one turn and returns its correlation reference.
`Interrupt` must target that exact active turn; it must never mean "interrupt
whatever is active when this request arrives." `Respond` must target one exact
pending request. A duplicate, stale, mismatched, or already-resolved response
fails closed.

`Events` is the only observe primitive at the provider boundary. It carries
events from the active connection. ATC fans normalized events out to multiple
frontend observers. It must never implement observation by calling `Resume`
again or opening a second provider writer.

`Close` releases the driver without deleting provider history. Create and
resume are distinct calls; neither may fall back to the other.

## Normalized events and state

The adapter should emit only the stable facts ATC needs while retaining the
raw provider event for diagnostics:

```text
connection_started
connection_stopped
activity_changed(idle | working | needs_input | unknown)
turn_started
turn_completed(completed | interrupted | failed)
request_opened(question | approval)
request_closed
protocol_error
```

Every normalized event carries:

- the ATC connection generation;
- the exact provider session ID;
- an ATC turn ID and provider turn ID when applicable;
- an ATC request ID and all provider correlation IDs when applicable; and
- the raw provider event or an immutable reference to it.

Connection generation prevents late events from an old process from changing
the state of a newer connection.

Agent Session lifecycle and agent activity remain separate:

| Dimension | Values | Authority |
| --- | --- | --- |
| Lifecycle | `starting`, `running`, `stopped`, `failed` | ATC process supervision and verified create/resume outcomes |
| Activity | `idle`, `working`, `needs_input`, `unknown` | Structured events from the active provider connection |

No active native connection means activity is `unknown`, not inferred from
provider transcripts or terminal output. A linked Terminal Session has its own
independent `live` / `ended` lifecycle.

When a request is pending, `needs_input` takes precedence over a provider's
more general active/running state. A request closes only after its correlated
response is accepted or its turn terminates.

## Provider mappings

### Codex App Server

| Provider evidence | Adapter result |
| --- | --- |
| `thread/status/changed` → `active` | `working` |
| `thread/status/changed` → `idle` | `idle` |
| `waitingOnUserInput` plus correlated server request | `needs_input`, kind `question` |
| `waitingOnApproval` plus correlated server request | `needs_input`, kind `approval` |
| successful `turn/interrupt` response plus terminal `interrupted` | interrupted turn |

Implementation constraints:

- Use one long-lived app-server process and correlate all turn, item, request,
  and status events by exact thread and turn IDs.
- Enforce one active writer per thread even though app-server may accept a
  concurrent second writer.
- A `thread/start` response with no turns is provisional. Set `ResumeReady`
  only after the first turn reaches a provider terminal state. Until a future
  experiment proves an earlier persistence boundary, do not claim that an
  empty or in-flight first turn is restart-resumable.
- Do not return successful durable Agent Session creation until the provider ID
  and cwd are exact and the service has represented the provisional state.
- `request_user_input` requires the experimental API and the advertised Plan
  collaboration preset. Advertise structured questions as a mode-dependent
  capability. Approval requests do not require Plan mode.
- Correlate a response by app-server request ID, thread ID, turn ID, and ATC
  connection generation.
- Treat a successful interrupt receipt, terminal turn status, and subsequent
  idle state as one outcome. A receipt alone is insufficient.
- A second app-server resume is a writer and snapshot reader, not a live
  observer.

### Claude Agent SDK

| Provider evidence | Adapter result |
| --- | --- |
| `session_state_changed/running` | `working` |
| `session_state_changed/requires_action` plus pending callback | `needs_input` |
| `session_state_changed/idle` | `idle` |
| interrupt receipt plus terminal error result and `idle` | interrupted turn |

Implementation constraints:

- Set `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1` while preserving the inherited
  environment.
- Use streaming input so the helper can interrupt an active query.
- Correlate interactive requests by `requestId`, `toolUseID`, provider session
  ID, and ATC connection generation.
- Determine `needs_input` from overlap between `requires_action` and a pending
  callback. Do not require one rigid callback/event order.
- Treat `result/error_during_execution` after a successful interrupt receipt as
  the provider's terminal representation of an interrupted turn, not as a
  failed interrupt command.
- Run one helper/query writer per connected Claude session and reject a second
  writer before calling the SDK.
- A second `query({ resume })` is a writer, not an observer.
- A model fallback does not change provider identity. Verify the exact session
  ID and cwd; never use the selected model as identity.
- Keep `listSessions` inventory checks in diagnostics and tests, not the
  production resume hot path.

## Identity, create, and resume rules

ATC persists at least:

```text
agentSessionId
agentId
providerSessionId
workspaceId
workingDirectory
resumeReady
connectionGeneration
linkedTerminalSessionId?
createdAt / updatedAt
```

Provider session ID, working directory, and agent/provider kind are immutable
after successful creation. A changed provider ID or cwd is never adopted as a
repair.

Create and resume follow these rules:

1. Validate the canonical working directory before starting a provider.
2. Create never accepts a caller-supplied provider ID.
3. Resume requires a nonblank persisted provider ID and expected cwd.
4. Accept create or resume only after provider evidence contains the exact
   expected identity and cwd.
5. On missing, invalid, mismatched, or unavailable identity, fail without
   creating or adopting a replacement conversation.
6. A failed resume leaves the durable Agent Session stopped and retryable. It
   does not rewrite provider identity or mark the conversation deleted.
7. Only a failed initial create, before durable identity is established, makes
   the Agent Session lifecycle `failed`.

The initial native create path should require initial input. Both tested
provider APIs establish useful durable identity while starting a turn, and
Codex cannot restart-resume a zero-turn thread. For a Codex Agent Session,
`resumeReady` remains false until the first turn reaches terminal provider
state. A blank provider TUI may still be launched as a generic Terminal
Session, but it must not be represented as a durable ATC Agent Session until
its provider identity can be captured and verified through a separately proven
flow.

## Concurrency and control

The Agent Session service, not individual HTTP handlers or helper processes,
owns the writer lease. Create, resume, native send, request response, and TUI
handoff all pass through that service.

- At most one active driver may advance a provider session.
- Multiple front ends may subscribe to ATC's normalized event stream.
- Multiple front ends may attach to the linked TUI through zmx under existing
  terminal leadership rules; that is not permission to open another provider
  driver.
- Native-driver to TUI-driver handoff is allowed only from verified `idle`
  with no pending request and after the old connection has closed.
- TUI-driver to native-driver handoff remains deferred until ATC can verify
  that the TUI is no longer writing. Do not approximate this by racing resume
  calls.
- Interrupt and request-response operations include the ATC turn/request ID so
  a delayed client cannot control a later turn.

## Error contract

Adapters return typed errors; provider prose is diagnostic detail only.

| Error | Meaning | Public HTTP mapping |
| --- | --- | --- |
| `invalid_request` | Missing ID, cwd, input, or malformed typed response | `400` |
| `agent_session_not_found` | Unknown ATC Agent Session | `404` |
| `driver_conflict` | Another writer or unsafe handoff is active | `409` |
| `stale_turn` / `stale_request` | Control target is no longer current | `409` |
| `provider_resume_failed` | Provider rejected the exact persisted ID | `502` |
| `provider_identity_mismatch` | Provider returned a different ID | `502` |
| `provider_cwd_mismatch` | Provider returned a different cwd | `502` |
| `provider_protocol_error` | Required structured evidence was absent or invalid | `502` |
| `provider_unavailable` | Binary/helper missing, crashed, or unsupported | `503` |
| `provider_timeout` | Bounded provider operation did not settle | `504` |

Errors should include the ATC Agent Session ID and a stable code. Raw provider
IDs, provider output, and credentials are excluded from ordinary client error
messages and retained in operator diagnostics.

## Future API and CLI

The public API addresses ATC resources and mirrors operations in the CLI.
Provider limitations are represented as capabilities or typed errors, not
provider-specific routes.

| Operation | HTTP API | CLI |
| --- | --- | --- |
| Discover agents | `GET /api/agents` | `atc agents list` |
| Create | `POST /api/agent-sessions` | `atc agent-sessions create` |
| List | `GET /api/agent-sessions` | `atc agent-sessions list` |
| Read | `GET /api/agent-sessions/{id}` | `atc agent-sessions show <id>` |
| Resume active presentation | `POST /api/agent-sessions/{id}/resume` | `atc agent-sessions resume <id>` |
| Send native input | `POST /api/agent-sessions/{id}/messages` | `atc agent-sessions send <id> <text>` |
| Interrupt exact turn | `POST /api/agent-sessions/{id}/interrupt` | `atc agent-sessions interrupt <id> --turn <id>` |
| Observe | `GET /api/agent-sessions/{id}/events` | `atc agent-sessions watch <id>` |
| Respond to request | `POST /api/agent-sessions/{id}/requests/{requestId}/response` | `atc agent-sessions respond <id> <request-id> ...` |
| Stop driver | `POST /api/agent-sessions/{id}/stop` | `atc agent-sessions stop <id>` |
| Archive | `POST /api/agent-sessions/{id}/archive` | `atc agent-sessions archive <id>` |

Create accepts `agentId`, `workspaceId`, initial input, and optional
presentation. It returns the full Agent Session plus a linked Terminal Session
reference when terminal presentation was requested. Resume returns the same
ATC Agent Session ID and a new or existing presentation reference; it never
creates a replacement Agent Session.

The event endpoint is an ATC-owned SSE stream of normalized state and request
events. It does not attach another provider observer. Native `send`,
`interrupt`, and `respond` may initially return a capability error while the
product remains terminal-first; the operation names and semantics should stay
the same when native interaction is enabled.

Terminal input continues to use the existing Terminal Session API and CLI:
`sessions attach`, `sessions send-text`, and `sessions send-key`. Agent Session
operations must not duplicate terminal byte injection.

## Implementation order

1. Add the Agent Session model and persistence with ATC-owned identity,
   provider identity, cwd, `resumeReady`, and separate lifecycle/activity.
2. Add the internal adapter types, typed errors, event normalization, and
   single-writer service before either provider implementation.
3. Implement the Codex adapter against one supervised multiplexed app-server.
4. Implement the Claude SDK helper and a narrow local protocol consumed by the
   Go adapter.
5. Add Agent Session API contract fixtures and mirror them in the CLI, web,
   and ATCKit using the repository's existing contract-fixture workflow.
6. Add linked Terminal Session creation and safe native-to-TUI handoff.
7. Add server-owned SSE fan-out; never add provider observer processes.

This POC intentionally does not implement those production changes. It defines
the boundary and safety invariants they must satisfy.
