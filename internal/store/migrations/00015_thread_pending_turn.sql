-- Pending turns (ATC-307). A submitted turn the provider has not started
-- is no longer held in the latest-turn columns — it displaced a turn the
-- provider was still running, and that turn's end was lost — but in its
-- own slot: the id the submission returned, the provider turn the thread
-- held at submission (so that turn re-reported is not mistaken for the
-- submitted one starting; '' for none), and when it was submitted. NULL
-- pending_turn_id means no submission is pending. The pre-launch shape
-- is rewritten rather than carried over.
-- +goose Up
ALTER TABLE threads DROP COLUMN turn_submitted_prior;
ALTER TABLE threads ADD COLUMN pending_turn_id TEXT;
ALTER TABLE threads ADD COLUMN pending_turn_prior TEXT;
ALTER TABLE threads ADD COLUMN pending_turn_submitted_at TEXT;
