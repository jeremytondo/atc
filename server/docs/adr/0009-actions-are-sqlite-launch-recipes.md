# ADR 0009: Actions are SQLite launch recipes

## Status

Accepted; supersedes ADR 0007.

## Decision

Actions are ordinary server-wide SQLite rows addressed only by opaque IDs.
The server reads an Action once when a session starts, copies its identity onto
the session, and launches its fixed command and literal arguments. Later Action
updates or deletion do not change existing sessions.
