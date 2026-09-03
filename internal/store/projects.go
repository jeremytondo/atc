package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// ProjectRecord is one projects row in domain terms. Directory is the
// canonical form (paths.CanonicalDir) — the domain canonicalizes before
// the repository ever sees a path.
type ProjectRecord struct {
	ID        string
	Name      string
	Directory string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Projects is the repository for project records. Reads go to the read
// pool, mutations to the single-writer pool.
type Projects struct {
	reads  *gen.Queries
	writes *gen.Queries
}

// Insert persists a new record; false reports an ID collision (the caller
// re-rolls). A duplicate directory surfaces as the UNIQUE constraint error
// — the domain pre-checks it and this is the backstop.
func (p *Projects) Insert(ctx context.Context, record ProjectRecord) (bool, error) {
	n, err := p.writes.InsertProject(ctx, gen.InsertProjectParams{
		ID:        record.ID,
		Name:      record.Name,
		Directory: record.Directory,
		CreatedAt: formatTime(record.CreatedAt),
		UpdatedAt: formatTime(record.UpdatedAt),
	})
	return n > 0, err
}

// Get returns one record by ID; false means no such record.
func (p *Projects) Get(ctx context.Context, id string) (ProjectRecord, bool, error) {
	row, err := p.reads.GetProject(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectRecord{}, false, nil
	}
	if err != nil {
		return ProjectRecord{}, false, err
	}
	record, err := projectFrom(row)
	return record, err == nil, err
}

// List returns every record in creation order.
func (p *Projects) List(ctx context.Context) ([]ProjectRecord, error) {
	rows, err := p.reads.ListProjects(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]ProjectRecord, 0, len(rows))
	for _, row := range rows {
		record, err := projectFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Update writes the name and directory and returns the committed row in
// the same operation (RETURNING), so a committed write can never surface
// as an error; false means no such record. A directory another project
// holds surfaces as the UNIQUE constraint error — the domain pre-checks
// it and this is the backstop.
func (p *Projects) Update(ctx context.Context, id, name, directory string, at time.Time) (ProjectRecord, bool, error) {
	row, err := p.writes.UpdateProject(ctx, gen.UpdateProjectParams{
		Name: name, Directory: directory, UpdatedAt: formatTime(at), ID: id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ProjectRecord{}, false, nil
	}
	if err != nil {
		return ProjectRecord{}, false, err
	}
	record, err := projectFrom(row)
	return record, err == nil, err
}

// Delete removes the record; false means no such record. Threads that
// referenced it are unassigned by the schema (ON DELETE SET NULL).
func (p *Projects) Delete(ctx context.Context, id string) (bool, error) {
	n, err := p.writes.DeleteProject(ctx, id)
	return n > 0, err
}

func projectFrom(row gen.Project) (ProjectRecord, error) {
	record := ProjectRecord{
		ID:        row.ID,
		Name:      row.Name,
		Directory: row.Directory,
	}
	var err error
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return ProjectRecord{}, fmt.Errorf("project %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return ProjectRecord{}, fmt.Errorf("project %s updated_at: %w", row.ID, err)
	}
	return record, nil
}
