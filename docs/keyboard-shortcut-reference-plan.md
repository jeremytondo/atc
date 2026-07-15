# Keyboard Shortcut Reference Plan

Status: Draft

Related: [DEV-46](https://linear.app/elevenideas/issue/DEV-46/keyboard-shortcut-reference),
[keyboard-shortcuts-plan.md](keyboard-shortcuts-plan.md), and
[keyboard-shortcuts-spec.md](keyboard-shortcuts-spec.md)

## Purpose

Give people a searchable, always-current reference for every configured atc
keyboard shortcut without taking space from the terminal, competing with
Session Details, or duplicating keybinding configuration in the UI.

## Confirmed Decisions

- Present the reference in a singleton SwiftUI `UtilityWindow` titled
  **Keyboard Shortcuts** rather than in the main window's trailing inspector.
- Keep the existing inspector dedicated to contextual Session Details.
- Use native SwiftUI window, menu, search, list, section, and empty-state
  behavior. The Linear screenshot is inspiration for content, not a layout
  requirement.
- Provide fuzzy search and group the unfiltered and filtered results under
  meaningful headings.

## Recommended Experience

- Add **Keyboard Shortcuts** to the Help menu. Selecting it opens the utility
  window or brings the existing instance forward.
- Remove the `UtilityWindow`'s automatic View-menu command and place a
  `WindowVisibilityToggle` in SwiftUI's `.help` command group so the feature has
  one canonical menu location.
- The window opens at a useful compact size, remains resizable, floats while
  atc is active, hides with the app, and supports the native Escape-to-dismiss
  behavior supplied by `UtilityWindow`.
- A native search field receives focus when the window opens. Below it, a
  sectioned `List` shows command titles on the leading edge and their resolved
  direct shortcuts or Command Sequences on the trailing edge.
- A command with multiple configured bindings appears once with all of its
  bindings. A two-step Command Sequence renders as two distinct key groups in
  execution order rather than as one opaque string.
- With no query, commands use stable category and command ordering. With a
  query, empty sections disappear and matches rank within their category.
- `ContentUnavailableView.search` handles a query with no matches. If the user
  clears every default binding, a separate empty state explains that no
  keyboard shortcuts are configured.
- The window displays the active resolved keymap. Reloading `config.toml`
  updates it immediately, including remaps, unbinds, a changed leader, and
  `clear_default_keybindings`.

## Command and Binding Model

The command registry and resolved keymap remain the only sources of truth.
The reference must not maintain a second hard-coded shortcut table.

- Add a stable `CommandCategory` to `CommandDescriptor`. Category titles and
  ordering belong to command metadata so menus, this reference, and the future
  command palette do not invent separate organization schemes.
- Add an ordered flat collection of `ResolvedBinding` values to
  `ResolvedKeymap` alongside its routing tree and `menuShortcuts`. Each value
  contains the resolved `KeySequence` and `CommandID`.
- Build that collection during keymap resolution, after defaults, user
  replacements, unbinds, and leader expansion have been applied. Do not
  reconstruct display data by walking the prefix tree.
- Project resolved bindings into one reference item per bound command by
  joining them with `CommandRegistry` metadata.
- Exclude commands with no resolved binding. This is a shortcut reference, not
  a catalog of every app command; menu-only and unbound commands remain
  discoverable through their normal menus.
- Reuse `KeyStroke.displayDescription` as the base display formatter and add a
  sequence-level formatter that also provides a spoken accessibility label.

Initial categories should cover the current registry without introducing
feature-specific UI logic:

- **Projects & Workspaces**: project and Workspace creation commands.
- **Sessions**: Agent Session and Terminal Session commands.
- **View**: sidebar and other presentation commands.
- **General**: app-wide operations such as refresh and configuration reload.

Only categories containing at least one bound command are displayed.

## Fuzzy Search

- Implement matching as a small, deterministic, Swift-only value type with no
  production dependency.
- Normalize case and diacritics, require query characters to occur in order,
  and reward exact prefixes, word-boundary matches, and contiguous runs.
- Search command title, category title, stable command identifier, and both
  human-readable and configuration-style binding text.
- Keep matching independent of SwiftUI and of `CommandID` so DEV-37 can reuse
  it for the command palette and session search without copying ranking logic.
- Preserve stable command order as the tie-breaker so results do not jump
  unpredictably while typing.

## System Shape

- **`ATCApp`**: Own the singleton `UtilityWindow` scene, inject the existing
  `KeyboardConfigStore`, set a reasonable default size, and remove the scene's
  automatic commands.
- **`AppCommands`**: Add the Help-menu `WindowVisibilityToggle` for the utility
  window. The existing registry-driven app commands remain unchanged.
- **Command registry**: Own category metadata and stable presentation order.
- **Keymap resolver**: Publish the final ordered bindings in addition to the
  routing tree and selected menu equivalents.
- **Shortcut reference model**: Join descriptors to resolved bindings, group
  and order items, and expose filtered sections.
- **Shortcut reference views**: Render the searchable native list, section
  headings, shortcut labels, accessibility descriptions, and empty states.
- **Fuzzy matcher**: Provide reusable, UI-independent matching and scoring.

The feature belongs under a focused macOS feature directory such as
`macos/ATC/Features/KeyboardShortcuts/`. Generic matching may live in
`macos/ATC/Shared/` once it has a second consumer; until then its API should be
generic but its file can remain with the feature.

## Implementation Sequence

1. Extend command descriptors with categories and stable presentation order.
2. Preserve the final ordered bindings in `ResolvedKeymap` and cover overrides,
   unbinding, leader expansion, cleared defaults, and reload behavior in tests.
3. Add the reference projection and fuzzy matcher with focused unit tests.
4. Build the sectioned, searchable `KeyboardShortcutsView` from the projection.
5. Add the singleton `UtilityWindow` and Help-menu visibility toggle.
6. Add hosting or presentation smoke coverage, then verify menu opening, window
   reuse, search focus, live configuration reload, Escape dismissal, and
   terminal focus behavior manually.

## Accessibility and Keyboard Behavior

- The window and every result must be navigable with Full Keyboard Access.
- Shortcut symbols need spoken labels such as “Command K, then B”; VoiceOver
  must not receive only glyphs like `⌘` or `⇧`.
- Search must not activate the main window's terminal keyboard router because
  the utility window is a separate key window.
- Closing the utility window should return naturally to the previous main
  window without mutating Workspace, selection, terminal, or inspector state.

## Non-Goals

- Editing, recording, resetting, or resolving keybinding conflicts in a GUI.
- Moving the reference into Settings.
- Replacing native menu shortcut labels.
- Executing commands from the reference list; DEV-46 is reference and search,
  not the DEV-37 command palette.
- Adding a custom right-side overlay, sheet, popover, or second inspector.
- Adding a third-party fuzzy-search dependency.

## Success Criteria

- Help → Keyboard Shortcuts opens one reusable utility window and never alters
  the main window's inspector or navigation state.
- The reference shows every and only currently resolved binding, grouped by
  registry-owned categories.
- Direct shortcuts, multiple bindings, and Command Sequences are legible and
  accessible.
- Fuzzy search returns useful, deterministic results and preserves headings.
- Reloading valid configuration updates the open window; invalid reloads keep
  showing the previous working keymap.
- Clearing or unbinding defaults never leaves stale shortcut rows behind.
- The window remains usable when the Dashboard or a terminal is active, and
  opening or closing it does not send keystrokes to the terminal.
- Automated tests cover binding projection, ordering, grouping, fuzzy ranking,
  empty states, and configuration changes.

## Open Question

- Should the Keyboard Shortcuts window receive a compiled default shortcut in
  this issue? Recommendation: ship the Help-menu entry first and defer a default
  until the command can participate in the same configurable registry and
  terminal-safe routing path as every other atc shortcut.
