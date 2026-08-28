-- Rename terminals.app to command (ATC-276): the field holds a free-form
-- command run through the user's shell — a dev server or build script is
-- not an "app". NULL still means a plain interactive shell.
-- +goose Up
ALTER TABLE terminals RENAME COLUMN app TO command;
