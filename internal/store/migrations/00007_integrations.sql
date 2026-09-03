-- Integrations (ATC-294): the tool-level identity is the Integration.
-- Terminals record the qualified App (integration/app) they were
-- launched with instead of an agent; threads record their producing
-- Integration (the namespace of the private identity), the App that
-- started them when reliably known at creation, and the
-- Integration-scoped agent id as mutable reported metadata. Pre-launch,
-- nothing is carried over: the agent column is dropped (an old label is
-- not App provenance, so surviving launches become plain terminals whose
-- hook registrations are reaped at boot) and the thread tables are
-- recreated with the final shape — thread rows are re-observed into
-- existence by their Integrations.
-- +goose Up
ALTER TABLE terminals DROP COLUMN agent;
ALTER TABLE terminals ADD COLUMN app_id TEXT;

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
    -- Set at first observation; immutable. Deleting a project
    -- cascade-deletes its thread records (unlike terminals, which block
    -- the delete).
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- Last terminal observed holding the conversation; NULL once that
    -- terminal is deleted, and always for threads an external program
    -- owns. Liveness (the active thread) is derived at runtime, never
    -- stored.
    terminal_id TEXT REFERENCES terminals (id) ON DELETE SET NULL,
    -- Observed best-effort metadata; NULL means never observed.
    title TEXT,
    -- Once a user sets the title through ATC, observation never
    -- overwrites it. Internal flag, not wire-visible.
    title_user_set INTEGER NOT NULL DEFAULT 0,
    model TEXT,
    effort TEXT,
    -- Provider-reported working directory — resume can happen from a
    -- different directory than the terminal's.
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
