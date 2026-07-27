# Provider identity and resume findings

Status values are `pass`, `fail`, `partial`, or `not tested`. Observations must
come from reviewed run artifacts, not protocol assumptions.

| Experiment | Codex | Claude |
| --- | --- | --- |
| Durable ID availability | pass | pass |
| Dormant zero-turn lifecycle | pass | not tested |
| Zero-turn recovery after provider restart | fail | not tested |
| Second turn in same process | pass | pass |
| Fresh-process resume with identity verification | pass | pass |
| Invalid or missing resume ID | pass | pass |
| Shared-process multiplexing | pass | not tested |
| Working directory after resume | pass | pass |
| `working`, `idle`, and `needs_input` signals | partial | not tested |
| Read-only permission or tool request | not tested | not tested |
| Active turn interruption | not tested | not tested |
| Second observer visibility and writer behavior | not tested | not tested |
| Native TUI interoperability | pass | not tested |

## Reviewed observations

### Codex create and resume — 2026-07-27

- Environment: `codex-cli 0.145.0`, working directory
  `/Users/jeremytondo/Projects/ATC/atc-agent-poc`.
- Marker: `ATC-CODEX-ATC68-02`.
- Durable thread ID:
  `019fa3f9-f2ea-7560-8548-5f2defbd8916`.
- The ID first appeared in the successful `thread/start` response. The
  `thread/started` notification carrying the same ID arrived on the next raw
  server event.
- The start response reported `ephemeral: false`, the requested cwd,
  `approvalPolicy: "never"`, and a read-only sandbox with network disabled.
- Two turns completed in the original app-server process. The second prompt did
  not contain the marker, and the final `item/completed` agent message returned
  it exactly.
- A new app-server process called `thread/resume`. Its response returned the
  expected ID and cwd and included both prior completed turns. No
  `thread/start` fallback exists in the resume command.
- The resumed prompt did not contain the marker, and the final agent message
  returned it exactly.
- `thread/status/changed` transitioned from `idle` to `active` at turn start and
  back to `idle` before `turn/completed` for all three turns. This supports
  `working` and `idle` mapping candidates. No `needs_input` state was exercised,
  so the activity row remains partial.
- The thread response reported `source: "vscode"` even though the custom client
  started `codex app-server` directly. Treat source classification as an
  observed compatibility detail, not provider identity.
- Reviewed artifacts:
  `runs/codex/2026-07-27T14-28-14-466Z-a84b0214.create.jsonl` (61 raw server
  messages) and
  `runs/codex/2026-07-27T14-28-32-077Z-7d84ab25.resume-019fa3f9-f2ea-7560-8548-5f2defbd8916.jsonl`
  (36 raw server messages).

### Codex dormant zero-turn lifecycle — 2026-07-27

- Environment: `codex-cli 0.145.0`, working directory
  `/Users/jeremytondo/Projects/ATC/atc-agent-poc`.
- Durable thread ID:
  `019fa428-0428-7ca3-9ec6-49bb173cc421`.
- The `thread/start` response and following `thread/started` notification both
  reported the same ID, requested cwd, `ephemeral: false`, `idle` status, and
  an empty `turns` array.
- The app-server process remained running while the probe sent no `turn/start`
  request for a measured 10,002 ms.
- The first `turn/start` response returned turn
  `019fa428-2ba3-7c82-b1f8-8132f2316bcd`. Its `turn/started` and
  `turn/completed` notifications were both attributed to the original thread
  ID, and the turn completed successfully.
- The first-turn prompt used marker `ATC-CODEX-ATC68-DORMANT-01`; the completed
  agent message returned the marker exactly.
- Reviewed artifacts:
  `runs/codex/2026-07-27T15-18-33-436Z-8f35d634.dormant-zero-turn.json` and its
  sibling `.jsonl` file (39 raw server messages).

### Codex gate completion — 2026-07-27

Environment: `codex-cli 0.145.0`, working directory
`/Users/jeremytondo/Projects/ATC/atc-agent-poc`.

#### Zero-turn recovery — fail

- App-server returned durable thread
  `019fa43b-47c6-7731-bb9a-d6ea66bb6fa3`, the requested cwd,
  `ephemeral: false`, and an empty turns array.
- The first app-server stopped without receiving `turn/start`.
- A fresh app-server called `thread/resume` for that exact ID and received
  `-32600 no rollout found for thread id
  019fa43b-47c6-7731-bb9a-d6ea66bb6fa3`.
- The probe did not fall back to `thread/start` and did not attempt a first
  turn. This shows that the ID is durable-looking at `thread/start` time but
  its rollout is not recoverable until at least one turn materializes it.
- Reviewed artifacts:
  `runs/codex/2026-07-27T15-39-35-997Z-073bae99.zero-turn-recovery.json`,
  its 6-event create JSONL, and its 3-event resume JSONL.

#### Invalid and missing resume IDs — pass

- Before and after `thread/list` each returned the same 93 IDs.
- Nonexistent ID `e4af06db-6ccb-4c5c-a331-165790e4cb13` returned
  `-32600 no rollout found for thread id ...`.
- Omitting `threadId` returned
  `-32600 Invalid request: missing field threadId`.
- Neither attempt created a replacement conversation.
- Reviewed artifacts:
  `runs/codex/2026-07-27T15-32-13-277Z-b5342fa5.invalid-resume.json` and its
  6-event sibling JSONL.

#### Shared-process multiplexing — pass

- One app-server created distinct threads
  `019fa435-76b9-7871-b114-45faccbdec61` and
  `019fa435-7722-7392-ac5f-622d9cdd584d`.
- `thread/loaded/list` returned both exact IDs.
- Four turns completed in A1 → B1 → A2 → B2 order. Each thread recalled only
  its own marker on the second turn.
- The validator correlated 28, 24, 26, and 22 turn-scoped events,
  respectively. Every correlated event carried the expected `threadId`.
- Delayed MCP-startup notifications for the other loaded thread can interleave
  with a turn; these are lifecycle events without that turn's `turnId`, not
  attribution leakage.
- Reviewed artifacts:
  `runs/codex/2026-07-27T15-33-14-802Z-0884639b.multiplex.json` and its
  131-event sibling JSONL.

#### Native TUI round trip — pass

- App-server created thread
  `019fa438-6452-7343-8b77-22d53fc8f9c9` and completed one seed turn so the
  rollout existed before handoff.
- `codex resume` opened that exact ID in the native TUI and completed a turn
  with marker `ATC-CODEX-ATC68-TUI-02`.
- A fresh app-server resumed the same ID and cwd. Its response contained
  exactly the seed turn and TUI turn, including the exact TUI marker.
- A subsequent app-server turn recalled the TUI-specific marker without the
  marker being repeated in its prompt. All 27 correlated turn events carried
  the same thread ID.
- Reviewed artifacts:
  `runs/codex/2026-07-27T15-36-26-730Z-b13bf0bf.tui-round-trip.json`, its
  36-event create JSONL, and its 41-event resume JSONL.

### Claude create and resume — 2026-07-27

- Environment: `@anthropic-ai/claude-agent-sdk 0.3.220`, Claude Code
  `2.1.220`, working directory
  `/Users/jeremytondo/Projects/ATC/atc-agent-poc`.
- Marker: `ATC-CLAUDE-ATC68-01`.
- Durable session ID: `7f5ed6a2-8fde-4bc2-a69b-c4dd44793d12`.
- The ID first appeared directly on event 1, the `system/init` message. The
  successful result repeated the same ID.
- `system/init` reported the requested cwd, `permissionMode: "dontAsk"`, and
  an empty tools array. Filesystem settings were disabled in the SDK options.
- A second `query()` call in the same Node.js process passed the exact ID to
  `resume`. Its init and result retained the ID and the response recalled the
  marker without the marker appearing in the prompt.
- A separately launched `pnpm claude resume` process loaded the session
  artifact and resumed the same ID and cwd. Its response also recalled the
  marker without the marker appearing in the prompt.
- All 12 reviewed messages across the three queries carried the same session
  ID. Each query emitted `system/init`, `assistant`, `rate_limit_event`, and
  `result/success`.
- None of these basic turns emitted `session_state_changed`; activity mapping
  remains not tested until the dedicated lifecycle scenarios.
- Reviewed artifacts:
  `runs/claude/2026-07-27T16-34-53-080Z-7b591836.session.json`, its two
  four-message create JSONL files, and the four-message
  `2026-07-27T16-35-06-374Z-2695c025.resume-7f5ed6a2-8fde-4bc2-a69b-c4dd44793d12.jsonl`.

### Claude invalid and missing resume IDs — 2026-07-27

- Environment: `@anthropic-ai/claude-agent-sdk 0.3.220`, Claude Code
  `2.1.220`, working directory
  `/Users/jeremytondo/Projects/ATC/atc-agent-poc`.
- Before the invalid attempt, the SDK's exact-directory session inventory
  contained three session IDs. The same three IDs were present afterward.
- Resuming well-formed nonexistent session
  `96e33714-0a7e-4b1e-bca6-ca892afec02a` emitted one
  `result/error_during_execution` message with the exact requested ID and
  `No conversation found with session ID` error.
- The error result reported zero turns, zero cost, and no token usage. No
  assistant message, model turn, or different session ID was emitted.
- Omitting the SDK `resume` option normally means create, so the probe requires
  an explicit nonblank resume ID before calling `query()`. The missing-ID case
  failed at that boundary and did not start an SDK process.
- The gate compares both raw emitted identities and SDK `listSessions`
  inventories. It fails on a different emitted ID, a new replacement ID, or a
  newly materialized session using the requested invalid UUID.
- Reviewed artifacts:
  `runs/claude/2026-07-27T16-49-15-611Z-a48a602b.invalid-resume.json` and its
  one-message sibling JSONL.

## Codex gate conclusion

Three remaining checks passed, but zero-turn recovery failed. Starting the
Claude probe therefore required explicitly accepting that Codex threads are
not recoverable across app-server restarts until a first turn has materialized
the rollout.

That constraint was accepted in ATC-68. Claude identity/resume and
invalid-resume safety now pass. Claude activity and input-state signals are the
next narrow lifecycle gate.

## Adapter recommendations

Deferred until identity and resume experiments have passed for both providers.
