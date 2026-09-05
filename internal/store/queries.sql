-- Repository queries (sqlc input). Nothing outside a repository speaks
-- SQL; the repositories in internal/store (terminals, spaces, projects,
-- threads) are the only consumers.

-- Insertion doubles as the mint-time ID collision check: a conflicting id
-- inserts zero rows and the caller re-rolls, with no check-then-insert
-- window.
-- name: InsertTerminal :execrows
INSERT INTO terminals (id, space_id, name, directory, command, app_id, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: ListTerminals :many
SELECT * FROM terminals ORDER BY created_at, id;

-- The two mutable terminal columns move together (a merge patch).
-- name: UpdateTerminal :execrows
UPDATE terminals SET name = ?, space_id = ?, updated_at = ? WHERE id = ?;

-- name: RecordTerminalStopIntent :execrows
UPDATE terminals SET stop_requested_at = ?, updated_at = ? WHERE id = ?;

-- The first observation of an exit wins; a re-reconcile never rewrites
-- evidence.
-- name: RecordTerminalExit :execrows
UPDATE terminals SET exited_at = ?, exit_code = ?, updated_at = ?
WHERE id = ? AND exited_at IS NULL;

-- name: DeleteTerminal :execrows
DELETE FROM terminals WHERE id = ?;

-- name: InsertSpace :execrows
INSERT INTO spaces (id, name, directory, is_default, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: ListSpaces :many
SELECT * FROM spaces ORDER BY created_at, id;

-- name: UpdateSpace :one
UPDATE spaces SET name = ?, directory = ?, updated_at = ? WHERE id = ? RETURNING *;

-- name: DeleteSpace :execrows
DELETE FROM spaces WHERE id = ?;

-- name: InsertProject :execrows
INSERT INTO projects (id, name, directory, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: GetProject :one
SELECT * FROM projects WHERE id = ?;

-- name: ListProjects :many
SELECT * FROM projects ORDER BY created_at, id;

-- RETURNING makes the mutation and the read one operation: an update
-- either fails with nothing committed or returns the committed row, so
-- the caller can never observe a committed write as an error.
-- name: UpdateProject :one
UPDATE projects SET name = ?, directory = ?, updated_at = ? WHERE id = ? RETURNING *;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE id = ?;

-- name: InsertThread :execrows
INSERT INTO threads (id, integration_id, app_id, agent_id, initial_directory, project_id, terminal_id, title,
    title_user_set, model, effort, cwd, permission_mode, status, status_detail, last_evidence_at, archived,
    archived_at, created_at, updated_at, turn_id, turn_provider_id, turn_state, turn_started_at,
    turn_completed_at, turn_error, turn_response)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: ListThreads :many
SELECT * FROM threads ORDER BY created_at, id;

-- One broad update: the domain service owns the view and serializes
-- mutations, so every mutable column is written from the record as one
-- statement instead of a query per verb.
-- name: UpdateThread :execrows
UPDATE threads SET agent_id = ?, project_id = ?, terminal_id = ?, title = ?, title_user_set = ?, model = ?, effort = ?,
    cwd = ?, permission_mode = ?, status = ?, status_detail = ?, last_evidence_at = ?,
    archived = ?, archived_at = ?, updated_at = ?, turn_id = ?, turn_provider_id = ?, turn_state = ?,
    turn_started_at = ?, turn_completed_at = ?, turn_error = ?, turn_response = ?
WHERE id = ?;

-- Backfill assigns only threads still unassigned, so a project change
-- never overwrites an association made in between.
-- name: AssignThreadProject :execrows
UPDATE threads SET project_id = ?, updated_at = ? WHERE id = ? AND project_id IS NULL;

-- name: DeleteThread :execrows
DELETE FROM threads WHERE id = ?;

-- name: InsertThreadIdentity :execrows
INSERT INTO thread_identities (integration_id, provider_conversation_id, thread_id)
VALUES (?, ?, ?)
ON CONFLICT (integration_id, provider_conversation_id) DO NOTHING;

-- name: ListThreadIdentities :many
SELECT * FROM thread_identities;

-- Webhook inbox (ATC-306). Acceptance is the deduplication: the
-- Integration-scoped unique constraint makes a redelivery insert zero rows,
-- with no check-then-insert window under the single writer.
-- name: InsertWebhookDelivery :execrows
INSERT INTO webhook_deliveries (id, integration_id, route, delivery_id, payload, state, attempts, next_attempt_at, accepted_at)
VALUES (?, ?, ?, ?, ?, 'pending', 0, ?, ?)
ON CONFLICT (integration_id, delivery_id) DO NOTHING;

-- name: CountPendingWebhookDeliveries :one
SELECT COUNT(*) FROM webhook_deliveries WHERE state = 'pending';

-- name: ListDueWebhookDeliveries :many
SELECT * FROM webhook_deliveries
WHERE state = 'pending' AND next_attempt_at <= ?
ORDER BY next_attempt_at, accepted_at, id
LIMIT ?;

-- Completion keeps the receipt and drops the payload.
-- name: CompleteWebhookDelivery :execrows
UPDATE webhook_deliveries
SET state = 'done', payload = NULL, completed_at = ?
WHERE id = ? AND state = 'pending';

-- name: FailWebhookDelivery :execrows
UPDATE webhook_deliveries
SET attempts = ?, next_attempt_at = ?
WHERE id = ? AND state = 'pending';

-- Receipts are bounded two ways: by age, and by count (oldest first).
-- Pending rows are never pruned.
-- name: PruneAgedWebhookReceipts :execrows
DELETE FROM webhook_deliveries WHERE state = 'done' AND completed_at < ?;

-- name: PruneExcessWebhookReceipts :execrows
DELETE FROM webhook_deliveries
WHERE state = 'done' AND id IN (
    SELECT id FROM webhook_deliveries WHERE state = 'done'
    ORDER BY completed_at DESC, id DESC LIMIT -1 OFFSET ?
);
