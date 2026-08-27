-- Terminals repository queries (sqlc input). Nothing outside a repository
-- speaks SQL; internal/store/terminals.go is the only consumer.

-- name: InsertTerminal :exec
INSERT INTO terminals (id, name, directory, app, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: ListTerminals :many
SELECT * FROM terminals ORDER BY created_at, id;

-- name: TerminalIDTaken :one
SELECT EXISTS (SELECT 1 FROM terminals WHERE id = ?);

-- name: UpdateTerminalName :execrows
UPDATE terminals SET name = ?, updated_at = ? WHERE id = ?;

-- name: RecordTerminalStopIntent :execrows
UPDATE terminals SET stop_requested_at = ?, updated_at = ? WHERE id = ?;

-- The first observation of an exit wins; a re-reconcile never rewrites
-- evidence.
-- name: RecordTerminalExit :execrows
UPDATE terminals SET exited_at = ?, exit_code = ?, updated_at = ?
WHERE id = ? AND exited_at IS NULL;

-- name: DeleteTerminal :execrows
DELETE FROM terminals WHERE id = ?;
