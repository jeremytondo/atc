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
	// IntegrationID is the Integration that produced the thread; immutable.
	IntegrationID string
	// AppID is the qualified App the conversation was started in, when
	// reliably known at creation; empty (stored NULL) permanently
	// otherwise. Immutable.
	AppID string
	// AgentID is the Integration-scoped agent id as the Integration reports
	// it; empty (stored NULL) when not reported, and free to change.
	AgentID string
	// InitialDirectory is the canonical directory the conversation
	// originated in; empty (stored NULL) when none was usable at creation.
	// Write-once: set at insert, never updated.
	InitialDirectory string
	// ProjectID is the zero-or-one project association; empty (stored
	// NULL) when unassigned. Project deletion clears it (ON DELETE SET
	// NULL) without deleting the row.
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
	// StatusDetail is the provider's explanation of a faulted session;
	// empty (stored NULL) unless Status is error.
	StatusDetail   string
	LastEvidenceAt *time.Time
	Archived       bool
	ArchivedAt     *time.Time
	CreatedAt      time.Time
	UpdatedAt      time.Time
	// Turn is the latest turn ATC observed or created on the thread; nil
	// until there is one.
	Turn *TurnRecord
}

// TurnRecord is the latest turn's persisted shape. ID is the ATC-minted
// public id; ProviderID is the provider's own turn id, private to the
// server, empty when the provider reports none or the turn is not yet
// bound to one.
type TurnRecord struct {
	ID          string
	ProviderID  string
	State       string
	StartedAt   time.Time
	CompletedAt *time.Time
	Error       string
	// Response is the turn's final assistant message, recovered after the
	// turn ended; empty (stored NULL) until then.
	Response string
}

// ThreadIdentity is one row of the private identity mapping:
// (integration, provider conversation id) → thread. Provider
// conversation ids never leave the server except through the
// Integration's own deep links.
type ThreadIdentity struct {
	IntegrationID          string
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
	turn := turnColumnsOf(record.Turn)
	return gen.InsertThreadParams{
		ID:               record.ID,
		IntegrationID:    record.IntegrationID,
		AppID:            nullString(record.AppID),
		AgentID:          nullString(record.AgentID),
		InitialDirectory: nullString(record.InitialDirectory),
		ProjectID:        nullString(record.ProjectID),
		TerminalID:       nullStringPtr(record.TerminalID),
		Title:            nullString(record.Title),
		TitleUserSet:     boolInt(record.TitleUserSet),
		Model:            nullString(record.Model),
		Effort:           nullString(record.Effort),
		Cwd:              nullString(record.Cwd),
		PermissionMode:   nullString(record.PermissionMode),
		Status:           record.Status,
		StatusDetail:     nullString(record.StatusDetail),
		LastEvidenceAt:   nullTime(record.LastEvidenceAt),
		Archived:         boolInt(record.Archived),
		ArchivedAt:       nullTime(record.ArchivedAt),
		CreatedAt:        formatTime(record.CreatedAt),
		UpdatedAt:        formatTime(record.UpdatedAt),
		TurnID:           turn.id,
		TurnProviderID:   turn.providerID,
		TurnState:        turn.state,
		TurnStartedAt:    turn.startedAt,
		TurnCompletedAt:  turn.completedAt,
		TurnError:        turn.err,
		TurnResponse:     turn.response,
	}
}

// turnColumns is a turn's column values, all NULL when there is none.
type turnColumns struct {
	id, providerID, state, startedAt, completedAt, err, response sql.NullString
}

func turnColumnsOf(turn *TurnRecord) turnColumns {
	if turn == nil {
		return turnColumns{}
	}
	return turnColumns{
		id:          nullString(turn.ID),
		providerID:  nullString(turn.ProviderID),
		state:       nullString(turn.State),
		startedAt:   nullString(formatTime(turn.StartedAt)),
		completedAt: nullTime(turn.CompletedAt),
		err:         nullString(turn.Error),
		response:    nullString(turn.Response),
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
		IntegrationID:          identity.IntegrationID,
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
// record. A terminal or project deleted since the record was read
// surfaces as ErrForeignKeyViolation.
func (t *Threads) Update(ctx context.Context, record ThreadRecord) (bool, error) {
	turn := turnColumnsOf(record.Turn)
	n, err := t.writes.UpdateThread(ctx, gen.UpdateThreadParams{
		AgentID:         nullString(record.AgentID),
		ProjectID:       nullString(record.ProjectID),
		TerminalID:      nullStringPtr(record.TerminalID),
		Title:           nullString(record.Title),
		TitleUserSet:    boolInt(record.TitleUserSet),
		Model:           nullString(record.Model),
		Effort:          nullString(record.Effort),
		Cwd:             nullString(record.Cwd),
		PermissionMode:  nullString(record.PermissionMode),
		Status:          record.Status,
		StatusDetail:    nullString(record.StatusDetail),
		LastEvidenceAt:  nullTime(record.LastEvidenceAt),
		Archived:        boolInt(record.Archived),
		ArchivedAt:      nullTime(record.ArchivedAt),
		UpdatedAt:       formatTime(record.UpdatedAt),
		TurnID:          turn.id,
		TurnProviderID:  turn.providerID,
		TurnState:       turn.state,
		TurnStartedAt:   turn.startedAt,
		TurnCompletedAt: turn.completedAt,
		TurnError:       turn.err,
		TurnResponse:    turn.response,
		ID:              record.ID,
	})
	return n > 0, foreignKeyError(err)
}

// AssignProject sets the project of a thread that has none; false means
// no such record or one already assigned. A project deleted since the
// caller chose it surfaces as ErrForeignKeyViolation.
func (t *Threads) AssignProject(ctx context.Context, id, projectID string, at time.Time) (bool, error) {
	n, err := t.writes.AssignThreadProject(ctx, gen.AssignThreadProjectParams{
		ProjectID: nullString(projectID), UpdatedAt: formatTime(at), ID: id,
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
			IntegrationID:          row.IntegrationID,
			ProviderConversationID: row.ProviderConversationID,
			ThreadID:               row.ThreadID,
		})
	}
	return identities, nil
}

func threadFrom(row gen.Thread) (ThreadRecord, error) {
	record := ThreadRecord{
		ID:               row.ID,
		IntegrationID:    row.IntegrationID,
		AppID:            row.AppID.String,
		AgentID:          row.AgentID.String,
		InitialDirectory: row.InitialDirectory.String,
		ProjectID:        row.ProjectID.String,
		TerminalID:       stringPtr(row.TerminalID),
		Title:            row.Title.String,
		TitleUserSet:     row.TitleUserSet != 0,
		Model:            row.Model.String,
		Effort:           row.Effort.String,
		Cwd:              row.Cwd.String,
		PermissionMode:   row.PermissionMode.String,
		Status:           row.Status,
		StatusDetail:     row.StatusDetail.String,
		Archived:         row.Archived != 0,
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
	if row.TurnID.Valid {
		turn := &TurnRecord{
			ID:         row.TurnID.String,
			ProviderID: row.TurnProviderID.String,
			State:      row.TurnState.String,
			Error:      row.TurnError.String,
			Response:   row.TurnResponse.String,
		}
		if turn.StartedAt, err = parseTime(row.TurnStartedAt.String); err != nil {
			return ThreadRecord{}, fmt.Errorf("thread %s turn_started_at: %w", row.ID, err)
		}
		if turn.CompletedAt, err = parseNullTime(row.TurnCompletedAt); err != nil {
			return ThreadRecord{}, fmt.Errorf("thread %s turn_completed_at: %w", row.ID, err)
		}
		record.Turn = turn
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
