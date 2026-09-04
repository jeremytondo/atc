-- The latest turn's final response (ATC-303): the provider-identified
-- final assistant message, recovered after the turn ends and stored in
-- full. NULL until recovered; replaced with the rest of the turn columns
-- when a newer turn becomes the latest.
-- +goose Up
ALTER TABLE threads ADD COLUMN turn_response TEXT;
