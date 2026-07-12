# Workspaces Phase 2 Implementation Spec

Status: Draft v1

Scope: the macOS Dashboard and Workspace shell described in
[workspaces-prd.md](workspaces-prd.md) Section 6, replacing the throwaway
Phase 1 bridge. All work lands in `macos/`; the server, CLI, web admin, and
`packages/` contracts from [Phase 1](workspaces-phase1-spec.md) are consumed
as-is — Phase 2 requires no server or ATCAPI contract change.

Related: [ui-ux-exploration-brief.md](ui-ux-exploration-brief.md) (visual
direction and screens), [keyboard-shortcuts-plan.md](keyboard-shortcuts-plan.md)
(command identifiers Phase 2 must align with),
[ADR 0004](../server/docs/adr/0004-sessions-launch-agents-from-a-registry.md)
(start contract), [ADR 0008](../server/docs/adr/0008-workspace-deletion-stops-sessions-but-not-files.md)
(deletion never touches files).

Throughout, an **active Session** has status `starting` or `running`, and an
**active Workspace** is an unarchived Workspace on a reachable Connection.
The UI labels a Session launched from an Agent Action a **Session** and a
Session launched from the Interactive Shell or a general Action a
**Terminal**; both remain the one generic `Session` model.

## 1. Navigation and Window Architecture

The app gains two top-level surfaces in the existing single `WindowGroup`:

```text
RootView (replaces today's ContentView body)
├── WorkspaceShellView(workspace)   — mounted once a Workspace is opened,
│                                     then kept mounted for the window's life
└── DashboardView                   — opaque full-window cover when the
                                      route is .dashboard
```

- A new `WorkspaceRef { connectionID: UUID, workspaceID: String }` joins
  `SessionRef`/`ProjectRef` in `ConnectionRuntime.swift`.
- Per-window navigation state (not in `AppModel`):
  `route: Route` (`.dashboard` or `.workspace`) plus
  `openWorkspace: WorkspaceRef?`. The app always launches on the Dashboard;
  the route is not persisted across launches.
- **Terminal surface invariant preserved.** Today `TerminalPane` keeps every
  live Ghostty surface in the view hierarchy (stacked at `opacity 0/1`) so
  WebSockets and surfaces survive sidebar navigation. The root `ZStack` above
  generalizes the existing opaque-cover pattern: the shell — and its
  `TerminalPane` — stays mounted while the Dashboard covers it, so returning
  from the Dashboard never tears down or replays a surface. Opening a
  different Workspace swaps the shell's content but not the pane's stacked
  surfaces.
- If the open Workspace disappears from its store (deleted via web/CLI, or
  its Connection is removed), the window returns to the Dashboard and clears
  `openWorkspace`. Session terminations that accompany a remote delete flow
  through the existing attach-end machinery.
- Multi-window remains out of scope for v1, matching today's behavior:
  `appModel.selection` stays app-level; route state is per-window only so a
  future multi-window pass has one obvious seam.

## 2. Data Layer

### WorkspacesStore

New `Features/Workspaces/WorkspacesStore.swift`, a straight copy of the
`ProjectsStore` pattern (`@Observable`, generation-guarded refresh, mutate →
merge → `scheduleRefresh`). It fetches `client.workspaces(projectID: nil,
includeArchived: true)` — one call per Connection, exactly what the Dashboard
needs — and exposes `create`, `rename`, `archive`, `unarchive`, `delete`.

`ConnectionRuntime.refresh()` fetches projects, workspaces, and sessions
concurrently; `reachability` derivation is unchanged.

### Archived filtering moves to the view layer

Today `AppModel.includeArchived` is fanned out to every store, which cannot
express the PRD's per-Workspace Archived filter. Phase 2 simplifies: all
three stores always fetch `includeArchived: true`, the stores stop filtering,
and each surface filters locally (Dashboard hides archived Projects and
Workspaces behind a toggle; the shell hides archived Sessions behind its
filter). `AppModel.includeArchived` and its fan-out are deleted.

### Store gaps

- `SessionsStore` gains `unarchive(id:)` and `delete(id:)` wrappers (the
  `ATCClient` calls already exist).
- `ProjectsStore` gains `delete(id:)`.
- All wrappers follow the existing mutate-merge-refresh shape; `delete`
  removes the row locally on success instead of merging.

### Session classification

A Session is displayed as a **Session** (agent) or **Terminal** by joining
its `action` name against the Connection's `ActionsStore`:

- `action == nil` → Terminal (Interactive Shell).
- Action resolves with `type == "agent"` → Session.
- Action resolves as a general Action, or does not resolve at all (a custom
  Action deleted after its Sessions ended) → Terminal.

This is a pure function (`SessionKind.classify(session:actions:)`) so it is
unit-testable like `SidebarGroups`. `ActionsStore` joins the runtime refresh
cycle (it is currently on-demand only) so classification does not depend on a
sheet having been opened. No action-type snapshot is added to the Session
contract; misclassification after an Action delete is cosmetic and accepted.

### Display-name fallbacks

Per the PRD, an unnamed Session shows its Agent Action label; an unnamed
Interactive Shell Terminal shows `Terminal`; an unnamed Action Terminal shows
its Action label. ATCAPI's generic `actionLabel` ("Shell") stays untouched;
the macOS layer adds one display helper next to `SessionKind` that owns this
rule so every row, header, and confirmation uses the same string.

## 3. Dashboard

`Features/Dashboard/DashboardView.swift`, a scrollable card/list hybrid per
the UX brief:

- **Connection sections** in `ConnectionsStore` order: name, reachability dot
  (reusing the `Reachability` color mapping), and local/remote context.
  Local/remote is derived in `ConnectionURL`: a loopback host (`localhost`,
  `127.0.0.1`, `::1`) reads "Local"; anything else reads as the remote host.
  An unreachable Connection keeps its section with the existing
  cable-connector iconography and a Retry action; its cached rows stay
  visible but Workspace open/create actions are disabled.
- **Project cards**: name, directory (secondary, head-truncated), a New
  Workspace button, and a context menu carrying the existing Project actions
  (rename, archive/unarchive) plus Delete Project (Section 6). Archived
  Projects appear only under the Dashboard's "Show Archived" toggle, with the
  existing 50%-opacity archived treatment.
- **Workspace rows** inside each card, newest-created-first: name only, plus
  an archived badge when shown by the toggle. No runtime status, activity, or
  recency indicators of any kind. Click/Return opens the Workspace (sets
  `openWorkspace`, routes to the shell). Row context menu: Open, Rename,
  Archive/Unarchive, Delete.
- **Empty states** (`ContentUnavailableView`, matching house style): no
  Connections → "Add a Connection" pointing at Settings; a Project with no
  Workspaces → a quiet inline row with a New Workspace action, not a full
  empty-state panel.
- There is no Project detail screen; the card's context menu is the entire
  Project surface.

The grouping is computed by a pure `DashboardGroups` struct mirroring
`SidebarGroups` (inputs: connections + per-connection projects/workspaces;
outputs: ordered sections/cards/rows; documented ordering and archived
rules) so it is testable without hosting.

## 4. Workspace Creation

One shared flow, `CreateWorkspaceSheet`, invoked from three contexts:

| Context | Project field |
| --- | --- |
| Project card / row button | preselected, fixed |
| Inside an open Workspace (`workspace.new` command) | preselected to that Workspace's Project, changeable |
| Context-free (File menu) | picker over unarchived Projects across Connections, required |

The sheet asks for a name only (same trim validation the server applies) and
uses the default Environment implicitly. On success it selects nothing —
creation **opens the new Workspace** (routes the window to the shell with an
empty-state sidebar). Server 409s (`project_archived`) surface through the
standard alert pattern.

## 5. Workspace Shell

`WorkspaceShellView` replaces today's `ContentView` split view when a
Workspace is open. Structure:

- **Window context**: toolbar leading edge holds a Back-to-Dashboard button
  (chevron, `⌘↑` equivalent) and the Workspace name; Project and Connection
  render as a secondary subtitle (name · ConnectionChip). Workspace identity
  lives here, at window level, not in the content header.
- **Sidebar**: two fixed sections, **Sessions** and **Terminals**, each with
  a header `+` button (New Session / New Terminal). Rows reuse
  `SessionRowView` (`StatusBadge` dot + display name + lifecycle caption) and
  are filtered to `session.workspace?.id == openWorkspace.workspaceID`,
  classified per Section 2, ordered newest-first. An **Archived** filter
  toggle at the sidebar footer reveals archived rows in both sections. The
  existing sidebar toggle, search, and `⌘B` behavior carry over; Vim-style
  list keys stay deferred per the keyboard brief.
- **Content area**: `SessionContentView` is reused nearly as-is — the
  header (`SessionHeaderBar`) gains the Action label ("Codex", "Terminal",
  custom Action label) next to the existing name, working directory, and
  lifecycle badge, and its action buttons gain Unarchive and Delete
  (Section 6). The `TerminalPane` stacking and inspector are unchanged.
- **Selection restore**: the shell persists `workspaceID → sessionID` in
  UserDefaults on every selection change. Opening a Workspace restores that
  Session if it still exists in the store (any lifecycle state); otherwise
  the shell shows the Workspace empty state (`ContentUnavailableView` with
  New Session and New Terminal buttons).
- Sessions display **process lifecycle only** (starting, running, ended,
  failed, archived) — no Agent Activity, no prompt composer, no agent
  controls. Terminal typing behavior is untouched.

## 6. New Session and New Terminal

Two small sheets replace the Phase 1 `CreateSessionSheet` (which is deleted
along with `NewSessionContext`):

- **New Session**: a picker over enabled **Agent Actions only**
  (`isAgent`), an optional name field, Start. No prompt field, no params UI,
  no Environment picker — the server default Environment is used.
- **New Terminal**: a picker whose first entry is **Interactive Shell**
  (default) followed by enabled **general Actions**, an optional name field,
  Start. Interactive Shell submits `StartSessionRequest(workspaceId:)` with
  `action: nil`.

Both submit through `SessionsStore.start`, select the new `SessionRef`, and
are available only inside an active Workspace: the commands are disabled when
the route is `.dashboard`, the open Workspace is archived, or its Connection
is unreachable. If an Action list fails to load, the sheet shows the standard
inline error with Retry rather than silently offering nothing.

## 7. Lifecycle UX

All archive/unarchive actions are single-click with no confirmation (they are
reversible). Every **delete** confirms via `confirmationDialog` with the
target named, and every confirmation ends with the ADR 0008 sentence:
**"Files on disk are not touched."**

- **Session delete** (row context menu + content header): "Delete Session
  '<display name>'? This stops the session if it is running and removes its
  atc history." Failure (stop error, 502) leaves the row and surfaces the
  alert.
- **Workspace delete** (Dashboard row + shell toolbar menu): "Delete
  Workspace '<name>' and its <n> sessions? <k> running sessions will be
  stopped." Counts come from the local store. On a 502 (stop failure) or 409
  (start race), everything remains and the error alert names the failing
  step. Deleting the currently open Workspace routes back to the Dashboard
  on success.
- **Project delete** (Dashboard card menu): enabled only when the card has
  zero Workspaces; otherwise the menu item is disabled with a "Delete all
  Workspaces first" hint. Server 409 (`project_has_workspaces`) still
  surfaces normally if a race slips through.
- Archive preconditions are mirrored client-side for affordance (Workspace
  archive disabled while it has active Sessions; Project archive disabled
  while any Workspace is unarchived) but the server 409 remains the source of
  truth and is surfaced through the standard alert.

## 8. Commands, Menus, and Shortcuts

Phase 2 does not implement the keyboard-shortcuts MVP (registry, config.toml,
leader key — that brief is a separate effort). It does lay the commands out
so that effort binds cleanly:

- A `Commands` scene adds the menu placement from the keyboard brief —
  **File**: New Project, New Workspace, New Session, New Terminal;
  **View**: Toggle Sidebar, Refresh — each implemented as one closure the
  future `session.new`/`workspace.new`/etc. registry entries will invoke, not
  as per-menu logic.
- Compiled shortcuts: `⌘N` New Session, `⌘T` New Terminal, `⌘⇧N` New
  Workspace, `⌘R` Refresh, `⌘B` Toggle Sidebar. New Project moves to
  menu-only (this reassigns today's `⌘⇧N`). Unavailable commands render as
  disabled menu items, matching the brief's availability rules.
- Availability contexts: `session.new`/`terminal.new` require an active open
  Workspace; `workspace.new` works everywhere (context-free form when no
  Project is implied); `data.refresh` always works.
- Dashboard list rows support arrow-key navigation and Return-to-open via
  standard `List` behavior; no bare-key global bindings are added.

## 9. Removed and Replaced Code

- `CreateSessionSheet.swift` and `NewSessionContext` — deleted (Phase 1
  throwaway bridge).
- `ProjectSidebarView` and `SidebarGroups` — deleted; the Dashboard's
  `DashboardGroups` and the shell's Workspace-scoped sidebar replace
  project-grouped session navigation. The `ConnectionChip` component moves to
  a shared location and is reused by both surfaces.
- `ContentView` — becomes the thin `RootView` router of Section 1.
- `AppModel.includeArchived` and store-level archived filtering — deleted per
  Section 2.
- `MockATCClient` fixtures grow: at least one Project with zero Workspaces,
  one archived Workspace, and Sessions covering agent/general/shell
  classification so previews and hosting tests can exercise every Dashboard
  and sidebar state.

## 10. Test Plan

Mapped to the PRD's macOS acceptance criteria, using the house patterns
(Swift Testing, `ScriptableClient`, `AppModel.preview`, NSHostingView smoke
tests):

- **Grouping/classification (pure)**: `DashboardGroups` ordering, archived
  filtering, empty-Project handling; `SessionKind.classify` for nil action,
  agent, general, and unresolved-action fallback; display-name fallback
  rules.
- **Stores**: `WorkspacesStore` refresh/mutation/merge/error paths mirroring
  `ProjectsStoreTests`; new `SessionsStore.unarchive/delete` and
  `ProjectsStore.delete` wrappers; runtime refresh includes workspaces and
  actions.
- **Flows (ScriptableClient)**: create-Workspace-then-open from each of the
  three command contexts; Workspace delete failure (scripted stop error)
  leaves rows intact and surfaces the error; open-Workspace deletion routes
  back to the Dashboard; selection restore hits the surviving Session and
  falls back to the empty state.
- **Command availability**: New Session/Terminal disabled on the Dashboard,
  in an archived Workspace, and on an unreachable Connection; New Workspace
  context preselection.
- **UI-hosting smoke tests**: Dashboard populated (two Connections; zero /
  one / many Workspaces), Dashboard no-Connections empty state, Workspace
  shell populated and empty, both creation sheets, and each delete
  confirmation — hosted in an `NSWindow`, run-loop pumped, per the existing
  smoke-test idiom.

## 11. Build Order

Each step leaves `mise run macos:test` (and the aggregate `check`) green and
is a jj checkpoint:

1. Data layer: `WorkspacesStore`, runtime refresh (workspaces + actions),
   store wrappers, archived-filtering simplification, mock fixture growth.
2. Navigation skeleton: `WorkspaceRef`, route state, `RootView` ZStack with a
   placeholder Dashboard listing openable Workspaces — the app is usable
   end-to-end from here on.
3. Dashboard UI: `DashboardGroups`, sections/cards/rows, empty states,
   archived toggle, reachability treatment.
4. Workspace creation: `CreateWorkspaceSheet`, all three contexts,
   create-and-open.
5. Workspace shell: sidebar sections + classification, selection restore,
   content-header Action label, Workspace empty state.
6. New Session / New Terminal sheets; delete the Phase 1 bridge.
7. Lifecycle UX: unarchive/delete on Sessions, Workspace and Project delete,
   all confirmations with counts and files-not-touched language.
8. Commands and menus: `Commands` scene, shortcut reassignments, availability
   wiring; final hosting-test sweep against the acceptance criteria.

## Resolved Decisions

Decisions made while drafting this spec, for review:

- The Dashboard is an opaque cover over a persistently mounted Workspace
  shell, generalizing the existing `TerminalPane` cover pattern, so terminal
  surfaces and WebSockets survive Dashboard round-trips without replay.
- Session-vs-Terminal classification is a client-side join against the
  Actions registry with a Terminal fallback for unresolvable Actions; no
  action-type snapshot is added to the Session contract.
- Archived filtering moves wholly to the view layer; stores always fetch
  archived rows and `AppModel.includeArchived` is removed.
- `ActionsStore` joins the polling refresh cycle so classification and the
  creation sheets never depend on stale on-demand data.
- Selection restore is per-Workspace in UserDefaults and restores Sessions in
  any lifecycle state; the route itself always resets to the Dashboard on
  launch.
- `⌘⇧N` is reassigned from New Project to New Workspace; New Project becomes
  menu-only. `⌘N` New Session and `⌘T` New Terminal are Workspace-scoped.
- The keyboard-shortcuts MVP (registry, config.toml, leader key) stays a
  separate effort; Phase 2 only lands menu placement and closures shaped for
  it.
- Phase 2 makes no server, CLI, web, or ATCAPI contract changes.
