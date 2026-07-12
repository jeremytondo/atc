# Configurable Keyboard Shortcuts Implementation Spec

Status: Draft v1

Scope: the command registry, configurable keybindings, per-window keyboard
routing, leader sequences, and menu synchronization described in
[keyboard-shortcuts-plan.md](keyboard-shortcuts-plan.md). All work lands in
`macos/`; no server, ATCKit, or GhosttyTerminal package change is required.
The packaged `TerminalSurfaceView` is consumed as-is — routing happens above
it, never inside it.

Related: [keyboard-shortcuts-plan.md](keyboard-shortcuts-plan.md) (the brief
this spec implements, including the terminal-safe routing findings),
[workspaces-phase2-spec.md](workspaces-phase2-spec.md) §8 (the current
`AppCommands` this spec replaces), `macos/CONTEXT.md` (terminology — the
user-visible name for a leader sequence is **Command Sequence**; "leader"
remains the config-file token, matching the brief).

Throughout, a **trigger** is a parsed key description (`cmd+b`), a
**sequence** is one or more triggers separated by `>`, a **direct binding**
is a one-step sequence, and the **resolved keymap** is the immutable product
of layering user configuration over compiled defaults.

## 1. Module Layout

New top-level folder `macos/ATC/Commands/` (application infrastructure, like
`TerminalBridge/`, not a feature). The Xcode project uses file-system
synchronized groups, so files are picked up without project edits.

```text
macos/ATC/Commands/
├── CommandID.swift            — stable identifiers
├── CommandRegistry.swift      — descriptors: title, menu, availability, execution
├── KeyStroke.swift            — KeyStroke, Modifiers, KeySequence, trigger parsing
├── Keymap.swift               — ordered bindings → validated prefix tree + menu shortcuts
├── KeyboardConfig.swift       — config.toml subset parser, schema, diagnostics
├── KeyboardConfigStore.swift  — load/reload pipeline, publishes ResolvedKeymap
├── WindowKeyboardRouter.swift — per-window state machine (pending, timeout, flash)
├── KeyboardMonitorHost.swift  — NSViewRepresentable owning the NSEvent monitor
└── CommandFeedbackViews.swift — Command Sequence hint, no-match flash, config notice
```

Changed files: `AppCommands.swift` (registry-driven menus),
`ATCApp.swift` (store creation and injection), `RootView.swift` (monitor
host + overlays). `WindowState.swift` keeps its navigation role unchanged;
availability logic stays there and the registry calls it.

`GhosttyTerminal` imports remain confined to `TerminalBridge/` and
`Features/Terminal/`. Nothing in `Commands/` imports it — the router sits at
the `NSEvent` layer and never talks to the terminal view.

## 2. Command Model

### CommandID

```swift
enum CommandID: String, CaseIterable, Sendable {
    case toggleSidebar = "view.toggle-sidebar"
    case newSession = "session.new"
    case newTerminal = "terminal.new"
    case newProject = "project.new"
    case newWorkspace = "workspace.new"
    case refresh = "data.refresh"
    case reloadConfiguration = "configuration.reload"
}
```

Raw values are the config-file identifiers and are permanent API; renaming a
case never changes its raw value.

### Descriptors and the registry

```swift
@MainActor
struct CommandContext {
    let appModel: AppModel
    let windowState: WindowState
    let configStore: KeyboardConfigStore
}

enum CommandAvailability: Equatable {
    case available
    case unavailable(reason: String)   // short, reused by menus and hint dimming
}

@MainActor
struct CommandDescriptor {
    let id: CommandID
    let title: String                       // menu item and hint label
    let availability: (CommandContext) -> CommandAvailability
    let perform: (CommandContext) -> Void
}
```

`CommandRegistry` is a `@MainActor` value with a fixed descriptor table and
one execution entry point used by every surface (menus, router, future
palette):

```swift
@MainActor
enum CommandRegistry {
    static func descriptor(for id: CommandID) -> CommandDescriptor
    /// Checks availability, executes when available, and returns the
    /// outcome so the caller can surface unavailable feedback.
    @discardableResult
    static func execute(_ id: CommandID, context: CommandContext) -> CommandAvailability
}
```

Behavior per command, lifted verbatim from today's `AppCommands` closures so
this refactor changes no behavior:

| Command | Perform | Availability (reason when unavailable) |
|---|---|---|
| `view.toggle-sidebar` | `windowState.toggleSidebar()` | `route == .workspace` — "Requires an open Workspace" |
| `session.new` | `windowState.startSessionKind = .agentSession` | `windowState.canStartSession(in:)` — "Requires an open Workspace on a reachable Connection" |
| `terminal.new` | `windowState.startSessionKind = .terminal` | same as `session.new` |
| `workspace.new` | `windowState.presentCreateWorkspace(in:)` | `!appModel.runtimes.isEmpty` — "Requires a configured Connection" |
| `project.new` | `windowState.isCreateProjectPresented = true` | same as `workspace.new` |
| `data.refresh` | `Task { await appModel.refreshAll() }` | always available |
| `configuration.reload` | `configStore.reload()` | always available |

## 3. Triggers, Sequences, and the Keymap

### KeyStroke

```swift
struct KeyStroke: Hashable, Sendable, CustomStringConvertible {
    struct Modifiers: OptionSet, Hashable, Sendable {
        static let command, control, option, shift: Modifiers
    }
    /// Normalized key: a single lowercase character ("b", "1", ","), or the
    /// named key "escape". Stored as String so named keys can grow post-MVP.
    let key: String
    let modifiers: Modifiers
}

typealias KeySequence = [KeyStroke]   // MVP: length 1, or 2 with leader first
```

### Trigger parsing

`KeyStroke.parse(_ text: String) -> Result<KeyStroke, TriggerError>` handles
one `+`-separated trigger; `KeySequence` parsing splits on `>` first.
Following Ghostty's syntax:

- Modifier aliases: `cmd`/`command`/`super`, `ctrl`/`control`,
  `opt`/`option`/`alt`, `shift`. Case-insensitive; whitespace around tokens
  is trimmed.
- The final token is the key: exactly one printable character, stored
  lowercase. Named keys, function keys, and bare/unmodified direct triggers
  are rejected with a "deferred beyond the MVP" diagnostic. (`escape` exists
  as a `KeyStroke` for the router's internal cancel check but is not
  accepted from configuration.)
- Duplicate modifiers, an empty key, multi-character keys, and unknown
  tokens are errors naming the offending token.
- The symbolic token `leader` is only meaningful to the resolver (§5); the
  parser treats it as a reserved first-step token of a sequence.

### Sequence rules (MVP)

- A direct binding is a one-step sequence.
- The only multi-step form is `leader>X` — two steps, first step literally
  `leader`. Any other `>` usage (three steps, `cmd+k>b` spelled explicitly,
  `x>leader`) is a validation error. This keeps the config surface exactly
  as documented in the brief while the prefix tree below stays general.
- A continuation (`X` in `leader>X`) may be unmodified or modified; a direct
  binding and the leader itself must include at least one of `cmd`, `ctrl`,
  or `option` (`shift` alone does not qualify).

### ResolvedKeymap

```swift
struct ResolvedKeymap: Sendable {
    enum Node: Sendable {
        case command(CommandID)
        case prefix([KeyStroke: Node])
    }
    let root: [KeyStroke: Node]
    /// Eligible direct binding shown next to each menu item (§7).
    let menuShortcuts: [CommandID: KeyStroke]
    let leaderTimeout: Duration
    /// Monotonic token; routers cancel pending sequences when it changes.
    let generation: Int
}
```

Direct shortcuts and sequences share one lookup path: a direct binding is a
root entry whose node is `.command`; the leader is a root entry whose node
is `.prefix`. The router does a single dictionary lookup per event — the
idle forward path (every ordinary terminal keystroke) is one hash lookup
with no allocation.

## 4. Configuration File

### Location and schema

`$XDG_CONFIG_HOME/atc/config.toml`, falling back to
`~/.config/atc/config.toml` when `XDG_CONFIG_HOME` is unset or empty. No
other locations are searched. A missing file is not an error: compiled
defaults apply silently.

```toml
[keyboard]
leader = "cmd+k"                  # direct trigger; must satisfy the modifier rule
leader_timeout_ms = 1800          # positive integer
clear_default_keybindings = false # boolean

[keybindings]
"cmd+b" = "view.toggle-sidebar"   # trigger or sequence = command id or "unbind"
"leader>b" = "view.toggle-sidebar"
```

Unknown keys inside `[keyboard]` produce a warning diagnostic and are
ignored. Unknown top-level tables are ignored **without** diagnostics —
`config.toml` is atc's general config file and other subsystems will add
tables later.

### Parser: a deliberate TOML subset

`KeyboardConfig.swift` contains a small hand-written parser rather than a
third-party TOML dependency, for three load-bearing reasons:

1. **Source order is semantic.** "Most recently configured" menu-shortcut
   selection (§7) requires `[keybindings]` entries in file order; general
   TOML libraries return unordered (or key-sorted) tables.
2. **Diagnostics need line numbers.** Every error must point at its line.
3. **The need is tiny.** Two tables of scalar values, in a project whose
   only external dependency is libghostty. This follows the
   `GhosttyConfigLoader` precedent, but strict where that loader is
   best-effort: config the user wrote must fail loudly, not silently.

Accepted grammar (a strict subset of TOML 1.0 — every accepted file is
valid TOML with identical meaning):

- UTF-8; blank lines; `#` comments (full-line and trailing).
- `[table]` headers with bare names.
- `key = value` where key is bare (`A-Za-z0-9_-`) or a basic quoted string
  with `\"' `\\` `\n` `\t` `\uXXXX` escapes.
- Values: basic quoted string (same escapes), integer, `true`/`false`.

Everything else — arrays, inline tables, dotted keys, literal/multiline
strings, floats, dates — is a parse **error** naming the line and
construct, not a silent skip. Duplicate keys within a table produce a
warning; the later entry wins (Ghostty-style replacement semantics applied
to one file).

Parser output preserves order:

```swift
struct ParsedConfig {
    struct Entry { let key: String; let value: Value; let line: Int }
    let tables: [String: [Entry]]     // entries in file order
    let diagnostics: [ConfigDiagnostic]
}

struct ConfigDiagnostic: Equatable, CustomStringConvertible {
    enum Severity { case error, warning }
    let severity: Severity
    let line: Int?
    let message: String    // actionable: names the trigger/command/construct
}
```

## 5. Resolution and Validation

`Keymap.resolve(defaults:user:) -> Result<ResolvedKeymap, [ConfigDiagnostic]>`
is a pure function — the whole pipeline below is unit-testable without
files, AppKit, or the store.

### Compiled defaults

Defaults use the same ordered trigger→command representation as user
entries:

```swift
static let compiledDefaults: [(sequence: String, command: CommandID)] = [
    ("cmd+b", .toggleSidebar), ("leader>b", .toggleSidebar),
    ("cmd+n", .newSession),    ("leader>n", .newSession),
    ("cmd+r", .refresh),       ("leader>r", .refresh),
    ("cmd+t", .newTerminal),         // kept — Resolved Decision 1
    ("cmd+shift+n", .newWorkspace),  // kept — Resolved Decision 1
]
```

Default leader `cmd+k`, default timeout 1800 ms,
`clear_default_keybindings = false`.

### Pipeline (both launch and reload)

1. Parse `[keyboard]`: validate `leader` as a direct trigger meeting the
   modifier rule; `leader_timeout_ms` must be a positive integer (zero,
   negative, or non-integer values are errors; missing uses 1800);
   `clear_default_keybindings` must be a boolean.
2. Build the ordered entry list: compiled defaults first (skipped entirely
   when `clear_default_keybindings = true`), then user `[keybindings]`
   entries in file order. User entries are therefore always "more recently
   configured" than defaults.
3. Parse each entry's sequence and value. The value must be a known
   `CommandID` raw value or `"unbind"`; anything else is an error naming
   the string and its line.
4. Expand `leader` to the configured leader trigger in every sequence.
5. Fold the list in order into a map keyed by full expanded sequence: an
   entry replaces any earlier entry with the same sequence; `"unbind"`
   removes it. Later entries win.
6. Validate the folded map:
   - **Modifier rule**: every step-one trigger includes `cmd`, `ctrl`, or
     `option`. Continuations are exempt.
   - **Direct/prefix conflict**: no trigger may be both a `.command` and
     the first step of a sequence. This is checked after leader expansion,
     so `leader = "cmd+b"` colliding with the default `cmd+b` direct
     binding is caught here; the diagnostic names the trigger and says to
     unbind the direct shortcut or choose another leader.
   - **Protected shortcuts**: no direct binding or leader may equal an
     entry in `ProtectedTriggers`, the fixed non-registry macOS shortcuts
     atc does not replace: `cmd+q`, `cmd+h`, `cmd+opt+h`, `cmd+,` (app
     menu); `cmd+w`, `cmd+shift+w` (window close); `cmd+z`, `cmd+shift+z`,
     `cmd+x`, `cmd+c`, `cmd+v`, `cmd+a` (editing — also the terminal's
     native copy/paste); `cmd+m` (minimize). The diagnostic names the
     protected command ("cmd+c is reserved for Copy").
7. Any error diagnostic invalidates the **entire** candidate; warnings do
   not. On failure the caller keeps the previous keymap (§6).
8. On success, build the prefix tree and select menu shortcuts (§7), bump
   `generation`, and return the immutable `ResolvedKeymap`.

The leader reserves nothing by itself: the root only gains a `.prefix` node
when at least one `leader>X` sequence survives resolution. With none, the
leader trigger is absent from the tree and that key forwards normally.

## 6. KeyboardConfigStore

```swift
@MainActor @Observable
final class KeyboardConfigStore {
    private(set) var keymap: ResolvedKeymap        // never nil; starts as compiled defaults
    private(set) var notice: ConfigNotice?         // one dismissible banner, not one per diagnostic

    func loadAtLaunch()
    func reload()                                  // configuration.reload
    func dismissNotice()
}

struct ConfigNotice: Equatable {
    let message: String    // e.g. "config.toml was not loaded (3 errors) — keeping the previous keybindings. See log for details."
}
```

Created once in `ATCApp` alongside `AppModel`, injected via
`.environment`. Load and reload are the same atomic pipeline:

1. Read the file. Missing file → resolve defaults-only, clear any notice.
2. Parse and resolve (§4–5).
3. **Success**: atomically replace `keymap` (routers observe `generation`
   and cancel any pending sequence, §8; menus re-derive shortcuts from the
   new map, §7), clear the notice, and log warnings if any.
4. **Failure**: keep the current `keymap` untouched — at launch that is
   compiled defaults, on reload the last valid map. Log every diagnostic
   with the config path via `Logger(subsystem: "ElevenIdeas.atc",
   category: "keyboard")`, and set exactly one `notice` stating whether atc
   retained the previous keymap or is running compiled defaults.

Invalid configuration can never prevent launch or leave the app without a
working keymap. No file watching; reload is explicit (menu command) or at
launch.

## 7. Menus and Shortcut Synchronization

`AppCommands` stays a SwiftUI `Commands` struct but becomes a thin
projection of the registry — each item invokes `CommandRegistry.execute`
and owns no behavior:

- **File** (`CommandGroup(replacing: .newItem)`): New Project…, New
  Workspace…, New Session, New Terminal.
- **View** (`CommandGroup(after: .sidebar)`): Toggle Sidebar, Refresh.
- **atc application menu** (`CommandGroup(after: .appSettings)`): Reload
  Configuration, adjacent to Settings.

Each item reads its title and availability from its descriptor
(`.disabled` when unavailable — commands stay visible) and its shortcut
from `keymap.menuShortcuts[id]`, converted via a
`KeyStroke → (KeyEquivalent, EventModifiers)` helper. A command with no
eligible binding renders without a shortcut.

Menu shortcut selection happens during resolution (§5 step 8): for each
command, the **last** direct binding in fold order that maps to it — user
entries are later than defaults, later file lines are later than earlier
ones. Unbinding that trigger falls back to the next-latest remaining direct
binding. Leader sequences are never eligible.

Menu key equivalents remain for display and pointer-free discoverability,
but they are not the routing path: while an atc window is key, the router
consumes every registered binding before menu dispatch (§8), and the
exactly-once guarantee holds because a consumed event never reaches
`performKeyEquivalent`. When the router is not in the path — e.g. a sheet
is the key window — the menu equivalent may fire and executes the same
registry path with the same availability check, so behavior stays
consistent either way.

Since `AppCommands` already receives the observable store, macOS 14+
Observation re-renders menu items when `keymap` is replaced. If menu
shortcut labels prove stale on some OS build, the fallback is re-keying the
commands on `keymap.generation`; the spec assumes plain observation first.

## 8. Per-Window Keyboard Router

### State machine (`WindowKeyboardRouter`)

`@MainActor @Observable final class`, one per window, holding the pieces
the brief scopes to the window rather than the app domain:

```swift
@MainActor @Observable
final class WindowKeyboardRouter {
    enum State { case idle, pending(node: [KeyStroke: ResolvedKeymap.Node]) }
    private(set) var state: State = .idle       // hint UI renders from this
    private(set) var flash: RouterFlash?        // no-match / unavailable feedback

    var keymap: ResolvedKeymap { didSet /* generation change → cancel(silently:) */ }

    /// The single entry point. Returns true when the event must be
    /// consumed (never delivered to the responder chain / terminal).
    func handle(_ stroke: KeyStroke, isRepeat: Bool) -> Bool
    func cancel()                               // Esc, focus loss, reload
}

struct RouterFlash: Equatable {
    let message: String                         // "No matching command" or the availability reason
}
```

Decision table for `handle`, implementing the brief's routing rules
exactly:

| State | Event | Action | Consumed |
|---|---|---|---|
| idle | stroke in root → `.command` | execute via registry; on `.unavailable`, flash the reason instead | yes |
| idle | stroke in root → `.prefix` | enter `.pending`, start timeout | yes |
| idle | anything else (incl. `escape`) | — | **no** (forward unchanged) |
| pending | `escape` | cancel silently | yes |
| pending | stroke in node → `.command` | end pending; execute or flash unavailable reason | yes |
| pending | stroke not in node | end pending; flash "No matching command" | yes (never leaks to the terminal — deliberate deviation from Ghostty's flush) |
| pending | timeout fires | cancel silently, forward nothing | n/a |
| pending | window/app resigns key/active | cancel silently | n/a |

Details:

- Continuations are looked up **only** in the pending node — a modified
  continuation that misses is an unknown continuation, never retried
  against the root.
- `isRepeat` events: consumed whenever the non-repeat event would be, but
  never execute a command a second time (holding `cmd+r` refreshes once)
  and never re-enter pending.
- Executing an unavailable command consumes the event, runs nothing, and
  flashes the descriptor's reason — the keystroke is never sent to the
  terminal.
- Timeout: `keymap.leaderTimeout` via a stored `Task` that calls an
  internal `handleTimeout(generation:)`; entering idle for any reason
  cancels the task, and a stale generation token makes a late firing a
  no-op. Tests call `handleTimeout` directly rather than sleeping.
- The flash auto-clears after ~800 ms through the same task pattern.
- Setting `keymap` (reload) cancels any pending sequence before the new
  tree takes effect.

The router is pure Swift over `KeyStroke` — no `NSEvent`, no terminal
types — so the whole table above is unit-testable synchronously.

### NSEvent monitor (`KeyboardMonitorHost`)

An `NSViewRepresentable` placed in `RootView`'s `.background()`. Its
coordinator owns the monitor and the router:

- On `viewDidMoveToWindow`, install
  `NSEvent.addLocalMonitorForEvents(matching: .keyDown)`. The handler:
  1. `guard event.window === hostWindow, hostWindow.isKeyWindow else
     return event` — the monitor is app-global by API, per-window by this
     filter. Sheets and Settings become the key window themselves, so their
     events forward untouched.
  2. Normalize to `KeyStroke` (below); unmappable events forward.
  3. `router.handle(stroke, isRepeat: event.isARepeat)` → `nil` when
     consumed, else the event.
- Remove the monitor on `dismantleNSView` and in coordinator `deinit`;
  re-install if the view moves to a new window.
- Observe `NSWindow.didResignKeyNotification` (host window) and
  `NSApplication.didResignActiveNotification` → `router.cancel()`.

Local monitors observe events before `NSApp.sendEvent` dispatches them —
ahead of `performKeyEquivalent`, the responder chain, and therefore ahead
of the Ghostty view. Returning `nil` is the consumption mechanism: a
recognized binding executes exactly once even when Ghostty defines the same
shortcut, and nothing about the packaged terminal view is subclassed or
modified. No Accessibility permission is involved; the monitor sees only
atc's own events.

**Normalization**: `event.characters(byApplyingModifiers: []) ?? event.charactersIgnoringModifiers`,
lowercased — yielding the layout-resolved, unshifted character so `opt+b`
matches `"b"` (not `"∫"`) and `cmd+shift+1` matches key `"1"`. Key code 53
maps to `"escape"`. Modifier flags map to `Modifiers` from
`.command/.control/.option/.shift`. Events with no usable character
(function keys, media keys, dead-key states) return `nil` and forward.

### Terminal safety invariants

- While idle, the only work per keystroke is one dictionary lookup; a miss
  forwards the original event object unchanged.
- Leader activation, continuations, cancellation, unmatched continuations,
  and unavailable-command keystrokes are never delivered to the responder
  chain, so they can never reach the terminal's WebSocket.
- Outside a pending sequence, `escape` and every other ordinary key behave
  exactly as today.

## 9. Command Sequence Hint and Feedback UI

Rendered by `RootView` as overlays above the existing `ZStack`, driven by
the router (injected via environment) and the config store. Per
`CONTEXT.md`, user-visible copy says **Command Sequence** — never "leader".

- **Hint** (`CommandSequenceHintView`): while `state == .pending`, a
  compact bottom-center HUD (thin material capsule) generated from the
  pending node: one row per continuation, sorted by key, showing a keycap
  glyph and the command title. Unavailable continuations stay listed but
  dimmed, annotated with the descriptor's short reason — availability is
  evaluated live against `CommandContext` at render time. Non-modal: it
  overlays without intercepting clicks (`allowsHitTesting(false)`).
- **Flash**: `router.flash` renders in the same HUD position for ~800 ms —
  "No matching command" after an unknown continuation, or the availability
  reason after an unavailable binding fires.
- **Config notice** (`ConfigNoticeView`): `configStore.notice` renders as
  one dismissible top-edge banner ("Keyboard configuration was not loaded —
  keeping previous keybindings. See log for details." / "…using default
  keybindings."). Never a blocking alert, never one banner per diagnostic.

All three are new lightweight SwiftUI views in
`CommandFeedbackViews.swift`; no existing surface changes.

## 10. Removed and Replaced Code

- `AppCommands.swift`: all seven hard-coded button closures and their
  static `.keyboardShortcut` literals are replaced by registry projection
  (§7). The behavior table in §2 is the contract that nothing user-visible
  changes in the refactor step.
- No other code is removed. `WindowState.canStartSession(in:)` and
  `presentCreateWorkspace(in:)` are reused as-is by descriptors.

## 11. Test Plan

All in `macos/ATCTests` using Swift Testing (`@Suite`/`@Test`), following
the `WorkspaceFlowTests` fixture pattern (`MockATCClient`, suite-scoped
`UserDefaults`). The parser, resolver, and router are pure, so everything
below runs without a window server.

**TriggerParsingTests** — modifier aliases and case-insensitivity; shift
normalization; multi-modifier triggers; sequence splitting on `>`; the
`leader` token only as step one; rejects: no-modifier direct trigger,
shift-only, unknown tokens, multi-character keys, named/function keys,
3-step sequences, explicit non-leader sequences.

**KeyboardConfigParserTests** — happy-path `[keyboard]` + `[keybindings]`;
**entry order preservation**; quoted and bare keys; escapes; comments;
duplicate-key warning with later-wins; line-numbered errors for arrays,
inline tables, dotted keys, floats, literal strings; unknown `[keyboard]`
key → warning; unknown table → silently ignored; empty and missing file.

**KeymapResolutionTests** — defaults-only resolution matches §5's compiled
table; user replacement of a default trigger; `"unbind"`; unbind-then-rebind
later in the file; `clear_default_keybindings = true` (leader prefix absent
when no sequences remain); leader expansion with a custom leader;
direct/prefix conflict, including the leader-expansion-induced case, with a
diagnostic naming the trigger; protected-shortcut rejection naming the
protected command; unknown command id; `leader_timeout_ms` zero/negative/
non-integer invalidate, missing defaults to 1800; menu-shortcut selection:
later-in-file wins over earlier and over defaults, fallback after unbinding
the winner, command with only leader bindings gets none; multiple triggers
onto one command; any error invalidates the whole candidate.

**KeyboardRouterTests** — direct hit executes once and consumes; unrelated
stroke and idle `escape` forward; leader entry consumes and pends;
continuation executes and consumes; modified continuation not retried at
root; unknown continuation consumed + "No matching command" flash and
returns to idle; pending `escape` cancels silently; `handleTimeout` with
current generation cancels, with stale generation no-ops; focus-loss cancel;
unavailable command consumed + reason flash, terminal receives nothing;
`isRepeat` consumed but executes once; keymap replacement cancels pending;
leader key with zero sequences forwards (no `.prefix` at root).

**CommandRegistryTests** — availability truth table per §2 against
configured `AppModel`/`WindowState` fixtures (reusing `WorkspaceFlowTests`
setup); `execute` returns the reason and performs nothing when unavailable;
each perform mutates the same state today's menu closures do.

**KeyboardConfigStoreTests** — launch with missing/valid/invalid file;
reload success swaps keymap, bumps generation, clears notice; reload
failure retains previous keymap and sets exactly one notice; diagnostics
logged (asserted via the diagnostics array, not log scraping).

**Event normalization** (small, real `NSEvent.keyEvent(...)` fixtures) —
`cmd+b`, `cmd+shift+1` → key `"1"`, `opt+b` → key `"b"`, Esc → `"escape"`,
function key → `nil`.

Manual verification (cannot be automated headlessly): both `Cmd-B` and
`Cmd-K, B` toggle the sidebar while a terminal has focus; held-key
passthrough typing in the terminal feels unchanged; menu bar shows resolved
shortcuts after a reload.

## 12. Build Order

Each step lands green (`mise run macos:test`) with a `jj` checkpoint.
Steps 1–3 are DEV-38; the brief's numbered sequence maps onto them.

1. **Registry extraction** — `CommandID`, `CommandRegistry`,
   `CommandContext`; rewrite `AppCommands` to call
   `CommandRegistry.execute` with shortcuts still hard-coded. Pure
   refactor; `CommandRegistryTests` pin the behavior table.
2. **Router with compiled bindings** — `KeyStroke`, `ResolvedKeymap` (tree
   built from a hard-coded default table; no parsing yet),
   `WindowKeyboardRouter`, `KeyboardMonitorHost` wired into `RootView`.
   All five existing direct shortcuts (`cmd+b/n/r/t`, `cmd+shift+n`) now
   work with the terminal focused.
3. **Exactly-once verification** — router and normalization test suites;
   manual pass over Ghostty-overlapping bindings and unrelated-input
   passthrough. *(End of DEV-38 — the routing seam is fixed from here.)*
4. **Configuration** — trigger parsing, TOML-subset parser, resolution
   pipeline, `KeyboardConfigStore` with launch load; menu shortcut labels
   switch from literals to `menuShortcuts`.
5. **Command Sequences** — pending state, timeout, `escape`, hint HUD,
   flashes; `leader>b/n/r` defaults become live.
6. **Reload and diagnostics** — Reload Configuration menu item, notice
   banner, store tests; sweep the brief's Success Criteria list as the
   exit checklist.

## Resolved Decisions

1. **`cmd+t` and `cmd+shift+n` stay compiled defaults.** The brief left
   `terminal.new` open and gave `workspace.new`/`project.new` no compiled
   bindings, but both shortcuts already shipped in `AppCommands`; removing
   working shortcuts is a UX regression with no offsetting benefit now that
   a user can `"cmd+t" = "unbind"` themselves. `project.new` remains
   menu-only. Deviation from the brief's MVP binding list, documented here.
2. **Hand-written TOML subset over a dependency** — order preservation and
   line-precise diagnostics are requirements no ergonomic Swift TOML
   library guarantees, and the accepted grammar is deliberately tiny (§4).
   Revisit only if `config.toml` grows constructs outside the subset.
3. **Only `leader>X` sequences are configurable.** Explicit multi-step
   triggers (`cmd+k>b`) are rejected even when equivalent, so there is one
   spelling per sequence and leader remapping can never orphan entries.
4. **Duplicate triggers warn, later wins** — replacement is already the
   layering semantic between defaults and user config; applying it within
   the file (with a warning, since TOML technically forbids duplicate
   keys) is the simplest predictable behavior.
5. **Repeats consume but execute once** — holding a bound key must not
   spam refreshes or sidebar toggles, yet must not leak repeat events to
   the terminal.
6. **Terminology**: UI copy says "Command Sequence" per `CONTEXT.md`;
   `leader` stays the config token per the brief. `CONTEXT.md`'s "waits
   for one unmodified next key" should be updated — continuations may be
   modified; they are simply not required to be.
