-- +goose Up
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
	created_at TEXT NOT NULL,
	updated_at TEXT NOT NULL,
	terminated_at TEXT,
	archived_at TEXT
);

CREATE INDEX sessions_created_at_idx ON sessions(created_at DESC);
CREATE INDEX sessions_status_created_at_idx ON sessions(status, created_at DESC);
CREATE INDEX sessions_archived_at_idx ON sessions(archived_at);

-- +goose Down
DROP TABLE sessions;
