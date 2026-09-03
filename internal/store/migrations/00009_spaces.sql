-- Spaces (ATC-296): the runtime container terminals belong to. A space
-- has a name and the directory terminals created in it default to; the
-- one Default space (is_default = 1) is the server's, rooted at its
-- user's home. Terminals now reference a space and never a project —
-- projects are durable codebase context for threads, not owners of live
-- sessions — so project deletion no longer blocks on terminals.
-- Pre-launch, the terminals table is recreated with the final shape;
-- the implicit delete clears thread linkage (ON DELETE SET NULL) and
-- live sessions are reaped as orphans at the next reconcile.
-- +goose Up
CREATE TABLE spaces (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    -- Canonical form only: absolute, cleaned, symlinks resolved. Not
    -- unique — spaces may share or overlap directories.
    directory TEXT NOT NULL,
    is_default INTEGER NOT NULL DEFAULT 0 CHECK (is_default IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
-- Exactly one Default space: the backstop behind the boot-time
-- get-or-create.
CREATE UNIQUE INDEX spaces_default ON spaces (is_default) WHERE is_default = 1;

DROP TABLE terminals;
CREATE TABLE terminals (
    id TEXT PRIMARY KEY,
    -- The FK is the backstop behind the domain checks: a terminal can
    -- never reference a missing space, and a space with terminals can
    -- never be deleted out from under them.
    space_id TEXT NOT NULL REFERENCES spaces (id),
    name TEXT NOT NULL,
    -- Resolved at create time; immutable.
    directory TEXT NOT NULL,
    -- NULL means a plain interactive shell; for an App launch, the
    -- Integration-composed command, never exposed.
    command TEXT,
    -- Qualified App the terminal was launched with; NULL for a plain
    -- terminal. Immutable launch intent.
    app_id TEXT,
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
