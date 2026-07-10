# atc server

atc server is a standalone Go service, CLI (`atc`), and embedded admin web UI for starting and
managing persistent terminal sessions. Sessions run configured typed Actions in
explicit Environments; local CLI commands talk to the background service through
the owner-only Unix socket.

## Requirements

- `mise` — manages the toolchain (Go, Node, pnpm) and provides task shortcuts
- `zmx` — required when the service starts or reconciles sessions; defaults to
  `zmx` on `PATH`
- `claude` and/or `codex` — optional action commands, depending on which
  built-in or configured Actions you want to launch

Install the pinned toolchain once:

```sh
mise install
```

This provisions Go, Node, and pnpm at the versions pinned in `mise.toml`, so you do not
need them installed separately.

## Installing Releases

atc release builds are published through GitHub Releases. The installer
supports Linux and macOS on `amd64` and `arm64`, including Arch Linux:

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/server/install.sh | sh
```

By default this installs `atc` to `~/.local/bin`. Override the location with
`ATC_INSTALL_DIR`:

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/server/install.sh | ATC_INSTALL_DIR="$HOME/bin" sh
```

Install a specific release tag:

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/server/install.sh | sh -s -- --version v0.1.0
```

Update an existing direct install in place:

```sh
curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/server/install.sh | sh
```

The installer verifies the GitHub Release checksum before replacing the binary.

## Running for Development

There are two ways to run the project:

| Goal | Command | Open |
| --- | --- | --- |
| Start dev servers (hot-reload) | `mise run dev` | `http://127.0.0.1:5173` |
| Run the service in the foreground | `mise run serve` | `http://127.0.0.1:7331` |
| Build the release binary | `mise run build` | → `dist/atc` |
| Build and install locally | `mise run install` | → `~/.local/bin/atc` |
| Run tests | `mise run test` | — |

- **`mise run dev`** starts the Go API and the SvelteKit dev server in the
  foreground. Open the Vite URL (`:5173`) for instant UI hot-reload; `/api`
  requests are proxied to the Go server on `:7332`, so the frontend talks to the
  real backend. Logs from both processes stream in the same terminal. Stop both
  with `Ctrl-C`.

  Dev is a hermetic profile: it generates `tmp/dev/atc.toml` and keeps its
  socket, database, and actions file under `tmp/dev/`, on a dedicated port.
  Your personal `~/.config/atc/atc.toml` and `ATC_*` overrides
  never apply, and it runs cleanly alongside an installed background service.
  Delete `tmp/dev` for a fresh dev state; to point the CLI at the dev service,
  pass its config: `go run ./cmd/atc --config tmp/dev/atc.toml sessions list`.
- **`mise run serve`** builds the web app, embeds it into the binary, and runs
  the service in the foreground. This is the development/debug/supervisor path;
  `atc start` is the normal operator path for a background service.
- **`mise run install`** builds the release-style local binary and installs it
  to `~/.local/bin/atc`. Override the destination directory with
  `ATC_INSTALL_DIR=/path/to/bin mise run install`.

## Remote Access Over SSH

When you develop on a remote workstation (e.g. over Tailscale) and want to view the UI in
your laptop's browser, use **SSH local port forwarding**. The servers bind to `127.0.0.1`
only, so nothing is exposed on the network — the traffic rides your existing encrypted SSH
connection, and Vite hot-reload works because the browser sees `localhost`.

Add this to your laptop's `~/.ssh/config`:

```sshconfig
Host workstation
  HostName workstation.tailnet.ts.net   # your MagicDNS name or host
  LocalForward 5173 127.0.0.1:5173      # mise run dev (UI; /api is proxied)
  LocalForward 7331 127.0.0.1:7331      # mise run serve (embedded UI + API)
```

Then, from your laptop:

```sh
ssh workstation        # forwards happen automatically
```

Run a task on the workstation and open the matching URL on your laptop:

- `mise run dev` → `http://localhost:5173` (only port 5173 is needed; `/api` is proxied to
  the Go server on the workstation)
- `mise run serve` → `http://localhost:7331`

To forward without editing your ssh config, use the `-L` flag directly:

```sh
ssh -L 5173:127.0.0.1:5173 workstation.tailnet.ts.net
```

**Why loopback?** The servers bind `127.0.0.1` by default. If you bind a
reachable TCP address, configure `auth.token` / `ATC_API_TOKEN`; the
owner-only Unix socket used by local CLI commands is always trusted.

**Remote trust model:** the API can browse files, launch configured commands,
and attach to terminals, so a reachable bind is a real exposure. Binding a
non-loopback address without a token is supported for trusted overlay networks
(a tailnet interface, or `tailscale serve` in front of loopback), and the
service logs a warning at startup instead of refusing to run. Never bind
`0.0.0.0` without a token on a network you don't fully trust.

**Tailscale bonus:** for the embedded mode only, you can expose a tailnet-only HTTPS URL
backed by the loopback port without any port forwarding:

```sh
tailscale serve --bg 7331   # -> https://<machine>.<tailnet>.ts.net
```

This is handy for reaching `mise run serve` from your phone or other tailnet devices. The
`dev` server would additionally need Vite `allowedHosts`/HMR configuration to work behind
this proxy, so prefer SSH forwarding for `mise run dev`.

## Service Commands

Start the service in the background:

```sh
go run ./cmd/atc start
```

Check status and stop it:

```sh
go run ./cmd/atc status
go run ./cmd/atc stop
```

Run the same service in the foreground for development, direct log observation,
external supervisors, containers, or tests:

```sh
go run ./cmd/atc serve
```

By default, the service listens on TCP address `127.0.0.1:7331` and on a
per-user Unix socket used by local CLI commands.

Use a different TCP bind address with a flag:

```sh
go run ./cmd/atc serve --http-addr 127.0.0.1:7332
```

Or with the environment variable:

```sh
ATC_HTTP_ADDR=127.0.0.1:7332 go run ./cmd/atc serve
```

For remote testing from another trusted machine on your tailnet, bind explicitly
to a reachable interface and configure a TCP API token:

```sh
ATC_API_TOKEN=secret go run ./cmd/atc serve --http-addr 0.0.0.0:7331
```

Do not expose this bind to the public internet.

Check the HTTP diagnostics API:

```sh
curl http://127.0.0.1:7331/api/health
curl http://127.0.0.1:7331/api/version
```

Stop a foreground service with `Ctrl-C`.

## Release Automation

Releases are manual from GitHub Actions:

1. Open **Actions → Release → Run workflow**.
2. For a branch test build, select any branch and set `channel` to `test`.
   GoReleaser builds archives and uploads them as workflow artifacts without
   creating a GitHub Release.
3. For a stable release, select the default branch, set `channel` to `stable`,
   and choose `patch`, `minor`, or `major`. The workflow creates the next
   `vX.Y.Z` tag, builds archives, writes `checksums.txt`, and publishes the
   GitHub Release.

Equivalent GitHub CLI commands:

```sh
gh workflow run release.yml --ref my-branch -f channel=test -f release_type=patch
gh workflow run release.yml --ref main -f channel=stable -f release_type=minor
```

## Configuration

atc reads an optional TOML config file. Every setting also has a built-in default, so the file is never required.

The default location is `$XDG_CONFIG_HOME/atc/atc.toml` (i.e. `~/.config/atc/atc.toml`). Override it with the `--config` flag or the `ATC_CONFIG` environment variable:

```sh
go run ./cmd/atc serve --config ./atc.toml
ATC_CONFIG=./atc.toml go run ./cmd/atc serve
```

A missing config file at the default location is fine — atc falls back to defaults. A file passed explicitly via `--config` or `ATC_CONFIG` that is missing or malformed is an error.

Settings resolve with the precedence **flag > environment variable > config file > built-in default**, so a more specific source always wins. `http_addr` has a CLI flag (`--http-addr`) and environment override (`ATC_HTTP_ADDR`). `zmx.bin`, `auth.token`, and `store.db_path` can be overridden with `ATC_ZMX_BIN`, `ATC_API_TOKEN`, and `ATC_DB_PATH`.

Full example with the default values:

```toml
[server]
http_addr = "127.0.0.1:7331"   # TCP listen address

[log]
level  = "info"                # debug | info | warn | error
format = "text"                # text | json

[paths]
control_dir = ""               # socket/PID/log directory; empty = auto
                               # (XDG_RUNTIME_DIR, then TMPDIR, then /tmp)

[store]
db_path = ""                   # SQLite state DB; empty = auto
                               # (XDG_STATE_HOME, then ~/.local/state)

[zmx]
bin = "zmx"                    # zmx binary; can be an absolute path

[auth]
token = ""                     # bearer token for TCP API; empty disables it
```

atc is pre-alpha, so SQLite migrations may be squashed while the state model
is still changing. If a local development database fails to migrate after
pulling schema changes, delete the configured state DB and let atc recreate
it.

The config file applies to background mode too: `atc start` forwards an explicit `--config` path to the detached service so it resolves the same file.

Actions are managed in `actions.json` beside `atc.toml` by default, or at
`ATC_ACTIONS_PATH` when that environment variable is set. The file is a
sparse overlay: built-in `claude` and `codex` are always present underneath, and
file entries add custom Actions or override built-ins by name. `[actions]` in
`atc.toml` is rejected.

Pre-release files that use `bin` and `kind` still load, but new API writes use
`command` and omit `kind`.

A minimal custom action only needs `command`:

```json
{
  "actions": {
    "lazygit": {
      "label": "LazyGit",
      "description": "Open LazyGit",
      "command": "lazygit"
    }
  }
}
```

The built-in `claude` and `codex` actions currently define command and prompt
support only; they do not include built-in params. Parameter rows in the Actions
UI come from `params` on custom actions or file-backed overrides.

To override a built-in and define params, use the same action name. This example
documents the `params` shape; it is not the default Codex definition:

```json
{
  "actions": {
    "codex": {
      "label": "Codex",
      "description": "OpenAI Codex CLI",
      "command": "codex",
      "args": [],
      "prompt": {},
      "params": {
        "model": {
          "type": "enum",
          "values": ["gpt-5-codex"],
          "default": "gpt-5-codex",
          "flag": "--model",
          "label": "Model",
          "description": "Model family"
        },
        "full-auto": {
          "type": "bool",
          "default": false,
          "flag": "--full-auto",
          "label": "Full Auto",
          "description": "Allow autonomous edits"
        }
      }
    }
  }
}
```

Environments still live in `atc.toml`:

```toml

[environments.workstation]
kind = "host-login-shell"
label = "Workstation login shell"
```

Configured `[environments]` entries overlay the built-in
`host-login-shell` default. Omitting `environment` from a start request still
uses `host-login-shell`.

## Background Service

`atc start` is the normal operator path. It starts the atc service in the
background by launching the foreground `serve` entrypoint.

Start the service:

```sh
go run ./cmd/atc start
```

Start it on a custom TCP address:

```sh
go run ./cmd/atc start --http-addr 127.0.0.1:7332
```

Check the service process status:

```sh
go run ./cmd/atc status
```

Check API health through the local Unix socket:

```sh
go run ./cmd/atc health
```

Stop the background service:

```sh
go run ./cmd/atc stop
```

Background mode writes lifecycle files under the service control directory:

- `$XDG_RUNTIME_DIR/atc` when `XDG_RUNTIME_DIR` is set
- `$TMPDIR/atc-$UID` when `TMPDIR` is set
- `/tmp/atc-$UID` otherwise

The current files are:

- `atc.sock` for local Unix-socket API checks
- `atc.pid` for background process status
- `atc.log` for background service output

API-backed CLI commands require this service to be running and do not auto-start
it:

```sh
go run ./cmd/atc actions list
go run ./cmd/atc environments list
go run ./cmd/atc sessions start --action codex --env host-login-shell --dir .
go run ./cmd/atc sessions list
go run ./cmd/atc sessions show <id>
go run ./cmd/atc sessions attach <id>
go run ./cmd/atc sessions send-text <id> "hello"
go run ./cmd/atc sessions send-key <id> enter
go run ./cmd/atc sessions terminate <id>
go run ./cmd/atc sessions archive <id>
```

Discovery, start, list, and show commands accept `--output json` for scripting.
If `--dir` is omitted, `sessions start` uses the current working directory.
The current named keys for `sessions send-key` are `enter`, `ctrl-c`, and
`escape`.

## Mise Tasks

List available tasks:

```sh
mise tasks
```

The task surface is split into primary entrypoints and the building blocks they compose:

Primary entrypoints:

- `dev`: start the Go API + SvelteKit dev server in the foreground (open `:5173`)
- `serve`: build, embed, and run the service in the foreground (open `:7331`)
- `build`: build web, stage embedded assets, compile `dist/atc`
- `test`: runs `go test ./...`

Building blocks:

- `web:dev`: run the SvelteKit dev server alone
- `web:build`: build the SvelteKit web app
- `assets:stage`: copy the web build into the Go embed directory

Pass a custom bind address through the environment (the Vite proxy follows it in `dev`):

```sh
ATC_HTTP_ADDR=127.0.0.1:7332 mise run dev
```

Use the direct `go run ./cmd/atc ...` commands for `start`, `stop`,
`status`, `health`, `actions`, `environments`, and `sessions`.

## CLI Help

Show the root command help:

```sh
go run ./cmd/atc --help
```

Show help for a subcommand:

```sh
go run ./cmd/atc serve --help
go run ./cmd/atc start --help
go run ./cmd/atc actions list --help
go run ./cmd/atc environments list --help
go run ./cmd/atc projects create --help
go run ./cmd/atc sessions start --help
go run ./cmd/atc sessions send-text --help
```

Projects name a workstation directory and group sessions. Create one, start a
session in it (the session inherits the project's directory), and list its
sessions:

```sh
go run ./cmd/atc projects create --name "atc" --dir ~/Projects/atc
go run ./cmd/atc sessions start --action claude --project <id>
go run ./cmd/atc sessions list --project <id>
```

Projects are renamed with `projects rename` and archived (never deleted) with
`projects archive`/`projects unarchive`; archived projects cannot start new
sessions, and a project with a starting or running session cannot be archived
(the API returns 409 `project_has_active_sessions`). The same operations are
available over the API under `/api/projects`.

## Tests

Run all Go tests directly:

```sh
go test ./...
```

Or through `mise`:

```sh
mise run test
```
