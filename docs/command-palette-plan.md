# Command Palette Plan

Status: Specified in [command-palette-spec.md](command-palette-spec.md);
that spec's Resolved Decisions record the deltas from this draft.

Related: [command-palette-spec.md](command-palette-spec.md),
[keyboard-shortcuts-plan.md](keyboard-shortcuts-plan.md),
[keyboard-shortcuts-spec.md](keyboard-shortcuts-spec.md), and
[keyboard-shortcut-reference-plan.md](keyboard-shortcut-reference-plan.md).

## Summary

Add a small, Ghostty-style Command Palette to the macOS app. The palette is a
flat, searchable list of actions from atc's existing command registry. It opens
with `Shift-Cmd-P`, executes one command, and dismisses.

This plan covers only the Command Palette. Workspace, Session, Agent Action,
file, symbol, task, source-control, and mixed-result pickers are separate future
features and are not architectural inputs to this implementation.

## Reference

Follow Ghostty's macOS Command Palette unless an existing atc command or focus
boundary requires a small deliberate difference.

Reference implementation:

- [Ghostty `CommandPaletteView`](https://github.com/ghostty-org/ghostty/blob/main/macos/Sources/Features/Command%20Palette/CommandPalette.swift)
- [Ghostty terminal integration](https://github.com/ghostty-org/ghostty/blob/main/macos/Sources/Features/Command%20Palette/TerminalCommandPalette.swift)

The useful Ghostty shape is:

- One in-window SwiftUI overlay.
- One flat array of command options.
- Local query, selection, and hover state.
- Simple title filtering with highlighted matches.
- Direct Keyboard Shortcut symbols on the trailing edge.
- Arrow-key, Return, Escape, and pointer interaction.
- Dismissal before command execution.
- Focus restoration to the terminal after dismissal.

atc keeps its existing `CommandRegistry`, `CommandAvailability`, resolved
keymap, and terminal-safe keyboard router rather than copying Ghostty's action
model.

## Goals

- Let users discover and execute atc commands without leaving the keyboard.
- Work while an embedded Ghostty terminal has focus.
- Reuse the same command identifiers, titles, availability, and execution paths
  as menus, toolbar controls, Keyboard Shortcuts, and Command Sequences.
- Display the current resolved direct Keyboard Shortcut where one exists.
- Keep the implementation small enough to understand in one pass.
- Preserve normal terminal input whenever the palette is closed.

## Non-Goals

- A reusable picker framework or provider protocol.
- Workspace, Session, Agent Action, file, symbol, task, source-control, or
  mixed-result search.
- Launching another picker from the Command Palette.
- Picker stacks, provider switching, back buttons, prefixes, or query handoff.
- Fuzzy scoring, typo correction, ranking weights, ranking fixtures, or an
  external matching dependency.
- Command history, usage ranking, recency, personalization, previews, or
  persisted palette state.
- A graphical keybinding editor.
- Displaying Command Sequences as Keyboard Shortcuts.
- Server, API contract, ATCKit, or GhosttyTerminal changes.

## Existing Foundation

The keyboard-shortcut implementation already provides the required seams:

- `CommandID` identifies each command.
- `CommandRegistry` owns titles, availability, and execution.
- `CommandContext` supplies the current `AppModel`, `WindowState`, and keyboard
  configuration.
- `ResolvedKeymap.menuShortcuts` supplies the one resolved direct Keyboard
  Shortcut already selected for native-menu display.
- `WindowKeyboardRouter` handles registered shortcuts before the embedded
  terminal consumes them.
- `CommandFeedbackOverlay` already renders brief command feedback; expose one
  small router method so the palette can present an unavailable-command reason
  through the same path.

The palette extends these seams rather than creating a second command system.

## Command Catalog

Extend `CommandDescriptor` with only the presentation metadata required by the
palette and Keyboard Shortcuts reference:

- A broad `CommandCategory`.
- Whether the command is eligible for the Command Palette.

Use these initial categories:

1. General.
2. Projects & Workspaces.
3. Sessions & Terminals.
4. View.

Add deterministic command enumeration to `CommandRegistry`. Registry tests must
verify that every `CommandID` has exactly one descriptor, identifiers are
unique, titles are nonempty, and enumeration order is stable.

The palette includes every palette-eligible command even when it has a familiar
shortcut. Toggle Sidebar therefore remains discoverable despite its `Cmd-B`
default. The `view.toggle-command-palette` opener itself is not
palette-eligible because executing it from the open palette would only close
the palette. Commands whose only purpose is opening another focused picker are
also not palette-eligible. The opener gets a View-menu item like every other
registered command, so the palette and its shortcut stay discoverable from the
menu bar.

## Presentation

Mirror Ghostty's compact presentation:

- Render an upper-center overlay inside the active atc window.
- Keep the terminal and current content mounted behind it.
- Use a compact material-backed surface with rounded corners, border, and
  shadow.
- Target a maximum width of roughly 500 points and a result-list height of
  roughly 200 points; adapt down for smaller windows.
- Do not add a separate window, `NSPanel`, sheet, popover, or modal scrim.
- Use one transparent outside-click layer to dismiss and consume the initiating
  click. This is a deliberate atc difference because content behind the palette
  includes navigation controls, not only a terminal surface.
- Place a plain query field at the top with an action-oriented placeholder such
  as `Execute a command…`.
- Render one flat result list. Do not group rows by category.

Each row contains:

- Command title.
- The resolved direct Keyboard Shortcut on the trailing edge, when present.
- Dimmed treatment and the existing availability reason when unavailable.

Command Sequences never appear as shortcut labels. A command bound only through
a Command Sequence has no trailing shortcut in the palette.

## Matching and Ordering

Use the same deliberately small matching behavior as Ghostty:

1. Try a case-insensitive substring match against the command title.
2. If that fails, match the query against the first letters of title words.
3. Return the matching character positions for highlighting.
4. Exclude commands that do not match.

Do not calculate scores or reorder matches by perceived quality. Empty and
filtered results preserve one deterministic alphabetical title order, with the
stable command identifier breaking ties.

Put the matcher in a small UI-independent atc type. The Keyboard Shortcuts
reference may reuse it for filtering, but the matcher does not become a generic
search framework.

## Interaction

- `Shift-Cmd-P` toggles the palette: it opens with the query field focused,
  and a second press dismisses.
- Do not ship a compiled Command Sequence for opening the palette.
- Typing filters the list.
- Up and Down move selection and wrap at the list boundaries.
- `Control-P` and `Control-N` mirror Ghostty's previous/next movement as aliases
  of the same selection actions.
- With an empty query, no row is selected until the user moves selection.
- Typing a nonempty query selects the first matching row.
- Return executes the selected available command.
- Return with no selected row dismisses without executing anything.
- Escape dismisses.
- Clicking an available row executes it; clicking an unavailable row selects
  it and shows its reason.
- Clicking outside dismisses and consumes that click; it does not activate
  content behind the palette.
- App or window deactivation dismisses the palette; do not preserve its query
  or introduce a suspended state.
- Dismissal clears the query for the next opening.

For an available result, dismiss the palette before calling
`CommandRegistry.execute`. For an unavailable result, keep the palette open and
show the existing registry-owned availability reason through the current
command feedback path.

## State and Integration

Add one per-window presentation Boolean to `WindowState`. Keep query, selected
row, and hover state local to `CommandPaletteView`.

Do not add a picker coordinator, provider protocol, candidate framework,
type-erased payload, generic action model, loading state, error state, async
stream, cancellation generation, or background search task.

Overlay `CommandPaletteView` at the existing stable window root. While it is
visible, its query field owns text and navigation input, and palette keystrokes
must not reach the terminal. When it dismisses, restore the previous first
responder when still valid, with the selected terminal as the practical
fallback. Do not create a generalized focus coordinator.

## Accessibility

- Give the overlay and query field clear accessible names.
- Expose command title, availability, reason, and direct Keyboard Shortcut to
  assistive technology.
- Provide spoken shortcut labels rather than only glyphs.
- Keep match highlighting supplemental; the full title remains readable.
- Support keyboard and pointer operation.
- Respect Reduce Motion and increased contrast without adding custom animation
  machinery.

## Testing

### Unit Tests

- Registry enumeration is complete, unique, and deterministic.
- Palette eligibility excludes the palette opener.
- Substring matching is case-insensitive.
- Word-initial matching handles representative atc command titles.
- Nonmatches are excluded and match positions are correct.
- Alphabetical order and identifier tie-breaking are stable.
- Direct Keyboard Shortcuts appear; Command Sequences do not.
- Availability is evaluated against the current `CommandContext`.

### Presentation and Manual Checks

- Open with `Shift-Cmd-P` while Ghostty has focus.
- Confirm the query receives focus and input never reaches the terminal.
- Dismiss with Escape, outside click, app deactivation, and Return with no
  selection.
- Confirm an outside click does not activate underlying navigation or terminal
  content.
- Execute with Return and pointer selection.
- Confirm available commands execute exactly once after dismissal.
- Confirm unavailable commands keep the palette open and show their reason.
- Confirm dismissal restores terminal focus when the terminal was previously
  focused.
- Confirm remapping or unbinding a direct shortcut updates the open palette.
- Verify VoiceOver labels, increased contrast, and Reduce Motion.

## Implementation Sequence

1. Extend command descriptors with category and palette eligibility, add stable
   enumeration, and add registry tests.
2. Add the small matcher and its focused tests.
3. Implement `CommandPaletteView` and its result rows using local state.
4. Add `view.toggle-command-palette`, the `cmd+shift+p` compiled binding, the
   View-menu item, and the per-window presentation Boolean.
5. Overlay the palette at the window root, verify terminal-safe input and focus
   restoration, and complete the manual checks.

This is one vertical feature. It does not establish an abstraction that future
pickers must adopt.

## Success Criteria

- `Shift-Cmd-P` opens a compact Command Palette from any main-window focus,
  including the embedded terminal.
- Users can filter and execute every palette-eligible registered command.
- Direct Keyboard Shortcuts are accurate; Command Sequences are never presented
  as shortcuts.
- Unavailable commands remain visible and explain why they cannot run.
- Query and navigation input never leak into the terminal.
- Selection dismisses before executing and commands run exactly once.
- Escape, outside click, app deactivation, and empty Return dismiss cleanly.
- Terminal focus is restored after dismissal when appropriate.
- The implementation adds no external search dependency or speculative picker
  framework.
