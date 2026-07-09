-- +goose Up
CREATE TABLE projects (
	id TEXT PRIMARY KEY NOT NULL CHECK (id <> ''),
	name TEXT NOT NULL CHECK (name <> ''),
	working_dir TEXT NOT NULL CHECK (working_dir <> ''),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	archived_at TEXT
);

CREATE INDEX projects_created_at_idx ON projects(created_at DESC);
CREATE INDEX projects_archived_at_idx ON projects(archived_at);

-- Projects are never deleted, so the FK needs no ON DELETE action.
ALTER TABLE sessions ADD COLUMN project_id TEXT REFERENCES projects(id);
CREATE INDEX sessions_project_id_idx ON sessions(project_id);

-- +goose Down
DROP INDEX sessions_project_id_idx;
ALTER TABLE sessions DROP COLUMN project_id;
DROP TABLE projects;
