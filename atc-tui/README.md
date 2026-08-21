# atc-tui prototype

A local terminal-native control surface for ATC Projects and Threads. The App
Server remains the control plane: it owns durable state and opens the selected
Thread's terminal. The client then attaches the caller's TTY directly with zmx,
following the process boundary proven by `experiments/atg-poc`.

Run from source with `mise run -C atc-tui dev`, or build the standalone
`dist/atc-tui` with `mise run tui:build`. The HTTP endpoint and local zmx
namespace use the App Server defaults; `atc-tui --help` documents overrides
for installations using different local settings.

Use the arrow keys or `j`/`k` to select a Thread, Enter to open and attach its
terminal, `r` to refetch, and `q` or Ctrl-C to exit. zmx owns the attached
terminal, so its native Ctrl-\ binding returns directly to the Thread list.
