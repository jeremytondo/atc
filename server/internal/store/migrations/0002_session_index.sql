-- +goose Up
ALTER TABLE sessions ADD COLUMN session_index INTEGER;

WITH ranked AS (
	SELECT
		id,
		ROW_NUMBER() OVER (
			PARTITION BY workspace_id
			ORDER BY created_at, id
		) AS session_index
	FROM sessions
)
UPDATE sessions
SET session_index = (
	SELECT ranked.session_index
	FROM ranked
	WHERE ranked.id = sessions.id
);

CREATE UNIQUE INDEX sessions_workspace_session_index_idx
	ON sessions(workspace_id, session_index);

-- +goose Down
DROP INDEX sessions_workspace_session_index_idx;
ALTER TABLE sessions DROP COLUMN session_index;
