-- A submitted turn's binding survives the process (ATC-302): the provider
-- turn the thread held before the submission is recorded with the
-- provisional turn, so after a restart or a reconnect the first provider
-- turn reported that is not the prior one still binds to the submitted
-- id. NULL means the latest turn is not an unbound submission; '' means a
-- submission on a thread that had no turn before.
-- +goose Up
ALTER TABLE threads ADD COLUMN turn_submitted_prior TEXT;
