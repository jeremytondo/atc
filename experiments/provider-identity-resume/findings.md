# Provider identity and resume findings

Status values are `pass`, `fail`, `partial`, or `not tested`. Observations must
come from reviewed run artifacts, not protocol assumptions.

| Experiment | Codex | Claude |
| --- | --- | --- |
| Durable ID availability | pass | not tested |
| Dormant zero-turn lifecycle | pass | not tested |
| Second turn in same process | pass | not tested |
| Fresh-process resume with identity verification | pass | not tested |
| Invalid or missing resume ID | not tested | not tested |
| Working directory after resume | pass | not tested |
| `working`, `idle`, and `needs_input` signals | partial | not tested |
| Read-only permission or tool request | not tested | not tested |
| Active turn interruption | not tested | not tested |
| Second observer visibility and writer behavior | not tested | not tested |
| Native TUI interoperability | not tested | not tested |

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

## Adapter recommendations

Deferred until identity and resume experiments have passed for both providers.
