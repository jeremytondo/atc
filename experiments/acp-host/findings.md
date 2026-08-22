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

## Permissions

The real adapters implement `session/request_permission`, but the harmless
terminal operations in this run were approved by their own configured policy
and did not emit a client permission request. The subprocess integration test
therefore remains the direct coverage for pending, allow, deny, and
cancel-while-pending behavior using the exact v1 request and response shapes.
This is enough to validate the host path, but a production evaluation should
also force each provider into a policy that emits a real permission request.

## Success criteria

1. **Can Go reliably own the ACP process and lifecycle for Claude and Codex?**
   Yes for the exercised v1 path: both real adapters passed startup, multiple
   turns, cancellation, close, relaunch, replay, and continuation.
2. **Does ACP expose enough information and control for ATC native chat?** Yes
   for messages, tool activity, permissions, cancellation, and terminal turn
   outcomes. V1 derives working/idle from the lifetime of `session/prompt`
   rather than explicit v2 state updates.
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
adapters are unavailable. Carry forward two explicit follow-ups: force a real
permission prompt under each provider's policy, and rerun the same matrix when
both official adapters publish protocol v2 support.
