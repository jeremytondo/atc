# atc-tui prototype

A local OpenTUI control surface for ATC Projects and Threads. The App Server
remains the control plane: it owns durable state and opens the selected Thread's
terminal. The client then releases OpenTUI and attaches the caller's TTY directly
with zmx, following the process boundary proven by `experiments/atg-poc`.

Run from source with `mise run -C atc-tui dev`, or build the standalone
`dist/atc-tui` with `mise run tui:build`. The HTTP endpoint and local zmx
namespace use the App Server defaults; `atc-tui --help` documents overrides
for installations using different local settings.

Tab cycles through active Threads, archived Threads, and Projects. Enter opens an
active Thread, restores an archived Thread, or renames a Project. Ctrl-N creates
and immediately attaches a Thread; Ctrl-P creates a Project. The contextual help
bar documents archive, restore, delete, refresh, and exit keys. zmx owns the
attached terminal, so its native Ctrl-\ binding returns directly to the Thread
list.
