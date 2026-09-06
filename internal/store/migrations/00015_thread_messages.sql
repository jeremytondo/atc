-- Pending turns and messages (ATC-307). A submitted turn the provider has
-- not started is no longer held in the latest-turn columns — it displaced
-- a turn the provider was still running, and that turn's end was lost —
-- but in its own slot: the id the submission returned, the provider turn
-- the thread held at submission (so that turn re-reported is not mistaken
-- for the submitted one starting; '' for none), and when it was
-- submitted. NULL pending_turn_id means no submission is pending. The
-- pre-launch shape is rewritten rather than carried over.
-- +goose Up
ALTER TABLE threads DROP COLUMN turn_submitted_prior;
ALTER TABLE threads ADD COLUMN pending_turn_id TEXT;
ALTER TABLE threads ADD COLUMN pending_turn_prior TEXT;
ALTER TABLE threads ADD COLUMN pending_turn_submitted_at TEXT;

-- Messages submitted to a thread: the durable identity of each
-- submission, so a lost answer is recovered as the same message — the
-- client's idempotency key finds it, and the Integration re-presents the
-- same command from the same id and text. Recent rows only: the domain
-- prunes each thread to its newest few. A thread's rows go with it.
CREATE TABLE thread_messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    -- The client's idempotency key; NULL when none was given (NULLs are
    -- distinct under the unique index, so unkeyed messages never collide).
    key TEXT,
    text TEXT NOT NULL,
    -- The ATC turn the message directs.
    turn_id TEXT NOT NULL,
    delivery TEXT NOT NULL CHECK (delivery IN ('accepted', 'rejected', 'uncertain')),
    -- The provider's reason for a rejection.
    detail TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX thread_messages_key ON thread_messages (thread_id, key);
CREATE INDEX thread_messages_thread ON thread_messages (thread_id, created_at);
