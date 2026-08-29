-- Threads (ATC-255): one row per observed provider conversation, the
-- durable index of resumable conversations. Rows are observed into
-- existence and leave only through the delete verb or a project cascade —
-- no time-based cleanup. Live statuses (working, waiting_*) are evidence
-- about a running observation and are coerced to 'unknown' at boot; idle,
-- error, and unknown persist as recorded. Timestamps are fixed-width RFC
-- 3339 UTC text (store.TimeFormat).
-- +goose Up
CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    -- Agent catalog id that owns the conversation; immutable.
    agent TEXT NOT NULL,
    -- Set from the observing terminal at first observation; immutable.
    -- Deleting a project cascade-deletes its thread records (unlike
    -- terminals, which block the delete).
    project_id TEXT NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    -- Last terminal observed holding the conversation; NULL once that
    -- terminal is deleted. Liveness (the active thread) is derived at
    -- runtime, never stored.
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

-- The private identity mapping: (agent, provider conversation id) to
-- thread. Provider conversation ids never leave the server; deleting a
-- thread removes its mapping, so a later observation of the same provider
-- conversation deliberately mints a fresh record.
CREATE TABLE thread_identities (
    agent TEXT NOT NULL,
    provider_conversation_id TEXT NOT NULL,
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    PRIMARY KEY (agent, provider_conversation_id)
) STRICT;
