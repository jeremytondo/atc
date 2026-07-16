# Terminal & libghostty Architecture Review

Status: Draft for review
Date: 2026-07-11
Scope: How atc hosts terminal sessions end to end — the macOS client's
libghostty integration, the attach WebSocket, the Go server's attach bridge,
and the zmx multiplexer boundary — evaluated against the stated priorities:
performance, reliability (survive sleep/reconnect), full TUI support, and
remote-first operation.

---

## 1. Verdict

**Do not rewrite.** The core architecture is the right one, and in a couple of
places it is genuinely ahead of the comparable open-source apps. The single
most important design decision — feeding terminal bytes into libghostty through
the **host-managed in-memory I/O backend** instead of a local PTY — is exactly
the abstraction a remote-first terminal client should be built on, and atc is
already using it correctly.

The gaps are not architectural. They are in **connection lifecycle and
resilience**, which is precisely the area the product depends on most and the
area the current implementation invests in least. The headline requirement —
"sessions survive or seamlessly reconnect when a user closes and opens a
laptop" — is **not met today**: reconnection is manual (a button), there is no
sleep/wake handling, and a reconnect can visually corrupt the screen. Those are
fixable with contained, well-understood work. The rest of this document lays
out what exists, what is strong, and a prioritized set of changes.

---

## 2. How it works today

The data path for one live terminal, end to end:

```
Ghostty surface (macOS, Metal)
   │  key/mouse → ghostty_surface_key → receiveBufferCallback
   ▼
InMemoryTerminalSession.write closure           (GhosttyTerminal, Lakr233 pkg)
   ▼
ConnectionRef → AttachConnection.enqueue         (TerminalBridge/AttachConnection.swift)
   ▼  bounded lock-guarded outbound queue, resize-ahead-of-data
URLSessionWebSocketTask  ── wss binary=keys, text=resize ──►
   ▼
coder/websocket Accept + read loop               (server/internal/api/attach.go)
   ▼
zmx.PTY  (os.File master of `zmx attach <name>`) (server/internal/zmx/zmx.go)
   ▼
zmx daemon  ── ghostty-vt server-side state + scrollback ──
   ▼
session PTY → the actual program (Claude Code, nvim, shell)
```

Output flows back up the same path: the server's PTY→WS pump reads 32 KiB
chunks and writes them as binary frames; the client's receive loop yields
`.output(Data)` events; `TerminalSessionController.deliver` writes them into the
surface via `ghostty_surface_write_buffer`.

Key properties confirmed by reading the code and probing a live `zmx`:

- **Two Ghostty VT engines in the stack.** zmx embeds `ghostty-vt` server-side
  (confirmed: `zmx --version` reports `ghostty_vt ghostty-1.3.2-dev`), so it
  keeps authoritative screen + scrollback state and **replays a reconstructed
  snapshot on attach** rather than a raw byte log. The client then renders that
  snapshot with full libghostty. This is a strong, deliberate design.
- **Rendering is display-link paced and coalescing.** Incoming bytes trigger a
  Ghostty wakeup → `requestImmediateTick()` → the next `MSDisplayLink` frame
  runs `ghostty_app_tick` + `surface.draw()`. Bursts of output collapse into one
  frame per refresh interval; there is no per-write repaint. Throughput for fast
  TUIs is not a concern on the render side.
- **Surfaces and connections survive sidebar navigation.** `AppModel.terminals`
  retains controllers; `TerminalPane` stacks all surfaces and toggles opacity,
  so switching sessions never tears down a surface or drops a socket.
- **The outbound (keystroke) path is carefully ordered and bounded.** Ghostty
  callbacks enqueue synchronously through a lock; resize is always sent ahead of
  data; the queue drops excess past 1 MiB rather than growing without bound.
- **Auth works for both native and browser attach.** Native sends
  `Authorization: Bearer`; browsers can't set WS headers, so the server also
  accepts the token via a `Sec-WebSocket-Protocol` subprotocol, compared in
  constant time.

---

## 3. What is already strong (keep it)

1. **Host-managed I/O backend is the correct core.** `InMemoryTerminalSession(write:resize:)`
   cleanly separates "render a terminal" from "where do the bytes come from."
   This is the same separation cmux reached for its cloud-VM WS transport, and
   it is what lets atc stay sandbox-friendly and never spawn a local PTY. No
   change needed.
2. **Snapshot-on-attach via zmx/ghostty-vt.** You get correct state restoration
   regardless of how long a client was detached, without buffering an unbounded
   byte history. Most projects in this space (Eternal Terminal, wezterm) build
   this themselves; atc gets it from zmx. Lean into it.
3. **The binary-data / text-control frame split** on the attach socket matches
   cmux's WS PTY transport exactly and is the conventional, correct design.
4. **Resize-before-data ordering and the mandatory initial resize.** zmx needs
   dimensions before it repaints; the client resends the last viewport on every
   (re)connect. This is the right sequencing.
5. **Server→client flow control is better than it looks.** The PTY→WS pump has no
   explicit bounded queue, but `conn.Write` blocks on TCP backpressure, which
   stops the pump reading the PTY, which fills the attach PTY, which back-pressures
   the producing program through the OS PTY. A `cat hugefile` naturally throttles
   to the client's drain rate. (This is the piece VS Code, tmux, and iTerm2 all
   had to engineer explicitly — atc gets a usable version for free from the PTY
   layering. See §4.2 for the one sharp edge.)
6. **Render lifecycle correctness in the Lakr233 layer** is unusually thorough:
   deferred surface creation until nonzero backing size, cross-display scale
   rescue, occlusion-aware ticking, teardown/rebuild that preserves scrollback.
   These are exactly the libghostty-from-Swift gotchas agterm documents, and
   they're already handled.

---

## 4. Findings, prioritized

Severity: **P0** blocks a stated core requirement; **P1** fix before more
terminal features; **P2** real defect, contained impact; **P3** hygiene/polish.

### P0-1 — No automatic reconnect and no sleep/wake handling

**This is the gap between the product's stated promise and its behavior.**

Evidence:
- `TerminalSessionController.reconnect()` exists but is only ever called from a
  **button** in `TerminalPane` (`Button("Reconnect")`). Nothing calls it
  automatically.
- There is no `NSWorkspace.willSleepNotification` / `didWakeNotification`
  observer anywhere in the app (grep confirms zero references to sleep/wake).
- On sleep, the TCP connection is silently dead. Detection relies on the 30 s
  ping loop (`AttachConnection.pingInterval = .seconds(30)`) or the next failed
  send, so the app can sit on a zombie socket for up to ~30 s before the phase
  even flips to `.ended(.transportFailure)`.
- Once ended, it stays ended until the user clicks Reconnect. Close laptop → open
  laptop → the terminal is dead with a "Disconnected" banner.

Impact: directly violates "sessions should survive or seamlessly reconnect when
users close and open a laptop." Today the session *does* survive server-side
(zmx keeps running — good), but the client does not re-attach on its own.

Recommendation:
- Add automatic reconnection with capped exponential backoff (e.g. 0.5 s → 8 s,
  jittered) on `.transportFailure` and `.serverError`, distinct from
  `.sessionEnded` (which is terminal) and `.closedByClient` (user intent).
- Observe `NSWorkspace.didWakeNotification` and network-path changes
  (`NWPathMonitor`) and proactively re-attach live controllers instead of
  waiting for the ping to time out.
- Lower the liveness-detection latency: shorten the ping interval (e.g. 10 s) or,
  better, drive reconnection from wake/path events so ping is only a backstop.
- Keep the manual Reconnect button as a fallback.

This is the highest-value work in the entire review.

### P1-1 — Reconnect replays a full snapshot into a surface that already has content

Evidence:
- On reconnect, `TerminalSessionController.connect()` makes a **new**
  `AttachConnection` but keeps the **same** `InMemoryTerminalSession` and Ghostty
  surface (the controller and its `viewState` are retained).
- zmx replays a full screen (and scrollback) snapshot immediately on every
  attach (confirmed via `zmx attach` behavior and the `handle(.connected)`
  comment: "zmx needs dimensions before it repaints").
- That snapshot is written into a surface that still holds the pre-disconnect
  screen. There is no clear/reset (`\e[2J\e[3J\e[H`) between the old content and
  the replayed snapshot.

Impact: after a reconnect the user can see duplicated prompts, doubled TUI
frames, or scrollback artifacts. For full-screen apps (nvim, Claude Code) the
alternate-screen redraw usually paints over it, but line-oriented shells will
show visible duplication. This undpercuts the "seamless reconnect" goal even
once P0-1 makes reconnect automatic.

Recommendation:
- On a *reconnect* (as opposed to first connect), reset the surface before
  draining the replayed snapshot — either feed a hard reset/clear sequence into
  `InMemoryTerminalSession.receive` or tear down and recreate the surface so the
  snapshot lands on a clean grid.
- Confirm what zmx emits on attach by reading its source (`src/main.zig`); its
  detach/attach buffering semantics are under-documented upstream and the exact
  bytes matter here.

### P1-2 — The 10 s write timeout turns a briefly-stalled client into a dropped session

Evidence:
- `attach.go` bounds each `conn.Write` with `context.WithTimeout(ctx, 10*time.Second)`.
- The natural PTY backpressure described in §3.5 means a slow-draining client
  *should* just throttle the producer — but if the client stalls for >10 s (busy
  main thread, a large paste being processed, a brief network stall short of a
  full disconnect), the write times out and the whole bridge tears down.

Impact: a recoverable slow-consumer moment becomes a disconnect, which (post
P0-1) then triggers a reconnect + full snapshot replay. More disruptive than the
stall it's guarding against.

Recommendation:
- Decide the intent explicitly. If the goal is "detect a dead client," a longer
  timeout plus the existing ping/keepalive is sufficient and less trigger-happy.
- If you want true bounded buffering independent of TCP, add an explicit bounded
  outbound queue on the server side that drops-to-reconnect only when genuinely
  saturated, rather than a per-write wall-clock deadline.

### P1-3 — Dependency risk: patched single-maintainer fork on an explicitly unstable upstream

Evidence:
- `libghostty-spm` (Lakr233) pulls a **prebuilt xcframework** from that repo's
  own GitHub Releases (`storage.1.2.8`, checksum-pinned) and its build applies
  **non-upstream patches**, most importantly the `HOST_MANAGED` I/O backend that
  atc's entire remote model depends on.
- Upstream libghostty is pre-1.0 with **no API-stability promise** (Ghostty 1.3.0
  release notes, March 2026: "we aren't sure yet when we'll tag the first
  libghostty releases"). The C surface/renderer API atc uses is further from
  stable than libghostty-vt.
- Release cadence is ~weekly by a single maintainer; there is no official
  ghostty-org SwiftPM channel to fall back to.

Impact: the load-bearing seam of the product is a patched fork that may never be
upstreamed as-is, from one maintainer, tracking a moving target. Not a reason to
change course now — it's the best option available and the abstraction is right —
but it is concentrated supply-chain risk that deserves a deliberate stance.

Recommendation:
- **Pin and vendor deliberately.** The xcframework is already checksum-pinned;
  keep a mirror of the exact `.xcframework.zip` you ship so a deleted/retagged
  release can't break your build.
- Keep the app's Ghostty dependency **contained behind the seam it already has**
  (`TerminalHostView` + the controller/bridge) so a future swap to a
  source-built GhosttyKit — or to official upstream packaging when it lands — is
  a localized change. This is already noted as a design goal in
  `TerminalHostView.swift`; make it an explicit invariant with a test.
- Track upstream's libghostty-vt / C API progress; the moment ghostty-org ships
  a stable embeddable API with a host-managed backend, plan the migration.
- Consider building the xcframework from a pinned ghostty commit in CI (agterm's
  approach) rather than trusting a third party's release binary, if the supply
  chain matters more than the maintenance cost.

### P2-1 — Every keystroke pays a full network round trip (no local/predictive echo)

Evidence:
- The in-memory backend sends key bytes out via the `write` closure; the terminal
  does **not** echo locally. Characters appear only when the remote PTY echoes
  them back and the bytes return over the WS.
- There is no predictive-echo layer (mosh-style) anywhere in the path.

Impact: on a LAN/tailnet this is invisible. But remote is the *stated primary*
use case, and over a high-RTT link (coffee-shop wifi to a home workstation,
cross-region VM) typing will feel laggy — every character waits a round trip.
mosh exists specifically because this is the dominant felt-latency problem in
remote terminals.

Recommendation:
- Treat as a **known, deferred** item, not a near-term build. Predictive echo is
  hard to do correctly (mosh speculatively renders ~70% of keystrokes with
  underline-until-confirmed and careful rollback).
- Before investing, measure: instrument keystroke→echo latency on a real remote
  link. If the product is used mostly over tailnet/LAN, the ROI is low.
- If it becomes necessary, the in-memory backend is actually a decent place to
  layer speculative echo, because atc controls both the byte feed and the
  render — but scope it as its own project.

### P2-2 — Large pastes are silently truncated at 1 MiB

Evidence:
- `OutboundQueue.maxBufferedBytes = 1 << 20`; `enqueue` drops data whole when the
  buffer would exceed it. The server's read limit is also 1 MiB per frame, and
  `readInitialAttachInput` caps pre-resize input at 256 KiB.

Impact: pasting a large payload (a big file, a long diff into Claude Code) while
the socket is momentarily behind can silently lose bytes with no user feedback.
For a coding tool this is a correctness surprise.

Recommendation:
- Prefer backpressure over silent drop for the outbound path: when the queue is
  full, apply flow control (stop accepting from the Ghostty callback / signal the
  paste path) rather than discarding. If a hard cap must remain, surface it to
  the user instead of dropping silently.
- Revisit whether 1 MiB is the right ceiling for a paste-heavy coding workflow.

### P2-3 — Dead-peer detection is slow (30 s ping)

Covered under P0-1 but worth calling out independently: even with manual
reconnect, a 30 s ping interval means the UI can misreport a dead session as
live for half a minute. Tighten alongside the reconnect work.

### P3-1 — Double-PTY hop adds a resize/lifecycle layer

Evidence: the Go server wraps `zmx attach` in a **real OS PTY**
(`pty.StartWithSize`), so resize flows client → WS → `pty.Setsize` → zmx →
session PTY. cmux's daemon avoids this by owning the PTY directly.

Impact: minor today; it's an extra hop where resize or teardown can race, and an
extra process (`zmx attach`) per attach to reap. Works, but it's the kind of
indirection that accretes edge cases. No action needed now; note it if resize
glitches appear.

### P3-2 — TERM / terminfo mismatch

Evidence: the server launches sessions with `TERM=xterm-256color`
(`zmx.sessionRunEnv`), while the actual renderer is Ghostty (whose native
terminfo is `xterm-ghostty`). This is a safe, deliberate choice —
`xterm-256color` is universally present — but it means Ghostty-specific
capabilities aren't advertised to programs. Acceptable; documenting the tradeoff
is enough.

---

## 5. How atc compares to the reference projects

| Concern | atc today | cmux | agterm | mosh/tmux/ET |
| --- | --- | --- | --- | --- |
| Renderer | libghostty (host-managed I/O) | libghostty (forked) | libghostty (upstream, pinned) | n/a |
| Remote transport | WS, binary data + JSON control | WS + JSON-RPC/SSH; tmux -CC | none (local only) | UDP-SSP / ssh |
| Persistence | zmx + ghostty-vt snapshot | daemon PTY + tmux | structural restore only | server state |
| Reconnect | **manual button** | daemon reattach | n/a | automatic, roaming |
| Sleep/wake | **none** | daemon-side survives | n/a | automatic |
| Flow control | PTY backpressure (implicit) | WS/TCP (unverified) | n/a | explicit (SSP/pause) |
| Predictive echo | none | none | n/a | mosh only |
| Multi-client resize | single client | smallest-wins | n/a | n/a |

Takeaways:
- atc's **rendering and persistence** choices are on par with or better than
  cmux (snapshot-on-attach via ghostty-vt is cleaner than replaying a byte log).
- atc's **connection resilience** is behind cmux and far behind mosh. That is the
  area to close.
- agterm is the reference for **libghostty-from-Swift correctness** — and the
  Lakr233 layer atc uses already implements most of what agterm documents
  (string lifetimes, deferred surface creation, callback threading, occlusion).

---

## 6. Recommended roadmap

1. **Make reconnection real (P0-1).** Auto-reconnect with backoff + jitter;
   `NSWorkspace.didWake` and `NWPathMonitor` re-attach; shorter ping backstop.
   This alone delivers the "survive close/open laptop" promise.
2. **Make reconnect clean (P1-1).** Reset/clear the surface before draining the
   replayed snapshot so reconnection is visually seamless, not doubled.
3. **Right-size the server write timeout (P1-2)** so a transient stall throttles
   instead of dropping.
4. **Formalize the Ghostty dependency stance (P1-3).** Mirror the pinned
   xcframework, harden the containment seam with a test, and track upstream's
   stable C API.
5. **Fix silent paste truncation (P2-2).**
6. **Instrument and defer predictive echo (P2-1).** Measure real remote latency
   before deciding whether to build it.

Nothing here requires abandoning the current design. The architecture is sound;
the work is to make the connection lifecycle as robust as the rendering already
is.

---

## Appendix: primary evidence

- Client bridge: `macos/ATC/TerminalBridge/AttachConnection.swift`,
  `macos/ATC/Features/Terminal/TerminalSessionController.swift`,
  `macos/ATC/Features/Terminal/TerminalPane.swift`, `.../TerminalHostView.swift`,
  `macos/ATC/AppModel.swift`, `macos/ATC/ConnectionRuntime.swift`.
- Server bridge: `server/internal/api/attach.go`,
  `server/internal/session/session.go`, `server/internal/zmx/zmx.go`,
  `server/internal/server/auth.go`.
- Ghostty layer (read from the resolved SPM checkout):
  `GhosttyTerminal/InMemory/InMemoryTerminalSession.swift`,
  `.../Surface/TerminalSurfaceCoordinator.swift`,
  `.../Platform/AppKit/AppTerminalView+Lifecycle.swift`,
  `.../Controller/TerminalController.swift`; `Package.swift` pins
  `libghostty` xcframework `storage.1.2.8` (built from ghostty ~1.2).
- Live probe: `zmx 0.6.0`, embedding `ghostty_vt ghostty-1.3.2-dev`; confirmed
  screen+scrollback replay on attach.
- External prior art (cmux, agterm, zmx, libghostty upstream, mosh/ET/wezterm/
  VS Code flow control) summarized in §5 from a parallel research pass.
