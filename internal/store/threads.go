package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// ThreadRecord is one threads row in domain terms: pointers for the
// nullable facts, time.Time for the text timestamps. The SQL row shape
// never leaves this package. Status is the persisted status string; the
// threads domain owns its vocabulary and the boot-time coercion of live
// states.
type ThreadRecord struct {
	ID string
	// Agent is the agent catalog id that owns the conversation; immutable.
	Agent string
	// ProjectID is set from the observing terminal at first observation;
	// immutable. Project deletion cascade-deletes the row.
	ProjectID string
	// TerminalID is the last terminal observed holding the conversation;
	// nil once that terminal is deleted (ON DELETE SET NULL). A pointer
	// because NULL and "" differ to the foreign key.
	TerminalID *string
	// Title and the rest of the observed metadata are best-effort; empty
	// means never observed (stored NULL). TitleUserSet records that a user
	// set the title through ATC, after which observation never overwrites
	// it.
	Title          string
	TitleUserSet   bool
	Model          string
	Effort         string
	Cwd            string
	PermissionMode string
	Status         string
	LastError      string
	LastEvidenceAt *time.Time
	Archived       bool
	ArchivedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ThreadIdentity is one row of the private identity mapping: (agent,
// provider conversation id) → thread. Provider conversation ids never
// leave the server.
type ThreadIdentity struct {
	Agent                  string
	ProviderConversationID string
	ThreadID               string
}

// Threads is the repository for thread records and their identity
// mappings. Reads go to the read pool, mutations to the single-writer
// pool; db is that pool's handle, for the one multi-statement write.
type Threads struct {
	reads  *gen.Queries
	writes *gen.Queries
	db     *sql.DB
}

// Threads returns the threads repository.
func (s *Store) Threads() *Threads {
	return &Threads{reads: gen.New(s.reads), writes: gen.New(s.writes), db: s.writes}
}

func insertThreadParams(record ThreadRecord) gen.InsertThreadParams {
	return gen.InsertThreadParams{
		ID:             record.ID,
		Agent:          record.Agent,
		ProjectID:      record.ProjectID,
		TerminalID:     nullStringPtr(record.TerminalID),
		Title:          nullString(record.Title),
		TitleUserSet:   boolInt(record.TitleUserSet),
		Model:          nullString(record.Model),
		Effort:         nullString(record.Effort),
		Cwd:            nullString(record.Cwd),
		PermissionMode: nullString(record.PermissionMode),
		Status:         record.Status,
		LastError:      nullString(record.LastError),
		LastEvidenceAt: nullTime(record.LastEvidenceAt),
		Archived:       boolInt(record.Archived),
		ArchivedAt:     nullTime(record.ArchivedAt),
		CreatedAt:      formatTime(record.CreatedAt),
		UpdatedAt:      formatTime(record.UpdatedAt),
	}
}

// InsertObserved persists a new record together with its identity mapping
// in one transaction — a thread must never exist without its mapping, or
// the next observation of the conversation would mint a duplicate. False
// reports an ID collision (the caller re-rolls); an already-mapped
// identity is an error, since the caller resolves identities before
// minting.
func (t *Threads) InsertObserved(ctx context.Context, record ThreadRecord, identity ThreadIdentity) (bool, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.InsertThread(ctx, insertThreadParams(record))
	if err != nil {
		return false, foreignKeyError(err)
	}
	if n == 0 {
		return false, nil
	}
	n, err = queries.InsertThreadIdentity(ctx, gen.InsertThreadIdentityParams{
		Agent:                  identity.Agent,
		ProviderConversationID: identity.ProviderConversationID,
		ThreadID:               identity.ThreadID,
	})
	if err != nil {
		return false, foreignKeyError(err)
	}
	if n == 0 {
		return false, fmt.Errorf("identity for thread %s already mapped", identity.ThreadID)
	}
	return true, tx.Commit()
}

// Update writes every mutable column from the record; false means no such
// record. A terminal deleted since the record was read surfaces as
// ErrForeignKeyViolation.
func (t *Threads) Update(ctx context.Context, record ThreadRecord) (bool, error) {
	n, err := t.writes.UpdateThread(ctx, gen.UpdateThreadParams{
		TerminalID:     nullStringPtr(record.TerminalID),
		Title:          nullString(record.Title),
		TitleUserSet:   boolInt(record.TitleUserSet),
		Model:          nullString(record.Model),
		Effort:         nullString(record.Effort),
		Cwd:            nullString(record.Cwd),
		PermissionMode: nullString(record.PermissionMode),
		Status:         record.Status,
		LastError:      nullString(record.LastError),
		LastEvidenceAt: nullTime(record.LastEvidenceAt),
		Archived:       boolInt(record.Archived),
		ArchivedAt:     nullTime(record.ArchivedAt),
		UpdatedAt:      formatTime(record.UpdatedAt),
		ID:             record.ID,
	})
	return n > 0, foreignKeyError(err)
}

// List returns every record in creation order.
func (t *Threads) List(ctx context.Context) ([]ThreadRecord, error) {
	rows, err := t.reads.ListThreads(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]ThreadRecord, 0, len(rows))
	for _, row := range rows {
		record, err := threadFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Delete removes the record; false means no such record. The identity
// mapping goes with it (ON DELETE CASCADE).
func (t *Threads) Delete(ctx context.Context, id string) (bool, error) {
	n, err := t.writes.DeleteThread(ctx, id)
	return n > 0, err
}

// ListIdentities returns every identity mapping.
func (t *Threads) ListIdentities(ctx context.Context) ([]ThreadIdentity, error) {
	rows, err := t.reads.ListThreadIdentities(ctx)
	if err != nil {
		return nil, err
	}
	identities := make([]ThreadIdentity, 0, len(rows))
	for _, row := range rows {
		identities = append(identities, ThreadIdentity{
			Agent:                  row.Agent,
			ProviderConversationID: row.ProviderConversationID,
			ThreadID:               row.ThreadID,
		})
	}
	return identities, nil
}

func threadFrom(row gen.Thread) (ThreadRecord, error) {
	record := ThreadRecord{
		ID:             row.ID,
		Agent:          row.Agent,
		ProjectID:      row.ProjectID,
		TerminalID:     stringPtr(row.TerminalID),
		Title:          row.Title.String,
		TitleUserSet:   row.TitleUserSet != 0,
		Model:          row.Model.String,
		Effort:         row.Effort.String,
		Cwd:            row.Cwd.String,
		PermissionMode: row.PermissionMode.String,
		Status:         row.Status,
		LastError:      row.LastError.String,
		Archived:       row.Archived != 0,
	}
	var err error
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return ThreadRecord{}, fmt.Errorf("thread %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return ThreadRecord{}, fmt.Errorf("thread %s updated_at: %w", row.ID, err)
	}
	if record.LastEvidenceAt, err = parseNullTime(row.LastEvidenceAt); err != nil {
		return ThreadRecord{}, fmt.Errorf("thread %s last_evidence_at: %w", row.ID, err)
	}
	if record.ArchivedAt, err = parseNullTime(row.ArchivedAt); err != nil {
		return ThreadRecord{}, fmt.Errorf("thread %s archived_at: %w", row.ID, err)
	}
	return record, nil
}

func nullStringPtr(value *string) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func stringPtr(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	s := value.String
	return &s
}

func nullTime(value *time.Time) sql.NullString {
	if value == nil {
		return sql.NullString{}
	}
	return nullString(formatTime(*value))
}

func boolInt(value bool) int64 {
	if value {
		return 1
	}
	return 0
}
