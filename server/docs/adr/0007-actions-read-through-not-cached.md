# Actions are read through from a JSON overlay

> **Terminology note (2026-07):** This ADR predates the Atelier Code rename. "Cockpit" is now the Atelier Code server (`atc`).

## Decision

Actions are resolved by reading `actions.json` at point of use and merging its
sparse entries over the built-in `claude` and `codex` defaults. Discovery,
detail reads, API writes, deletes, and session start all use the same
file-backed store.

Cockpit does not keep a long-lived Action registry cache and does not expose a
reload endpoint for Actions in v1.

## Rationale

Actions are both hand-editable operator state and authenticated API-managed
state. A read-through store keeps those paths symmetric: an app write, a delete
that reverts a built-in override, or a direct file edit is visible on the next
API request or session start without coordinating daemon reload state.

The expected file is small, and Action reads are not on a hot terminal I/O path,
so the simplicity and correctness of reading the file each time is worth more
than a cache with invalidation rules.

## Consequences

- `actions.json` is the source of truth for custom Actions and built-in
  overrides.
- Built-ins are always merged underneath the file overlay.
- A corrupt or invalid file makes discovery and session start fail with an
  operator/config error until the file is fixed.
- Writes are serialized in process and persisted with a temporary file followed
  by an atomic rename, so readers do not observe a torn write.
- File watching, SIGHUP reload, and cache invalidation are out of scope for v1.
