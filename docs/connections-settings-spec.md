# Connections and Settings — Spec and Implementation Plan

Status: Ready for implementation
Supersedes: `docs/connections-settings-plan.md` (Draft)
Related: `docs/adr/0002-local-connections-scope-cockpit-projects.md`, `CONTEXT.md` language definitions

## Goal

AtelierCode supports multiple named Connections to Cockpit servers. Projects and
Terminal Sessions remain Cockpit-owned records; the app displays Projects from
all configured Connections in one flat, project-first sidebar, shows the owning
Connection on each Project row, and manages Connections in a real macOS
Settings window.

## Changes from the draft plan

These are deliberate deviations or additions relative to
`docs/connections-settings-plan.md`. Everything else in the draft is confirmed
as written.

1. **Phase 0 is real work, not a mirror.** The draft said archive-with-active-
   sessions "should be enforced by Cockpit." It is not enforced today:
   `store.ArchiveProject` is a bare `UPDATE ... SET archived_at` with no session
   check. Phase 0 adds that enforcement server-side. This matters beyond
   correctness: removing `Other Sessions` from the sidebar is only safe if the
   server guarantees an active session can never belong to an archived project.
2. **Legacy settings migration added.** The draft says first launch shows an
   empty state and never seeds a `Workstation` Connection — confirmed — but it
   is silent about existing installs, which store `serverURLString` and
   `apiToken` in UserDefaults. We migrate those into a single Connection once,
   then delete the legacy keys. Without this, shipping the feature silently
   disconnects every existing install.
3. **Reachability is defined as poll outcome, not a separate health pinger.**
   The draft never said where the status dot's state comes from. Decision: the
   dot reflects the most recent per-Connection refresh result (the existing
   ~7s poll cadence). Gray = no completed refresh yet, green = last refresh
   succeeded, red = last refresh failed. No dedicated `/health` polling loop.
4. **Architecture decision made explicit: per-Connection runtimes.** The draft
   said "replace the single shared client/store assumption." Decision: keep
   `ProjectsStore`/`SessionsStore` exactly as they are (single-client,
   well-tested) and instantiate one of each per Connection inside a new
   `ConnectionRuntime`. Aggregation happens above the stores in pure code, not
   by making the stores multi-tenant.
5. **Composite identity defined.** Selection, the terminal registry, and every
   cross-Connection reference use `connectionID + server record ID`, never a
   bare session/project ID.
6. **Archived-project session reachability defined.** With `Other Sessions`
   gone, sessions of an archived project are reachable only by enabling the
   archived filter (the archived project row appears with its sessions nested).
   This is acceptable and intentional; it is the same visibility rule the
   project itself follows.

## Non-Goals (unchanged from draft)

- No Cockpit API changes for Connection storage — Connections are app-local.
- No cross-server Project identity.
- No durable offline Project cache.
- No user-configurable Connection colors; chip color never identifies a Connection.
- No Connection reordering in v1.
- No Keychain token storage in this pass.
- No in-main-window Settings overlay.
- No app UI for unscoped Terminal Sessions in v1 (Cockpit API/CLI keep supporting them).
- No hardcoded seeded production Connection.

---

# Part 1 — Specification

## 1. Connection model and persistence

A Connection is an app-local record:

```swift
struct ConnectionRecord: Codable, Identifiable, Hashable, Sendable {
    let id: UUID            // stable local identity, never shown to the user
    var name: String        // required, app-chosen, uniqueness NOT enforced
    var urlString: String   // normalized origin-only URL, explicit http/https
    var token: String       // "" means no token
}
```

- Persisted as one JSON-encoded `[ConnectionRecord]` array under a single
  UserDefaults key (`connections`). Array order **is** creation order; no
  separate position field. New Connections append.
- Lives in the app target (e.g. `Settings/ConnectionsModel.swift` or similar),
  not in CockpitKit — CockpitKit stays a pure API client package.
- A `ConnectionsStore` (`@Observable`) owns the array, performs validation,
  persists on every mutation, and exposes add/update/remove.

### URL validation and normalization

Applied when saving a draft (add or edit):

- Trim whitespace. If no scheme, infer `http://` for validation and **persist
  the explicit scheme** — saved URLs are always explicit `http` or `https`.
- Scheme must be `http` or `https`; anything else is invalid.
- Host is required.
- URL must be origin-only: no path (a single trailing `/` is stripped, any
  other path is invalid), no query, no fragment, no userinfo.
- Effective port = explicit port, else 80 for `http`, 443 for `https`.

### Duplicate rule

Two Connections are duplicates when `lowercased(host)` and effective port
match. Scheme is ignored for duplicate detection (per draft). Saving a
duplicate is rejected with an inline error. The record being edited is excluded
from its own duplicate check.

### Legacy migration

On first launch of the new version, if the legacy `serverURLString` key exists
in UserDefaults and holds a valid URL:

- Create one Connection: name derived from the URL host (e.g. first host
  label, capitalized — `workstation.tail…` → `Workstation`), URL from
  `serverURLString`, token from `apiToken`.
- Delete both legacy keys. Migration runs at most once (keyed off the presence
  of the legacy keys themselves; once deleted it cannot re-run).
- If the legacy key is absent or invalid, do nothing — first launch shows the
  empty state. `AppSettings.defaultServerURLString` is deleted outright; it
  must not survive as a fallback anywhere (check `AppModel.makeClient` and
  `CockpitServerTests`).

## 2. Connection-aware app state

### ConnectionRuntime

`AppModel` replaces its single `client` + two stores with:

```swift
@Observable final class ConnectionRuntime: Identifiable {
    let record: ConnectionRecord        // snapshot at build time
    let client: any CockpitClient
    let projects: ProjectsStore
    let sessions: SessionsStore
    var reachability: Reachability      // .unknown / .connected / .unreachable
}
```

- `AppModel` holds `private(set) var runtimes: [UUID: ConnectionRuntime]` (or
  an ordered array), rebuilt from `ConnectionsStore` state.
- `ProjectsStore` and `SessionsStore` are unchanged internally. Each runtime
  owns one of each, polling on the existing cadence. Poll loops start/stop with
  runtime lifecycle (task per runtime, cancelled on teardown).
- `Reachability` is derived from each refresh completion: success → green,
  thrown error → red, never-completed → gray. A Connection that goes red keeps
  its last successfully loaded Projects/Sessions in memory for the running app
  session (stores already behave this way — `lastError` doesn't clear data).

### Identity

```swift
struct SessionRef: Hashable { let connectionID: UUID; let sessionID: String }
struct ProjectRef: Hashable { let connectionID: UUID; let projectID: String }
```

- `ContentView` selection becomes `SessionRef?` (today it is a bare
  `String?`).
- The terminal registry becomes `[SessionRef: TerminalSessionController]`.
  `TerminalSessionController` itself is unchanged — it already snapshots
  `attachURL`/`attachHeaders` from the client it was constructed with; the
  registry just constructs it from the right runtime's client.

### Edit/save/delete semantics

- **Saving a name-only change** updates the record; no client rebuild, no
  disconnects. Chips re-render with the new name.
- **Saving a URL or token change** rebuilds that Connection's runtime
  (new client, fresh stores, immediate refresh). If that Connection has live
  terminal attaches, a confirmation dialog explains they will be disconnected;
  Cancel aborts the save entirely. Other Connections are untouched.
- **Deleting a Connection** always confirms, and the dialog states that
  Projects and Terminal Sessions remain on the Cockpit server. Delete tears
  down the runtime, disconnects its terminals, clears selection if the
  selected session belonged to it, and removes the record. Local-only; no
  server calls.
- Stale async results (a Test Connection or poll finishing after its
  Connection was edited/removed) must be dropped harmlessly — reuse the
  `refreshGeneration` pattern already in the stores, and generation-guard the
  Test Connection task in the Settings UI.

## 3. Settings window

- The SwiftUI `Settings` scene remains the canonical entry point (⌘,). Add an
  always-visible main-window toolbar button that opens the same window via
  `@Environment(\.openSettings)`.
- Layout is a sidebar-style, multi-section-capable settings UI with
  `Connections` as the only v1 section.
- **Connections section**: list of Connections (name + URL + status dot), Add
  (+) and Remove (−) controls, and a detail editor.
- **Editor uses draft fields** (local `@State` copies) with explicit
  Save/Cancel. Nothing touches `ConnectionsStore` or any runtime until Save.
  Validation errors (bad URL, duplicate host+port, empty name) render inline
  and block Save.
- **Test Connection** is enabled whenever the draft URL parses (independent of
  save state). It builds a throwaway `HTTPCockpitClient` from the draft values
  and calls `health()` then `version()`. Success shows server name + version
  (tolerate `"dev"`/`"unknown"` builds); failure shows the error. Results are
  generation-guarded so edits invalidate in-flight tests.
- Empty state (no Connections configured): both the Settings section and the
  main window show a friendly empty state with an Add Connection path. No
  seeded defaults.

## 4. Sidebar

- One flat list of Projects aggregated across all runtimes, replacing the
  current single-store list. Grouping stays in a pure, testable struct —
  extend `SidebarGroups` to take `[(connection, projects, sessions)]` input
  and emit connection-qualified rows (`ProjectRef`/`SessionRef` identity).
- Ordering: by Connection creation order, then each Connection's existing
  server ordering (newest-first as returned). No additional sort policy unless
  the UI proves confusing.
- **Connection chip** on every Project row: Connection name + status dot
  (gray/green/red per §2). Chip color is neutral and never identifies the
  Connection.
- **`Other Sessions` is removed.** The sidebar contains only real Project rows
  from loaded data; it never invents rows for broken Connections. Unscoped
  sessions no longer appear in the app (v1 decision). Sessions of archived
  projects appear only under the archived project row when the archived filter
  is on. This is safe because Phase 0 guarantees active sessions can't be
  orphaned by archiving.
- **Archived filter moves** from the window toolbar toggle to a compact
  filter/menu control on the project list itself (e.g. a filter menu in the
  sidebar header). It drives `includeArchived` on all runtimes' stores.
- **Archive gating**: the Archive action on a Project is disabled when any of
  its known sessions is `starting` or `running` (mirroring Phase 0's server
  rule). If the app's view is stale and the request goes through anyway, the
  server's 409 surfaces as the normal store error alert.

## 5. Creation flows

- **New Project** gains a Connection selector. Preselect the first Connection
  in creation order; the selection is always visible and changeable. The
  folder picker is disabled until a Connection is selected (browsing is
  server-specific) and browses through the selected Connection's client.
  Changing the selected Connection clears the Chosen Folder but keeps the
  typed name. Create routes through the selected runtime's `ProjectsStore`.
- **New Session** is created against the target Project's Connection: action
  loading (`client.actions()`), the start request, and the subsequent terminal
  attach all use that runtime's client. The existing archived-project block
  stays.
- Terminal attach continues to auto-attach on selection, now resolving the
  controller through `SessionRef`.

## 6. Cockpit facts this spec relies on (verified against the repo and live server)

- `GET /api/health` → `{"status":"ok"}` and `GET /api/version` →
  `{name, version, commit}` both exist — Test Connection needs no server work.
- Auth is an optional bearer token (`Authorization: Bearer …`), enforced only
  on TCP when configured; empty token disables auth. The client already sends
  this header when a token is set.
- `GET /api/sessions` includes the nested `project` object, so one sessions
  call per Connection is enough for grouping.
- Projects have no DELETE — archive/unarchive only — so a session's project
  can never vanish server-side, only hide.
- Session statuses are exactly `starting`, `running`, `failed`, `terminated`.

---

# Part 2 — Implementation Plan

Checkpoint with `jj describe`/`jj new` after each phase (and after coherent
steps within a phase) once tests pass.

## Phase 0 — Cockpit API changes (do first, in the cockpit repo)

The only server-side prerequisite. Everything else in this spec is app-local.

1. **Enforce archive-with-active-sessions.** `POST /api/projects/{id}/archive`
   must fail when the project has any session with status `starting` or
   `running`. Return 409 with a stable error code, proposed
   `{"error":"project_has_active_sessions", "message":…}`, matching the
   existing error envelope. Implement the check in the project service/store
   layer (transactionally with the archive update, so a session starting
   concurrently can't slip through), not just the handler.
2. **CLI + web UI parity.** Surface the new 409 sensibly in the Cockpit CLI
   and existing web UI (they share wire structs; likely just error-message
   passthrough).
3. **Tests** in the cockpit repo covering: archive blocked with a `starting`
   session, blocked with a `running` session, allowed with only
   `failed`/`terminated`/archived sessions, allowed with no sessions.
4. Deploy to the workstation before starting Phase 4 (the sidebar phase that
   removes `Other Sessions`). Phases 1–3 don't depend on it.

Optional, non-blocking: release builds currently report `version: "dev",
commit: "unknown"`; stamping real build info would make Test Connection output
nicer, but the app must tolerate the dev values regardless.

## Phase 1 — Connection model, persistence, migration

App-local, no UI yet. Everything keeps compiling because the old `AppSettings`
stays in place until Phase 3.

1. Add `ConnectionRecord` + URL validation/normalization (pure functions:
   normalize, validate, effective-port, duplicate check).
2. Add `ConnectionsStore` (`@Observable`): load/persist JSON array under the
   `connections` UserDefaults key; add/update/remove with validation;
   creation-order semantics via array order.
3. Add legacy migration from `serverURLString`/`apiToken` (runs in
   `ConnectionsStore.init` or app startup; deletes legacy keys after).
4. **Tests** (`AtelierCodeTests`): validation matrix (scheme inference,
   explicit-scheme persistence, origin-only rejection of path/query/fragment,
   trailing-slash normalization), duplicate detection (same host+port across
   schemes, differing effective ports, case-insensitive host, self-exclusion
   on edit), persistence round-trip against an injected `UserDefaults`
   (`UserDefaults(suiteName:)`), migration (legacy keys present/absent/invalid,
   keys removed after, runs once).

## Phase 2 — Settings window Connections UI

1. Rebuild `SettingsView` as the sidebar-style multi-section layout with the
   single `Connections` section (list + editor as specced in §3).
2. Draft-field editor with Save/Cancel, inline validation, Add/Remove with
   the delete confirmation copy ("server data remains").
3. Test Connection action with generation guard; throwaway client from draft
   values; health + version display.
4. Add the main-window toolbar Settings button (`openSettings`).
5. At this point Settings edits the Connection list but the running app still
   uses the old single-client path — acceptable mid-refactor state within the
   branch; note it in the checkpoint message.
6. **Tests**: Settings hosting smoke test (NSHostingView + run-loop pump,
   same pattern as `ProjectUIHostingSmokeTest`); draft save/cancel behavior
   if extracted into a testable draft model.

## Phase 3 — Connection-aware app state

The structural core; biggest phase.

1. Add `ConnectionRuntime` and rework `AppModel`: `runtimes` keyed by
   Connection ID, built from `ConnectionsStore`; per-runtime poll tasks
   (started from the root `.task`, cancelled on teardown); delete
   `AppSettings`, `rebuildClient()`, `defaultServerURLString`, and the old
   single `client`.
2. Add `SessionRef`/`ProjectRef`; convert `ContentView` selection and the
   terminal registry to `SessionRef`.
3. Implement edit/save/delete semantics from §2: name-only fast path; URL/token
   change → confirm-if-attached → rebuild runtime; delete → teardown +
   selection cleanup. Wire Settings Save to these.
4. Reachability tracking on refresh completion.
5. **Tests**: runtime lifecycle (add/edit/delete rebuilds only the affected
   runtime; others' store instances are untouched), selection cleared when its
   Connection is deleted, `SessionRef` identity, reachability transitions
   (mock client that fails on demand), stale-result harmlessness on
   edit-during-refresh.

## Phase 4 — Sidebar (requires Phase 0 deployed)

1. Extend `SidebarGroups` to multi-connection input and ref-based identity;
   delete the `Other Sessions` bucket and its reachability-fallback routing.
2. Update `ProjectSidebarView`: flat aggregated project list, Connection chip
   with status dot on each row, per-ref disclosure/collapse state.
3. Move the archived toggle from the window toolbar to the sidebar filter
   menu.
4. Disable the Archive context-menu action when the project has known
   `starting`/`running` sessions; let the server 409 surface via the existing
   error alert as the stale-view fallback.
5. Empty states: no Connections at all → main-window empty state with an
   Add Connection path into Settings; Connections configured but nothing
   loaded yet → existing loading behavior per store.
6. **Tests**: `SidebarGroupsTests` rewritten for multi-connection input —
   aggregation order (connection creation order, then server order), no
   `Other Sessions` under any input (unscoped sessions dropped, archived-
   project sessions nested under the archived row only when the filter is on),
   chip/reachability data plumbing, archive-disabled predicate. Hosting smoke
   test updated for the new row content.

## Phase 5 — Creation flows

1. `CreateProjectSheet`: Connection selector (preselect first by creation
   order), folder picker disabled until a Connection is selected and bound to
   the selected runtime's client, Chosen Folder cleared on Connection change
   (name preserved), create routed to the selected runtime.
2. `CreateSessionSheet` + attach path: route actions/start/attach through the
   owning runtime resolved from the target `ProjectRef`.
3. Update `MockCockpitClient`/preview fixtures to multi-connection shapes
   where previews need them.
4. **Tests**: New Project selector behavior (preselection, directory cleared
   on connection change, name kept, picker disabled with no selection),
   session creation routed to the correct runtime's client (assert via
   distinct mock clients per runtime).

## Phase 6 — Cleanup and end-to-end pass

1. Sweep for dead code: `AppSettings`, legacy keys, `defaultServerURLString`
   references in tests, old toolbar archived toggle.
2. Full test run; manual verification against the live workstation server:
   add the workstation Connection (via migration or manually), add a second
   Connection (a local cockpit dev server) and verify aggregation, chips,
   red-dot behavior when one is stopped, edit-URL disconnect confirmation,
   delete-Connection cleanup, New Project across both, Phase 0 409 on
   archiving a project with a running session.
3. Update `docs/connections-settings-plan.md` status line to point at this
   spec, and update `CONTEXT.md` if any language shifted.

## Risks / watch items

- **List/DisclosureGroup diffing crashes**: sidebar rows are changing identity
  type (`String` → refs) and gaining chips — keep rows inline in the `ForEach`
  (known preview/hosting crash with helper-func rows) and lean on the hosting
  smoke tests.
- **Poll fan-out**: N Connections × 2 stores × ~7s polling is fine for small N;
  no throttling work in v1, but keep runtime teardown airtight so deleted
  Connections stop polling immediately.
- **Mid-branch inconsistency** (Phases 2–3): Settings edits a list the app
  doesn't consume yet. Land the whole branch as one PR; don't ship between
  phases.
- **Swift 6 concurrency**: `ConnectionRuntime` juggles per-runtime tasks;
  keep stores main-actor-bound as they are today and cancel tasks in
  deterministic teardown paths.
