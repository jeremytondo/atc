-- +goose Up
DELETE FROM sessions
WHERE workspace_id IN (
	SELECT w.id
	FROM workspaces w
	JOIN projects p ON p.id = w.project_id
	WHERE w.archived_at IS NOT NULL OR p.archived_at IS NOT NULL
);

DELETE FROM workspaces
WHERE archived_at IS NOT NULL
	OR project_id IN (SELECT id FROM projects WHERE archived_at IS NOT NULL);

DELETE FROM projects WHERE archived_at IS NOT NULL;

DROP INDEX projects_archived_at_idx;
DROP INDEX workspaces_archived_at_idx;

ALTER TABLE projects DROP COLUMN archived_at;
ALTER TABLE workspaces DROP COLUMN archived_at;

-- +goose Down
ALTER TABLE projects ADD COLUMN archived_at TEXT;
ALTER TABLE workspaces ADD COLUMN archived_at TEXT;

CREATE INDEX projects_archived_at_idx ON projects(archived_at);
CREATE INDEX workspaces_archived_at_idx ON workspaces(archived_at);
