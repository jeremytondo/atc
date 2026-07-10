> **Historical (archived 2026-07):** Describes the pre-monorepo Cockpit-era system. Names, paths, and instructions here are obsolete — see AGENTS.md and docs/platform-policy.md for current structure and policy.

# AtelierCode POC — Native macOS Cockpit Client with Embedded libghostty Terminal

## Context

AtelierCode is a proof-of-concept native macOS app for Cockpit (`github.com/jeremytondo/cockpit`), the self-hosted Go service that manages persistent zmx-backed terminal sessions running AI coding agents. The web UI has no session-management + terminal combo in a native shell; this POC proves two things: (1) SwiftUI + libghostty can coexist for working inside Cockpit sessions, and (2) a layered architecture that survives future UI/feature churn. MVP: list / create / stop / archive sessions, and connect to one in an embedded terminal.

**Key facts verified against the Cockpit source** (cloned read-only to scratchpad):
- Server: Go, loopback-only `127.0.0.1:7331`, all routes under `/api`, optional bearer token (disabled by default). **SSH port-forwarding works** (`ssh -L 7331:127.0.0.1:7331 <host>`) — nothing in the API is hostname-dependent.
- **Attach is a WebSocket, not SSH**: `GET /api/sessions/{id}/attach` — server→client binary frames = terminal output, client→server binary = keystrokes, client→server TEXT frame = `{"type":"resize","cols":<u16>,"rows":<u16>}`. Close codes: 1000 `session_ended`, 1011 `internal_error`. 1 MiB frame read limit. The app never runs `ssh`/`zmx` itself.
- Session model: status enum `starting|running|failed|terminated`; **archived is not a status** — it's a non-null `archivedAt`. Timestamps are Go RFC3339Nano with *variable* fractional digits. List response is wrapped: `{"sessions":[...]}`. Errors: `{"error":"code","message":"...","sessionId?":""}`.
- Endpoints: `GET /sessions?includeArchived=&status=`, `GET /sessions/{id}`, `POST /sessions/start` `{action*, environment?, params?, workingDir*, prompt?, name?}`, `POST /sessions/{id}/terminate`, `POST /sessions/{id}/archive` (409 `session_live` if still running), `POST .../send-text|send-key`, `GET /actions` (params is a **map** `{name: ParamSpec}`), `GET /environments`, `GET /health|/version`. No SSE — polling only.

**Terminal layer decision**: use **Lakr233/libghostty-spm** (MIT, SwiftPM, prebuilt GhosttyKit.xcframework, v1.2.8, no Zig needed). It's deliberately built around libghostty's host-managed I/O backend (`InMemoryTerminalSession`) — a perfect match for the WS byte stream. Building GhosttyKit from ghostty source (Zig 0.15.2) is the documented fallback if rendering/config fidelity disappoints; all Ghostty imports are isolated to 2 files to keep that swap cheap.

**Connectivity — verified working (2026-07-03)**: `~/.ssh/config` has `Host workstation` (Tailscale MagicDNS) with `LocalForward` for 7331 and 5173. With `ssh -fN workstation` up, `http://127.0.0.1:7331/api/health`, `/version`, `/sessions`, `/actions`, `/environments` all respond with real data (dev server, 4 sessions, actions: claude/codex/lazygit, one environment `host-login-shell`). Live responses confirmed: `ses_`-prefixed IDs, 9-digit fractional RFC3339Nano timestamps (validating the custom date strategy), `actions[].params`/`prompt` as objects. Note: key auth required fixing `~/.ssh/id_rsa` to mode 600.

**Decisions**:
- **Connectivity — direct over Tailscale is the target setup**: Cockpit's listen address is configurable (`COCKPIT_HTTP_ADDR` / `--http-addr` / `[server].http_addr` in `cockpit.toml`); bind it to the workstation's tailnet address so the app talks straight to `http://workstation.tail1f9a09.ts.net:7331` (and `ws://...` for attach). One TCP connection over WireGuard, no tunnel process, no double encryption, survives network changes (stable tailnet IP). Bind the tailnet IP specifically (`COCKPIT_HTTP_ADDR=100.91.7.102:7331`), NOT `0.0.0.0` (which would also expose Cockpit — effectively RCE-as-you — to the home LAN). No token for dev: tailnet peers are already WireGuard-authenticated as the user's own devices, extending Cockpit's loopback trust model to the tailnet. The app still supports `COCKPIT_API_TOKEN` + bearer token in Settings if that changes. The `ssh -fN workstation` tunnel (verified above) remains the dev-era fallback — the app doesn't care which is used, it's just the base URL in Settings. No app-managed tunnel ever.
- **Terminal UI**: **single-window app, sidebar-driven**. Clicking a session in the sidebar opens its terminal in the main content area immediately (auto-attach if attachable); clicking another session swaps the visible terminal in place. Live connections persist across switches — no separate windows, no explicit Connect step.

## Architecture

- **`Packages/CockpitKit`** — local SPM package, target `CockpitAPI`: pure Foundation, no SwiftUI/Ghostty. Protocol `CockpitClient` (the interface the app depends on) + `HTTPCockpitClient` (URLSession implementation), Codable models, typed `CockpitError`, RFC3339Nano date strategy. Gives `swift test` with zero pbxproj surgery and enforces the layering boundary.
- **App target groups**: `Settings/` (UserDefaults-backed `AppSettings`), `Features/Sessions/` (`SessionsStore` + list/detail/create views), `Features/Terminal/` (terminal pane, per-session controller), `TerminalBridge/` (WS actor + Ghostty config loader).
- Modern Swift: `@Observable`, async/await, actor for the WS connection, Swift Testing. Note the project sets `SWIFT_DEFAULT_ACTOR_ISOLATION = MainActor` — off-main code (WS pump, Ghostty callbacks) needs explicit `nonisolated`/actor isolation; expect a compile-error round, not a design change.
- **Apple-recommended SwiftUI patterns — explicitly NO MVVM**: no per-view ViewModel classes. Views are the view layer *and* the presentation logic; view-local UI state lives in `@State`/`@FocusState` inside the view. Shared domain state lives in a few `@Observable` model objects (`AppModel`, `SessionsStore`, per-session `TerminalSessionController`) created at the app root and passed via `.environment`/init — these are domain models, not view models: they know nothing about views and would survive a total UI rewrite. Business logic goes in the stores and the `CockpitAPI` package; views stay declarative and dumb. Previews work by injecting `MockCockpitClient` into a store, not by mocking view models.

### File tree (new/modified)

```
Packages/CockpitKit/
├── Package.swift
├── Sources/CockpitAPI/
│   ├── CockpitClient.swift           # protocol (async throws)
│   ├── HTTPCockpitClient.swift       # URLSession impl
│   ├── CockpitServer.swift           # baseURL + token → REST/WS URLs, auth header
│   ├── CockpitError.swift            # .api(code,message,sessionID?) / .transport / .badStatus
│   ├── CockpitDateDecoding.swift     # RFC3339Nano strategy (see below)
│   └── Models/ SessionModels.swift, ActionModels.swift, EnvironmentModels.swift, RequestModels.swift
└── Tests/CockpitAPITests/            # Swift Testing; fixtures from real server responses
    DateDecodingTests.swift, SessionDecodingTests.swift, ErrorEnvelopeTests.swift

AtelierCode/AtelierCode/
├── AtelierCodeApp.swift              # MODIFY: AppModel DI, Settings scene (single main WindowGroup)
├── AppModel.swift                    # @Observable root; rebuilds client on settings change
├── ContentView.swift                 # REPLACE: NavigationSplitView shell
├── Settings/ AppSettings.swift, SettingsView.swift
├── Features/Sessions/ SessionsStore.swift, SessionListView.swift, SessionRowView.swift,
│                      StatusBadge.swift, SessionDetailView.swift, CreateSessionSheet.swift
├── Features/Terminal/ TerminalPane.swift, TerminalSessionController.swift, TerminalHostView.swift
└── TerminalBridge/ AttachConnection.swift, GhosttyConfigLoader.swift
```

### Tricky joints

**Date decoding** (Go RFC3339Nano, 0–9 fractional digits — `ISO8601DateFormatter` can't handle it):
```swift
static let rfc3339Nano = JSONDecoder.DateDecodingStrategy.custom { decoder in
    let s = try decoder.singleValueContainer().decode(String.self)
    if let d = try? Date(s, strategy: .iso8601.time(includingFractionalSeconds: true)) { return d }
    if let d = try? Date(s, strategy: .iso8601) { return d }
    throw DecodingError.dataCorrupted(...)
}
```
Unit-test 0/3/6/9-digit fixtures; if 9 digits fails, truncate fraction to 3 digits in this one file.

**WS ↔ terminal pump** (`AttachConnection` actor owning `URLSessionWebSocketTask`):
- Terminal→server: `InMemoryTerminalSession(write:resize:)` callbacks → binary frame / TEXT resize frame.
- Server→terminal: receive loop feeds binary data into the terminal surface. **Phase-4 verification item**: confirm libghostty-spm's exact API for pushing bytes *into* `InMemoryTerminalSession`.
- Send an initial resize right after connect (zmx needs dimensions).
- **Mandatory 30s ping loop** — URLSessionWebSocketTask doesn't auto-ping; detects dead peers promptly on the direct tailnet path and keeps the SSH-tunnel fallback alive (idle forwards die).
- Chunk outgoing writes >256 KiB (server 1 MiB frame limit).

## UI (macOS 26 HIG, stock chrome)

- **Dark mode only** (user decision): `.preferredColorScheme(.dark)` on the root scene content — the whole app renders dark regardless of the system setting. Don't fight it elsewhere: no light-variant assets, no `colorScheme` conditionals; pick colors once against the dark appearance (which also suits the Catppuccin Mocha terminal).
- **Main window**: `NavigationSplitView`. Sidebar `List(selection:)` sectioned Running / Starting / Failed / Terminated, archived behind a filter toggle; rows: name-or-action, workingDir caption, status dot (`StatusBadge`), a subtle "connected" indicator (e.g. `terminal` glyph) for sessions with a live attach. `.searchable`. Toolbar: New Session, Refresh, archived filter. `ContentUnavailableView` empty state.
- **Content area (single window, sidebar-driven)**: selecting an **attachable** session auto-attaches and shows its terminal immediately (connecting overlay while the WS handshakes) — no explicit Connect step. Selecting a **non-attachable** session (terminated/failed/archived) shows a metadata view via `LabeledContent` instead. A compact header bar above either: name, status dot, Stop/Archive (with `.confirmationDialog`), Disconnect when connected, and an `.inspector` toggle for full metadata alongside a live terminal.
  - `AppModel` keeps a `[SessionID: TerminalSessionController]` registry so **connections and terminal surfaces stay alive when you switch sessions in the sidebar** — implemented as a `ZStack` of all connected `TerminalHostView`s with only the selected one visible/hit-testable (avoids surface teardown and WS churn on every sidebar click).
  - Phase-driven banner overlays the terminal: connecting spinner / "Session ended" (1000) / "Server error" + Reconnect (1011) / "Disconnected" + Reconnect (transport drop).
  - Detach happens via an explicit **Disconnect** button, when the session ends, or on app quit (WS close = zmx detach; session keeps running server-side).
  - Archive disabled until terminated (mirror server rule, still surface 409 message).
  - Keyboard: `cmd+1..9` or `cmd+{`/`}` to jump between connected sessions is a cheap nice-to-have.
- **Create sheet**: `Form` — action Picker (enabled actions), environment Picker (preselect default), workingDir TextField (+ folder picker), name, prompt TextEditor (when action allows), generic param renderer (`values` → Picker, else TextField). Inline error text from the error envelope.
- **Settings scene**: server URL (default `http://workstation.tail1f9a09.ts.net:7331`; `http://127.0.0.1:7331` when using the SSH-tunnel fallback), optional bearer token (plain storage for POC; Keychain = stretch goal).

## Data flow

`AppModel` → `SessionsStore` (`@Observable @MainActor`, takes any `CockpitClient` so previews/tests can inject a mock): `sessions`, `isLoading`, `lastError`; `refresh()`; polling `Task` loop (~7s) started in root `.task {}` (auto-cancels); action methods merge the returned `SessionDetail` into the list immediately, then refresh. That's the whole sync story — no SSE exists.

## Phasing (checkpoint each green phase: `jj describe -m "<msg>" && jj new`; on broken build `jj undo`; never git)

- **Phase 0 — Hygiene**: set `ENABLE_APP_SANDBOX = NO` in both configs (POC needs loopback sockets + reading `~/.config/ghostty/config`; keep hardened runtime; re-sandboxing path documented: network.client entitlement + home-relative read exception). Add SPM deps: libghostty-spm pinned `from: "1.2.8"` (GhosttyKit, GhosttyTerminal, GhosttyTheme) + local `Packages/CockpitKit` stub. Verify: `xcodebuild ... build` green, app launches.
- **Phase 1 — CockpitAPI package**: all sources + decoding tests (fixtures curl'd from the live server). Verify: `swift test --package-path Packages/CockpitKit`.
- **Phase 2 — Sessions list UI**: AppModel/AppSettings/SettingsView/SessionsStore/shell/list + metadata view (terminal placeholder until Phase 4), polling. Verify against live server (`mise run serve` in cockpit repo): sessions appear, groups correct, poll picks up web-UI-started session ≤7s.
- **Phase 3 — Actions**: CreateSessionSheet (+ /actions,/environments), terminate/archive + confirmations + error alerts. Verify: create from app → visible in web UI; stop; archive; archive-while-running shows 409 message.
- **Phase 4 — Terminal attach (the POC linchpin)**: AttachConnection, TerminalSessionController + registry, TerminalHostView (`TerminalSurfaceOptions(backend: .inMemory(session))`), TerminalPane in the content area (ZStack of live surfaces, selected visible), compact header bar, auto-attach-on-select + Disconnect wiring, banners, reconnect, ping loop. Theme hardcoded Catppuccin Mocha via GhosttyTheme for now. Verify: typing echoes; `vim`/`htop` render (escape handling); pane resize changes `stty size` server-side; **switch to another session and back → terminal state intact, no reconnect**; Disconnect → session still running server-side; terminate from web UI → "Session ended" banner.
- **Phase 5 — Ghostty config polish (time-boxed)**: `GhosttyConfigLoader` — try C API via GhosttyKit (`ghostty_config_new` → `ghostty_config_load_default_files`); if awkward, hand-parse `~/.config/ghostty/config` for `theme` (→ GhosttyTheme), `font-family`, `font-size`, `window-padding-*`, `background-opacity`; per-key graceful fallback. Verify: side-by-side match with Ghostty.app; missing config → defaults, no crash.

## Verification

- Server: `mise run serve` in the cockpit repo on the workstation. Target setup: bind the tailnet address (`COCKPIT_HTTP_ADDR=100.91.7.102:7331` or via `cockpit.toml`), no token; sanity: `curl http://workstation.tail1f9a09.ts.net:7331/api/health` from the Mac. Dev fallback: loopback bind + `ssh -fN workstation` tunnel, `curl 127.0.0.1:7331/api/health`. Optional token path: set `COCKPIT_API_TOKEN` server-side + token in Settings, confirm 401→200.
- Package: `swift test --package-path Packages/CockpitKit`.
- App: `xcodebuild -project AtelierCode/AtelierCode.xcodeproj -scheme AtelierCode build` or Xcode MCP `BuildProject`; `RenderPreview` for list/detail/sheet previews.
- End-to-end: the Phase 4 checklist above is the acceptance test for the whole POC.

## Risks

1. **libghostty-spm API churn** (young wrapper) — pin exact version; Ghostty imports isolated to `TerminalHostView` + `GhosttyConfigLoader` so swapping to a source-built GhosttyKit touches 2 files.
2. **Config fidelity gap** — theme/font/padding best-effort; keybinds/splits out of scope by design (bytes come over WS, not a local Ghostty).
3. **WS keepalive** — the ping loop is mandatory, not polish.
4. **Fonts** — missing `font-family` must fall back to monospaced system font, never crash surface creation.
5. **MainActor-by-default isolation** — expect explicit `nonisolated`/`Sendable` work in the bridge layer.
