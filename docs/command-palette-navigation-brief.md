# Command Palette Navigation Brief

Status: Draft

## Purpose

Make the Command Palette the fastest keyboard-driven way to move between
Workspaces and the Sessions or Terminals inside the Active Workspace, without
creating a separate switcher or bypassing the app's existing navigation model.

## Idea Definition

The Command Palette should search both registered commands and live navigation
targets. Workspace results are available across the app. Session and Terminal
results are scoped to the Active Workspace so the palette always makes their
context clear.

Choosing a Workspace activates it and restores its remembered content using the
same behavior as the Projects Navigator and toolbar Workspace Switcher. Choosing
a Session or Terminal selects it through the existing window navigation path,
attaches when appropriate, and moves keyboard focus into its terminal.

## Recommended Direction

- Keep the existing `Shift-Cmd-P` palette and extend its result model rather
  than introducing nested pickers or another shortcut.
- Present Commands, Workspaces, and Sessions & Terminals as clearly identified
  result types in one searchable list.
- Search unarchived Workspaces across all configured Connections. Show Project
  and Connection context when needed to disambiguate similar names.
- Search unarchived Sessions and Terminals only within the Active Workspace.
  Use the same display-name and classification rules as the Workspace Navigator.
- Route every selection through `WindowState.activateWorkspace` or
  `WindowState.selectSession` so restoration, identity validation, attachment,
  and focus behavior stay consistent across navigation surfaces.
- Reuse the existing query matcher and extract shared target projections where
  the palette and existing navigators need the same filtering, labeling, or
  ordering logic.

## Key Features

- Search for a Workspace by Workspace, Project, or Connection name.
- Search for a Session or Terminal by its displayed name within the Active
  Workspace.
- Distinguish each result with a compact type label or icon and enough parent
  context to avoid ambiguous navigation.
- Preserve the palette's current keyboard and pointer behavior: typing filters,
  arrows move selection, Return activates, and Escape dismisses.
- Dismiss before navigation, then restore focus to the selected Terminal Session.
- Keep unavailable Workspace results visible but disabled when their Connection
  is unreachable, consistent with the toolbar Workspace Switcher.
- Provide clear empty states when there is no Active Workspace or no matching
  Session or Terminal.

## Non-Goals / Deferred Ideas

- Creating, renaming, archiving, or deleting navigation targets from the palette.
- Showing archived Workspaces, Sessions, or Terminals.
- Searching Sessions or Terminals across inactive Workspaces.
- Adding Project, file, symbol, task, source-control, or Agent Action results.
- Usage ranking, history, favorites, typo correction, previews, or a provider
  plugin system.
- Server, API, persistence, or terminal transport changes.

## System Shape

- **Palette result model**: Represents either a registered command, Workspace,
  Session, or Terminal with a stable connection-qualified identity.
- **Result projection**: Combines command descriptors with live Workspace and
  Active-Workspace Session data, applies shared matching, and produces
  deterministic display order.
- **Navigation handlers**: Activate results through the existing `WindowState`
  transitions rather than duplicating selection or attachment logic.
- **Palette view**: Renders heterogeneous rows while retaining the current
  overlay, focus ownership, accessibility, and interaction behavior.

## Open Questions

- After this first version, should selecting an inactive Workspace keep the
  palette open in a Workspace-scoped mode so the user can immediately choose one
  of its Sessions or Terminals, or should that remain a deliberate two-step flow?
