# Configurable Keyboard Shortcuts and Leader Key MVP Brief

This brief supersedes the earlier fixed-shortcut plan. The MVP now prioritizes configurable, terminal-safe command routing; the command palette and sidebar-local Vim navigation are deferred.

## Background

atc should be comfortable to operate from the keyboard even when an embedded terminal has focus. It needs a useful set of compiled defaults while allowing users to override or disable them in `config.toml`.

The shortcut system should follow Ghostty's general model: stable application commands, trigger-to-command bindings, defaults layered with user configuration, one resolved keymap, centralized input dispatch, and native menus sourced from the same configuration. atc will add an explicit leader-key concept for concise, discoverable command sequences.

Ghostty is the primary reference implementation for keybinding syntax, layering, sequence semantics, menu synchronization, and configuration behavior. When this brief does not specify an edge case, the MVP follows Ghostty where its behavior fits atc. Any deviation must be deliberate and documented—particularly where Ghostty can assume a focused terminal but atc must also support native app controls. When Ghostty's behavior does not transfer cleanly, atc chooses the simplest predictable behavior consistent with its terminal-safety rules.

Unlike a modal editor, atc cannot reserve ordinary unmodified keys globally because embedded terminals must receive normal input immediately. The default leader will therefore be a modified key that temporarily enters a short-lived command sequence.

The current app exposes several commands through SwiftUI menu key equivalents, but the embedded Ghostty view participates in `performKeyEquivalent` before those menu actions. When Ghostty recognizes the same binding, it can consume the event while the terminal is focused and the atc command never runs. Native menu shortcuts therefore remain useful for display and standard macOS discoverability, but they are not a reliable input-routing boundary for terminal-safe application commands.

## Objective

Establish the base command and keybinding architecture, then prove it with a small set of configurable direct shortcuts and leader sequences that work reliably while the embedded terminal has focus.

## MVP Experience

- Direct defaults:
  - `Cmd-B`: Toggle Sidebar
  - `Cmd-N`: New Session in the active Workspace
  - `Cmd-R`: Refresh Projects, Workspaces, and Sessions
- `Cmd-K` activates leader mode from normal app contexts, including when an embedded terminal has focus.
- Leader mode displays a compact, non-modal hint generated from the resolved keymap. Available continuations show their key and command title; unavailable continuations remain visible but dimmed with the same short reason used by unavailable-command feedback.
- Initial leader sequences:
  - `Cmd-K, B`: Toggle Sidebar
  - `Cmd-K, N`: New Session in the active Workspace
  - `Cmd-K, R`: Refresh Projects, Workspaces, and Sessions
- `Esc` cancels an active leader sequence.
- An unmatched continuation is consumed, briefly indicates that no command matched, and is not sent to the terminal.
- Leader mode expires silently after approximately two seconds.
- Outside leader mode, ordinary terminal input—including `Esc`—continues to work unchanged.

## Command Model

Commands have stable identifiers independent of their shortcuts and UI locations. Initial identifiers include:

- `view.toggle-sidebar`
- `session.new`
- `terminal.new`
- `project.new`
- `workspace.new`
- `data.refresh`
- `configuration.reload`

Each command has a descriptor containing its title, category, default bindings, menu placement, availability rules, and execution behavior. Menus, toolbars, direct shortcuts, leader sequences, and future command-palette entries invoke the same command path.

Command availability is evaluated against the focused window context. `session.new` opens the same Agent Action selection flow as the visible New Session control and is unavailable without an active Workspace. `terminal.new`, `workspace.new`, and `project.new` represent their corresponding visible creation actions but have no compiled bindings in this MVP. Menus and toolbar controls display unavailable commands as disabled. Invoking an unavailable command through a configured binding consumes the event, executes nothing, and shows brief non-modal feedback explaining the required context; the keystroke is never sent to the embedded terminal.

### Native Menu Placement

- **File**: New Project, New Workspace, New Session, and New Terminal.
- **View**: Toggle Sidebar and Refresh.
- **atc application menu**: Reload Configuration, near Settings.

Unavailable creation commands remain visible but disabled. Each menu item invokes its command identifier through the registry rather than owning separate behavior.

## Configuration Model

Configuration maps triggers to command identifiers, matching Ghostty's trigger-oriented model:

atc reads configuration from `$XDG_CONFIG_HOME/atc/config.toml`, falling back to `~/.config/atc/config.toml` when `XDG_CONFIG_HOME` is unset. The MVP does not search additional locations.

```toml
[keyboard]
leader = "cmd+k"
leader_timeout_ms = 1800
clear_default_keybindings = false

[keybindings]
"cmd+b" = "view.toggle-sidebar"
"leader>b" = "view.toggle-sidebar"
"cmd+n" = "session.new"
"leader>n" = "session.new"
"cmd+r" = "data.refresh"
"leader>r" = "data.refresh"
```

The symbolic `leader` token expands to the configured leader before the resolved keymap is built. The `>` character separates steps in a sequence, following Ghostty's notation.

`leader_timeout_ms` defaults to `1800` and accepts a positive integer number of milliseconds. A missing value uses the default; zero, negative, or non-integer values invalidate the candidate configuration with an actionable diagnostic.

The leader setting does not reserve a key by itself. Leader mode activates only when the resolved keymap contains at least one sequence under the expanded leader prefix; otherwise that key passes through normally and no hint is shown.

Compiled defaults use the same internal trigger-to-command representation. User configuration is layered over those defaults:

- An omitted trigger retains its compiled default.
- A user entry replaces the command assigned to the same trigger.
- Assigning `"unbind"` removes that trigger.
- `clear_default_keybindings = true` starts from an empty keymap.
- Multiple triggers may invoke the same command.
- A trigger resolves to only one command.
- A trigger cannot resolve as both a direct command and a sequence prefix. This conflict invalidates the candidate keymap, including when leader expansion introduces it; diagnostics identify the trigger and require the user to unbind the direct shortcut or choose another leader.
- Direct bindings and the configured leader must include at least one of `cmd`, `ctrl`, or `option`; `shift` alone does not qualify. Unmodified printable keys are accepted only as continuations after an active leader prefix. Bare and function-key direct bindings are deferred beyond the MVP.
- A direct binding or leader that conflicts with a fixed non-registry macOS menu shortcut is invalid. The MVP does not replace standard application, Settings, editing, window, or terminal-native menu shortcuts; diagnostics identify the protected command and conflicting trigger.
- The most recently configured eligible direct binding for a command appears as its native menu shortcut. "Most recently configured" means later in `config.toml`, so the loader preserves keybinding source order rather than collapsing entries into an unordered dictionary. Removing it falls back to another remaining eligible direct binding. Leader sequences are not eligible for menu display.
- Unknown commands, invalid triggers, and ambiguous configuration produce useful diagnostics.
- Invalid configuration must not prevent launch; atc retains the last valid keymap or safe compiled defaults.

Example targeted override:

```toml
[keybindings]
"cmd+b" = "unbind"
"cmd+shift+b" = "view.toggle-sidebar"
```

## Architecture Requirements

- A command registry owns stable command metadata, availability, and execution.
- A configuration store loads compiled defaults, applies user entries, validates them, and publishes an immutable resolved keymap.
- All bindings are represented as key sequences internally; a direct shortcut is a one-step sequence.
- The resolved keymap uses a prefix tree so direct shortcuts and shared sequence prefixes use the same lookup path.
- A per-window keyboard router resolves events before the focused terminal consumes them.
- The router forwards every unrelated event unchanged while no sequence is pending.
- Native menu shortcut labels are synchronized from the resolved keymap rather than hard-coded separately.
- Native menu key equivalents are not relied upon to route registered atc bindings while a terminal has focus.
- Toolbar buttons and future palette entries invoke command identifiers directly rather than simulating shortcuts.
- Pending leader and presentation state are scoped to the active window rather than stored in the application domain model.
- Reloading configuration atomically replaces the resolved keymap, updates menus, and cancels any pending sequence.
- The terminal integration remains behind its existing containment boundary so keyboard routing does not spread Ghostty-specific types throughout the app.

## Input Routing and Terminal Safety

The MVP uses a local `NSEvent` key-down monitor owned by each atc window because the packaged `TerminalSurfaceView` constructs its Ghostty `NSView` internally. The monitor filters events to its key window and resolves every registered atc binding before the focused responder can consume it. It intercepts recognized direct bindings, configured sequence prefixes, and every event received while a sequence is pending. It is installed when the window becomes active and removed when that window closes; it does not require subclassing or modifying the packaged terminal view.

Native menus continue to display the resolved eligible direct binding and invoke the same command identifiers, but they are not responsible for delivering those bindings. This avoids depending on responder-chain ordering or on whether Ghostty also defines a matching shortcut.

- Recognized direct shortcut: execute its command and consume the event.
- Recognized leader prefix: consume it and enter the pending state.
- Recognized continuation: execute its command and consume it.
- Continuations, including modified keys, are resolved only within the pending sequence; they are not retried against the root keymap.
- Unknown continuation: consume it, end the sequence, and show a brief no-match indication. This deliberately differs from Ghostty, which flushes an invalid sequence to the terminal; atc never leaks an activated leader sequence into terminal input.
- `Esc` while pending: consume it and cancel.
- Sequence timeout: cancel silently and forward nothing.
- App or window focus loss: cancel the sequence.
- While idle, unrelated keyboard events pass through unchanged.

Leader activation, recognized continuations, cancellation, and unmatched continuations are never sent over the terminal connection. The MVP must not require macOS Accessibility permission or monitor input outside atc.

## Implementation Sequence

DEV-38 is the first architectural slice of this brief rather than a terminal-specific shortcut workaround:

1. Centralize the actions currently exposed by `AppCommands` behind their stable command identifiers so menus and keyboard routing execute the same behavior.
2. Add the per-window keyboard router with compiled bindings and make the existing direct shortcuts work while the terminal has focus.
3. Verify that recognized atc bindings are consumed exactly once and unrelated terminal input is forwarded unchanged.
4. Add configuration layering, the resolved keymap, leader sequences, menu synchronization, reload, diagnostics, and hint presentation without replacing the routing seam established by DEV-38.

The pre-MVP app currently assigns `Cmd-T` to New Terminal even though this brief gives `terminal.new` no compiled binding. The DEV-38 slice should preserve that existing shortcut while repairing routing so it does not mix a bug fix with a default-keymap change. The configurable-keybindings MVP must then deliberately confirm whether `Cmd-T` remains a compiled default or is removed as currently specified.

## Configuration Reload and Diagnostics

Configuration reload is an atomic operation:

1. Read and parse `config.toml`.
2. Expand symbolic leader bindings.
3. Validate triggers, command identifiers, and conflicts.
4. Build a complete resolved keymap.
5. Replace the active keymap only when it is valid.
6. Synchronize native menu shortcuts.
7. Cancel any pending leader sequence.
8. Report actionable diagnostics.

atc loads configuration at launch. The MVP also provides an always-available Reload Configuration command in the native menu, backed by `configuration.reload`. It has no compiled shortcut or leader binding. Automatic file watching is not required.

Every configuration failure is written to the system log with the configuration path and precise parse or validation diagnostics. The active window also shows one dismissible, non-modal notice explaining that configuration was not loaded and whether atc retained the previous keymap or restored compiled defaults. Failures do not present blocking alerts or one notice per diagnostic.

## Non-Goals

- A graphical keybinding editor.
- A command palette or keyboard-shortcuts reference UI.
- Sequences longer than the leader plus one continuation key.
- Sidebar-local Vim navigation such as `j`, `k`, `h`, and `l`.
- Automatic config file watching.
- Global shortcuts that work while another application is active.
- A bare, unmodified leader such as Space.
- Ghostty-style global, all-surface, unconsumed, or catch-all binding flags.
- Chained commands, physical-key bindings, or named key tables for full modal behavior.

## Success Criteria

- Users can toggle the sidebar with both `Cmd-B` and `Cmd-K, B` while the terminal has focus.
- Normal terminal keystrokes are unaffected when no shortcut or sequence is active.
- Initial bindings can be remapped, unbound, or cleared through `config.toml` without code changes.
- Menus display the resolved direct shortcuts, and toolbar/menu invocations execute the same commands as keyboard bindings.
- Registered direct shortcuts execute exactly once while an embedded terminal has focus, even when Ghostty defines the same binding.
- Unavailable commands are handled consistently and never leak shortcut input into the terminal.
- Invalid configuration preserves a working keymap and produces a useful diagnostic.
- Automated tests cover trigger parsing, default layering, replacement and unbinding, leader expansion, command availability, menu shortcut selection, sequence timeout and cancellation, unmatched keys, reload, and terminal event forwarding.
