-- Linear conversations (ATC-309): a session bound to one Thread for as
-- long as the conversation lasts, every Linear submission into it under
-- its own identity, and every question or approval presented to Linear
-- with the exact choices offered. Answers gain the reply form: the
-- user's text as submitted, kept for the wire beside its provider form.
-- +goose Up

ALTER TABLE thread_answers ADD COLUMN reply TEXT;

-- The ATC-302 shape bound a session to one Turn and ended with it. The
-- pre-launch tables are rewritten: a session is now a durable binding to
-- a Thread, and the executions it tracks are its submissions.
DROP TABLE linear_sessions;
CREATE TABLE linear_sessions (
    -- Linear's Agent Session id.
    id TEXT PRIMARY KEY,
    -- accepted: owed a start (its prompt and retry state are the start
    -- submission's). starting: a start attempt is recorded (with
    -- thread_id once the record exists, before dispatch). bound: the
    -- Thread is known; submissions continue it. done: nothing more can
    -- happen here, per `outcome`.
    state TEXT NOT NULL CHECK (state IN ('accepted', 'starting', 'bound', 'done')),
    thread_id TEXT,
    outcome TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX linear_sessions_state ON linear_sessions (state);

-- One Linear submission ATC acts on: the mention that starts the Thread
-- ('start', keyed by the session, its text the first prompt), and each
-- prompt activity afterwards, keyed by Linear's activity id so a
-- repeated delivery finds the same row. What the user sent is kept
-- verbatim (text), with the exact ATC request and choice a selection
-- targets; the ATC operation it became (operation_id), the execution it
-- watches (turn_id), its delivery, and its outcome follow as they are
-- learned. pending: owed a dispatch, once next_attempt_at — attempts the
-- program could not take yet back off here. sent: dispatched; the
-- outcome is watched for. done: reported, per `outcome`. Rows are never
-- pruned: they are the record that keeps an old activity from being
-- dispatched twice.
CREATE TABLE linear_submissions (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES linear_sessions (id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('start', 'message', 'reply', 'choice', 'decision', 'stop')),
    text TEXT,
    request_id TEXT,
    question_id TEXT,
    value TEXT,
    state TEXT NOT NULL CHECK (state IN ('pending', 'sent', 'done')),
    delivery TEXT NOT NULL DEFAULT '' CHECK (delivery IN ('', 'accepted', 'uncertain')),
    operation_id TEXT,
    turn_id TEXT,
    attempts INTEGER NOT NULL DEFAULT 0,
    next_attempt_at TEXT NOT NULL,
    -- When the watched turn was first seen completed without a response.
    completed_seen_at TEXT,
    outcome TEXT,
    detail TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX linear_submissions_session ON linear_submissions (session_id, created_at, id);

-- One request presented to a session: the ATC approval or input request
-- id, its kind (a notice is an input request ATC cannot relay an answer
-- to, shown with its reason), and the options offered (JSON: label,
-- value, and the exact target each stands for), so a selection is
-- matched against what was offered and never by a label alone. open
-- while pending; closed once its resolution was reported.
CREATE TABLE linear_requests (
    id TEXT PRIMARY KEY,
    session_id TEXT NOT NULL REFERENCES linear_sessions (id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('approval', 'input', 'notice')),
    options TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('open', 'closed')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX linear_requests_session ON linear_requests (session_id, state);
