-- Projects (ATC-256): ATC's unit of organization. Everything belongs to
-- exactly one project; the canonical directory is the identity and the
-- answer to where new things in the project start. Timestamps are
-- fixed-width RFC 3339 UTC text (store.TimeFormat).
-- +goose Up
CREATE TABLE projects (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    -- Canonical form only: absolute, cleaned, symlinks resolved.
    directory TEXT NOT NULL UNIQUE,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

-- Breaking reset (ATC-256): pre-existing terminal rows do not survive.
-- Terminals are recreated with a required project reference; no
-- assignment logic, no seeded default project.
DROP TABLE terminals;
CREATE TABLE terminals (
    id TEXT PRIMARY KEY,
    -- The FK is the backstop behind the domain checks: a terminal can
    -- never reference a missing project, and a project with terminals
    -- can never be deleted.
    project_id TEXT NOT NULL REFERENCES projects (id),
    name TEXT NOT NULL,
    -- Copied from the project at create time; immutable.
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
