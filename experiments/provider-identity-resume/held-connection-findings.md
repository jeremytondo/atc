# Held-connection and observation findings (ATC-83)

Status: complete

These experiments sharpen the one-writer rule established by the ATC-68 POC.
ATC-68 proved that two **concurrent writers** corrupt Codex turn attribution
and diverge Claude history. It did not test whether a **passively held**
adapter connection is unsafe while another surface drives the conversation.
ATC-83 answers that, and settles where live status can come from in the
terminal-first flow.

Environment: `codex-cli 0.145.0`, `@anthropic-ai/claude-agent-sdk 0.3.220`
(Claude Code 2.1.220), working directory
`/Users/jeremytondo/Projects/ATC/atc-agent-poc` — the same provider versions
as the ATC-68 evidence.

Method note: the external writer is `codex exec resume <threadId>` /
`claude -p --resume <sessionId>`. Both are separate processes using the same
core resume path as the interactive TUIs (ATC-68 separately verified real
TUI round trips). The observation results depend only on process separation,
so they generalize to a TUI driving the conversation.

## Vocabulary

- **Writer** — a connection that advances the conversation (sends turns or
  responds to provider requests).
- **Observer** — a connection that stays attached, may read, and never
  sends.
- **Stale connection** — a connection whose in-memory conversation state
  predates turns written by another process.

## Results

| Question | Codex App Server | Claude Agent SDK |
| --- | --- | --- |
| Held connection receives live events for externally driven turns | No | No |
| Passive hold corrupts history, attribution, or resume | No | No |
| External process can drive turns while the connection is held | Yes | Yes, same session ID |
| History after external turns + adapter restart | Intact, no synthetic turns | Intact |
| Serialized stale write: persisted history | Linear and intact | **Divergent — external turn dropped from active history** |
| Serialized stale write: model context | Blind to external turns | Blind to external turns |
| Structured read of external turns through the held connection | `thread/read` (visible in ~2s) | None (no SDK read API) |
| Provider activity signal for externally driven turns | `thread/read` status stays `idle` (not authoritative) | No events emitted to the held query |
| Clean observer release | `thread/unsubscribe` → `{"status":"unsubscribed"}` | End the streaming input (close the query) |

## Reviewed observations

### Codex passive hold — 2026-07-28

- Thread `019fa9dc-16aa-7773-9ed1-a2b4908f46f2` was created and seeded
  through a held app-server connection, which then went passive while
  `codex exec resume` drove two external turns that recalled and extended
  the continuity markers.
- During both external turns the held connection received exactly **one**
  message: a thread-scoped `mcpServer/startupStatus/updated` notification.
  No `turn/*`, `item/*`, or `thread/status/changed` events for the external
  turns arrived. A held app-server connection is not a live observer of
  turns driven by another process.
- The external turns appended to the same rollout file
  (44,635 → 59,460 bytes).
- `thread/read { includeTurns: true }` on the held connection returned all
  three turns including both external markers — the read reflects the
  rollout on disk, not the connection's stale in-memory view.
- `thread/unsubscribe` on the held connection succeeded with
  `{"status":"unsubscribed"}`.
- After stopping the held process, a fresh app-server resumed the exact ID
  and cwd: 3 provider turn IDs, no synthetic turns, all three markers in
  history, and a recall turn returned
  `ALL MARKERS: <seed> <ext1> <ext2>` exactly.
- Reviewed artifact: `runs/codex/2026-07-28T17-53-21-028Z-f719f1c3.held-passive.json`
  and its app-server/fresh-resume JSONL streams.

### Codex serialized stale write — 2026-07-28

- Thread `019fa9de-5263-7d60-9ae4-ee379afed44f`: seed through the held
  connection, one external `codex exec resume` turn, then — without
  re-resuming — one more turn from the held connection.
- The held connection observed **zero** messages during the external turn.
- The stale turn completed successfully but its model context omitted the
  external turn: it recalled the seed and its own new marker only
  (`sawExternalMarker: false`).
- A fresh resume showed **linear, intact history**: 3 provider turn IDs in
  order (seed, external, stale), no synthetic `rollout-N` turns, and all
  three markers present.
- Contrast with ATC-68's concurrent-writer run, where attribution moved to
  a synthetic `rollout-36` turn. Codex persistence corruption requires
  **concurrent** sends; a serialized stale write persists cleanly but
  produces a turn whose model never saw the external context.
- Reviewed artifact: `runs/codex/2026-07-28T17-55-47-079Z-0fba44ca.stale-write.json`.

### Codex poll-observe — 2026-07-28

- While `codex exec resume` drove a 4.4 s external turn, the held
  connection polled `thread/read` every 2 s.
- The external turn's completed agent message became visible through
  `thread/read` at the 2,018 ms poll — before the external process exited.
- `thread.status` reported `idle` in every poll, including while the
  external turn was demonstrably active. The held connection's status field
  does **not** reflect another process's activity and cannot power
  `activityState`.
- Reviewed artifact: `runs/codex/2026-07-28T17-58-24-385Z-f67ba0e4.poll-observe.json`.

### Claude passive hold — 2026-07-28

- Session `a18349f8-6e67-45e8-99bb-844f3054f3a7` was created and seeded by
  a streaming SDK `query()` with `CLAUDE_CODE_EMIT_SESSION_STATE_EVENTS=1`,
  which then stayed open without sending while `claude -p --resume` drove
  two external turns.
- Both external turns preserved the exact session ID (no fork) and
  recalled the prior markers, completing in ~3 s each.
- The held query observed **zero** messages during the external turns — no
  session-state events, no assistant messages. Session-state events are
  visible only to the driving process.
- The session file grew from 3 to 7 entries and contained all three
  markers; the session inventory still contained the held session.
- After closing the held query without writing, a fresh SDK resume of the
  exact ID recalled `ALL MARKERS: <seed> <ext1> <ext2>` exactly. Passive
  hold caused no divergence.
- Reviewed artifact: `runs/claude/2026-07-28T17-55-48-514Z-363e5fff.held-passive.json`.

### Claude serialized stale write — 2026-07-28

- Session `b8d11031-856c-4454-b16e-4f8a31761bd0`: seed through the held
  query, one external `claude -p --resume` turn, then — without
  re-resuming — one more message through the held query.
- The held query observed only its own seed turn's trailing `idle` event;
  nothing from the external turn.
- The stale write succeeded but its context omitted the external turn
  (`sawExternalMarker: false`).
- A fresh resume of the exact ID recalled the **seed and stale markers
  only**. The external turn's text still exists in the session file, but
  the stale write formed a divergent branch that won on resume and dropped
  the external turn from the active conversation.
- Unlike Codex, a Claude stale write does not need a concurrent send to
  damage the conversation: **any** write from a connection that predates an
  external turn rewrites the active history.
- Reviewed artifact: `runs/claude/2026-07-28T17-56-20-282Z-78711d3e.stale-write.json`.

## Conclusions

### The refined rule

The unit of exclusivity is the **writer role**, not the connection.

1. **One active writer per provider conversation** — unchanged from ATC-68.
2. **Holding a connection passively is safe** for both providers. The
   adapter does not need to disconnect when a TUI attaches. Passive holds
   corrupt nothing: history, attribution, and resume stayed intact across
   external turns and an adapter restart for both providers.
3. **A passive connection is not an observer.** Neither provider delivers
   live events for turns driven by another process. Local live observation
   remains unsupported; ATC-owned fan-out remains the only observation
   path.
4. **A connection that lost the writer role is stale and must never write
   again without refreshing.** This is the sharpened constraint the
   original rule was missing:
   - Claude: a stale write actively **rewrites history** — the externally
     driven turns are dropped from the resumed conversation even though the
     writes were serialized. The adapter must close the held query and
     re-resume before writing after any external turn.
   - Codex: a stale write persists linearly but the turn's model context
     silently omits the external turns. The adapter must re-resume (or
     refresh from `thread/read`) before writing after any external turn.

### V1 status source

While a provider TUI holds the writer role, no provider gives the adapter
an authoritative live activity signal:

- The held connection's event stream is silent for external turns (both
  providers).
- Codex `thread/read` polling through the held connection reflects
  externally driven turns within ~2 s, but its `status` field stays `idle`,
  so it is history visibility, not activity truth.
- Claude has no SDK read surface at all; the only local signal is session
  file append activity.

Settled V1 answer for the terminal-first flow:

- **Lifecycle** comes from ATC-owned process/zmx state, as already
  accepted.
- **`activityState` is `unknown` while a TUI holds the writer role.** The
  `unknown` value in the contract is load-bearing, not a placeholder. ATC
  reports precise activity only when the adapter connection is itself the
  writer (create/seed, future native mode, headless operations).
- The adapter keeps its connection during TUI sessions anyway: it is safe,
  it preserves instant `thread/read` history access for Codex, and it
  avoids a reconnect penalty on writer handback. It must treat that
  connection as read-only until it re-verifies state.

Post-V1 upgrade paths for live status, in preference order:

1. Provider push hooks fired by the driving process — Claude Code hooks
   (`Stop`, `Notification`, `UserPromptSubmit`) and Codex `notify`
   (`agent-turn-complete`) run in the TUI's own process and can call back
   into ATC. Structured, provider-owned, and works while the TUI drives.
   Not exercised in these runs; requires config management design.
2. Codex `thread/read` polling for turn-boundary evidence (verified above,
   ~2 s latency).
3. Session/rollout file append activity as a coarse "something happened"
   signal — never parsed for correctness, per the accepted architecture.

### Protocol facts discovered along the way

- `thread/unsubscribe` exists and works — a clean observer-release
  operation for a multiplexed app-server client.
- `ThreadResumeParams` documents that resuming a thread already running in
  the same app-server "rejoins" it. Cross-client rejoin fan-out inside one
  app-server process was not exercised (ATC's adapter is the app-server's
  only client) and remains unverified.
- Claude Agent SDK 0.3.220 has an alpha daemon/multi-client surface
  ("agent view", `--bg`, multi-client fan-out transports). It applies to
  daemon-hosted background sessions, not to a locally driven TUI session,
  and is not a V1 dependency. Worth re-evaluating when Native mode is
  designed.
