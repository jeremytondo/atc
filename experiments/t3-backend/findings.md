# ATC-236 findings

Status: direct Go wire path validated; stock T3 rejected as the current ATC
runtime backend because cancellation is not reliable.

Observed on 2026-08-23 with:

- T3 `0.0.34-nightly.20260823.1168`
- T3 source `b1670ac7d9b5b7bb9d7ebd969f27384daee22813`
- Codex provider, model `gpt-5.6-sol`
- isolated T3 state and an empty disposable workspace under `/tmp`

No T3 package was installed, linked, patched, or forked. The live probe used
the stock `t3` executable already present on the machine.

## Experiment matrix

| Capability | Result | Evidence |
| --- | --- | --- |
| Authenticate | Passed | Bootstrap credential exchanged at `/oauth/token`; bearer token exchanged for a one-use WebSocket ticket. |
| Enumerate environment, projects, and providers | Passed | `server.getConfig` returned five provider instances and models; the shell snapshot returned the disposable project and workspace root. |
| Create thread and start prompt | Passed | `thread.create` and `thread.turn.start` produced a persisted thread and a `starting -> running -> ready` session. |
| Stream output and completion | Passed | Basic turn streamed `ATC_T3_BASIC_OK` and settled ready. |
| Approval | Passed | Command approval appeared as `approval.requested`; after a forced reconnect it remained in the snapshot, accepted, ran, and completed. |
| User input / waiting | Passed | `user-input.requested` survived a forced reconnect, accepted an answer, and the turn completed with `Alpha.`. |
| Reconnect / recover | Passed | A new ticket and WebSocket recovered full snapshots, output, lifecycle, and pending approval/input requests. |
| Failure | Passed | An unknown provider instance projected session `error`, a useful `lastError`, and `provider.turn.start.failed`. |
| Interrupt an ordinary running turn | Partly passed | The turn returned to `ready` promptly, but did not expose a canceled outcome and its `sleep 120` tool continued for 119.854 seconds. |
| Interrupt while waiting for user input | Failed | The interrupt intent persisted but never settled the turn; later provider work on that server instance stopped advancing until restart. |

The final clean five-scenario control run exited successfully. Its normalized
results were:

| Scenario | Adapter outcome | T3 session lifecycle | Reconnected | Pending action recovered |
| --- | --- | --- | --- | --- |
| Basic | completed | starting, running, ready | no | n/a |
| Approval | completed | starting, running, ready | yes | yes |
| Input | completed | starting, running, ready | yes | yes |
| Cancel | canceled (inferred from ATC's interrupt intent) | starting, running, ready | no | n/a |
| Failure | failed | error | no | n/a |

## Wire surface

The external protocol is accessible from Go and does not require a TypeScript
bridge. The implemented path is:

1. OAuth-shaped bootstrap-token exchange over HTTP.
2. Bearer-authenticated WebSocket ticket request.
3. WebSocket connection to `/ws?wsTicket=...`.
4. Effect RPC JSON envelopes:
   `Request`, streamed `Chunk`, terminal `Exit`, `Ack`, and `Interrupt`.

Effect RPC is not JSON-RPC 2.0. A request looks like:

```json
{"_tag":"Request","id":"1","tag":"server.getConfig","payload":{},"headers":[]}
```

The probe needs only four RPC tags:

- `server.getConfig`
- `orchestration.subscribeShell`
- `orchestration.dispatchCommand`
- `orchestration.subscribeThread`

Commands exercised project creation, thread creation, turn start, approval
response, user-input response, and turn interrupt. T3 owns persistence,
provider processes, transcripts, activities, and projection snapshots. The Go
side neither reconstructs the complete T3 contract nor stores a second copy of
the transcript.

The shell projection represents a workspace as each project's
`workspaceRoot`; there was no separate workspace collection needed for this
surface. Waiting is likewise derived rather than a session status: an
unresolved `approval.requested` or `user-input.requested` activity is the
authoritative waiting condition, and its matching `*.resolved` activity clears
it.

The reusable transport is 468 lines including error handling, concurrent
calls, subscriptions, chunk acknowledgements, and interruption. Deterministic
tests pin the auth requests and wire envelopes. The larger executable is an
evidence harness, not the proposed production adapter.

## Recovery behavior

Thread subscriptions begin with a self-contained snapshot followed by ordered
events. Reconnecting with a fresh ticket and subscribing again was sufficient
to recover:

- current session and latest-turn state;
- assistant messages accumulated before disconnect;
- approval and user-input activities; and
- whether a request had already been resolved.

This is a simpler production baseline than persisting a second event cursor in
ATC. T3 also exposes sequence fields and cursor parameters, so an optimized
adapter could resume incrementally later, but the experiment did not need
that optimization to prove recovery.

## Lifecycle blockers

### Cancellation is not an authoritative outcome

For the normal cancellation control, the adapter dispatched
`thread.turn.interrupt` after observing `tool.started`. T3 emitted the interrupt
intent and changed the Codex session from `running` to `ready` in milliseconds.
It did not project session `interrupted` or latest-turn `interrupted`; ATC can
only label this canceled by remembering that ATC requested the interrupt.

More importantly, the agent-owned command was not terminated. T3 later emitted
`tool.completed` with `durationMs: 119854` and exit code 0. A ready thread
therefore does not mean its work has stopped.

### Waiting-input interruption wedges later work

A second probe interrupted Codex while `request_user_input` was pending. T3
persisted `thread.turn-interrupt-requested`, but emitted no later session or
activity event for that thread. A failure probe submitted afterward persisted
its create, message, and turn-start intent but never reached session `error`.
After restarting the isolated T3 server and submitting the same failure probe,
it reached `error` immediately.

The current T3 reactor awaits the provider interrupt operation. The observed
behavior is therefore consistent with that awaited call never returning while
Codex is blocked on user input, which stalls subsequent provider intents on
the server instance. Regardless of the internal cause, this violates ATC's
required reliable canceled and waiting transitions.

## Coupling and upgrade cost

The Go approach avoids Effect schema duplication, but it is coupled to four
things that T3 does not expose as a versioned Go SDK:

- Effect RPC envelope JSON;
- RPC method tags;
- orchestration command payloads; and
- snapshot/event payload fields.

That coupling is narrow and testable. Unknown JSON fields are ignored, and
only the fields ATC needs are decoded. A T3 upgrade should be gated by unit
fixtures plus the live five-scenario matrix. The more important upgrade risk
is behavioral: T3 is a nightly build and lifecycle semantics can change even
when payloads still decode.

A tiny TypeScript bridge would remove little risk. It would reuse T3's Effect
types at compile time, but ATC would still need to version, supervise, deploy,
authenticate, and behavior-test that bridge. It would add a second runtime and
another process boundary without fixing the cancellation defects.

## Ownership boundary if reconsidered

If T3 later satisfies the lifecycle gate, T3 should own:

- provider discovery, authentication state, models, and provider processes;
- projects/workspace roots, threads, provider sessions, turns, and transcripts;
- approval/input request state and response routing; and
- persisted orchestration events, projections, and reconnect snapshots.

ATC should retain only its product boundary:

- the stable canonical HTTP API, generated clients, CLI workflows, and trust
  model;
- ATC-specific policy and normalization, including an explicit terminal turn
  outcome rather than leaking T3's `ready` status;
- integrations such as Linear and future automation clients;
- ATC terminal-session workflows that are independent of provider-owned chat
  tools; and
- client-facing notifications and compatibility across pinned T3 upgrades.

ATC should not mirror T3 transcripts or rebuild provider orchestration.

## Decision

Choose **outcome 3 for the tested stock runtime: do not use T3 as ATC's
runtime/orchestration backend yet**. The required lifecycle surface is
accessible but not reliable: cancellation is ambiguous, does not stop tool
work, and can stall later provider work while waiting for input.

If those runtime defects are fixed upstream, choose outcome 1 for a retry: the
experiment proves that a narrow native Go adapter is practical and preferable
to a TypeScript bridge. The re-entry gate should require all of the following
against a pinned T3 release:

1. canceling ordinary and waiting-for-input turns always reaches an explicit,
   recoverable canceled outcome;
2. canceling either case cannot block unrelated later turns;
3. provider-owned foreground tool processes are terminated or remain
   explicitly visible as running after turn cancellation; and
4. the full matrix passes before and after a server restart.
