-- Thread provenance (ATC-285): every thread records the adapter that
-- produced it — a local TUI launcher (claude, codex) or an external
-- program ATC observes (t3code) — and the private identity mapping is
-- keyed by that adapter. agent becomes a plain, mutable label that may be
-- absent (an observed thread with no session yet). Pre-launch, the tables
-- are recreated with the final shape rather than patched: thread rows are
-- re-observed into existence by their adapters.
-- +goose Up
DROP TABLE thread_identities;
DROP TABLE threads;

CREATE TABLE threads (
    id TEXT PRIMARY KEY,
    -- Adapter that produced the thread; immutable.
    adapter TEXT NOT NULL,
    -- Agent label as the adapter reports it; NULL when not reported.
    agent TEXT,
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

-- The private identity mapping: (adapter, provider conversation id) to
-- thread. Provider conversation ids never leave the server except as the
-- adapter's own deep links; deleting a thread removes its mapping, so a
-- later observation of the same conversation deliberately mints a fresh
-- record.
CREATE TABLE thread_identities (
    adapter TEXT NOT NULL,
    provider_conversation_id TEXT NOT NULL,
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    PRIMARY KEY (adapter, provider_conversation_id)
) STRICT;
