# Workspace Sessions have server-owned indexes

Each Workspace Session exposes `sessionIndex`, an immutable positive integer in
one Workspace-local namespace shared by agent and terminal Sessions. It is a
human-facing address; the stable Session ID remains the API identity.

The server allocates the smallest unused index atomically when it creates the
provisional Launch Attempt. Provisional records reserve their indexes, with a
SQLite uniqueness constraint on `(workspace_id, session_index)` as the
concurrency backstop. Ended tombstones retain their indexes. Record deletion,
including failed-launch cleanup and Workspace deletion, releases them for
reuse; remaining Sessions are never renumbered.

Existing Sessions are backfilled per Workspace, oldest first by `created_at`
with Session ID as the tie-break.
