-- +goose Up
CREATE TABLE workspaces (
	id TEXT PRIMARY KEY NOT NULL CHECK (id <> ''),
	project_id TEXT NOT NULL REFERENCES projects(id),
	name TEXT NOT NULL CHECK (name <> ''),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	archived_at TEXT
);

CREATE INDEX workspaces_project_created_idx ON workspaces(project_id, created_at DESC);
CREATE INDEX workspaces_archived_at_idx ON workspaces(archived_at);

-- Sessions move from projects to workspaces as a clean break: the table is
-- rebuilt and every pre-workspace session row is destroyed. Projects are
-- preserved. No data migration is attempted, so an old database can never
-- brick the server. Compared to the 0002 shape: project_id is gone,
-- workspace_id is required, and action is nullable (NULL means the
-- Interactive Shell).
DROP TABLE sessions;
CREATE TABLE sessions (
	id TEXT PRIMARY KEY NOT NULL CHECK (id <> ''),
	name TEXT,
	action TEXT CHECK (action IS NULL OR action <> ''),
	environment TEXT NOT NULL CHECK (environment <> ''),
	params TEXT NOT NULL CHECK (json_valid(params) AND json_type(params) = 'object'),
	working_dir TEXT NOT NULL CHECK (working_dir <> ''),
	prompt TEXT,
	status TEXT NOT NULL CHECK (status IN ('starting', 'running', 'failed', 'terminated')),
	failure_reason TEXT,
	failure_code TEXT,
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	terminated_at TEXT,
	archived_at TEXT
);

CREATE INDEX sessions_created_at_idx ON sessions(created_at DESC);
CREATE INDEX sessions_status_created_at_idx ON sessions(status, created_at DESC);
CREATE INDEX sessions_archived_at_idx ON sessions(archived_at);
CREATE INDEX sessions_workspace_id_idx ON sessions(workspace_id);

-- +goose Down
DROP TABLE sessions;
CREATE TABLE sessions (
	id TEXT PRIMARY KEY NOT NULL CHECK (id <> ''),
	name TEXT,
	action TEXT NOT NULL CHECK (action <> ''),
	environment TEXT NOT NULL CHECK (environment <> ''),
	params TEXT NOT NULL CHECK (json_valid(params) AND json_type(params) = 'object'),
	working_dir TEXT NOT NULL CHECK (working_dir <> ''),
	prompt TEXT,
	status TEXT NOT NULL CHECK (status IN ('starting', 'running', 'failed', 'terminated')),
	failure_reason TEXT,
	failure_code TEXT,
	project_id TEXT REFERENCES projects(id),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	terminated_at TEXT,
	archived_at TEXT
);

CREATE INDEX sessions_created_at_idx ON sessions(created_at DESC);
CREATE INDEX sessions_status_created_at_idx ON sessions(status, created_at DESC);
CREATE INDEX sessions_archived_at_idx ON sessions(archived_at);
CREATE INDEX sessions_project_id_idx ON sessions(project_id);

DROP INDEX workspaces_archived_at_idx;
DROP INDEX workspaces_project_created_idx;
DROP TABLE workspaces;
