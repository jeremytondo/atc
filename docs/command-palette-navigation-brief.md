# Command Palette Navigation Brief

Status: Draft

## Purpose

Make the Command Palette the fastest keyboard-driven way to move between
Workspaces and the Sessions or Terminals inside the Active Workspace, without
replacing the toolbar Workspace Switcher or bypassing the app's existing
navigation model.

## Idea Definition

The Command Palette should search both registered commands and live navigation
targets. Workspace results are available across the app. Session and Terminal
results are scoped to the Active Workspace so the palette always makes their
context clear.

Choosing a Workspace activates it and restores its remembered content using the
same behavior as the Projects Navigator and toolbar Workspace Switcher. Choosing
a Session or Terminal selects it through the existing window navigation path,
attaches when appropriate, and moves keyboard focus into its terminal.
Every selection is a one-shot action: the palette dismisses before navigation.
To navigate within a newly activated Workspace, the user reopens the palette.

## Recommended Direction

- Keep the existing `Shift-Cmd-P` palette and extend its result model rather
  than introducing nested pickers or another shortcut.
- Present Commands, Workspaces, and Sessions & Terminals as clearly identified
  result types in one searchable list.
- Preserve the existing eligible command list when the query is empty. Add live
  navigation targets only after the user enters a non-whitespace query; the
  Workspace Switcher remains the browsing surface for Workspaces.
- For a nonempty query, keep one flat deterministic order: Commands, Workspaces,
  then one combined Sessions & Terminals bucket. Sort alphabetically by title
  with stable identity tie-breaking within each bucket; do not split Sessions
  from Terminals or introduce relevance scoring.
- Search unarchived Workspaces across all configured Connections. Show Project
  and Connection context on every Workspace result. Reuse the shared
  Project/Workspace projection so Workspaces beneath archived Projects remain
  excluded exactly as they are from the Projects Navigator and Workspace
  Switcher.
- Keep the Active Workspace in matching Workspace results without special
  current-state presentation. Selecting it can return from the Dashboard to its
  remembered content through the normal activation path.
- Search unarchived Sessions and Terminals only within the Active Workspace.
  Use the same display-name and classification rules as the Workspace Navigator.
- Ignore the Workspace Navigator's local `Show Archived` setting. Archived
  Workspaces, Sessions, and Terminals never enter palette results.
- Apply archive filtering per target. If an archived Workspace remains active,
  its unarchived Sessions and Terminals remain eligible Active-Workspace
  results even though the Workspace itself is absent from Workspace results.
- Keep the currently selected Session or Terminal in matching results without
  special current-state presentation; selecting it dismisses the palette and
  restores terminal focus through the normal selection path.
- Keep already-loaded Session and Terminal results selectable when the Active
  Workspace's Connection is unreachable. Selection is local navigation; the
  terminal surface remains responsible for disconnected and recovery state.
- Route every selection through `WindowState.activateWorkspace` or
  `WindowState.selectSession` so restoration, identity validation, attachment,
  and focus behavior stay consistent across navigation surfaces.
- Reuse the existing query matcher and extract shared target projections where
  the palette and existing navigators need the same filtering, labeling, or
  ordering logic.
- Build navigation results synchronously from already-loaded in-memory stores.
  Opening and filtering the palette must not start API requests, attachment, or
  other asynchronous work; only activating a result may trigger navigation work.
- Recompute filtered results immediately for every query change. Do not add
  debounce, loading, cancellation, or background-search state.
- Return the complete matching set without an arbitrary result cap. Retain lazy
  row rendering and validate filtering with a large synthetic candidate set so
  performance problems are detected rather than hidden by truncation.

## Key Features

- Search for a Workspace by Workspace, Project, or Connection name. Apply the
  existing matcher independently to each field; the complete query must match
  one field, without cross-field token combinations or scoring.
- Search for a Session or Terminal by its displayed name within the Active
  Workspace. Do not match hidden Action names, lifecycle status, or raw
  identifiers.
- Keep navigation targets out of an empty-query result set so they do not
  overwhelm command discovery.
- Keep navigation rows minimal and do not add status, previews, current-target
  indicators, or other metadata.
- Preserve the existing Command row with its title and optional Keyboard
  Shortcut. Give Session and Terminal rows only a small explicit `Session` or
  `Terminal` text label; do not rely on ambiguous type icons.
- Render each Workspace result with its Workspace name as the primary label and
  `Project · Connection` as stable secondary context, even when names are unique.
- Preserve the palette's current keyboard and pointer behavior: typing filters,
  arrows move selection, Return activates, and Escape dismisses.
- Replace command-only copy with the query placeholder
  `Search commands and navigation…`, the empty state `No matching results`, and
  the accessibility label `Palette search`.
- Dismiss before navigation, then restore focus to the selected Session or
  Terminal.
- Keep Workspace results whose Connection is unreachable visible and
  keyboard-selectable, but unavailable. Return or click keeps the palette open
  and reports a concise Connection-unavailable reason through the palette's
  existing unavailable-command feedback path.
- When there is no Active Workspace, omit Session and Terminal candidates
  without adding a passive informational row. Show one generic no-results state
  only when the entire filtered list is empty.

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
