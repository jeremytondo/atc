-- +goose Up
CREATE TABLE projects (
	id TEXT PRIMARY KEY NOT NULL CHECK (id <> ''),
	name TEXT NOT NULL CHECK (name <> ''),
	working_dir TEXT NOT NULL CHECK (working_dir <> ''),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE INDEX projects_created_at_idx ON projects(created_at DESC);

CREATE TABLE workspaces (
	id TEXT PRIMARY KEY NOT NULL CHECK (id <> ''),
	project_id TEXT NOT NULL REFERENCES projects(id),
	name TEXT NOT NULL CHECK (name <> ''),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE INDEX workspaces_project_created_idx ON workspaces(project_id, created_at DESC);

CREATE TABLE actions (
	id TEXT PRIMARY KEY NOT NULL CHECK (id <> ''),
	name TEXT NOT NULL CHECK (name <> ''),
	description TEXT,
	enabled INTEGER NOT NULL,
	command TEXT NOT NULL CHECK (command <> ''),
	args TEXT NOT NULL CHECK (json_valid(args) AND json_type(args) = 'array'),
	is_agent INTEGER NOT NULL
);

CREATE TABLE sessions (
	id TEXT PRIMARY KEY NOT NULL CHECK (id <> ''),
	name TEXT,
	action_id TEXT,
	action_name TEXT,
	is_agent INTEGER NOT NULL DEFAULT 0,
	working_dir TEXT NOT NULL CHECK (working_dir <> ''),
	status TEXT NOT NULL CHECK (status IN ('starting', 'live', 'ended')),
	workspace_id TEXT NOT NULL REFERENCES workspaces(id),
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL
);

CREATE INDEX sessions_created_at_idx ON sessions(created_at DESC);
CREATE INDEX sessions_status_created_at_idx ON sessions(status, created_at DESC);
CREATE INDEX sessions_workspace_id_idx ON sessions(workspace_id);

INSERT INTO actions (id, name, description, enabled, command, args, is_agent) VALUES
	('act_vpj2tlg9viqd8ms52ptuvao5c4', 'Claude', 'Anthropic''s coding agent', 1, 'claude', '[]', 1),
	('act_fh9g7e6571qo53r0t647ughtfg', 'Codex', 'OpenAI''s coding agent', 1, 'codex', '[]', 1);

-- +goose Down
DROP TABLE sessions;
DROP TABLE actions;
DROP TABLE workspaces;
DROP TABLE projects;
