-- Repository queries (sqlc input). Nothing outside a repository speaks
-- SQL; internal/store/terminals.go and internal/store/projects.go are the
-- only consumers.

-- Insertion doubles as the mint-time ID collision check: a conflicting id
-- inserts zero rows and the caller re-rolls, with no check-then-insert
-- window.
-- name: InsertTerminal :execrows
INSERT INTO terminals (id, project_id, name, directory, command, app_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
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

-- name: ListProjects :many
SELECT * FROM projects ORDER BY created_at, id;

-- RETURNING makes the mutation and the read one operation: a rename either
-- fails with nothing committed or returns the committed row, so the caller
-- can never observe a committed write as an error.
-- name: UpdateProjectName :one
UPDATE projects SET name = ?, updated_at = ? WHERE id = ? RETURNING *;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = ?;

-- name: InsertThread :execrows
INSERT INTO threads (id, integration_id, app_id, agent_id, project_id, terminal_id, title, title_user_set,
    model, effort, cwd, permission_mode, status, last_error, last_evidence_at, archived, archived_at,
    created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: ListThreads :many
SELECT * FROM threads ORDER BY created_at, id;

-- One broad update: the domain service owns the view and serializes
-- mutations, so every mutable column is written from the record as one
-- statement instead of a query per verb.
-- name: UpdateThread :execrows
UPDATE threads SET agent_id = ?, terminal_id = ?, title = ?, title_user_set = ?, model = ?, effort = ?,
    cwd = ?, permission_mode = ?, status = ?, last_error = ?, last_evidence_at = ?,
    archived = ?, archived_at = ?, updated_at = ?
WHERE id = ?;

-- name: DeleteThread :execrows
DELETE FROM threads WHERE id = ?;

-- name: InsertThreadIdentity :execrows
INSERT INTO thread_identities (integration_id, provider_conversation_id, thread_id)
VALUES (?, ?, ?)
ON CONFLICT (integration_id, provider_conversation_id) DO NOTHING;

-- name: ListThreadIdentities :many
SELECT * FROM thread_identities;
