# Command Palette Implementation Spec

Status: Draft v1 — reconciled against the current `macos/` sources

Scope: the Command Palette described in
[command-palette-plan.md](command-palette-plan.md) — a flat, searchable,
Ghostty-style overlay over the existing command registry. All work lands in
`macos/`; no server, ATCKit, or GhosttyTerminal package change is required.
This is one vertical feature: no picker framework, provider protocol, or
generic search infrastructure is introduced.

Related: [command-palette-plan.md](command-palette-plan.md) (the brief this
spec implements), [keyboard-shortcuts-spec.md](keyboard-shortcuts-spec.md)
(the registry, keymap, and router this feature extends),
[keyboard-shortcut-reference-plan.md](keyboard-shortcut-reference-plan.md)
(the future second consumer of `CommandCategory` and the matcher),
`macos/CONTEXT.md` (terminology — a Command Sequence is not a Keyboard
Shortcut and never appears as one).

Reference implementation, followed unless an atc seam requires a deliberate
difference (each difference is recorded in Resolved Decisions):

- [Ghostty `CommandPaletteView`](https://github.com/ghostty-org/ghostty/blob/main/macos/Sources/Features/Command%20Palette/CommandPalette.swift)
- [Ghostty terminal integration](https://github.com/ghostty-org/ghostty/blob/main/macos/Sources/Features/Command%20Palette/TerminalCommandPalette.swift)

## 1. Module Layout

New feature folder (file-system synchronized groups pick the files up
without project edits):

```text
macos/ATC/Features/CommandPalette/
├── CommandPaletteView.swift     — overlay chrome, query field, rows, local state
├── CommandPaletteContent.swift  — pure row projection: eligibility, match, order
└── QueryMatcher.swift           — substring / word-initial matcher (UI-independent)
```

Changed files, all small:

- `Commands/CommandID.swift` — one new case.
- `Commands/CommandRegistry.swift` — `CommandCategory`, descriptor metadata,
  stable enumeration, the new descriptor.
- `Commands/Keymap.swift` — one new compiled default binding.
- `Commands/WindowKeyboardRouter.swift` — suspension check and a public
  unavailable-reason flash method.
- `Commands/KeyboardMonitorHost.swift` — `KeyboardRoutingContainer` mounts the
  palette overlay and wires suspension; the coordinator gains a deactivation
  callback.
- `Commands/KeyStroke.swift` — `spokenDescription` for assistive technology.
- `WindowState.swift` — one presentation Boolean and one sheet-presence
  computed property.
- `AppCommands.swift` — one View-menu item.

`QueryMatcher` stays with the feature until the Keyboard Shortcuts reference
becomes its second consumer; its API is already UI- and `CommandID`-independent
so that move is a file relocation, not a rewrite.

## 2. Command Catalog

### Category and palette eligibility

`CommandDescriptor` gains exactly the presentation metadata the palette and
the future Keyboard Shortcuts reference need — nothing else:

```swift
enum CommandCategory: CaseIterable, Sendable {
    case general              // "General"
    case projectsAndWorkspaces // "Projects & Workspaces"
    case sessionsAndTerminals  // "Sessions & Terminals"
    case view                  // "View"

    var title: String
}

@MainActor
struct CommandDescriptor {
    let id: CommandID
    let title: String
    let category: CommandCategory
    var isPaletteEligible = true
    let availability: (CommandContext) -> CommandAvailability
    let perform: (CommandContext) -> Void
}
```

`CommandCategory` declaration order is the stable presentation order for
grouped surfaces. The palette itself renders one flat list and ignores
grouping; the category ships now so menus, the palette, and the reference
window never invent competing organization schemes.

| Command | Category | Palette-eligible |
|---|---|---|
| `view.toggle-sidebar` | View | yes |
| `view.toggle-command-palette` | View | **no** — executing it from the open palette would only close the palette |
| `session.new` | Sessions & Terminals | yes |
| `terminal.new` | Sessions & Terminals | yes |
| `project.new` | Projects & Workspaces | yes |
| `workspace.new` | Projects & Workspaces | yes |
| `data.refresh` | General | yes |
| `configuration.reload` | General | yes |

Every eligible command appears even when it has a familiar shortcut — Toggle
Sidebar stays discoverable despite `Cmd-B`.

### Stable enumeration

```swift
@MainActor
enum CommandRegistry {
    static var allDescriptors: [CommandDescriptor] {
        CommandID.allCases.map(descriptor(for:))
    }
    // descriptor(for:) and execute(_:context:) unchanged
}
```

Enumeration order is `CommandID.allCases` declaration order and therefore
stable across launches. Registry tests pin completeness (every `CommandID`
yields a descriptor whose `id` matches), identifier uniqueness, nonempty
titles, and stable order (§9).

### The opener command

```swift
case toggleCommandPalette = "view.toggle-command-palette"
```

| Field | Value |
|---|---|
| Title | `Toggle Command Palette` |
| Category | View |
| Palette-eligible | No |
| Perform | `windowState.isCommandPalettePresented.toggle()` |
| Availability | `.unavailable(reason: "Not available while a dialog is open")` while `windowState.isSheetPresented`; otherwise available |

`WindowState.isSheetPresented` is a computed property over the three existing
sheet drivers (`isCreateProjectPresented`, `createWorkspaceContext != nil`,
`startSessionKind != nil`). The guard exists because menu key equivalents
fire even while a sheet is the key window (keyboard-shortcuts-spec §7); a
palette presented underneath a sheet could neither take focus nor dismiss
itself. The router path never encounters this case — a sheet is its own key
window, so the monitor is already out of the path.

Compiled default, added to `Keymap.compiledDefaults`:

```swift
("cmd+shift+p", .toggleCommandPalette),
```

No compiled Command Sequence is shipped for the palette (per the brief).
`cmd+shift+p` conflicts with no protected trigger and no existing default;
users can rebind or unbind it in `[keybindings]` like any other command.

Menu item: `AppCommands` adds `commandButton(.toggleCommandPalette)` to the
existing `CommandGroup(after: .sidebar)`, after Refresh and before the
divider. This gives the command the same menu home every other registered
command has, shows the resolved shortcut in the menu bar, and — because menu
equivalents remain live while the router is suspended (§7) — is the mechanism
that makes a second `Shift-Cmd-P` close the open palette.

## 3. QueryMatcher

One small UI-independent type; deliberately not a search framework:

```swift
struct QueryMatch: Equatable {
    /// Ranges within the matched title, for supplemental highlighting.
    let ranges: [Range<String.Index>]
}

enum QueryMatcher {
    static func match(_ query: String, in title: String) -> QueryMatch?
}
```

Behavior, in order:

1. Trim whitespace from the query. An empty trimmed query matches every
   title with no highlight ranges.
2. **Substring**: `title.range(of: trimmed, options: .caseInsensitive)` —
   on a hit, return that single range.
3. **Word-initial**: split the title into words — maximal runs of
   alphanumeric characters, so `New Project…` yields `["New", "Project"]`
   and punctuation never becomes a word. Lowercase each word's first
   character to form the initials string (`"np"`). If the initials string
   contains the lowercased trimmed query as a substring, return one
   single-character range per matched word's first character.
4. Otherwise return `nil`; the command is excluded from results.

No scores, no reordering by match quality, no typo tolerance, no external
dependency. Matching is deterministic for a given query and title.

## 4. Row Projection (`CommandPaletteContent`)

A pure `@MainActor` function the view calls from `body`, so SwiftUI
observation of `AppModel`, `WindowState`, and the keymap re-evaluates rows
automatically (live availability changes and config reloads update the open
palette without extra machinery):

```swift
struct CommandPaletteRow: Identifiable {
    let id: CommandID
    let title: String
    let matchedRanges: [Range<String.Index>]
    let shortcut: KeyStroke?              // keymap.menuShortcuts[id]
    let availability: CommandAvailability
}

@MainActor
enum CommandPaletteContent {
    static func rows(
        query: String,
        keymap: ResolvedKeymap,
        context: CommandContext
    ) -> [CommandPaletteRow]
}
```

Rules:

- Start from `CommandRegistry.allDescriptors`, keeping only
  `isPaletteEligible` descriptors.
- Apply `QueryMatcher`; drop nonmatches.
- Order alphabetically by lowercased title (plain Unicode comparison, not
  locale-dependent), breaking ties with the raw `CommandID` value. The same
  order holds for the empty query and every filtered result.
- `shortcut` comes from `keymap.menuShortcuts` only. That map is built solely
  from direct bindings, so a command bound only through a Command Sequence
  gets `nil` and shows no trailing shortcut — Command Sequences are never
  presented as Keyboard Shortcuts.
- `availability` is the descriptor's closure evaluated against the current
  `CommandContext` at projection time. Unavailable commands stay in the list.

## 5. Presentation

### State

`WindowState` gains one per-window Boolean:

```swift
var isCommandPalettePresented = false
```

Query text, selected row, and hover state are `@State` local to
`CommandPaletteView`. The overlay is mounted conditionally
(`if windowState.isCommandPalettePresented`), so dismissal destroys the view
identity and every next opening starts with a cleared query and no selection —
no explicit reset code and no suspended state.

### Placement

`KeyboardRoutingContainer` (the existing stable window root) mounts the
palette between the content and the feedback overlay, so router flashes stay
visible above the open palette:

```swift
content
    .overlay {
        if windowState.isCommandPalettePresented { CommandPaletteView() }
    }
    .overlay { CommandFeedbackOverlay() }
```

No separate window, `NSPanel`, sheet, popover, or modal scrim. The terminal
and current content stay mounted behind the overlay.

### Layout

- Upper-center: top-aligned with a fixed ~48 pt top inset below the toolbar
  edge.
- One transparent full-window layer behind the panel
  (`Color.clear.contentShape(Rectangle())` with a tap gesture) dismisses and
  consumes the initiating click, so the click never activates navigation or
  terminal content behind the palette. This is the deliberate atc difference
  from Ghostty: content behind the palette includes navigation controls, not
  only a terminal surface.
- Panel: material background (`.regularMaterial`), rounded corners
  (~10–12 pt), hairline border stroke, shadow. Maximum width 500 pt, adapting
  down with ~20 pt side margins in smaller windows.
- A plain borderless query field at the top with placeholder
  `Execute a command…`, then a divider, then the result list.
- Result list: one flat scrollable list, maximum height ~200 pt, shrinking to
  fit fewer rows; no category grouping, no section headers. The selected row
  is kept scrolled into view (`ScrollViewReader`).
- Empty result set (nonempty query, no matches): a single non-interactive
  secondary-styled `No matching commands` label in the list area.

### Rows

Each row renders:

- The command title, with matched ranges emphasized (e.g. bold or primary
  color). Highlighting is supplemental — the full title stays readable.
- The resolved direct Keyboard Shortcut from `row.shortcut` on the trailing
  edge as `KeyStroke.displayDescription` glyphs, when present.
- Unavailable commands: dimmed treatment (mirroring
  `CommandSequenceHintView`'s secondary style and reduced opacity) with the
  registry-owned reason inline in tertiary text.
- Selection: prominent background highlight. Hover: a lighter highlight that
  never moves keyboard selection.

Reduce Motion and increased contrast are respected by using system materials,
semantic colors, and default (or no) transitions — no custom animation
machinery.

## 6. Interaction

Selection is tracked by `CommandID`, not index. On every query change the
selection resets: first matching row for a nonempty query, no selection for
an empty query.

| Input | Behavior |
|---|---|
| Typing | Filters the list; selection resets per the rule above |
| Down / `Control-N` | Next row, wrapping at the end; with no selection, selects the first row |
| Up / `Control-P` | Previous row, wrapping at the start; with no selection, selects the last row |
| Return, available row selected | Dismiss, then execute (below) |
| Return, unavailable row selected | Keep the palette open; flash the reason via the router (below) |
| Return, no selection | Dismiss without executing |
| Escape | Dismiss |
| Click on an available row | Same as selecting it and pressing Return: dismiss, then execute |
| Click on an unavailable row | Selects it and flashes its reason; stays open |
| Pointer hover | Visual highlight only |
| Click outside the panel | Dismiss; the click is consumed |
| Window resigns key / app deactivates | Dismiss (query is not preserved) |
| `Shift-Cmd-P` (the resolved opener binding) | Dismisses via the menu key equivalent (§7) |

Keys are handled in SwiftUI on the focused query field: `.onSubmit` for
Return, `.onKeyPress` for Up/Down/Escape and `Control-P`/`Control-N`. The
control-modified presses must move selection without inserting control
characters into the query; the palette root also carries `.onExitCommand` as
an Escape backstop if focus ever leaves the field while the palette is open.

### Execution

For an available command:

1. Set `isCommandPalettePresented = false` (dismissal first).
2. Call `CommandRegistry.execute(id, context:)` synchronously in the same
   MainActor turn.

One code path serves Return and click, so a command executes exactly once per
activation. Dismissing first means a command that presents a sheet or moves
focus never fights the open palette.

For an unavailable command, the palette stays open and the reason surfaces
through the existing feedback path. `WindowKeyboardRouter` exposes its
private flash as one small public method:

```swift
func showUnavailable(reason: String)   // wraps the existing showFlash(_:)
```

The palette calls it with the row's availability reason;
`CommandFeedbackOverlay` renders it exactly as it renders an unavailable
binding fired from the keyboard.

## 7. Input Routing and Focus

### Router suspension

The `NSEvent` local monitor sees every key-down in the key window before the
responder chain, so without a check the router would keep executing
registered bindings (and swallowing them) while the palette is open. The
suspension lives on the router so it is unit-testable without `NSEvent`:

```swift
// WindowKeyboardRouter
@ObservationIgnored var isSuspended: @MainActor () -> Bool = { false }

func handle(_ stroke: KeyStroke, isRepeat: Bool) -> Bool {
    guard !isSuspended() else { return false }   // forward untouched
    ...
}
```

`KeyboardRoutingContainer` wires `router.isSuspended = {
windowState.isCommandPalettePresented }` and cancels any pending Command
Sequence when the palette presents (reachable only via the menu item while a
sequence is pending, but cheap to make deterministic):

```swift
.onChange(of: windowState.isCommandPalettePresented) { _, presented in
    if presented { router.cancel() }
}
```

Consequences while the palette is open, all deliberate:

- Unmodified typing, arrows, Return, and Escape forward through the monitor
  to the responder chain and land in the focused query field.
- Registered bindings are not routed; native menu key equivalents still
  apply, which is standard macOS text-field behavior and matches Ghostty.
  In particular the opener's own menu equivalent fires, executes
  `toggleCommandPalette`, and closes the palette — the toggle needs no
  second dispatch path. A binding like `Cmd-N` may likewise fire through its
  menu item; if it presents a sheet, the window resigns key and the palette
  dismisses cleanly.
- Nothing reaches the terminal: the terminal view is not first responder
  while the palette owns focus, and the responder handoff below closes the
  presentation race.

### Focus transfer and restoration

`CommandPaletteView` hosts a minimal `NSViewRepresentable` window accessor
(no generalized focus coordinator):

- **On attach to the window**: capture `window.firstResponder` weakly, then
  immediately `window.makeFirstResponder(nil)` so no keystroke can reach the
  terminal in the gap before SwiftUI focuses the query field; then request
  field focus via `@FocusState`.
- **On dismissal**: if the captured responder is still valid (same window,
  still in the view hierarchy, accepts first responder), restore it;
  otherwise fall back to `windowState.requestTerminalFocus()` — the existing
  seam sheets already use, a no-op when no Terminal Session is selected. If
  teardown ordering proves to steal focus back after restoration (verified in
  build step 5), defer the restoration by one runloop turn rather than adding
  machinery.

### Deactivation dismissal

`KeyboardMonitorHost.Coordinator` already observes
`NSWindow.didResignKeyNotification` (host window) and
`NSApplication.didResignActiveNotification` to cancel pending sequences. It
gains one callback, `var onDeactivate: (() -> Void)?`, invoked from both
handlers; `KeyboardRoutingContainer` sets it to clear
`isCommandPalettePresented`. One observation site keeps sheet presentation,
window switching, and app switching all dismissing the palette through the
same path.

### Invariants

- Palette closed: routing, Command Sequences, and terminal input behave
  exactly as today; the suspension check is one closure call on the
  hot path.
- Palette open: no palette keystroke — query text, navigation, Return,
  Escape — is ever delivered to the terminal.
- A command activated from the palette executes exactly once, after
  dismissal.

## 8. Accessibility

- The panel is an accessibility container named `Command Palette` with
  `.isModal` so assistive technology stays within it while open.
- The query field has an accessible label (`Command query`) in addition to
  its placeholder.
- Each row's accessibility label combines the full title (highlighting is
  visual-only), availability (`Unavailable — <reason>` when applicable), and
  the shortcut via a new `KeyStroke.spokenDescription` — e.g.
  `Shift Command P`, modifiers in `⌃⌥⇧⌘` order, then the uppercased key —
  so VoiceOver never reads bare glyphs.
- Rows are reachable and activatable by keyboard and pointer; selection state
  is exposed with `.isSelected`.
- Reduce Motion and increased contrast per §5 (system materials and semantic
  colors, no custom animation).

## 9. Test Plan

All in `macos/ATCTests` (Swift Testing), reusing the `CommandRegistryTests` /
`WorkspaceFlowTests` fixture pattern. Projection, matcher, and router logic
are pure enough to run without a window server.

**CommandRegistryTests (extended)** — enumeration covers every `CommandID`
with matching ids; ids unique; titles nonempty; enumeration order equals
declaration order; category assignments match §2's table; palette
eligibility excludes exactly `toggleCommandPalette`; opener perform toggles
the Boolean both directions; opener availability is unavailable with the §2
reason while each sheet driver is active and available otherwise.

**QueryMatcherTests** — empty and whitespace-only queries match with no
ranges; case-insensitive substring with correct range (`sid` →
`Toggle Sidebar`); word-initial hits on representative titles (`np` →
`New Project…`, `nt` → `New Terminal`, `ts` → `Toggle Sidebar`, `rc` →
`Reload Configuration`); punctuation never forms a word; substring wins over
word-initial when both apply; nonmatches return `nil`; returned ranges
identify the exact matched characters.

**CommandPaletteContentTests** — ineligible commands never appear;
alphabetical order with identifier tie-breaking is stable for empty and
filtered queries; nonmatches are excluded; `shortcut` reflects
`menuShortcuts` (rebinds and unbinds change it; a keymap where a command is
bound only via `leader>x` yields `nil`); availability rows match fixtures
configured available and unavailable.

**KeymapResolutionTests (extended)** — compiled defaults resolve
`cmd+shift+p` → `toggleCommandPalette` in both the routing tree and
`menuShortcuts`; the binding can be rebound and unbound by user config.

**KeyboardRouterTests (extended)** — with `isSuspended` returning true, a
registered direct binding is forwarded (returns `false`) and executes
nothing; the same stroke routes again once unsuspended; `showUnavailable`
sets and auto-clears the flash like an unavailable binding.

**CommandPaletteHostingSmokeTest** — hosts `CommandPaletteView` with preview
fixtures (pattern of `ShellHostingSmokeTest`) to catch layout and environment
crashes.

**Manual checks** (build step 5 exit list):

- Open with `Shift-Cmd-P` while the embedded Ghostty terminal has focus; the
  query field takes focus and no keystroke reaches the terminal, including
  fast typing immediately after opening.
- View menu shows `Toggle Command Palette` with the resolved shortcut; the
  item opens and closes the palette; a second `Shift-Cmd-P` closes it.
- Dismiss with Escape, outside click, app deactivation, and Return with no
  selection; the outside click activates nothing behind the palette; the
  query is cleared on the next opening.
- Execute via Return and via click; commands run exactly once, after
  dismissal.
- Unavailable commands render dimmed with their reason, and activating one
  keeps the palette open and flashes the reason.
- Dismissal restores terminal focus when the terminal was previously focused,
  and restores a non-terminal responder (e.g. the sidebar) when that was
  focused.
- Remapping or unbinding `cmd+shift+p` and reloading configuration updates
  both opening behavior and the open palette's trailing shortcuts.
- The opener menu item is disabled while a creation sheet is open.
- VoiceOver reads container, query field, and rows with spoken shortcuts;
  verify increased contrast and Reduce Motion.

## 10. Build Order

Each step lands green (`mise run macos:test`) with a `jj` checkpoint.

1. **Catalog metadata** — `CommandCategory`, `isPaletteEligible`,
   `allDescriptors`, extended registry tests. Pure metadata; no behavior
   change.
2. **Matcher** — `QueryMatcher` with its full test suite.
3. **Palette view** — `CommandPaletteContent` projection and
   `CommandPaletteView` with local state, plus content tests and the hosting
   smoke test. Not yet mounted.
4. **Command and binding** — `toggleCommandPalette` id + descriptor,
   `WindowState.isCommandPalettePresented` and `isSheetPresented`, the
   `cmd+shift+p` compiled default, the View-menu item, router
   `isSuspended`/`showUnavailable`, keymap and router test extensions.
5. **Integration** — mount the overlay in `KeyboardRoutingContainer`, wire
   suspension, pending-sequence cancellation, focus capture/transfer/restore,
   and deactivation dismissal; run the manual checklist.

## Resolved Decisions

Differences from and clarifications to the brief, recorded per its
instruction; the brief has been updated to match.

1. **Command id `view.toggle-command-palette`, not
   `navigation.show-command-palette`.** Raw values are permanent config API.
   Every existing id lives in an established namespace (`view.`, `session.`,
   `terminal.`, `project.`, `workspace.`, `data.`, `configuration.`), and the
   palette is a view-layer presentation command exactly like
   `view.toggle-sidebar`; minting a one-command `navigation.` namespace adds
   a second spelling convention for no benefit. `toggle` (not `show`)
   because the command genuinely toggles (decision 3), matching both its
   sibling `view.toggle-sidebar` and Ghostty's `toggle_command_palette`.
2. **A View-menu item is added.** The brief was silent on menus. Every other
   registered command has a menu home; the item costs one `commandButton`
   line, makes the palette discoverable and its shortcut visible, and — with
   the router suspended while the palette is open — its key equivalent is
   what lets `Shift-Cmd-P` close the palette without a second dispatch path.
3. **The opener toggles.** Pressing `Shift-Cmd-P` with the palette open
   closes it instead of doing nothing. It remains palette-ineligible.
4. **Clicking an available row executes it.** The brief's interaction list
   said clicking only selects, but its own manual checks required "execute
   with … pointer selection", and click-to-execute is the universal palette
   convention (Ghostty, VS Code). Clicking an unavailable row selects it and
   shows its reason.
5. **Router suspension is a router-level check**, not monitor logic, so the
   forward-while-open guarantee is unit-testable; menu key equivalents
   staying live while the palette is open is documented, deliberate, and
   native behavior.
6. **The opener is unavailable while a sheet is presented**, because menu
   equivalents fire even when a sheet is the key window and a palette opened
   under a sheet could neither focus nor dismiss.
7. **Small presentation additions**: a `No matching commands` empty-state
   label, and matcher words defined as maximal alphanumeric runs so
   punctuation (`…`, `&`) never breaks word-initial matching.
