# ATC-233 play client findings

Status: implementation, deterministic gate, and isolated interactive client
smoke passing; live provider rerun requires authenticated private profiles

Observed on 2026-08-22. The client is a separate `internal/play` package with
its own canonical HTTP DTOs. A package-boundary test rejects imports from the
core, adapters, ports, domain, store, provider, or status packages. Canonical
control and observation go through `/v1` routes. Terminal I/O does not: the UI
creates the zmx session, displays its public Terminal ID, and leaves attachment
to direct `zmx attach` in another terminal.

## What the prototype makes legible

The thread list and detail panel keep combined activity, foreground Turn,
background activity, pending count, and Terminal lifecycle/reachability
visible as separate dimensions. Chat event history displays the normalized
event type and canonical payload summary. Approval and question screens show
human labels while returning the selected opaque option ID, or free text when
the request has no options.

Polling is cursor-addressed. A failed refresh preserves the last complete
Thread/event snapshot and cursor. On recovery, the next `/v1/events?after=`
request catches up before advancing the local cursor. Terminal reconciliation
is throttled separately because `GET /v1/terminals` performs real inventory
work; its failure is a partial warning and does not make canonical Threads or
events disappear.

## Validation performed

The deterministic tests exercise the complete HTTP client route and payload
shape, typed API errors, labeled option handling, independent lifecycle
rendering, cursor de-duplication and catch-up, partial Terminal inventory
failure, and the forbidden-import boundary.

An interactive run used a core bound to isolated loopback port `17332`, state
under `/tmp/atc-233-state`, and a no-op terminal executable. Through `play`, it
created a Claude chat Thread and a Codex TUI Thread, displayed their normalized
`thread.created` events and independent lifecycle fields, retained the screen
after the exact server process stopped, and reconnected to the restarted core
without losing the cursor or resources. The expected missing Codex remote
diagnostic was shown as an inline action error instead of exiting the client.

The live Claude chat/TUI and Codex chat/two-TUI provider matrix was not rerun
through this client because this environment exposes only the developer's
shared Codex profile and no authenticated private `CODEX_HOME` or
`CLAUDE_CONFIG_DIR`; project rules prohibit using the shared profile. The
underlying canonical routes and provider composition were already exercised
in the ATC-232 live matrix recorded in `findings.md`. A final ATC-233 live pass
should use private authenticated profiles and drive that same matrix entirely
through `play`.

## API awkwardness found

- `GET /v1/terminals` reconciles the external inventory and can fail while
  Threads and events remain healthy. The client treats that as degraded
  Terminal state and keeps the rest of the UI usable.
- Normalized assistant output is exposed as small delta events. The prototype
  faithfully shows those events, but a production conversation view will want
  a canonical message projection rather than client-side provider-neutral
  delta assembly.
- The HTTP byte-stream attach experiment produced broken raw-mode and resize
  behavior. It was removed: this prototype now tests only zmx creation and
  status tracking, while a real terminal attaches directly through zmx.
- Local-only Thread creation means a remotely connected play client can
  observe and control existing resources but receives the canonical
  `local_only` error when creating one. That trust boundary needs an explicit
  product decision before a remote native client ships.
