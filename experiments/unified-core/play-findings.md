# ATC-233 play client findings

Status: implementation, deterministic gate, authenticated ACP matrix, and
direct-zmx live TUI acceptance flow passing

Observed on 2026-08-22. The client is a separate `internal/play` package with
its own canonical HTTP DTOs. A package-boundary test rejects imports from the
core, adapters, ports, domain, store, provider, or status packages. Canonical
control and observation go through `/v1` routes. Terminal I/O does not: the UI
creates the zmx session, displays its public Terminal ID, and leaves attachment
to direct `zmx attach` in another terminal.

## What the prototype makes legible

The thread list and detail panel keep combined activity, foreground Turn,
background activity, pending count, and Terminal lifecycle/reachability
visible as separate dimensions. ACP event history is projected into a small
conversation view: submitted user prompts remain visible and streamed
assistant chunks coalesce into readable responses. Approval and question
screens show human labels while returning the selected opaque option ID, or
free text when the request has no options. TUI threads instead show lifecycle
and status events plus the direct zmx command.

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

The isolated profiles were authenticated and `make smoke-acp` passed against
both official adapters, including allow, deny, cancellation, and exact reload.
A separate live acceptance run created a Claude TUI Terminal, attached with the
installed zmx client in a real PTY, submitted a no-tool marker prompt, received
the exact response, and observed normalized `idle -> working -> idle` events in
the core. That run also caught Claude Code's fresh-profile onboarding marker
bug; the launcher now repairs the missing marker only after
`claude auth status` succeeds.

## API awkwardness found

- `GET /v1/terminals` reconciles the external inventory and can fail while
  Threads and events remain healthy. The client treats that as degraded
  Terminal state and keeps the rest of the UI usable.
- Normalized assistant output is exposed as small delta events. The play client
  assembles those into a sufficient test transcript, but a production
  conversation view will want a canonical message projection rather than
  client-side delta assembly.
- The HTTP byte-stream attach experiment produced broken raw-mode and resize
  behavior. It was removed: this prototype now tests only zmx creation and
  status tracking, while a real terminal attaches directly through zmx.
- Local-only Thread creation means a remotely connected play client can
  observe and control existing resources but receives the canonical
  `local_only` error when creating one. That trust boundary needs an explicit
  product decision before a remote native client ships.
