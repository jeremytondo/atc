# Keyboard Shortcuts Plan

Status: Draft

## Goals

AtelierCode should be comfortable to drive from the keyboard while preserving
the Terminal Session as the primary typing surface. Defaults should combine
macOS and IDE conventions with Vim-flavored navigation where it fits the native
UI.

The first version should provide useful defaults, a command palette, and a
keyboard shortcuts reference. It should not include user-configurable
keybindings yet.

## Product Rules

- Terminal Session focus sends ordinary typed keys to the terminal.
- App-level Command-key shortcuts may work while a Terminal Session has focus.
- Command aliases should share one command path whether invoked from a menu,
  toolbar, keyboard shortcut, Atelier Command Sequence, or command palette.
- `Cmd-K` starts an Atelier Command Sequence. The user releases `Cmd-K`, then
  presses an unmodified next key that targets AtelierCode rather than the
  terminal.
- Atelier Command Sequence letters are case-insensitive. Documentation may display
  them uppercase for readability, but Shift should not be required.
- Documentation should display Atelier Command Sequences with a comma, such as
  `Cmd-K, N`, to make the sequence clear.
- Keeping Command held for the next key is not part of the Atelier Command
  Sequence. It should be treated as a normal macOS Command-key shortcut.
- An unmatched Atelier Command Sequence is swallowed, reports no matching command,
  and does not forward the key to the Terminal Session.
- The Atelier Command Sequence times out after about two seconds.
- Pressing `Cmd-K` immediately shows a compact non-modal hint with common next
  keys.
- Unknown next keys show a brief visual "no command" indication. Sequence
  timeout and explicit cancellation are silent.
- `Esc` cancels an active Atelier Command Sequence and is not sent to the
  terminal while the sequence is active.
- Outside an active Atelier Command Sequence, `Esc` belongs to the focused
  Terminal Session.
- Modal sheets and forms suspend global shortcuts and Atelier Command Sequences
  for v1. Their keyboard behavior should stay limited to normal text editing,
  default actions, and cancellation.
- Native menu and context-menu keyboard handling owns the keyboard while menus
  are open.

## Default Shortcuts

| Action | Shortcut |
| --- | --- |
| New Terminal Session | `Cmd-N`, `Cmd-K, N` |
| New Project | `Shift-Cmd-N` |
| Refresh Projects and Sessions | `Cmd-R`, `Cmd-K, R` |
| Focus Sidebar | `Cmd-1`, `Cmd-K, 1` |
| Focus Terminal | `Cmd-2`, `Cmd-K, 2` |
| Toggle Inspector | `Cmd-I` |
| Open Command Palette | `Shift-Cmd-P`, `Cmd-K, P` |
| Show Keyboard Shortcuts | `Shift-Cmd-?`, `Cmd-K, ?` |
| Open Settings | `Cmd-,` |

New Terminal Session uses the nearest Project context:

- If sidebar focus is on a Project, create the Terminal Session in that Project.
- If sidebar focus is on a Terminal Session, create the new Terminal Session in
  that Terminal Session's Project.
- If the Terminal Session has focus, use the selected Terminal Session's
  Project.
- If there is no Project context, the command is disabled or does nothing.

Refresh Projects and Sessions invokes the same command path as the toolbar
Refresh action. Shortcut aliases should not define separate refresh behavior.

`Cmd-F`, `Cmd-P`, `Cmd-T`, `Cmd-L`, and `Cmd-W` are intentionally unassigned
for AtelierCode-specific behavior in v1.

## Sidebar Navigation

Sidebar Vim-style navigation applies only while sidebar rows have focus. It
does not apply while typing in search, rename, create, or settings fields.

| Key | Behavior |
| --- | --- |
| `j` / Down Arrow | Move the Focused Sidebar Row down |
| `k` / Up Arrow | Move the Focused Sidebar Row up |
| `h` / Left Arrow on expanded Project | Collapse Project |
| `h` / Left Arrow on Terminal Session | Move focus to parent Project |
| `l` / Right Arrow on collapsed Project | Expand Project |
| `l` / Right Arrow on expanded Project | Move focus to first visible child session, if any |
| `l` / Right Arrow on Terminal Session | Select/open Terminal Session |
| `Enter` on Project | Expand/collapse Project |
| `Enter` on Terminal Session | Select/open Terminal Session |
| `/` | Focus sidebar search |
| `Esc` in sidebar search | Clear query first; a second `Esc` returns focus to sidebar rows |

Sidebar navigation does not wrap at the top or bottom. If filtering removes the
focused row, focus falls back to the first visible row. If there are no visible
rows, sidebar navigation keys do nothing.

`gg`, `G`, `o`, `Space`, bare creation shortcuts, bare rename shortcuts, and
destructive shortcuts are not included in v1 sidebar navigation.

## Command Palette

`Shift-Cmd-P` opens a minimal command palette from normal app contexts,
including Terminal Session focus. `Cmd-K, P` is an alias through the Atelier
Command Sequence.

The palette should list implemented app commands only. It should not introduce
hidden functionality that is unavailable elsewhere in the UI.

Initial commands:

- New Terminal Session
- New Project
- Refresh Projects and Sessions
- Focus Sidebar
- Focus Terminal
- Toggle Inspector
- Show Keyboard Shortcuts
- Open Settings

Contextually unavailable commands may remain visible but dimmed. Any reason
shown for a disabled command should be short and unobtrusive.

When the palette opens, its text input should be focused. Arrow keys and
`Ctrl-N` / `Ctrl-P` may navigate results while the input remains focused.
`Enter` runs the selected command. `Esc` closes the palette. Bare `j/k` should
not navigate while the search field is focused.

## Keyboard Shortcuts Reference

`Shift-Cmd-?` and `Cmd-K, ?` open a dedicated keyboard shortcuts reference. The
reference should also be reachable from the command palette.

The shortcuts reference should show all v1 shortcuts grouped by context:

- Global
- Atelier Command Sequences
- Sidebar
- Terminal
- Sheets and Forms

Intentional aliases should be shown explicitly. Menu-backed commands should
show their menu paths. Sidebar-local keys should be documented as local
interaction keys rather than menu commands.

## Menu Placement

- File > New Terminal Session
- File > New Project
- View > Command Palette...
- View > Refresh Projects and Sessions
- View > Focus Sidebar
- View > Focus Terminal
- View > Toggle Inspector
- Help > Keyboard Shortcuts
- App menu > Settings

Settings should also be available in the command palette.

## Explicit Non-Goals

- User-configurable keybindings.
- A settings file for shortcuts.
- A full keybinding engine.
- Conflict detection or remapping UI.
- File quick-open or project/session quick-switch shortcuts.
- Terminal buffer search.
- Destructive lifecycle shortcuts for archiving, terminating, or deleting.
- Disconnect shortcuts or command-palette actions.
