# Held-connection and observation findings (ATC-83)

Status: complete

These experiments sharpen the one-writer rule established by the ATC-68 POC
and settle where live Agent Session status comes from in the terminal-first
flow, where ATC's adapter creates the conversation and a provider TUI drives
it.

Environment: `codex-cli 0.145.0`, `@anthropic-ai/claude-agent-sdk 0.3.220`
(Claude Code 2.1.220), working directory
`/Users/jeremytondo/Projects/ATC/atc-agent-poc`.

## Headline

**Transport choice decides everything for Codex.** A held connection to a
*private* app-server observes nothing, but two clients of *one shared*
app-server — which is how ATC would actually run it — give the adapter a
complete live event stream while a real `codex resume --remote` TUI drives
the conversation. Codex live status is available in V1.

**Claude has no equivalent transport.** A held SDK `query()` receives
nothing from a separate driving process, and the SDK exposes no cross-process
event channel. Claude status must come from out-of-band sources:
`claude agents --json` (live per-session `busy`/`idle`) or Claude Code hooks
fired by the driving process. Both are verified against a real TUI.

## Vocabulary

- **Writer** — a connection that advances the conversation.
- **Observer** — a connection that stays attached and never sends.
- **Stale connection** — one whose in-memory state predates turns written by
  another process.

## Results

| Question | Codex, shared app-server | Codex, private app-server | Claude Agent SDK |
| --- | --- | --- | --- |
| Live events for turns driven by another process | **Full fan-out** | None | None |
| Passive hold corrupts history/attribution/resume | No | No | No |
| Provider activity signal while a TUI drives | **Yes, `thread/status/changed`** | No (`thread/read` status stays `idle`) | Not from the connection — use `claude agents --json` |
| Structured history read on the held connection | `thread/read` | `thread/read` (~2 s lag) | None |
| Serialized stale write | Linear history, context-blind | Linear history, context-blind | **Rewrites history — external turn dropped** |
| Clean observer release | `thread/unsubscribe` | `thread/unsubscribe` | Close the streaming query |

## Reviewed observations

### Codex shared app-server with a real remote TUI — 2026-07-28 (decisive)

- One dedicated `codex app-server --listen ws://127.0.0.1:<port>` process.
  An "adapter" WebSocket client created and seeded thread
  `019faa03-dffd-72f2-a7e5-5e7ad042a629`, then held passively.
- A second WebSocket client resumed the same thread on the same server (the
  documented rejoin-a-running-thread path) and drove a turn. The held
  observer received **48 messages**, including `turn/started`,
  `item/started`, `item/completed`, `item/agentMessage/delta`, two
  `thread/status/changed` transitions, and `turn/completed` — plus the
  external turn's marker text.
- A **real native TUI** then attached with
  `codex resume --remote ws://127.0.0.1:<port>` and drove a turn. The held
  observer received **47 messages (45 thread-scoped)** with the same full
  event set: `turn/started`, `item/completed`, agent-message deltas, two
  `thread/status/changed` transitions, `turn/completed`, and the marker.
- This is ATC's intended topology working end to end: the adapter is a
  passive observer with a complete live event stream while the user drives
  the conversation in the TUI.
- Reviewed artifacts:
  `runs/codex/2026-07-28T18-35-50-433Z-b803975f.shared-server.json` (two
  app-server clients) and
  `runs/codex/2026-07-28T18-36-47-972Z-fde7c6ea.shared-server.json`
  (adds the real `--remote` TUI leg), with observer and writer JSONL streams.

### Codex private app-server, separate TUI process — 2026-07-28

- The adapter ran its own stdio `codex app-server` and held the connection
  while a separate `codex` process drove the thread. Two variants were run:
  `codex exec resume` (thread `019fa9dc-16aa-…`) and a **real
  expect-driven `codex resume` TUI** (thread `019fa9f7-cd56-…`).
- In both, the held connection observed **no** turn or status events for the
  external turns — 1 unrelated MCP-startup notification in the first, 0 in
  the TUI run.
- History stayed intact in both: after an app-server restart a fresh resume
  returned all real turn IDs with no synthetic turns, and a recall turn
  returned every marker exactly.
- `thread/read { includeTurns: true }` on the held connection surfaced the
  external turns (they are read from the rollout on disk), and
  `thread/unsubscribe` succeeded with `{"status":"unsubscribed"}`.
- Reviewed artifacts:
  `runs/codex/2026-07-28T17-53-21-028Z-f719f1c3.held-passive.json` and
  `runs/codex/2026-07-28T18-23-36-903Z-0e372177.tui-hold.json`.

### Codex poll-observe — 2026-07-28

- While an external turn ran for 4.4 s, the held private-app-server
  connection polled `thread/read` every 2 s. The completed agent message
  appeared at the 2,018 ms poll, before the external process exited.
- `thread.status` reported `idle` in every poll, including while the
  external turn was demonstrably active. On a private app-server,
  `thread/read` gives history visibility, never activity truth.
- Reviewed artifact:
  `runs/codex/2026-07-28T17-58-24-385Z-f67ba0e4.poll-observe.json`.

### Codex serialized stale write — 2026-07-28

- Seed through the held connection, one external turn, then one more turn
  from the held connection **without re-resuming**.
- The stale turn completed successfully but its model context omitted the
  external turn (`sawExternalMarker: false`).
- A fresh resume showed linear, intact history: three real turn IDs in
  order, no synthetic `rollout-N` turns, all markers present.
- Codex persistence corruption therefore requires *concurrent* sends
  (ATC-68). A serialized stale write persists cleanly but is context-blind.
- Reviewed artifact:
  `runs/codex/2026-07-28T17-55-47-079Z-0fba44ca.stale-write.json`.

### Claude passive hold — 2026-07-28

- A streaming SDK `query()` with `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1`
  seeded session `a18349f8-6e67-45e8-99bb-844f3054f3a7` and stayed open
  while `claude -p --resume` drove two turns.
- The held query observed **zero** messages during both external turns. The
  external turns preserved the exact session ID and recalled prior markers.
- The session file grew from 3 to 7 entries; a fresh resume after closing
  the held query recalled every marker exactly. Passive hold caused no
  divergence.
- Reviewed artifact:
  `runs/claude/2026-07-28T17-55-48-514Z-363e5fff.held-passive.json`.

### Claude serialized stale write — 2026-07-28

- Seed through the held query, one external turn, then one more message from
  the held query **without re-resuming**.
- The stale write succeeded but was context-blind, and a fresh resume
  recalled the **seed and stale markers only**. The external turn's text
  survives in the session file on a losing branch.
- Unlike Codex, a Claude stale write damages the conversation without any
  concurrency: any write from a connection that predates an external turn
  rewrites the active history.
- Reviewed artifact:
  `runs/claude/2026-07-28T17-56-20-282Z-78711d3e.stale-write.json`.

### Claude live status via `claude agents --json` — 2026-07-28

- `claude agents --json` enumerates live Claude Code processes with
  `sessionId`, `cwd`, `pid`, `kind`, and a `status` field.
- Driving a **real interactive `claude --resume` TUI** through a pty while
  polling that command produced the transition
  `absent` → `busy` → `idle` across one turn, keyed by the exact session ID.
- This is an out-of-band but provider-owned activity signal for TUI-driven
  Claude sessions — the only one found. Latency is bounded by the poll
  interval.

### Provider push hooks — 2026-07-28

- **Claude Code hooks** (`UserPromptSubmit`, `Stop`, `Notification`)
  configured through `--settings` fired for both `claude -p --resume` and a
  **real interactive `claude --resume` TUI**, delivering JSON on stdin
  containing the exact `session_id` and `transcript_path`.
- **Codex `notify`** configured with `-c notify=[...]` fired from a **real
  `codex resume` TUI**, delivering
  `{"type":"agent-turn-complete","thread-id":…,"turn-id":…,"client":"codex-tui",
  "last-assistant-message":…}`.
- Both are launch-time configuration ATC controls when it starts the TUI, so
  they need no provider cooperation beyond flags.

### Claude TUI attach to an adapter-created session — 2026-07-28

- A controlled three-leg test attached a real `claude --resume` TUI to an
  SDK-created session at T+0 s while the adapter held it, again at T+98 s
  still held, and at T+158 s after release. **All three attached** and saw
  the seeded conversation.
- Earlier orchestrated runs intermittently hit
  `No conversation found with session ID` while several experiments ran
  concurrently. Treat hot-attach as working but re-verify on a quiet machine
  before ATC depends on attaching within seconds of session creation.
- Operational note: a fresh-environment TUI launch can hit Claude Code's
  first-run trust dialog (for this repo, the `@AGENTS.md` import in
  `CLAUDE.md`). ATC must expect provider TUIs to open with a blocking
  first-run prompt.

## Conclusions

### The refined rule

The unit of exclusivity is the **writer role**, not the connection.

1. **One active writer per provider conversation** — unchanged (ATC-68).
2. **Holding a connection passively is safe** for both providers. The
   adapter should not disconnect when a TUI attaches.
3. **Observation depends on transport, not on the provider being "closed".**
   Codex fans events out to every client of the same app-server process;
   two *separate* app-server processes share nothing but the rollout file.
   Claude has no cross-process event channel at all.
4. **A connection that lost the writer role is stale and must not write
   again without refreshing.** Claude: close the held `query()` and
   re-resume — a stale write silently drops the TUI's turns. Codex:
   re-resume or reload via `thread/read` — a stale write persists cleanly
   but its model context omits the external turns.

### V1 status source

**Codex — live status is available.** Run one supervised
`codex app-server --listen` per ATC App Server profile and launch the TUI
with `codex resume --remote <endpoint>`. The adapter's observer connection
then receives `thread/status/changed`, turn lifecycle, and item events for
TUI-driven turns, so `activityState` can be `working`/`idle` with provider
truth. Keep `notify` configured as a redundant turn-completion signal.

**Claude — status is out-of-band.** The adapter connection cannot observe.
Use, in order: Claude Code hooks configured at TUI launch (push, exact
`session_id`, no polling), with `claude agents --json` as a
discovery/reconciliation poll for `busy`/`idle`. Reserve `activityState:
unknown` for the window before those signals are wired, not as the steady
state.

Lifecycle continues to come from ATC-owned process and zmx state.
Transcript text is never a correctness dependency.

### Protocol facts discovered

- `codex app-server --listen ws://…` serves the same JSON-RPC protocol over
  WebSocket, with `/readyz` and `/healthz`. `codex resume --remote` attaches
  a native TUI to it. `codex app-server proxy --sock` reaches the shared
  daemon socket, but the machine-wide daemon started by the Codex desktop
  integration held the control socket, so ATC should run its own listener
  rather than share that one.
- `thread/unsubscribe` is a clean observer-release operation.
- `claude agents --json` is an undocumented-but-stable-looking live session
  registry including interactive TUI sessions.
- Claude Code disables transcript saving when nested-session environment
  markers (`CLAUDE_CODE_CHILD_SESSION` and friends) are inherited. Probes
  scrub them so spawned processes behave like a production ATC deployment;
  ATC should scrub them too if it ever launches providers from inside
  another Claude Code session.
