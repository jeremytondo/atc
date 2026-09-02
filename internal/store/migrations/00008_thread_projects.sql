-- Threads are classified into projects by origin (ATC-295): a thread
-- records the canonical directory its conversation reliably originated
-- in, write-once, and references zero or one project — assigned by
-- containment at first observation or backfill, or set explicitly —
-- which project deletion clears rather than cascading. Projects keep
-- their canonical directory unique but editable. Pre-launch, the thread
-- tables are recreated with the final shape: rows are re-observed into
-- existence by their Integrations.
-- +goose Up
DROP TABLE thread_identities;
DROP TABLE threads;

CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    -- Integration that produced the thread; immutable.
    integration_id TEXT NOT NULL,
    -- Qualified App the conversation was started in, when reliably known
    -- at creation; NULL permanently otherwise. Immutable.
    app_id TEXT,
    -- Integration-scoped agent id as the Integration reports it; NULL
    -- when not reported.
    agent_id TEXT,
    -- Canonical directory the conversation originated in; NULL when no
    -- usable local directory was reported at creation. Write-once.
    initial_directory TEXT,
    -- Zero or one project. Deleting a project clears the reference; the
    -- thread survives unassigned.
    project_id TEXT REFERENCES projects (id) ON DELETE SET NULL,
    -- Terminal currently or most recently observed hosting the
    -- conversation; NULL once that terminal is deleted, and always for
    -- threads an external program owns. Liveness (the active thread) is
    -- derived at runtime, never stored.
    terminal_id TEXT REFERENCES terminals (id) ON DELETE SET NULL,
    -- Observed best-effort metadata; NULL means never observed.
    title TEXT,
    -- Once a user sets the title through ATC, observation never
    -- overwrites it. Internal flag, not wire-visible.
    title_user_set INTEGER NOT NULL DEFAULT 0,
    model TEXT,
    effort TEXT,
    -- Provider-reported current working directory, mutable best-effort
    -- runtime state.
    cwd TEXT,
    -- Provider-native permission-mode string, read-only, no normalized
    -- vocabulary.
    permission_mode TEXT,
    status TEXT NOT NULL,
    last_error TEXT,
    last_evidence_at TEXT,
    archived INTEGER NOT NULL DEFAULT 0,
    archived_at TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;

-- The private identity mapping: (integration, provider conversation id)
-- to thread. Provider conversation ids never leave the server except as
-- the Integration's own deep links; deleting a thread removes its
-- mapping, so a later observation of the same conversation deliberately
-- mints a fresh record.
CREATE TABLE thread_identities (
    integration_id TEXT NOT NULL,
    provider_conversation_id TEXT NOT NULL,
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    PRIMARY KEY (integration_id, provider_conversation_id)
) STRICT;
