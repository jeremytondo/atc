# ATC-284 shared-server probes

Run from the repository root:

```sh
./scripts/atc-284 doctor
./scripts/atc-284 listen
./scripts/atc-284 create "$PWD"
```

`doctor` and `listen` are observational after the required app-server
handshake. `create` sends one `thread/start` request and prints the new thread
ID. The helper connects to the user's existing Codex control socket; it never
starts, stops, or configures the server.

Use `--socket PATH` before the command to target an explicitly isolated test
server. The listener prints only `thread/started` and
`thread/status/changed`; redirect or pipe it through `tee` when preserving a
run as evidence.
