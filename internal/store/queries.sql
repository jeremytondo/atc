-- Terminals repository queries (sqlc input). Nothing outside a repository
-- speaks SQL; internal/store/terminals.go is the only consumer.

-- Insertion doubles as the mint-time ID collision check: a conflicting id
-- inserts zero rows and the caller re-rolls, with no check-then-insert
-- window.
-- name: InsertTerminal :execrows
INSERT INTO terminals (id, name, directory, app, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: ListTerminals :many
SELECT * FROM terminals ORDER BY created_at, id;

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
