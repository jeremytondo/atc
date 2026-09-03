package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// SpaceRecord is one spaces row in domain terms. Directory is the
// canonical form (paths.CanonicalDir) — the domain canonicalizes before
// the repository ever sees a path.
type SpaceRecord struct {
	ID        string
	Name      string
	Directory string
	IsDefault bool
	CreatedAt time.Time
	UpdatedAt time.Time
}

// Spaces is the repository for space records. Reads go to the read pool,
// mutations to the single-writer pool.
type Spaces struct {
	reads  *gen.Queries
	writes *gen.Queries
}

// Spaces returns the spaces repository.
func (s *Store) Spaces() *Spaces {
	return &Spaces{reads: gen.New(s.reads), writes: gen.New(s.writes)}
}

// ErrDefaultExists reports an insert of a second Default space — the
// schema's one-default index refused it.
var ErrDefaultExists = errors.New("a Default space already exists")

// Insert persists a new record; false reports an ID collision (the caller
// re-rolls). A second Default surfaces as ErrDefaultExists.
func (p *Spaces) Insert(ctx context.Context, record SpaceRecord) (bool, error) {
	n, err := p.writes.InsertSpace(ctx, gen.InsertSpaceParams{
		ID:        record.ID,
		Name:      record.Name,
		Directory: record.Directory,
		IsDefault: boolInt(record.IsDefault),
		CreatedAt: formatTime(record.CreatedAt),
		UpdatedAt: formatTime(record.UpdatedAt),
	})
	// The driver exposes constraint failures only through the message
	// text, which names the column the partial unique index covers.
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed: spaces.is_default") {
		return false, fmt.Errorf("%w: %w", ErrDefaultExists, err)
	}
	return n > 0, err
}

// List returns every record in creation order.
func (p *Spaces) List(ctx context.Context) ([]SpaceRecord, error) {
	rows, err := p.reads.ListSpaces(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]SpaceRecord, 0, len(rows))
	for _, row := range rows {
		record, err := spaceFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Update writes the name and directory and returns the committed row in
// the same operation (RETURNING); false means no such record.
func (p *Spaces) Update(ctx context.Context, id, name, directory string, at time.Time) (SpaceRecord, bool, error) {
	row, err := p.writes.UpdateSpace(ctx, gen.UpdateSpaceParams{
		Name: name, Directory: directory, UpdatedAt: formatTime(at), ID: id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return SpaceRecord{}, false, nil
	}
	if err != nil {
		return SpaceRecord{}, false, err
	}
	record, err := spaceFrom(row)
	return record, err == nil, err
}

// Delete removes the record; false means no such record. A space that
// still owns terminals fails with ErrForeignKeyViolation — the domain
// deletes them first and this is the backstop.
func (p *Spaces) Delete(ctx context.Context, id string) (bool, error) {
	n, err := p.writes.DeleteSpace(ctx, id)
	return n > 0, foreignKeyError(err)
}

func spaceFrom(row gen.Space) (SpaceRecord, error) {
	record := SpaceRecord{
		ID:        row.ID,
		Name:      row.Name,
		Directory: row.Directory,
		IsDefault: row.IsDefault != 0,
	}
	var err error
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return SpaceRecord{}, fmt.Errorf("space %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return SpaceRecord{}, fmt.Errorf("space %s updated_at: %w", row.ID, err)
	}
	return record, nil
}
