-- Repository queries (sqlc input). Nothing outside a repository speaks
-- SQL; internal/store/terminals.go and internal/store/projects.go are the
-- only consumers.

-- Insertion doubles as the mint-time ID collision check: a conflicting id
-- inserts zero rows and the caller re-rolls, with no check-then-insert
-- window.
-- name: InsertTerminal :execrows
INSERT INTO terminals (id, project_id, name, directory, app, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: ListTerminals :many
SELECT * FROM terminals ORDER BY created_at, id;

-- The project-empty check behind project delete's refusal.
-- name: ListTerminalIDsByProject :many
SELECT id FROM terminals WHERE project_id = ? ORDER BY created_at, id;

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

-- name: InsertProject :execrows
INSERT INTO projects (id, name, directory, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- The canonical-directory uniqueness pre-check on project create.
-- name: GetProjectByDirectory :one
SELECT * FROM projects WHERE directory = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY created_at, id;

-- name: UpdateProjectName :execrows
UPDATE projects SET name = ?, updated_at = ? WHERE id = ?;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = ?;
