-- Terminals record the agent they were launched for (ATC-254): the agent
-- catalog id, set only when the server itself resolved the launch command
-- through the catalog. NULL means a plain terminal. Launch intent only —
-- liveness stays with the derived status, and an id absent from a future
-- catalog still renders as a normal terminal.
-- +goose Up
ALTER TABLE terminals ADD COLUMN agent TEXT;
