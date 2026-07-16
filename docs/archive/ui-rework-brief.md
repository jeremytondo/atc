# Atelier Code Navigation Review

## Summary

Restructure the macOS app around a stable, Xcode-inspired sidebar and navigation model. The sidebar remains a persistent region of the window, while a row of navigator icons changes only the content shown inside it. The main content area changes only when the user selects a destination from the active navigator.

This should give users a clear global view of the app while allowing them to focus deeply on one workspace without fighting SwiftUI's native navigation patterns.

## Problem

The current UI treats Dashboard and the workspace experience as separate modes with different layout behavior. Moving between them can recreate or destabilize parts of the view hierarchy, contributing to layout bugs and inconsistent sidebar and toolbar behavior.

The app also needs a navigation model that can grow to support projects, machine-specific workspaces, agent sessions, terminals, files, and future workspace tools without placing everything in one crowded sidebar.

## Goals

- Use one stable, SwiftUI-native window and sidebar structure.
- Provide clear navigation across the whole app and within the active workspace.
- Allow users to stay focused on a particular workspace.
- Keep sidebar navigation independent from the content currently shown in the main area.
- Make the active `Project > Workspace` context continuously visible and easy to switch.
- Leave room for additional navigators such as search, source control, tasks, and history.
- Preserve relevant UI state when switching navigators or workspaces.

## Non-Goals

- Redesign the contents of agent sessions, terminals, or the file browser.
- Define every future navigator.
- Introduce multi-window workspace support as part of the initial implementation.
- Turn the trailing sidebar into primary navigation.
- Add Navigator commands, workspace-switching commands, or keyboard shortcuts; these belong in a later keyboard-focused pass.

## Proposed Experience

### Navigator Sidebar

The left sidebar includes an Xcode-style navigator selector at the top. Selecting an icon changes only the sidebar contents; it does not immediately replace the main content.

The initial Navigators are:

- **Projects Navigator:** Displays the Dashboard followed by a flat app-wide list of unarchived Projects with their unarchived Workspaces nested beneath them. The plural name reflects its multi-Project scope. Connections are not Navigator rows; each Project row shows its Connection name and status as secondary context so same-named Projects remain distinguishable. Archived Projects and Workspaces are managed from the Dashboard and MUST NOT appear in this Navigator.
- **Workspace Navigator:** Displays agent sessions and terminals belonging to the active workspace without a search field. Its Show Archived control remains the macOS access point for archived Sessions and Terminals until a separate archive-management surface exists.
- **File Navigator:** Reserves the future active-workspace file tree. In this implementation its selector is visible, disabled when no workspace is active, and selectable when a workspace is active. Its sidebar shows a restrained `File navigation is not available yet` empty state and does not replace the main content. This stub MUST NOT add file APIs, tree state, or fake file data.

Selecting an item within a navigator opens or focuses that item in the main content area.

Within the Projects Navigator, Dashboard and Workspace rows are destinations. Project rows are structural: their disclosure control expands or collapses their Workspaces, row selection does not replace the main content, contextual actions remain available from the row menu, and Project management remains on Dashboard. This implementation MUST NOT add a Project-detail destination.

Dashboard is always the first row. Projects sort alphabetically using a case-insensitive comparison, with Connection name and then stable identity as tie-breakers. Each Project's Workspaces remain newest-created-first, matching Dashboard. Project disclosure state lasts for the current app run and resets on launch; this version does not add manual reordering.

### Active Workspace Context

The toolbar prominently displays a clickable Workspace Switcher labeled `Project › Workspace`, preceded by a small Connection-status dot. The persistent label omits the Connection name to remain compact; the menu, tooltip, and accessibility description include it. With no active workspace, the control reads `Select Workspace…`. An archived workspace is identified inside the menu rather than with another permanent toolbar badge. The Workspace Switcher remains visible when the sidebar is hidden or displaying a different Navigator.

The workspace context is clickable and opens a quick switcher grouped by project, with Connection identity included where needed to disambiguate Projects. Switching workspaces preserves the selected navigator, updates any workspace-scoped navigator contents, and restores the target workspace's most recent useful main content when possible.

The Projects Navigator and the toolbar workspace switcher must use the same underlying workspace-selection behavior.

### No Active Workspace

The Projects Navigator is always available. Workspace-scoped Navigators are unavailable when no workspace is active and should explain their requirement through tooltips and disabled menu commands.

Selecting a workspace in the Projects Navigator establishes it as active and enables the Workspace and File Navigators. If an active workspace is temporarily offline, its Navigators remain available and display an appropriate disconnected state. If the workspace is closed or removed, the sidebar returns to the Projects Navigator.

### Trailing Inspector

The right side of the window remains a contextual inspector rather than workspace navigation. Its availability and contents are derived from the item selected in the main content area. SwiftUI's native inspector behavior should be preferred over a custom trailing panel.

The stable root content host owns one native SwiftUI inspector and one window-level presentation Boolean. Dashboard, no-selection, unsupported-content, and workspace-switch transitions close it. Selecting another inspectable item while it is open updates its contents; changing Navigators alone leaves it unchanged. Inspector visibility is not remembered per workspace or persisted by the app. The inspector toggle is available only when the selected main content supports inspection.

## State and Behavior

The design should distinguish between:

- The selected navigator, which controls the left sidebar.
- The active workspace and its derived Project context, which scope workspace-specific tools.
- The selected content, which controls the main content area.
- Inspector presentation and selection-specific details.

Changing navigators must not unnecessarily replace the main content. Changing the active workspace is an explicit context switch and must not leave content from the previous workspace presented as though it belongs to the new one.

Navigator selection is window-level state and remains stable when the active workspace changes. Workspace-specific UI state should be preserved where practical, including the selected session, terminal or file and workspace-scoped sidebar expansion state.

Workspace activation restores the last selected session or terminal only when it still exists, still belongs to that workspace, and is not archived. Running, ended, and failed content are all restorable, and a disconnected Connection does not invalidate the selection. Deleted, moved, or archived content clears the stale memory and falls back to the workspace's no-selection state; the app must not choose a replacement automatically.

Showing the Dashboard does not clear the active workspace. Workspace-scoped commands are evaluated against the active workspace rather than the selected Navigator or main content; connectivity and archival state may still disable individual commands.

Archiving the active workspace removes it from the Projects Navigator but does not clear it or replace its main content. It remains active with creation commands disabled until the user activates another workspace or it is removed.

The app launches with the Projects Navigator selected, the Dashboard shown in the main content area, and no active workspace. Each workspace's last selected session or terminal persists across launches and is restored only after the user explicitly activates that workspace. Other window state, including Navigator selection and inspector visibility, resets for a new launch.

## Implementation Direction

- Keep a single stable `NavigationSplitView` at the window root.
- Swap navigator content within the sidebar instead of replacing the root navigation hierarchy.
- Model navigator selection, active workspace, main-content selection, and inspector presentation as separate state.
- Prefer native SwiftUI sidebar, list, toolbar, menu, and inspector APIs.

## Success Criteria

- Moving between Projects, Workspace, and File Navigators does not disturb the main content or window layout.
- Users can always identify the active project and workspace.
- Users can switch workspaces quickly from the toolbar or Projects Navigator.
- Workspace-scoped navigation cannot silently operate against the wrong workspace.
- Returning to a workspace restores a useful working context.
- The Dashboard-to-workspace layout bugs and incorrect trailing-sidebar controls described in DEV-41 are eliminated by the new structure.
