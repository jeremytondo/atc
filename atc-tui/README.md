# atc-tui

An OpenTUI control surface for ATC Projects and Threads. The App Server remains
the control plane: it owns durable state and opens the selected Thread's
terminal. The client then releases OpenTUI and gives the caller's real TTY to
zmx locally or system SSH remotely, following the process boundary proven by
`experiments/atg-poc`.

Run from source with `mise run -C atc-tui dev`, or build the standalone
`dist/atc-tui` with `mise run tui:build`. `mise run tui:install` installs that
binary under `ATC_INSTALL_DIR` (default `~/.local/bin`). The HTTP endpoint and
local zmx namespace use the App Server defaults; `atc-tui --help` documents all
connection overrides.

For a workstation whose SSH alias is `workstation`, with its normal `atc`
installation and App Server already running, start a local controller with:

```sh
atc-tui --remote workstation
```

The controller keeps the remote App Server loopback-only through a supervised
SSH forward. Opening a Thread gives the TTY to a second interactive SSH client.
If that connection is lost, the controller retries the same terminal with
backoff; the zmx session survives and repaints after reconnection.

The footer shows the keys available in the current view. Ctrl-Space temporarily
replaces them with global navigation keys for active Threads, archived Threads,
and Projects. Enter performs the selected item's primary action. Ctrl-\ returns
from a local zmx attachment; the remote `atc terminal attach` uses Ctrl-].
