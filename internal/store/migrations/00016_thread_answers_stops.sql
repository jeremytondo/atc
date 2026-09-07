-- Answers and stops (ATC-308): the durable identity of each submission,
-- like messages, so a lost answer is recovered as the same operation and
-- an operation still unresolved survives a restart.
-- +goose Up

-- A message withdrawn by a confirmed stop (its turn was covered) is a
-- fourth delivery: never on the wire, where a replay of its key is the
-- withdrawal. The pre-launch table is rewritten for the wider check.
DROP TABLE thread_messages;
CREATE TABLE thread_messages (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    key TEXT,
    text TEXT NOT NULL,
    turn_id TEXT NOT NULL,
    delivery TEXT NOT NULL CHECK (delivery IN ('accepted', 'rejected', 'uncertain', 'withdrawn')),
    detail TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE UNIQUE INDEX thread_messages_key ON thread_messages (thread_id, key);
CREATE INDEX thread_messages_thread ON thread_messages (thread_id, created_at);

-- One answer submitted through ATC to a structured request: the ATC
-- request id it targets, the provider's private request id, the answers
-- in ATC's form (the wire) and the provider's (the command a retry
-- re-presents), the request's questions as they stood (so the request
-- is readable after a restart), and the outcome as the provider's
-- evidence settles it. Recent rows only, but an unresolved answer is
-- never pruned.
CREATE TABLE thread_answers (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    request_id TEXT NOT NULL,
    provider_request_id TEXT NOT NULL,
    questions TEXT NOT NULL,
    answers TEXT NOT NULL,
    provider_answers TEXT NOT NULL,
    delivery TEXT NOT NULL CHECK (delivery IN ('accepted', 'uncertain')),
    state TEXT NOT NULL CHECK (state IN ('sent', 'resolved', 'failed', 'superseded')),
    detail TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
) STRICT;
CREATE INDEX thread_answers_thread ON thread_answers (thread_id, created_at);

-- One stop operation: the client's idempotency key (NULL for none), so a
-- retry recovers this stop in whatever state rather than stopping later
-- work; its scope at acceptance — the turn that was running, the turn
-- that was pending (NULL for none), and the thread's status — so
-- recovery reconciles the original scope and never whatever runs later;
-- its delivery; and its outcome. A stop still stopping is never pruned.
CREATE TABLE thread_stops (
    id TEXT PRIMARY KEY,
    thread_id TEXT NOT NULL REFERENCES threads (id) ON DELETE CASCADE,
    key TEXT,
    state TEXT NOT NULL CHECK (state IN ('stopping', 'stopped', 'finished', 'failed')),
    delivery TEXT NOT NULL CHECK (delivery IN ('accepted', 'uncertain')),
    scope_turn TEXT,
    scope_pending TEXT,
    scope_status TEXT NOT NULL,
    detail TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    resolved_at TEXT
) STRICT;
CREATE UNIQUE INDEX thread_stops_key ON thread_stops (thread_id, key);
CREATE INDEX thread_stops_thread ON thread_stops (thread_id, created_at);
