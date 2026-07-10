package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Project is one persisted atc-owned project record. Like Session, it
// carries no JSON tags; the API layer owns wire types and serialization.
type Project struct {
	ID         string
	Name       string
	WorkingDir string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

// CreateProjectInput is the record stored when a project is created.
type CreateProjectInput struct {
	ID         string
	Name       string
	WorkingDir string
}

// ProjectListFilter controls project list queries. Archived projects are
// hidden by default.
type ProjectListFilter struct {
	IncludeArchived bool
}

// CreateProject inserts a new project.
func (s *Store) CreateProject(ctx context.Context, input CreateProjectInput) (Project, error) {
	q := s.queries()
	now := q.nowUTC()
	created, err := scanProject(q.runner.QueryRowContext(ctx, `
	INSERT INTO projects (id, name, working_dir, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?)
	RETURNING`+projectColumnsSQL,
		input.ID,
		input.Name,
		input.WorkingDir,
		formatTime(now),
		formatTime(now),
	))
	if err != nil {
		return Project{}, fmt.Errorf("create project %s: %w", input.ID, err)
	}
	return created, nil
}

// GetProject loads a project by id.
func (s *Store) GetProject(ctx context.Context, id string) (Project, error) {
	project, err := scanProject(s.queries().runner.QueryRowContext(ctx, selectProjectSQL+` WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, id)
	}
	if err != nil {
		return Project{}, fmt.Errorf("get project %s: %w", id, err)
	}
	return project, nil
}

// ListProjects loads projects newest-first. Archived records are excluded
// unless IncludeArchived is true.
func (s *Store) ListProjects(ctx context.Context, filter ProjectListFilter) ([]Project, error) {
	query := selectProjectSQL
	if !filter.IncludeArchived {
		query += " WHERE archived_at IS NULL"
	}
	// rowid breaks created_at ties by insertion order, matching session lists.
	query += " ORDER BY created_at DESC, rowid DESC"

	rows, err := s.queries().runner.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	defer rows.Close()

	projects := make([]Project, 0)
	for rows.Next() {
		project, err := scanProject(rows)
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		projects = append(projects, project)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

// RenameProject updates a project's name.
func (s *Store) RenameProject(ctx context.Context, id, name string) (Project, error) {
	if strings.TrimSpace(name) == "" {
		return Project{}, errors.New("rename project: name is required")
	}
	q := s.queries()
	now := q.nowUTC()
	return q.updateOneProject(ctx, id, "rename", `
	UPDATE projects
	SET name = ?, updated_at = ?
	WHERE id = ?
	RETURNING`+projectColumnsSQL,
		name, formatTime(now), id,
	)
}

// ArchiveProject archives a project. Archiving an archived project is a
// no-op that returns the current record. A project with a starting or
// running session cannot be archived; the check and the update share one
// transaction so a session starting concurrently cannot slip through.
func (s *Store) ArchiveProject(ctx context.Context, id string) (Project, error) {
	var archived Project
	err := s.WithTx(ctx, func(tx *Tx) error {
		var err error
		archived, err = tx.q.archiveProject(ctx, id)
		return err
	})
	if err != nil {
		return Project{}, err
	}
	return archived, nil
}

func (q queries) archiveProject(ctx context.Context, id string) (Project, error) {
	var active int
	if err := q.runner.QueryRowContext(ctx, `
	SELECT count(*) FROM sessions WHERE project_id = ? AND status IN (?, ?)`,
		id, StatusStarting, StatusRunning,
	).Scan(&active); err != nil {
		return Project{}, fmt.Errorf("archive project %s: count active sessions: %w", id, err)
	}
	if active > 0 {
		return Project{}, fmt.Errorf("%w: %s", ErrProjectHasActiveSessions, id)
	}
	now := q.nowUTC()
	return q.updateOneProject(ctx, id, "archive", `
	UPDATE projects
	SET archived_at = COALESCE(archived_at, ?),
		updated_at = CASE WHEN archived_at IS NULL THEN ? ELSE updated_at END
	WHERE id = ?
	RETURNING`+projectColumnsSQL,
		formatTime(now), formatTime(now), id,
	)
}

// UnarchiveProject reactivates a project. Unarchiving an active project is a
// no-op that returns the current record.
func (s *Store) UnarchiveProject(ctx context.Context, id string) (Project, error) {
	q := s.queries()
	now := q.nowUTC()
	return q.updateOneProject(ctx, id, "unarchive", `
	UPDATE projects
	SET updated_at = CASE WHEN archived_at IS NOT NULL THEN ? ELSE updated_at END,
		archived_at = NULL
	WHERE id = ?
	RETURNING`+projectColumnsSQL,
		formatTime(now), id,
	)
}

func (q queries) updateOneProject(ctx context.Context, id, action, query string, args ...any) (Project, error) {
	project, err := scanProject(q.runner.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, fmt.Errorf("%w: %s", ErrProjectNotFound, id)
	}
	if err != nil {
		return Project{}, fmt.Errorf("%s project %s: %w", action, id, err)
	}
	return project, nil
}

const projectColumnsSQL = `
		id,
		name,
		working_dir,
		created_at,
		updated_at,
		archived_at`

const selectProjectSQL = `
SELECT` + projectColumnsSQL + `
	FROM projects`

func scanProject(row scanner) (Project, error) {
	var project Project
	var createdAt, updatedAt string
	var archivedAt sql.NullString
	if err := row.Scan(
		&project.ID,
		&project.Name,
		&project.WorkingDir,
		&createdAt,
		&updatedAt,
		&archivedAt,
	); err != nil {
		return Project{}, err
	}

	var err error
	project.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Project{}, fmt.Errorf("parse created_at: %w", err)
	}
	project.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Project{}, fmt.Errorf("parse updated_at: %w", err)
	}
	project.ArchivedAt, err = parseOptionalTime(archivedAt)
	if err != nil {
		return Project{}, fmt.Errorf("parse archived_at: %w", err)
	}
	return project, nil
}
