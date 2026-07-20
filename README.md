# atc

atc is a product and development workspace for working with remote
Terminal Sessions: a standalone server that owns Projects and Terminal
Sessions, plus native client apps that attach to it.

This repository is a monorepo holding every atc surface:

| Directory | Surface |
| --- | --- |
| [`server/`](server/) | atc server — the standalone Go service, HTTP/WebSocket API, `atc` CLI, and embedded admin web UI |
| [`macos/`](macos/) | atc for macOS — the native SwiftUI client |
| `ios/` | Reserved for a future atc for iOS client |
| [`packages/`](packages/) | Shared cross-surface libraries (currently `ATCKit`, the Swift API client) |
| [`docs/`](docs/) | Product, architecture, and planning documentation |
| [`scripts/`](scripts/) | Repo-level helper scripts |

## atc server

The server is independently installable and useful on its own — no native
client required. It runs on the workstation where your terminal sessions live.

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/server/install.sh | sh
```

`atc` is the server's command line interface:

```sh
atc start
atc status
atc projects list
atc sessions start --project <id>
atc sessions attach <id>
```

See [`server/README.md`](server/README.md) for full setup, development, and
API documentation.

## atc for macOS

The macOS app connects to one or more atc servers, browses their
Projects, and attaches to Terminal Sessions in a native terminal. Open
`macos/atc.xcodeproj` in Xcode to build and run it.

### Configuration

The macOS app optionally reads `$XDG_CONFIG_HOME/atc/macos.toml`. If
`XDG_CONFIG_HOME` is empty or unset, it reads `~/.config/atc/macos.toml`.
The app never creates the configuration directory or file.

```toml
[keyboard]
leader = "cmd+k"                       # default: "cmd+k"
leader_timeout_ms = 1800               # default: 1800
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

Binding keys are either direct triggers such as `cmd+b` or two-step leader
sequences such as `leader>b`. Values are command IDs, or `"unbind"` to remove
a default binding. Configuration uses a closed schema: unknown tables, unknown
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

All `[terminal]` keys are optional. Terminal presentation starts from
libghostty's compiled defaults, then applies the values set in this table.
`theme` must name a bundled Ghostty theme; `font_family` must be non-empty; and
`font_size` must be positive. `padding_x` and `padding_y` are non-negative
integers and default to `8` and `6`, respectively. Set either padding value to
`0` for an intentional edge-to-edge layout, which may place content beneath a
window's rounded corners.

Use **Reload Configuration** in the app menu after editing the file. A valid
reload replaces the complete application configuration; an invalid reload
keeps the last-known-good configuration. Deleting the file and reloading
restores defaults. Most terminal presentation changes apply live to every
retained surface without recreating terminal sessions or reconnecting
WebSockets. Padding changes are guaranteed for new terminal surfaces or after
restarting the app; retained surfaces may not recalculate their padding on
reload.
**Reveal Configuration** selects `macos.toml` in Finder when it exists, or
reveals its expected directory without creating anything.

ATC does not read Ghostty's configuration files. To match an existing Ghostty
setup, copy the desired presentation values into `[terminal]` in `macos.toml`.

This file belongs only to the macOS process. It is separate from the server's
`~/.config/atc/server/config.toml`; the app never reads server configuration
files. Connections are stored by the app, and connection tokens remain in the
Keychain.

## Development

Tasks are run with [mise](https://mise.jdx.dev). From the repo root:

```sh
mise run check   # every gate: gofmt, go vet, Go tests, web type check,
                 # web tests, ATCKit tests, macOS app tests
mise run test    # all test suites
```

Per-surface tasks (`server:build`, `web:check`, `kit:test`, `macos:test`, …)
are listed by `mise tasks`; `server/mise.toml` has the server's own dev/build
tasks. CI (`.github/workflows/`) runs the same mise tasks on every push, so a
green local `check` means a green build.

The HTTP API's wire shapes are pinned by shared fixtures in
[`packages/contracts/`](packages/contracts/) that the Go server, Swift
client, and web client all test against. Platform and security decisions
(macOS floor, sandbox/ATS stance, token storage) live in
[`docs/platform-policy.md`](docs/platform-policy.md).

Each surface builds, tests, and releases independently; see the workflows in
`.github/workflows/`.
