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
    turn_completed_at, turn_error, turn_response, pending_turn_id, pending_turn_prior, pending_turn_submitted_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
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
    turn_started_at = ?, turn_completed_at = ?, turn_error = ?, turn_response = ?, pending_turn_id = ?,
    pending_turn_prior = ?, pending_turn_submitted_at = ?
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

-- Thread messages (ATC-307): the durable identity of each submission.
-- name: InsertThreadMessage :execrows
INSERT INTO thread_messages (id, thread_id, key, text, turn_id, delivery, detail, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: GetThreadMessageByKey :one
SELECT * FROM thread_messages WHERE thread_id = ? AND key = ?;

-- name: GetThreadMessage :one
SELECT * FROM thread_messages WHERE id = ?;

-- name: UpdateThreadMessageDelivery :execrows
UPDATE thread_messages SET delivery = ?, detail = ?, updated_at = ? WHERE id = ?;

-- Keeps a thread's newest messages only; the domain names the bound.
-- name: PruneThreadMessages :exec
DELETE FROM thread_messages WHERE id IN (
    SELECT older.id FROM thread_messages AS older WHERE older.thread_id = ?
    ORDER BY older.created_at DESC, older.id DESC LIMIT -1 OFFSET ?
);

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

-- Linear sessions (ATC-302). Insertion is the duplicate-session check: a
-- second `created` delivery for one session inserts nothing.
-- name: InsertLinearSession :execrows
INSERT INTO linear_sessions (id, prompt, state, thread_id, turn_id, noticed_status, completed_seen_at, outcome, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: GetLinearSession :one
SELECT * FROM linear_sessions WHERE id = ?;

-- name: ListOpenLinearSessions :many
SELECT * FROM linear_sessions WHERE state != 'done' ORDER BY created_at, id;

-- name: CountLinearSessions :one
SELECT
    COUNT(*) FILTER (WHERE state != 'done') AS open,
    COUNT(*) AS total
FROM linear_sessions;

-- name: UpdateLinearSession :execrows
UPDATE linear_sessions SET prompt = ?, state = ?, thread_id = ?, turn_id = ?, noticed_status = ?,
    completed_seen_at = ?, outcome = ?, updated_at = ?
WHERE id = ?;

-- Linear outbox (ATC-302). The key is the deduplication.
-- name: InsertLinearOutbox :execrows
INSERT INTO linear_outbox (id, session_id, kind, body, attempts, next_attempt_at, created_at)
VALUES (?, ?, ?, ?, 0, ?, ?)
ON CONFLICT (id) DO NOTHING;

-- name: ListDueLinearOutbox :many
SELECT * FROM linear_outbox
WHERE sent_at IS NULL AND failed IS NULL AND next_attempt_at <= ?
ORDER BY next_attempt_at, created_at, id
LIMIT ?;

-- name: CountPendingLinearOutbox :one
SELECT COUNT(*) FROM linear_outbox WHERE sent_at IS NULL AND failed IS NULL;

-- name: MarkLinearOutboxSent :execrows
UPDATE linear_outbox SET sent_at = ? WHERE id = ? AND sent_at IS NULL;

-- name: MarkLinearOutboxFailed :execrows
UPDATE linear_outbox SET failed = ?, attempts = ? WHERE id = ? AND sent_at IS NULL;

-- name: RetryLinearOutbox :execrows
UPDATE linear_outbox SET attempts = ?, next_attempt_at = ? WHERE id = ? AND sent_at IS NULL;

-- Sent and refused rows are receipts; they go once the window that could
-- repeat them has passed. Refused rows carry no completion time, so their
-- creation time bounds them.
-- name: PruneLinearOutbox :execrows
DELETE FROM linear_outbox
WHERE (sent_at IS NOT NULL AND sent_at < sqlc.arg(cutoff))
   OR (failed IS NOT NULL AND created_at < sqlc.arg(cutoff));
