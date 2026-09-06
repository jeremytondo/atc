-- Linear Integration state (ATC-302): the durable binding between a Linear
-- Agent Session and the one ATC Thread and Turn it started, and the outbox
-- of Linear API calls owed for it. Both survive restarts: a session is
-- resumed from its state, and an outbox row is retried until Linear
-- records it. Rows are Integration state, not a domain — nothing here is a
-- public resource.
-- +goose Up
CREATE TABLE linear_sessions (
    -- Linear's Agent Session id.
    id TEXT PRIMARY KEY,
    -- The prompt to start the Thread with; NULL once the start is over.
    prompt TEXT,
    -- accepted: owed a start. starting: a start attempt is recorded (with
    -- thread_id and turn_id once the record exists, before dispatch).
    -- started: the Thread and Turn are known and watched. done: the
    -- outcome was reported, per `outcome`.
    state TEXT NOT NULL CHECK (state IN ('accepted', 'starting', 'started', 'done')),
    thread_id TEXT,
    turn_id TEXT,
    -- The waiting status last announced in Linear, so repeated
    -- observations of one wait announce nothing.
    noticed_status TEXT,
    -- When the Turn was first seen completed without a response, so the
    -- response recovery window is measured from evidence, not from now.
    completed_seen_at TEXT,
    outcome TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX linear_sessions_state ON linear_sessions (state);

CREATE TABLE linear_outbox (
    -- Deterministic per (session, purpose): inserting the same key twice
    -- is one send, however often the inbox or an observation repeats.
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL,
    -- 'activity' creates an Agent Activity; 'links' sets the session's
    -- external URLs.
    kind TEXT NOT NULL CHECK (kind IN ('activity', 'links')),
    -- The GraphQL input, JSON. For an activity it carries the id Linear
    -- deduplicates on, so an ambiguous retry cannot post twice.
    body BLOB NOT NULL,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    sent_at TEXT,
    -- Why Linear refused the row for good; kept as the record until pruned
    -- like a sent row.
    failed TEXT,
    created_at TEXT NOT NULL
) STRICT;
CREATE INDEX linear_outbox_due ON linear_outbox (sent_at, failed, next_attempt_at);
