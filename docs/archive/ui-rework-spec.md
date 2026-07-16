# macOS Navigation Rework Implementation Specification

Status: Draft v1

Purpose: Replace the macOS app's Dashboard-cover and Workspace-shell routing with one stable, native SwiftUI navigation structure that separates sidebar navigation, active Workspace context, main content, and inspector presentation.

Scope: `macos/ATC` and `macos/ATCTests`. This work consumes the existing server and `ATCAPI` contracts without changing them.

Related:

- [ui-rework-brief.md](ui-rework-brief.md) — normative product direction and resolved grilling decisions.
- [CONTEXT.md](../CONTEXT.md) — canonical product language.
- [workspaces-phase2-spec.md](workspaces-phase2-spec.md) — existing Dashboard and Workspace behavior retained except where this specification supersedes its navigation, selection, toolbar, inspector, and command-availability rules.
- [keyboard-shortcuts-spec.md](keyboard-shortcuts-spec.md) — existing keyboard work; this specification adds no commands or bindings.
- [DEV-41](https://linear.app/elevenideas/issue/DEV-41/ui-review) — layout and inspector regressions motivating this work.

## Normative Language

The key words `MUST`, `MUST NOT`, `REQUIRED`, `SHOULD`, `SHOULD NOT`, `RECOMMENDED`, `MAY`, and `OPTIONAL` are to be interpreted as described in RFC 2119.

## 1. Problem and Boundary

The current root alternates between an opaque Dashboard cover and a nested Workspace `NavigationSplitView`. The covered Workspace shell remains mounted to preserve terminal surfaces, but its toolbar and inspector state can leak into Dashboard, and repeated transitions can destabilize the Dashboard layout.

This implementation MUST:

- Use one stable window-root `NavigationSplitView`.
- Keep Navigator selection independent from main-content selection.
- Keep one explicit Active Workspace independent from both.
- Preserve existing terminal controllers, Ghostty surfaces, and WebSocket attachments across Navigator and Workspace transitions.
- Make Dashboard a main-content destination, not a route or mode.
- Eliminate the Dashboard layout and trailing-inspector regressions identified by DEV-41.

This implementation MUST NOT:

- Change server, CLI, web, SQLite, or `ATCAPI` contracts.
- Redesign Session or Terminal content, terminal rendering, creation sheets, lifecycle actions, or file browsing.
- Add Project-detail or Connection-detail destinations.
- Add multi-window support.
- Add Navigator or Workspace-switching command IDs, menu commands, or keyboard shortcuts.
- Add a file-tree API, file-tree state, or fake file data.

## 2. Goals and Non-Goals

### 2.1 Goals

- Navigator switching MUST leave the main content and window layout unchanged.
- Users MUST be able to identify and switch the Active Workspace while any Navigator is selected or the sidebar is hidden.
- Workspace activation MUST restore the target Workspace's last useful main content without showing stale content from the previous Workspace.
- Dashboard MUST remain available without clearing the Active Workspace.
- Workspace-scoped capabilities MUST be evaluated against the Active Workspace, not the currently visible Navigator or main content.
- Native SwiftUI `NavigationSplitView`, toolbar, list, menu, and inspector behavior SHOULD own platform presentation wherever possible.

### 2.2 Non-Goals

- Persisting the Active Workspace across launches.
- Persisting Navigator choice, inspector visibility, searches, filters, or disclosure state across launches.
- Showing archived Projects or Workspaces in the Projects Navigator.
- Defining future Search, Source Control, Task, History, or other Navigators.
- Making the trailing inspector a navigation surface.

## 3. Implementation Profile

This section is normative. Implementation work MUST follow this profile unless this specification is amended.

### 3.1 Technical Context

- Language: Swift 5 language mode as configured by `macos/atc.xcodeproj`.
- UI: SwiftUI with Observation.
- Target: macOS 26.0, single `WindowGroup`.
- API dependency: existing `ATCAPI` package.
- Storage: existing stores plus `UserDefaults` for connection-qualified main-content memory only.
- Tests: Swift Testing for state and grouping; existing `NSHostingView`/`NSWindow` hosting-test patterns for UI structure.
- Required verification command: `mise run macos:test`.
- Full repository verification before completion: `mise run check`.

### 3.2 Dependency Policy

- No new production dependency is permitted for this work.
- Shared navigation transitions, identity checks, and sorting MUST be implemented once and reused by the Projects Navigator, Workspace Switcher, Dashboard actions, and menu actions.
- Views MUST NOT duplicate Workspace activation or stale-selection cleanup logic.

### 3.3 Performance and Reliability

- Navigator changes and Dashboard presentation MUST NOT create or destroy terminal controllers or Ghostty surfaces.
- Workspace activation MUST NOT disconnect terminal controllers solely because their Workspace is no longer active; the existing attachment budget remains responsible for later eviction.
- The rework MUST NOT introduce additional server polling or per-row network requests.
- Projects Navigator grouping and ordering SHOULD remain pure and deterministic for inexpensive recomputation and unit testing.
- State transitions that change Active Workspace and main content MUST be atomic from the rendered UI's perspective.

## 4. Canonical UI Model

The canonical terms in `CONTEXT.md` apply throughout this specification.

### 4.1 Navigator Identity

The implementation MUST define exactly these initial Navigator identities:

```swift
enum NavigatorID: String, CaseIterable, Sendable {
    case projects
    case workspace
    case file
}
```

User-facing labels MUST be:

- `.projects` → `Projects Navigator`
- `.workspace` → `Workspace Navigator`
- `.file` → `File Navigator`

The Projects Navigator is plural because it crosses Project boundaries. Workspace and File Navigators are singular because each is scoped to one Active Workspace.

### 4.2 Main-Content Identity

Main content MUST have an explicit, connection-qualified selection model equivalent to:

```swift
enum MainContentSelection: Equatable, Sendable {
    case dashboard
    case workspace(WorkspaceRef)
    case session(SessionRef)
}
```

- `.dashboard` renders Dashboard.
- `.workspace(ref)` renders the existing Workspace empty/no-selection content for `ref`.
- `.session(ref)` renders the selected Session or Terminal using the existing shared Session content and terminal surfaces.
- File content is absent from this version and MUST NOT be represented by a fake selection.

The Active Project MUST be derived from the Active Workspace's current model record. It MUST NOT be stored as a second independently mutable identity.

### 4.3 Window State

Per-window state MUST have one owner, `WindowState` or an equivalently focused type. It MUST own:

- `selectedNavigator`, defaulting to `.projects`.
- `activeWorkspace`, defaulting to `nil`.
- `selectedContent`, defaulting to `.dashboard`.
- `columnVisibility`, defaulting to `.all`.
- One inspector-presentation Boolean, defaulting to `false`.
- Projects Navigator disclosure and focus state for the current app run.
- Existing sheet-presentation state used by window commands.

`Route` and `hasOpenedWorkspaceShell` MUST be removed. Dashboard and Workspace MUST NOT remain mutually exclusive routes.

`AppModel` MUST continue to own Connections, per-Connection runtimes, stores, and terminal controllers. It MUST NOT remain the independent source of truth for Active Workspace or selected main content.

### 4.4 Terminal Retention Context

The existing attachment budget MUST continue protecting the selected Session and Sessions belonging to the Active Workspace. Because navigation state moves to the window, eviction MUST receive a derived retention context rather than reading duplicate navigation state from `AppModel`.

An interface equivalent to the following is RECOMMENDED:

```swift
struct TerminalRetentionContext {
    let activeWorkspace: WorkspaceRef?
    let selectedSession: SessionRef?
}
```

`attachIfNeeded` or its eviction helper MUST receive this context when an attachment can trigger eviction. The implementation MUST NOT maintain a second mutable copy of Active Workspace or selected content in `AppModel` merely for eviction.

## 5. Stable Window Structure

The window MUST use one stable hierarchy equivalent to:

```text
RootView
└── NavigationSplitView
    ├── NavigatorSidebar
    │   ├── NavigatorSelector
    │   └── NavigatorContent
    └── MainContentHost
        ├── persistent TerminalPane stack
        └── opaque content/state covers
```

### 5.1 Root `NavigationSplitView`

- `RootView` MUST own the only window-root `NavigationSplitView`.
- Selecting a Navigator MUST swap only `NavigatorContent`.
- Dashboard MUST NOT cover or replace the root split view.
- The leading column MUST use native sidebar behavior and the existing practical width range unless validation shows a necessary adjustment.
- Sidebar visibility MUST be meaningful in every main-content state; the existing Toggle Sidebar action MUST no longer depend on a Dashboard/Workspace route.

### 5.2 Persistent Main Content

- `MainContentHost` MUST keep the shared `TerminalPane` stack mounted while the window exists.
- Dashboard, Workspace no-selection, disconnected, ended, and other non-live-terminal states MUST be opaque layers over that stable host where necessary.
- Changing Navigator MUST NOT change `selectedContent`.
- Selecting Dashboard MUST change only `selectedContent` to `.dashboard`; it MUST NOT clear `activeWorkspace`.
- Existing Session headers, terminal status behavior, reconnect behavior, and detail views MUST be reused rather than reimplemented.

## 6. Navigator Sidebar

### 6.1 Navigator Selector

- The selector MUST appear at the top of the leading sidebar and use an Xcode-style row of icon controls.
- Every icon MUST have its canonical label as help and accessibility text.
- Projects Navigator MUST always be enabled.
- Workspace and File Navigators MUST be visible but disabled when there is no Active Workspace.
- Disabled Navigator help MUST explain `Requires an Active Workspace`.
- Changing Navigator MUST preserve main content and inspector presentation.

### 6.2 Projects Navigator

The Projects Navigator MUST contain:

1. Dashboard as the first row and a main-content destination.
2. A flat, app-wide list of unarchived Projects.
3. Each Project's unarchived Workspaces nested under that Project.

Connections MUST NOT appear as hierarchy rows. Each Project row MUST show its Connection name and reachability status as secondary context so same-named Projects remain distinguishable.

Project rows are structural:

- Their disclosure control expands or collapses Workspaces.
- Focusing or clicking the Project row MUST NOT replace main content.
- Existing Project actions remain available through the row context menu.
- Project management remains on Dashboard.
- No Project-detail destination may be added.

Workspace rows are destinations:

- Selecting an enabled Workspace row MUST invoke the shared Workspace activation transition in Section 8.
- A Workspace on a disconnected Connection SHOULD remain visible from cached data but MUST be disabled for new activation, matching existing Dashboard reachability behavior.
- The already Active Workspace MAY remain active if its Connection later disconnects.

Ordering MUST be deterministic:

- Dashboard first.
- Projects by localized case-insensitive name.
- Same-named Projects by localized case-insensitive Connection name.
- Remaining ties by stable connection-qualified Project identity.
- Workspaces newest-created-first, matching Dashboard.

Project disclosure state lasts for the current app run and resets on launch. Manual reordering is out of scope.

Archived Projects and Workspaces MUST NOT appear in the Projects Navigator. Dashboard remains the native macOS management surface for viewing and unarchiving them.

### 6.3 Workspace Navigator

- The Workspace Navigator MUST reuse the existing Workspace-scoped Sessions and Terminals sections, creation actions, classification, ordering, row display, context menus, and Show Archived behavior.
- The Workspace Navigator MUST NOT include session search. The row count is intentionally small enough to scan directly, and the sidebar should avoid controls without a demonstrated need.
- Its contents MUST derive from the Active Workspace, never from a Workspace identity captured by a replaced view hierarchy.
- When the Active Workspace changes, the Navigator MUST update its rows without changing `selectedNavigator`.
- A disconnected Active Workspace retains cached rows and a visible disconnected state; network-dependent actions are disabled.
- An archived Active Workspace remains browseable, with creation actions disabled.
- Show Archived remains available for archived Sessions and Terminals because no other native macOS management surface currently exposes them.

### 6.4 File Navigator Stub

- The File Navigator icon MUST be visible.
- It MUST be disabled without an Active Workspace and selectable with one.
- Its sidebar MUST show a restrained `File navigation is not available yet` empty state.
- Selecting it MUST NOT replace main content.
- It MUST NOT call filesystem APIs, maintain tree state, or display fixture/fake files.

## 7. Dashboard

- Dashboard MUST remain the existing app-wide Project and Workspace management surface.
- It MUST render as `.dashboard` inside `MainContentHost`, not as an opaque root cover.
- Its existing Connection sections, creation flows, archived filter, lifecycle actions, and empty states remain in scope and SHOULD be moved with minimal behavioral change.
- Connection sections MUST use a restrained, full-width hierarchy: Connection identity and reachability, followed by Project groups containing Workspace rows.
- Project groups with Workspaces SHOULD use a rounded system content surface with aligned header and row separators. Empty Projects SHOULD use a lighter dashed treatment with one explicit New Workspace action.
- Workspace rows SHOULD show only name, activity state, and recency in the default presentation. Lifecycle and management actions remain in context menus.
- Dashboard chrome MUST stay minimal. New Project remains a primary toolbar action; archived visibility and refresh belong in one secondary options menu.
- Dashboard content MUST use system colors, typography, controls, and spacing so it adapts to appearance and accessibility settings. Custom glass effects are not permitted.
- Dashboard's Show Archived control remains the only native macOS place to reveal archived Projects and Workspaces.
- Selecting or showing Dashboard MUST NOT clear the Active Workspace, change Navigator, disconnect terminals, or disable Workspace commands solely because Dashboard is visible.
- Dashboard MAY activate an archived Workspace for review; creation remains disabled while that Workspace is active.

## 8. Workspace Activation and State Transitions

All entry points MUST call one shared Workspace activation operation. This includes Workspace rows in Projects Navigator, Dashboard open actions, Workspace creation completion, and Workspace Switcher selections.

### 8.1 Activate Workspace

For a target `WorkspaceRef`, activation MUST:

1. Validate that the connection-qualified Workspace still exists in the current store.
2. Treat activation of the already Active Workspace as idempotent while Workspace content is visible. From Dashboard, the same action restores that Workspace's remembered content so its row and the Workspace Switcher provide a way back.
3. Set `activeWorkspace` to the target.
4. Preserve `selectedNavigator`.
5. Close the inspector when the target differs from the previous Active Workspace.
6. Restore the target's remembered content under Section 10.
7. Fall back to `.workspace(target)` when no remembered content is valid.
8. Attach the restored Session only through the existing attach/reconnect path and only when its current state permits attachment.

The transition MUST NOT briefly render content from the previous Workspace under the new Workspace Switcher label.

### 8.2 Select Session or Terminal

Selecting a Session or Terminal row MUST:

- Verify that it belongs to the Active Workspace and uses the same Connection identity.
- Set `selectedContent` to `.session(ref)`.
- Remember the selection under the Active Workspace's composite identity.
- Attach through the existing terminal path when attachable.
- Update an already-presented inspector to the new selection.

An invalid cross-Workspace selection MUST be rejected and MUST NOT be rendered.

### 8.3 Show Dashboard

Showing Dashboard MUST:

- Set `selectedContent` to `.dashboard`.
- Preserve `activeWorkspace`.
- Preserve `selectedNavigator`.
- Close the inspector.
- Preserve remembered Workspace content for later activation/restoration.

### 8.4 Disconnected Active Workspace

When the Active Workspace's Connection becomes disconnected:

- The Workspace remains active.
- Navigator selection and main content remain unchanged.
- Cached navigation and metadata remain visible.
- Creation and other network-dependent mutations become unavailable.
- Existing terminal reconnect/status behavior remains responsible for its own presentation.
- A transient refresh failure MUST NOT clear the Active Workspace.

### 8.5 Archived Active Workspace

When the Active Workspace is archived:

- It remains active and its main content remains selected.
- It disappears from the Projects Navigator.
- Workspace and File Navigators remain enabled.
- New Session and New Terminal become unavailable.
- The Workspace Switcher menu identifies the current Workspace as archived.
- The user may leave it by activating another Workspace; archival alone MUST NOT force navigation.

### 8.6 Removed Active Workspace or Connection

Once current store data positively establishes that the Active Workspace or its Connection was removed, the window MUST atomically:

- Clear `activeWorkspace`.
- Clear stale remembered content for that Workspace when appropriate.
- Set `selectedNavigator` to `.projects`.
- Set `selectedContent` to `.dashboard`.
- Close the inspector.
- Disconnect affected terminal controllers through the existing teardown path when the Connection was removed.

Before the relevant store has completed its first load, absence MUST be treated as unresolved rather than removed.

## 9. Workspace Switcher

The toolbar MUST contain one Workspace Switcher that remains visible when the sidebar is hidden or any Navigator is selected.

### 9.1 Label

With an Active Workspace, the label MUST contain:

- A small status dot using the Active Workspace's Connection reachability.
- `Project › Workspace` as the persistent text.
- A menu affordance.

The persistent label MUST NOT include the Connection name. The menu, tooltip, and accessibility description MUST include Connection identity and connected/disconnected state.

With no Active Workspace, the label MUST read `Select Workspace…`.

### 9.2 Menu Contents

- Workspaces MUST be grouped by unarchived Project.
- Connection identity MUST accompany Project context where names could be ambiguous.
- Archived Projects and Workspaces MUST NOT be offered as switch targets.
- Disconnected cached targets SHOULD remain visible but disabled.
- Projects and Workspaces SHOULD use the same deterministic ordering as the Projects Navigator.
- If the current Active Workspace is archived, the menu header or current-context section MUST identify that fact even though the archived Workspace is omitted from switch targets.

Selecting a target MUST call the shared activation operation from Section 8.1.

## 10. Restoration and Persistence

### 10.1 Restorable Content

The remembered Session or Terminal is valid only when it:

- Still exists in the target Connection's Session store.
- Still belongs to the target Workspace.
- Is not archived.

Running, starting, ended, terminated, and failed lifecycle states are restorable. Connection disconnection does not invalidate remembered content.

Deleted, moved, or archived content MUST clear its stale memory and fall back to `.workspace(target)`. The implementation MUST NOT automatically select a replacement Session or Terminal.

### 10.2 Composite Persistence Identity

Persistence MUST key Workspace memory by both local Connection UUID and server Workspace ID. A bare Workspace ID is prohibited because independent Connections may issue the same server ID.

The existing bare-ID `workspaceSelections` storage MUST be replaced by a versioned, connection-qualified representation. Because the old keys are ambiguous across Connections, the implementation SHOULD ignore or remove the old map rather than guess a migration.

Persisted records need only contain:

- Connection UUID.
- Workspace ID.
- Session ID.

### 10.3 Launch State

Every app launch MUST begin with:

- Projects Navigator selected.
- Dashboard selected in main content.
- No Active Workspace.
- Inspector closed.
- Navigator filters and disclosure state reset.

Only the connection-qualified last Session/Terminal selection per Workspace persists across launches. SwiftUI MAY restore native inspector column width, but the app MUST NOT persist inspector presentation.

## 11. Inspector

- One native SwiftUI `.inspector(isPresented:content:)` modifier MUST be attached to the stable root content host.
- One window-level Boolean MUST control presentation.
- The inspector target MUST be derived from current main content; Session and Terminal selections use the existing `SessionDetailView` contract.
- Dashboard, Workspace no-selection, unsupported content, removed content, and a different Workspace activation MUST close the inspector.
- Selecting another inspectable Session while the inspector is open MUST update its contents without closing it.
- Navigator changes alone MUST leave inspector state unchanged.
- Inspector presentation MUST NOT be remembered per Workspace or persisted by app code.
- The inspector toggle MUST be absent or disabled whenever current main content has no inspector target.
- Existing native flexible column width behavior SHOULD remain.

## 12. Commands and Availability

This rework changes the context evaluation of existing actions but adds no new keyboard work.

- New Session and New Terminal MUST require an Active Workspace that is unarchived and on a connected Connection. Dashboard visibility and Navigator choice MUST NOT affect availability.
- New Workspace SHOULD preselect the Active Workspace's Project when that Project remains a valid creation target; otherwise it uses the existing context-free Project picker.
- Toggle Sidebar MUST be available for the stable root split view regardless of main content.
- Refresh remains app-wide.
- Existing menu titles and compiled shortcuts remain unchanged.
- No `navigator.*` or `workspace.switch` command identifiers or `⌘1`–`⌘3` bindings may be added in this scope.

## 13. Failure Handling and Recovery

- Workspace activation failures MUST leave the previous Active Workspace, main content, and Navigator unchanged.
- Store refresh failures MUST preserve cached rows and mark Connection status through existing reachability behavior.
- A stale remembered selection MUST fail closed to the target Workspace no-selection state.
- A Workspace-switch transition MUST never retain an inspector target or main-content reference from the previous Workspace.
- Removing a Connection MUST clear all window references into that Connection and use existing terminal teardown.
- User-facing errors from retained Project, Workspace, and Session actions remain governed by existing behavior and MUST NOT be swallowed by the new navigation layer.

## 14. Acceptance Criteria

The implementation is complete when all of the following are true:

- The window contains one stable root `NavigationSplitView` in Dashboard and Workspace states.
- Projects, Workspace, and File Navigator switching changes only sidebar contents.
- Dashboard can be shown while an Active Workspace remains visible in the Workspace Switcher and eligible Workspace commands remain available.
- Projects Navigator shows Dashboard plus correctly ordered unarchived Projects and Workspaces, without Connection rows or archived records.
- Project rows expand/collapse without creating a Project-detail destination.
- Projects Navigator and Workspace Switcher use the same activation operation.
- Workspace Navigator contains no session search field.
- Workspace activation preserves Navigator, closes inspector, and restores only valid remembered content.
- Switching Workspace never presents content from the previous Workspace under the new context.
- Disconnection preserves Active Workspace; confirmed removal clears it and returns to Projects Navigator plus Dashboard.
- File Navigator exhibits only the specified placeholder behavior.
- The inspector toggle cannot appear for Dashboard or other unsupported content.
- Terminal surfaces and attachments survive Dashboard and Navigator transitions without unnecessary replay or reconnection.
- All persisted Workspace selections are connection-qualified.
- No new keyboard commands, bindings, production dependencies, or API changes are introduced.

The implementation is not acceptable if:

- Dashboard remains a root cover or route.
- More than one root navigation hierarchy is conditionally mounted.
- Navigator selection implicitly changes main content.
- Active Workspace identity is duplicated as independently mutable state across `WindowState` and `AppModel`.
- Archived Projects or Workspaces appear in Projects Navigator or Workspace Switcher targets.
- A blank File Navigator appears without explanatory state.

## 15. Verification Plan

### 15.1 State and Unit Tests

Tests MUST cover:

- Launch defaults.
- Navigator selection preserving main content.
- Dashboard preserving Active Workspace and Navigator.
- Workspace activation preserving Navigator and restoring valid content.
- Same-Workspace activation idempotence.
- Cross-Workspace and cross-Connection selection rejection.
- Connection-qualified persistence and old bare-key handling.
- Running, ended, failed, archived, deleted, and moved-selection restoration cases.
- Disconnected, archived, removed, and unresolved Active Workspace transitions.
- Inspector close/update rules.
- Command availability independent of Dashboard/Navigator and dependent on Active Workspace capability.
- Attachment-budget retention using the window-derived context.

### 15.2 Pure Grouping Tests

Projects Navigator grouping tests MUST cover:

- Dashboard-first ordering.
- Case-insensitive Project ordering.
- Connection-name and stable-identity tie-breakers.
- Newest-first Workspace ordering.
- Same-named Projects on different Connections.
- Archived Project and Workspace exclusion.
- Connection context and reachability projection.

### 15.3 UI Hosting Tests

Hosting tests MUST verify:

- One stable split view hosts Dashboard and Workspace content.
- All three Navigator selector states.
- Workspace and File disabled states without an Active Workspace.
- File placeholder with an Active Workspace.
- Workspace Switcher active, disconnected, archived, ambiguous-Project, and no-active states.
- Dashboard does not expose an inspector toggle.
- Session content exposes the inspector and updates it when selection changes.
- Switching Navigator does not replace hosted main content.

### 15.4 Regression and Manual Checks

Manual checks MUST include:

- Repeated Dashboard, Navigator, and Workspace transitions without Dashboard layout corruption.
- Sidebar hide/show from Dashboard and Session content.
- Live terminal typing and scrollback preservation across Navigator and Dashboard transitions.
- Workspace switching from both Projects Navigator and Workspace Switcher.
- Connection loss and recovery while a Workspace is active.
- Archiving and removing the Active Workspace.
- VoiceOver/help labels for Navigator icons and Workspace Switcher.

Required commands:

```sh
mise run macos:test
mise run check
```

## 16. Definition of Done

Required for completion:

- Root navigation, Navigator contents, Workspace Switcher, activation transitions, restoration, and inspector behavior satisfy this specification.
- Obsolete Dashboard/Workspace route and cover state is removed.
- Shared activation and selection logic has one owner.
- Existing Project, Workspace, Session, Terminal, Dashboard, and lifecycle behavior remains covered.
- New state, grouping, persistence, hosting, and regression tests pass.
- `mise run macos:test` and `mise run check` pass.
- Relevant code comments and documentation use the canonical language from `CONTEXT.md`.

Out of scope for completion:

- File browsing.
- Additional Navigators.
- Multi-window behavior.
- Navigator or Workspace-switch keyboard commands and shortcuts.
- Dedicated archive-management UI beyond existing Dashboard and Workspace Navigator controls.

## 17. Open Questions

None. Product decisions needed for this implementation are resolved in the source brief and this specification.
