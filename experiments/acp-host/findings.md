# ATC-229 findings

Status: ACP v1 host path validated with both current official adapters

Observed on 2026-08-22.

## Provider matrix

| Provider | Official adapter | Protocol | Create and prompt | Cancel | Fresh-process load/replay | Context continuity |
| --- | --- | --- | --- | --- | --- | --- |
| Codex | `@agentclientprotocol/codex-acp` 1.6.2 | v1 | passed | passed | passed | passed |
| Claude | `@agentclientprotocol/claude-agent-acp` 0.70.0 | v1 | passed | passed | passed | passed |

Each run launched a new adapter subprocess and session, stored a unique marker,
streamed a response, invoked a harmless terminal tool, cancelled a later turn,
and closed cleanly. A second adapter process then loaded the exact persisted
session ID, replayed prior user/assistant/tool history, and correctly recalled
the marker in a follow-up prompt.

Both adapters advertise `loadSession` and `sessionCapabilities.resume` and
`close`. The experiment maps replay to `session/load` and no-replay reconnects
to `session/resume`; the deterministic integration suite asserts that these
methods are not conflated.

## Host behavior established

- The Go process owns one adapter PID and its stdio connection, with graceful
  close followed by an exact-PID termination fallback.
- ACP v1 initialization, session creation, load, resume, close, prompt,
  cancellation, updates, and permission responses stay in one wire module.
- A prompt remains pending until its terminal `stopReason`. This gives the
  normalized host an authoritative completed, refused, limited, or cancelled
  outcome without relying on draft v2 state notifications.
- Raw JSON-RPC and normalized events share an append-only JSONL timeline.
- Session metadata is atomically persisted without copying credentials or
  transcripts into ATC-owned storage.
- Resume never falls back to a new session and therefore cannot hide lost
  continuity.

## Execution ownership addendum

A second real-adapter matrix made the execution boundary explicit. The host
continued to initialize with empty client capabilities and handled no
`fs/read_text_file`, `fs/write_text_file`, or `terminal/*` methods. In isolated
workspaces, both adapters nevertheless:

- read a seeded file using their own file tool;
- created a file using their own edit/write tool;
- ran `wc` using their own command tool; and
- reported the tool lifecycle and results through `session/update`.

The raw ACP logs contained only initialization, session control, updates, and
permission requests. They contained no client-side filesystem or terminal
request. This proves that ATC can use ACP v1 with the v2-style ownership model:
the agent owns tool execution and filesystem access, while ATC supervises the
agent and uses ACP as its control and observation channel.

## Permissions

The follow-up matrix forced real `session/request_permission` calls from both
providers. Allowing a file mutation caused the agent-owned operation to create
the expected file. Denying a second mutation produced a failed tool update and
left the filesystem unchanged. Claude also requested and honored permission
for the command tool. This validates real provider round trips for both allow
and deny, rather than only the deterministic fake-agent path.

## Tool cancellation and background work

Agent-owned execution makes turn state and tool-process state distinct. A
long-running command exposed two cases that the original cancellation result
did not cover:

- Codex returned `stopReason: cancelled` immediately after `session/cancel`,
  but its command continued until the original sleep elapsed and only then
  emitted its final tool update. Turn cancellation therefore did not terminate
  the tool process.
- Claude chose to run the command as a background task and ended the prompt
  normally while it was still running. The harness could no longer issue
  `session/cancel` because there was no active prompt. When that background task
  later completed, it produced more tool activity and a permission request
  while the session was idle.

The test-owned Claude shell and sleep PIDs were terminated explicitly and no
delayed file was created. These results do not weaken the execution-ownership
decision, but they do mean a production ATC lifecycle cannot equate a terminal
prompt response with all agent-owned work being finished. The next composition
prototype needs an explicit model for background activity plus process-tree
cleanup or a stronger adapter contract for cancellation.

## Success criteria

1. **Can Go reliably own the ACP process and lifecycle for Claude and Codex?**
   Yes for the exercised v1 path: both real adapters passed startup, multiple
   turns, close, relaunch, replay, and continuation. Turn cancellation is
   observable, but it does not guarantee agent-owned tool cleanup.
2. **Does ACP expose enough information and control for ATC native chat?** Yes
   for messages, tool activity, real permission decisions, cancellation, and
   terminal turn outcomes. V1 derives foreground working/idle from the lifetime
   of `session/prompt`; background agent work must be tracked separately.
3. **Can sessions resume after the ATC host restarts?** Yes. Both providers
   replayed history and retained context using the exact saved session ID in a
   fresh adapter process.
4. **Can provider differences stay behind a small normalized model?** Yes in
   these runs. Provider selection only changes the launch command; lifecycle
   handling is shared.
5. **Is protocol instability isolated cheaply?** Yes. Wire shapes and method
   names are confined to `internal/acp`, while the harness depends on a small
   provider-neutral model.

## Decision

Use ACP v1 for the next experiment while the official Claude and Codex ACP v2
adapters are unavailable. Keep the client capability surface empty: agents own
tools, filesystem access, and command execution; ATC owns supervision,
permission mediation, normalized state, and the canonical API. Carry forward
the background-work and tool-cleanup lifecycle gap, and rerun the protocol
matrix when both official adapters publish protocol v2 support.
