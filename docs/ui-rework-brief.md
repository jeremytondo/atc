# Atelier Code Navigation Review

## Summary

Restructure the macOS app around a stable, Xcode-inspired sidebar and navigation model. The sidebar remains a persistent region of the window, while a row of navigator icons changes only the content shown inside it. The main content area changes only when the user selects a destination from the active navigator.

This should give users a clear global view of the app while allowing them to focus deeply on one workspace without fighting SwiftUI's native navigation patterns.

## Problem

The current UI treats the global overview and workspace experience as separate modes with different layout behavior. Moving between them can recreate or destabilize parts of the view hierarchy, contributing to layout bugs and inconsistent sidebar and toolbar behavior.

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

## Proposed Experience

### Navigator Sidebar

The left sidebar includes an Xcode-style navigator selector at the top. Selecting an icon changes only the sidebar contents; it does not immediately replace the main content.

The initial navigators are:

- **Global navigator:** Displays app-wide content such as projects, their related workspaces, connections, and an overview destination.
- **Workspace navigator:** Displays agent sessions and terminals belonging to the active workspace.
- **File navigator:** Displays the active workspace's file tree. (Note that this does not exist just yet so will be a future state addition. For now we should just stub it in with nothing in the navigator.)

Selecting an item within a navigator opens or focuses that item in the main content area.

### Active Workspace Context

The window prominently displays the active `Project > Workspace`, preferably in the toolbar above the main content area. This control remains visible when the sidebar is hidden or displaying a different navigator.

The workspace context is clickable and opens a quick switcher grouped by project. Switching workspaces updates the entire window context together, including workspace-scoped navigators and main content. The target workspace should restore its most recent useful UI state when possible.

The Global navigator and the toolbar workspace switcher must use the same underlying workspace-selection behavior.

### No Active Workspace

The Global navigator is always available. Workspace-scoped navigators are unavailable when no workspace is active and should explain their requirement through tooltips and disabled menu commands.

Selecting a workspace in the Global navigator establishes it as active and enables the Workspace and File navigators. If an active workspace is temporarily offline, its navigators remain available and display an appropriate disconnected state. If the workspace is closed or removed, the sidebar returns to the Global navigator.

### Trailing Inspector

The right side of the window remains a contextual inspector rather than workspace navigation. Its availability and contents are derived from the item selected in the main content area. SwiftUI's native inspector behavior should be preferred over a custom trailing panel.

## State and Behavior

The design should distinguish between:

- The selected navigator, which controls the left sidebar.
- The active project and workspace, which scope workspace-specific tools.
- The selected content, which controls the main content area.
- Inspector presentation and selection-specific details.

Changing navigators must not unnecessarily replace the main content. Changing the active workspace is an explicit context switch and must not leave content from the previous workspace presented as though it belongs to the new one.

Workspace-specific UI state should be preserved where practical, including the selected navigator, selected session, terminal or file, sidebar expansion state, and inspector visibility.

## Implementation Direction

- Keep a single stable `NavigationSplitView` at the window root.
- Swap navigator content within the sidebar instead of replacing the root navigation hierarchy.
- Model navigator selection, active workspace, main-content selection, and inspector presentation as separate state.
- Prefer native SwiftUI sidebar, list, toolbar, menu, and inspector APIs.
- Make navigator and workspace-selection actions available to the app's keyboard shortcut system.

## Success Criteria

- Moving between Global, Workspace, and File navigators does not disturb the main content or window layout.
- Users can always identify the active project and workspace.
- Users can switch workspaces quickly from the toolbar or Global navigator.
- Workspace-scoped navigation cannot silently operate against the wrong workspace.
- Returning to a workspace restores a useful working context.
- The Dashboard-to-workspace layout bugs and incorrect trailing-sidebar controls described in DEV-41 are eliminated by the new structure.

## Questions for the Spec

- What should the Global navigator be called in the UI, and which destinations belong in its first version?
- What exact content should be restored when switching back to a workspace?
- Should selecting a workspace initially show a workspace overview or immediately restore its last-open content?
- How should the toolbar represent workspace location and connection status without becoming visually busy?
- Which navigator and workspace state should persist across app launches versus only for the current window session?
1Password menu is available. Press down arrow to select.
