-- Terminals: ATC-owned durable facts only (ATC-251). zmx is the source of
-- truth for live session existence; these rows are reconciled against it,
-- never trusted over it. Status is derived at runtime, not stored.
-- Timestamps are fixed-width RFC 3339 UTC text (store.TimeFormat), so
-- lexical order is time order.
-- +goose Up
CREATE TABLE terminals (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    directory TEXT NOT NULL,
    -- NULL means a plain interactive shell.
    app TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    -- Stop intent, persisted before the kill is attempted, so a deliberate
    -- termination stays distinguishable even when the wrapper never writes
    -- its exit marker.
    stop_requested_at TEXT,
    -- Exit evidence copied from the wrapper's marker file. exit_code is
    -- NULL when the stop was ATC-initiated (a kill is not a meaningful
    -- program result) or when the marker carried none.
    exited_at TEXT,
    exit_code INTEGER
) STRICT;
