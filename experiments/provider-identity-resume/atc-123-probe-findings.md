# ATC-123 pre-implementation probe findings

Date: 2026-08-03. Versions: codex-cli 0.146.0, Claude Code 2.1.221,
`@anthropic-ai/claude-agent-sdk` 0.3.220 (app-server pinned), macOS arm64.

Three probes run during the ATC-123 grilling session to close the questions
the decision record left open. Probe scripts were session-scratch; the
protocol recipes they used are the ones already recorded in this directory
(`codex/app-server-client.ts`, `codex/held-connection.ts`).

## 1. Codex TUI does NOT survive an app-server restart

Setup: `codex app-server --listen ws://127.0.0.1:17987`, thread created and
seeded with one completed turn over WebSocket JSON-RPC, writer disconnected,
then `codex resume --remote ws://127.0.0.1:17987 <threadId>` spawned in a PTY
(rendered the session, echoed the seed turn). Server killed with SIGKILL,
then restarted on the same port 10s later.

Result: the TUI exited (code 1) immediately on server death, printing:

> ERROR: remote app server at `ws://127.0.0.1:17987/` transport failed:
> WebSocket protocol error: Connection reset without closing handshake
> To continue this session, run codex resume 019fcac1-d24c-7183-a30d-2560d2754fa3

There is no reconnect loop in the TUI (v0.146.0). Restarting the server on
the same port changed nothing; the process was already gone.

**Consequence (settled in the ATC-123 decision record):** the codex
app-server must be spawned detached, zmx-style — surviving ATC restarts,
re-adopted on boot via persisted identity + `/readyz`, adopt-or-replace,
never accumulate. A scoped child would kill every live Codex TUI on every
ATC restart.

## 2. Claude hooks fire as in-process callbacks in SDK mode (ATC-105 backfill)

`query()` with the full hook set in `options.hooks`, nested-session env
markers scrubbed, prompt "Reply with exactly: OK", `maxTurns: 2`:
`UserPromptSubmit` and `Stop` fired as in-process callbacks with the correct
`session_id`; result `success`. SDK-mode hook delivery is confirmed.

## 3. StopFailure did NOT fire on error_max_turns (ATC-105 backfill — negative)

`query()` with `maxTurns: 1` and a prompt forcing one Bash tool use
(`bypassPermissions`): hooks fired `UserPromptSubmit`, `PreToolUse`,
`PostToolUse`; the stream delivered `result: error_max_turns`; then **neither
`Stop` nor `StopFailure` fired** (3s settle window).

The architecture doc's claim "`Stop` does not fire when a turn ends on an
error path — `StopFailure` does" did not reproduce for `error_max_turns` in
SDK mode. Per ATC-105's instruction, the architecture doc is corrected rather
than the requirement kept: the adapter must derive error-path idle in SDK
mode from the `result` message, with the staleness/reconciliation path as
backstop. Whether `StopFailure` fires for other error subtypes (e.g.
`error_during_execution`) or in TUI mode remains unverified.

Bonus SDK behavior finding: the SDK's async iterator **throws** on an error
result ("Claude Code returned an error result: Reached maximum number of
turns (1)") — the adapter must treat that throw as a normal turn-failed
outcome carrying the result, not as a transport defect.

## 4. Detach → survive → adopt → resume → turn → stop: verified end-to-end

The detached-server lifecycle decided in finding 1 was exercised as a full
cycle across two separate processes (simulating an ATC restart):

Phase A spawned `codex app-server --listen ws://127.0.0.1:17993` detached
(`nohup`, backgrounded through an intermediate `sh` that exits immediately,
so the server is reparented with no controlling terminal), seeded a thread
with one completed turn, persisted `{pid, port, threadId, cwd}`, and exited
without killing anything. Phase B, a fresh process started 5s later:

- server pid still alive after the spawner's exit;
- `/readyz` on the persisted port answered;
- `thread/resume` returned the exact thread ID and cwd with 1 existing turn;
- a turn driven over the adopted connection completed, with 21 correlated
  events (`thread/status/changed`, `turn/started`, `item/started`,
  `item/agentMessage/delta`, `item/completed`, `turn/completed`,
  `thread/tokenUsage/updated`) — the full status feed works on an adopted
  connection;
- explicit `SIGTERM` stopped the server cleanly (the intentional-stop path).

Verdict: PASS on first run. Every load-bearing behavior of the accepted
Codex topology now has direct evidence: shared-server fan-out and real-TUI
observation (ATC-83), identity/resume/fail-closed (ATC-68), TUI
non-survival of server death (finding 1), and detached adoption (this).
