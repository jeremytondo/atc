package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Workspace is one persisted atc-owned workspace record. Like Session and
// Project, it carries no JSON tags; the API layer owns wire types and
// serialization.
type Workspace struct {
	ID         string
	ProjectID  string
	Name       string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

// CreateWorkspaceInput is the record stored when a workspace is created.
type CreateWorkspaceInput struct {
	ID        string
	ProjectID string
	Name      string
}

// WorkspaceListFilter controls workspace list queries. Archived workspaces
// are hidden by default; ProjectID is optional (empty lists all workspaces).
type WorkspaceListFilter struct {
	IncludeArchived bool
	ProjectID       string
}

// CreateWorkspace inserts a new workspace. The single-statement guard makes
// the insert atomic with the project check (mirroring the guarded session
// insert), so a create racing a project archive cannot slip through.
func (s *Store) CreateWorkspace(ctx context.Context, input CreateWorkspaceInput) (Workspace, error) {
	q := s.queries()
	now := q.nowUTC()
	created, err := scanWorkspace(q.runner.QueryRowContext(ctx, `
	INSERT INTO workspaces (id, project_id, name, created_at, updated_at)
	SELECT ?, ?, ?, ?, ?
	WHERE EXISTS (SELECT 1 FROM projects WHERE id = ? AND archived_at IS NULL)
	RETURNING`+workspaceColumnsSQL,
		input.ID,
		input.ProjectID,
		input.Name,
		formatTime(now),
		formatTime(now),
		input.ProjectID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, q.classifyProjectGuardFailure(ctx, input.ProjectID)
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("create workspace %s: %w", input.ID, err)
	}
	return created, nil
}

// GetWorkspace loads a workspace by id.
func (s *Store) GetWorkspace(ctx context.Context, id string) (Workspace, error) {
	return s.queries().GetWorkspace(ctx, id)
}

func (q queries) GetWorkspace(ctx context.Context, id string) (Workspace, error) {
	workspace, err := scanWorkspace(q.runner.QueryRowContext(ctx, selectWorkspaceSQL+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, id)
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("get workspace %s: %w", id, err)
	}
	return workspace, nil
}

// ListWorkspaces loads workspaces newest-first. Archived records are excluded
// unless IncludeArchived is true; a non-empty ProjectID restricts the list to
// one project's workspaces.
func (s *Store) ListWorkspaces(ctx context.Context, filter WorkspaceListFilter) ([]Workspace, error) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 1)
	if !filter.IncludeArchived {
		clauses = append(clauses, "archived_at IS NULL")
	}
	if filter.ProjectID != "" {
		clauses = append(clauses, "project_id = ?")
		args = append(args, filter.ProjectID)
	}

	query := selectWorkspaceSQL
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	// rowid breaks created_at ties by insertion order, matching session lists.
	query += " ORDER BY created_at DESC, rowid DESC"

	rows, err := s.queries().runner.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	defer rows.Close()

	workspaces := make([]Workspace, 0)
	for rows.Next() {
		workspace, err := scanWorkspace(rows)
		if err != nil {
			return nil, fmt.Errorf("list workspaces: %w", err)
		}
		workspaces = append(workspaces, workspace)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	return workspaces, nil
}

// RenameWorkspace updates a workspace's name. Renaming is allowed while the
// workspace is archived.
func (s *Store) RenameWorkspace(ctx context.Context, id, name string) (Workspace, error) {
	if strings.TrimSpace(name) == "" {
		return Workspace{}, errors.New("rename workspace: name is required")
	}
	q := s.queries()
	now := q.nowUTC()
	return q.updateOneWorkspace(ctx, id, "rename", `
	UPDATE workspaces
	SET name = ?, updated_at = ?
	WHERE id = ?
	RETURNING`+workspaceColumnsSQL,
		name, formatTime(now), id,
	)
}

// ArchiveWorkspace archives a workspace. Archiving an archived workspace is a
// no-op that returns the current record. A workspace with a starting or
// live session cannot be archived; the check and the update share one
// transaction so a session starting concurrently cannot slip through.
func (s *Store) ArchiveWorkspace(ctx context.Context, id string) (Workspace, error) {
	var archived Workspace
	err := s.WithTx(ctx, func(tx *Tx) error {
		active, err := tx.q.countActiveWorkspaceSessions(ctx, id)
		if err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("%w: %s", ErrWorkspaceHasActiveSessions, id)
		}
		now := tx.q.nowUTC()
		archived, err = tx.q.updateOneWorkspace(ctx, id, "archive", `
		UPDATE workspaces
		SET archived_at = COALESCE(archived_at, ?),
			updated_at = CASE WHEN archived_at IS NULL THEN ? ELSE updated_at END
		WHERE id = ?
		RETURNING`+workspaceColumnsSQL,
			formatTime(now), formatTime(now), id,
		)
		return err
	})
	if err != nil {
		return Workspace{}, err
	}
	return archived, nil
}

// UnarchiveWorkspace reactivates a workspace. Unarchiving an active workspace
// is a no-op that returns the current record; unarchiving under an archived
// project is rejected so the "archived project has only archived workspaces"
// invariant holds.
func (s *Store) UnarchiveWorkspace(ctx context.Context, id string) (Workspace, error) {
	var unarchived Workspace
	err := s.WithTx(ctx, func(tx *Tx) error {
		workspace, err := tx.q.GetWorkspace(ctx, id)
		if err != nil {
			return err
		}
		project, err := scanProject(tx.q.runner.QueryRowContext(ctx, selectProjectSQL+` WHERE id = ?`, workspace.ProjectID))
		if err != nil {
			return fmt.Errorf("unarchive workspace %s: load project: %w", id, err)
		}
		if project.ArchivedAt != nil {
			return fmt.Errorf("%w: %s", ErrProjectArchived, workspace.ProjectID)
		}
		now := tx.q.nowUTC()
		unarchived, err = tx.q.updateOneWorkspace(ctx, id, "unarchive", `
		UPDATE workspaces
		SET updated_at = CASE WHEN archived_at IS NOT NULL THEN ? ELSE updated_at END,
			archived_at = NULL
		WHERE id = ?
		RETURNING`+workspaceColumnsSQL,
			formatTime(now), id,
		)
		return err
	})
	if err != nil {
		return Workspace{}, err
	}
	return unarchived, nil
}

// DeleteWorkspace removes a workspace and all of its session rows in one
// transaction. The active-session re-check inside the transaction is the
// whole concurrency story for deletion: a session start that committed after
// the caller ended everything makes the delete fail with
// ErrWorkspaceHasActiveSessions and the user retries. Files are never
// touched.
func (s *Store) DeleteWorkspace(ctx context.Context, id string) error {
	return s.WithTx(ctx, func(tx *Tx) error {
		if _, err := tx.q.GetWorkspace(ctx, id); err != nil {
			return err
		}
		active, err := tx.q.countActiveWorkspaceSessions(ctx, id)
		if err != nil {
			return err
		}
		if active > 0 {
			return fmt.Errorf("%w: %s", ErrWorkspaceHasActiveSessions, id)
		}
		if _, err := tx.q.runner.ExecContext(ctx, `DELETE FROM sessions WHERE workspace_id = ?`, id); err != nil {
			return fmt.Errorf("delete workspace %s sessions: %w", id, err)
		}
		if _, err := tx.q.runner.ExecContext(ctx, `DELETE FROM workspaces WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete workspace %s: %w", id, err)
		}
		return nil
	})
}

func (q queries) countActiveWorkspaceSessions(ctx context.Context, workspaceID string) (int, error) {
	var active int
	if err := q.runner.QueryRowContext(ctx, `
	SELECT count(*) FROM sessions WHERE workspace_id = ? AND status IN (?, ?)`,
		workspaceID, StatusStarting, StatusLive,
	).Scan(&active); err != nil {
		return 0, fmt.Errorf("count active sessions for workspace %s: %w", workspaceID, err)
	}
	return active, nil
}

func (q queries) updateOneWorkspace(ctx context.Context, id, action, query string, args ...any) (Workspace, error) {
	workspace, err := scanWorkspace(q.runner.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Workspace{}, fmt.Errorf("%w: %s", ErrWorkspaceNotFound, id)
	}
	if err != nil {
		return Workspace{}, fmt.Errorf("%s workspace %s: %w", action, id, err)
	}
	return workspace, nil
}

const workspaceColumnsSQL = `
		id,
		project_id,
		name,
		created_at,
		updated_at,
		archived_at`

const selectWorkspaceSQL = `
SELECT` + workspaceColumnsSQL + `
	FROM workspaces`

func scanWorkspace(row scanner) (Workspace, error) {
	var workspace Workspace
	var createdAt, updatedAt string
	var archivedAt sql.NullString
	if err := row.Scan(
		&workspace.ID,
		&workspace.ProjectID,
		&workspace.Name,
		&createdAt,
		&updatedAt,
		&archivedAt,
	); err != nil {
		return Workspace{}, err
	}

	var err error
	workspace.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Workspace{}, fmt.Errorf("parse created_at: %w", err)
	}
	workspace.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Workspace{}, fmt.Errorf("parse updated_at: %w", err)
	}
	workspace.ArchivedAt, err = parseOptionalTime(archivedAt)
	if err != nil {
		return Workspace{}, fmt.Errorf("parse archived_at: %w", err)
	}
	return workspace, nil
}
