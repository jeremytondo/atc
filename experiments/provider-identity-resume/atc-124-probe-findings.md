# ATC-124 pre-implementation probe findings

Date: 2026-08-05. Versions: codex-cli 0.146.0, macOS arm64, Bun 1.3.14.

Two probes run during the ATC-124 grilling session to settle where a
Thread's provider session is created in the terminal-first flow. Probe
scripts were session-scratch; the protocol recipe they used is the one
already recorded in this directory (`codex/ws-client.ts`: WebSocket
JSON-RPC against `codex app-server --listen`, `initialize`/`initialized`
handshake).

## 1. A zero-turn Codex thread cannot be joined by a TUI — even on the live server

Setup: dedicated `codex app-server --listen ws://127.0.0.1:<port>`,
`thread/start { cwd, approvalPolicy: "never", sandbox: "read-only" }` over
WebSocket JSON-RPC — thread created, **no turn started** — then
`codex resume --remote ws://127.0.0.1:<port> <threadId>` spawned in a PTY
(Bun native terminal) while the same server process kept running.

Result: the TUI exited during bootstrap:

> Error: Failed to resume session from ~/.codex/sessions/…/rollout-….jsonl:
> thread/resume failed during TUI bootstrap: thread/resume failed:
> no rollout found for thread id 019fd257-… (code -32600)

The ATC-68 provisional-thread rule ("a zero-turn thread may return `no
rollout found` after an app-server restart") is broader than recorded:
`thread/resume` requires a materialized rollout **within the same server
process too**. Only a completed first turn materializes one.

**Consequence:** the archived "create the provider conversation, then open
the TUI into it" flow is impossible for Codex without ATC driving the first
turn itself. Combined with the Claude SDK emitting no session identity
until a turn starts (2026-08-03 probe), create-then-attach is dead for both
providers.

## 2. Fresh `codex --remote` launch: identity capture and status fan-out — verified

`codex --remote <ADDR>` is a top-level TUI flag (connect the TUI to a
remote app server). Setup: same dedicated app-server, one held observer
connection (initialized, **no thread created, no subscription**), then a
fresh `codex --remote ws://127.0.0.1:<port>` TUI spawned in a PTY and a
first prompt typed through it.

Observed on the held observer connection:

- `thread/started` fired **at TUI bootstrap, before any prompt**, carrying
  the full thread object (`id`, `sessionId`, …) — ATC can capture the new
  thread's identity from its own feed the moment the TUI opens.
- `thread/status/changed` (`active` → `idle`) fanned out for the TUI's turn
  **without any subscription**. One held connection per codex server yields
  coarse activity for every thread on it. (Full `turn/*` / `item/*` events
  did **not** fan out to the unsubscribed observer — those still require
  joining the thread, per the ATC-83 capability table. Coarse status is
  what the terminal-first flow needs; the full feed is native-mode work.)
- `thread/list` answered with all threads including status and timestamps —
  a reconciliation aid for staleness re-derivation.
- The typed first turn completed normally (response rendered in the TUI,
  idle observed on the feed).

Verdict: PASS. **Fresh-launch + identity-capture is the settled flow for
both providers** (Claude's equivalent — hooks registered at launch deliver
`session_id` in every payload — was already verified in the ATC-83 real-TUI
runs). Reopen resumes by exact id once the session is confirmed by a
completed first turn; unconfirmed zero-turn sessions may re-materialize on
the next open (generalized ATC-68 provisional rule — decision record on
ATC-124).
