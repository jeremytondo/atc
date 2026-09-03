-- Latest turn and status detail (ATC-301): a thread's status says what
-- the agent is doing now; its latest turn says how the most recent
-- execution ATC observed ended. The two never share a column. last_error
-- goes: turn failure detail lives on the turn, session fault detail on
-- status_detail. Live claims (a running turn, like a live status) are
-- coerced to 'unknown' at boot; finished turns persist as recorded.
-- +goose Up
ALTER TABLE threads DROP COLUMN last_error;
-- The provider's own explanation of a faulted session; present only
-- while status is 'error'.
ALTER TABLE threads ADD COLUMN status_detail TEXT;
-- The latest turn: NULL turn_id means none observed or created yet. The
-- ATC-minted id is public; the provider's turn id is private, kept for
-- binding a submitted turn and re-matching after a reconnect.
ALTER TABLE threads ADD COLUMN turn_id TEXT;
ALTER TABLE threads ADD COLUMN turn_provider_id TEXT;
ALTER TABLE threads ADD COLUMN turn_state TEXT;
ALTER TABLE threads ADD COLUMN turn_started_at TEXT;
ALTER TABLE threads ADD COLUMN turn_completed_at TEXT;
ALTER TABLE threads ADD COLUMN turn_error TEXT;
