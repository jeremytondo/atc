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
}

// CreateProjectInput is the record stored when a project is created.
type CreateProjectInput struct {
	ID         string
	Name       string
	WorkingDir string
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

// ListProjects loads projects newest-first.
func (s *Store) ListProjects(ctx context.Context) ([]Project, error) {
	query := selectProjectSQL
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

// DeleteProject removes a project row. Deletion is allowed only when the
// project has zero workspaces; the check and the delete share one transaction
// so a workspace created concurrently cannot slip through. Files are never
// touched.
func (s *Store) DeleteProject(ctx context.Context, id string) error {
	return s.WithTx(ctx, func(tx *Tx) error {
		if _, err := scanProject(tx.q.runner.QueryRowContext(ctx, selectProjectSQL+` WHERE id = ?`, id)); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("%w: %s", ErrProjectNotFound, id)
			}
			return fmt.Errorf("delete project %s: %w", id, err)
		}
		var workspaces int
		if err := tx.q.runner.QueryRowContext(ctx, `
		SELECT count(*) FROM workspaces WHERE project_id = ?`, id).Scan(&workspaces); err != nil {
			return fmt.Errorf("delete project %s: count workspaces: %w", id, err)
		}
		if workspaces > 0 {
			return fmt.Errorf("%w: %s", ErrProjectHasWorkspaces, id)
		}
		if _, err := tx.q.runner.ExecContext(ctx, `DELETE FROM projects WHERE id = ?`, id); err != nil {
			return fmt.Errorf("delete project %s: %w", id, err)
		}
		return nil
	})
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
		updated_at`

const selectProjectSQL = `
SELECT` + projectColumnsSQL + `
	FROM projects`

func scanProject(row scanner) (Project, error) {
	var project Project
	var createdAt, updatedAt string
	if err := row.Scan(
		&project.ID,
		&project.Name,
		&project.WorkingDir,
		&createdAt,
		&updatedAt,
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
	return project, nil
}
