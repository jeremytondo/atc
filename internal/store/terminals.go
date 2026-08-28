package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// TerminalRecord is one terminals row in domain terms: pointers for the
// nullable facts, time.Time for the text timestamps. The SQL row shape
// never leaves this package.
type TerminalRecord struct {
	ID string
	// ProjectID is the owning project; required, immutable, and enforced
	// by the schema's foreign key.
	ProjectID string
	Name      string
	Directory string
	// App is the free-form command the terminal was created with; empty
	// means a plain interactive shell.
	App       string
	CreatedAt time.Time
	UpdatedAt time.Time
	// StopRequestedAt is the persisted stop intent (set before the kill is
	// attempted).
	StopRequestedAt *time.Time
	// ExitedAt and ExitCode are the recorded exit evidence. ExitCode is nil
	// when the stop was ATC-initiated or the marker carried no code.
	ExitedAt *time.Time
	ExitCode *int
}

// Terminals is the repository for terminal records. Reads go to the read
// pool, mutations to the single-writer pool.
type Terminals struct {
	reads  *gen.Queries
	writes *gen.Queries
}

// Insert persists a new record; false reports an ID collision (the caller
// re-rolls). Create persists before starting the session, so a record
// exists for every session ATC ever starts.
func (t *Terminals) Insert(ctx context.Context, record TerminalRecord) (bool, error) {
	n, err := t.writes.InsertTerminal(ctx, gen.InsertTerminalParams{
		ID:        record.ID,
		ProjectID: record.ProjectID,
		Name:      record.Name,
		Directory: record.Directory,
		App:       nullString(record.App),
		CreatedAt: formatTime(record.CreatedAt),
		UpdatedAt: formatTime(record.UpdatedAt),
	})
	return n > 0, err
}

// ListIDsByProject returns the IDs of the project's terminals in creation
// order — the project-empty check behind project delete's refusal.
func (t *Terminals) ListIDsByProject(ctx context.Context, projectID string) ([]string, error) {
	return t.reads.ListTerminalIDsByProject(ctx, projectID)
}

// List returns every record in creation order.
func (t *Terminals) List(ctx context.Context) ([]TerminalRecord, error) {
	rows, err := t.reads.ListTerminals(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]TerminalRecord, 0, len(rows))
	for _, row := range rows {
		record, err := recordFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// UpdateName renames the terminal; false means no such record.
func (t *Terminals) UpdateName(ctx context.Context, id, name string, at time.Time) (bool, error) {
	n, err := t.writes.UpdateTerminalName(ctx, gen.UpdateTerminalNameParams{
		Name: name, UpdatedAt: formatTime(at), ID: id,
	})
	return n > 0, err
}

// RecordStopIntent persists the delete verb's stop intent; false means no
// such record.
func (t *Terminals) RecordStopIntent(ctx context.Context, id string, at time.Time) (bool, error) {
	n, err := t.writes.RecordTerminalStopIntent(ctx, gen.RecordTerminalStopIntentParams{
		StopRequestedAt: nullString(formatTime(at)), UpdatedAt: formatTime(at), ID: id,
	})
	return n > 0, err
}

// RecordExit persists exit evidence: exitedAt is the wrapper's recorded
// exit time, observedAt the reconciliation time stamping updated_at (so a
// late-observed marker never rewinds the record). The first observation
// wins: a row that already carries evidence is left untouched.
func (t *Terminals) RecordExit(ctx context.Context, id string, exitedAt, observedAt time.Time, code *int) error {
	var exitCode sql.NullInt64
	if code != nil {
		exitCode = sql.NullInt64{Int64: int64(*code), Valid: true}
	}
	_, err := t.writes.RecordTerminalExit(ctx, gen.RecordTerminalExitParams{
		ExitedAt: nullString(formatTime(exitedAt)), ExitCode: exitCode, UpdatedAt: formatTime(observedAt), ID: id,
	})
	return err
}

// Delete removes the record; false means no such record.
func (t *Terminals) Delete(ctx context.Context, id string) (bool, error) {
	n, err := t.writes.DeleteTerminal(ctx, id)
	return n > 0, err
}

func recordFrom(row gen.Terminal) (TerminalRecord, error) {
	record := TerminalRecord{
		ID:        row.ID,
		ProjectID: row.ProjectID,
		Name:      row.Name,
		Directory: row.Directory,
		App:       row.App.String,
	}
	var err error
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return TerminalRecord{}, fmt.Errorf("terminal %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return TerminalRecord{}, fmt.Errorf("terminal %s updated_at: %w", row.ID, err)
	}
	if record.StopRequestedAt, err = parseNullTime(row.StopRequestedAt); err != nil {
		return TerminalRecord{}, fmt.Errorf("terminal %s stop_requested_at: %w", row.ID, err)
	}
	if record.ExitedAt, err = parseNullTime(row.ExitedAt); err != nil {
		return TerminalRecord{}, fmt.Errorf("terminal %s exited_at: %w", row.ID, err)
	}
	if row.ExitCode.Valid {
		code := int(row.ExitCode.Int64)
		record.ExitCode = &code
	}
	return record, nil
}

func nullString(value string) sql.NullString {
	if value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: value, Valid: true}
}

func parseNullTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid {
		return nil, nil
	}
	t, err := parseTime(value.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}
