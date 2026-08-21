# atc-tui prototype

A terminal-native control surface for ATC Projects and Threads. It talks only
to one App Server's public HTTP, SSE, and WebSocket APIs; zmx remains owned by
the server and the caller's terminal remains responsible for rendering.

Run from source with `mise run -C atc-tui dev -- --endpoint URL`, or build the
standalone `dist/atc-tui` with `mise run tui:build`. The endpoint defaults to
`ATC_ENDPOINT` and then `http://127.0.0.1:7331`. Set `ATC_TOKEN` for a remote
server that requires bearer authentication. The client stores no local state.

Use the arrow keys or `j`/`k` to select a Thread, Enter to open and attach its
terminal, `r` to refetch, and `q` or Ctrl-C to exit. Ctrl-\\ detaches from a
terminal without ending its zmx session. `atc-tui --help` is the command
reference.
