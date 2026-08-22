# atc-tui prototype

A local OpenTUI control surface for ATC Projects and Threads. The App Server
remains the control plane: it owns durable state and opens the selected Thread's
terminal. The client then releases OpenTUI and attaches the caller's TTY directly
with zmx, following the process boundary proven by `experiments/atg-poc`.

Run from source with `mise run -C atc-tui dev`, or build the standalone
`dist/atc-tui` with `mise run tui:build`. The HTTP endpoint and local zmx
namespace use the App Server defaults; `atc-tui --help` documents overrides
for installations using different local settings.

The footer shows the keys available in the current view. Ctrl-Space temporarily
replaces them with global navigation keys for active Threads, archived Threads,
and Projects. Enter performs the selected item's primary action. zmx owns the
attached terminal, so its native Ctrl-\ binding returns directly to the Thread
list.
