# atc for macOS

The native SwiftUI client. It connects to one or more atc servers, browses
their Projects, and attaches to Terminal Sessions in a native terminal. Open
`macos/atc.xcodeproj` in Xcode to build and run it, or use `mise run
macos:test` from the repo root.

See [`CONTEXT.md`](CONTEXT.md) for the app's domain language.

## Configuration

The app optionally reads `$XDG_CONFIG_HOME/atc/macos.toml`. If
`XDG_CONFIG_HOME` is empty or unset, it reads `~/.config/atc/macos.toml`. The
app never creates the configuration directory or file.

```toml
[keyboard]
leader = "cmd+k"                       # default: "cmd+k"
clear_default_keybindings = false      # default: false

[keybindings]
"cmd+b" = "view.toggle-sidebar"
"leader>b" = "view.toggle-sidebar"
"cmd+r" = "unbind"

[terminal]
theme = "Catppuccin Mocha"
font_family = "Berkeley Mono"
font_size = 14.0
padding_x = 8
padding_y = 6
```

### Keybindings

Binding keys are either direct triggers such as `cmd+b` or two-step leader
sequences such as `leader>b`. Values are command IDs, or `"unbind"` to remove a
default binding. Configuration uses a closed schema: unknown tables, unknown
keys, duplicate keys, wrong types, and invalid values reject the entire file.
When a command has several direct triggers, the menu bar shows one of them,
chosen deterministically (user bindings beat defaults; ties resolve
alphabetically).

The scoped Command Palette commands search within a specific navigation type:

| Command ID | Default bindings |
| --- | --- |
| `view.search-sessions` | `cmd+shift+s`, `leader>s` |
| `view.search-terminals` | `cmd+shift+t`, `leader>t` |
| `view.search-workspaces` | `cmd+shift+o`, `leader>w` |

### Terminal presentation

All `[terminal]` keys are optional. Terminal presentation starts from
libghostty's compiled defaults, then applies the values set in this table.
`theme` must name a bundled Ghostty theme; `font_family` must be non-empty; and
`font_size` must be positive. `padding_x` and `padding_y` are non-negative
integers and default to `8` and `6`, respectively. Set either padding value to
`0` for an intentional edge-to-edge layout, which may place content beneath a
window's rounded corners.

atc does not read Ghostty's configuration files. To match an existing Ghostty
setup, copy the desired presentation values into `[terminal]` in `macos.toml`.

### Reloading

Use **Reload Configuration** in the app menu after editing the file. A valid
reload replaces the complete application configuration; an invalid reload keeps
the last-known-good configuration. Deleting the file and reloading restores
defaults. Most terminal presentation changes apply live to every retained
surface without recreating terminal sessions or reconnecting WebSockets.
Padding changes are guaranteed for new terminal surfaces or after restarting
the app; retained surfaces may not recalculate their padding on reload.

**Reveal Configuration** selects `macos.toml` in Finder when it exists, or
reveals its expected directory without creating anything.

This file belongs only to the macOS process — the app never reads server
configuration files. Connections are stored by the app, and connection tokens
remain in the Keychain.
