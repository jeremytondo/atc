# Plugin orchestration prototype (ATC-238)

This disposable Go experiment tests whether two deliberately unlike
integrations can share a small ATC orchestration surface without making the
core understand either integration's protocol.

The fake managed-agent plugin can discover, create, respond to, and cancel
agent sessions. The fake external-workspace plugin discovers an observed
coding session and a delegated terminal; the former can only be opened in its
own tool, while the latter can be controlled or attached. Nothing launches a
real provider, terminal, or browser.

The package boundaries are the prototype: `model` is the canonical resource
and lifecycle vocabulary, `plugin` contains narrow capability interfaces,
`core` validates and composes integrations, and `httpapi` exposes the same
resource view to clients. Opaque integration data is allowed only in an
explicit extension namespace and cannot affect core behavior.

## Run it

Requirements: Go 1.26 or newer.

```sh
cd experiments/plugin-orchestration
make check
make demo | jq
```

The deterministic demo prints plugin descriptors, the final normalized
resource inventory, an ordered lifecycle timeline, and the link returned by an
`open_external` action.

Build and run the local HTTP server with:

```sh
make build
./bin/atc-orchestration
```

Use `./bin/atc-orchestration --help` for its disposable runtime options. The
HTTP transport's module header and focused test show the complete API workflow.
See [findings.md](findings.md) for the proposed model and conclusions.
