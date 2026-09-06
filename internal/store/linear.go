package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// LinearSession is one linear_sessions row in domain terms (ATC-302): a
// Linear Agent Session and what the Integration owes or watches for it.
// The Linear Integration owns the state vocabulary; this package stores
// it.
type LinearSession struct {
	// ID is Linear's Agent Session id.
	ID string
	// Prompt is the text the Thread is started with; empty (stored NULL)
	// once the start is over.
	Prompt string
	State  string
	// ThreadID and TurnID bind the session to the exact ATC Thread and
	// Turn it started; empty until the record exists.
	ThreadID string
	TurnID   string
	// NoticedStatus is the waiting status last announced in Linear.
	NoticedStatus string
	// CompletedSeenAt is when the Turn was first seen completed without a
	// response; nil otherwise.
	CompletedSeenAt *time.Time
	Outcome         string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// LinearOutboxRow is one owed Linear API call.
type LinearOutboxRow struct {
	// ID is the deterministic idempotency key the Integration chose.
	ID        string
	SessionID string
	// Kind is "activity" or "links".
	Kind string
	// Body is the GraphQL input, JSON.
	Body          []byte
	Attempts      int
	NextAttemptAt time.Time
	SentAt        *time.Time
	Failed        string
	CreatedAt     time.Time
}

// ErrLinearSessionNotFound reports a session id with no row.
var ErrLinearSessionNotFound = errors.New("linear session not found")

// Linear is the Linear Integration's repository: sessions and the outbox.
// Reads go to the read pool, mutations to the single-writer pool; db is
// that pool's handle, for the writes that move a session and the calls
// it owes together.
type Linear struct {
	reads  *gen.Queries
	writes *gen.Queries
	db     *sql.DB
}

// Linear returns the Linear Integration's repository.
func (s *Store) Linear() *Linear {
	return &Linear{reads: gen.New(s.reads), writes: gen.New(s.writes), db: s.writes}
}

// InsertSession stores a new session together with the calls it owes, in
// one transaction, and reports whether the session was new. A known
// session id inserts nothing for the session; the rows are still added
// under their keys where absent — an earlier attempt may have died
// between the two — so a repeated delivery leaves exactly one of each.
func (l *Linear) InsertSession(ctx context.Context, record LinearSession, rows ...LinearOutboxRow) (bool, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.InsertLinearSession(ctx, gen.InsertLinearSessionParams{
		ID:              record.ID,
		Prompt:          nullString(record.Prompt),
		State:           record.State,
		ThreadID:        nullString(record.ThreadID),
		TurnID:          nullString(record.TurnID),
		NoticedStatus:   nullString(record.NoticedStatus),
		CompletedSeenAt: nullTime(record.CompletedSeenAt),
		Outcome:         nullString(record.Outcome),
		CreatedAt:       formatTime(record.CreatedAt),
		UpdatedAt:       formatTime(record.UpdatedAt),
	})
	if err != nil {
		return false, err
	}
	if err := enqueueAll(ctx, queries, rows); err != nil {
		return false, err
	}
	return n > 0, tx.Commit()
}

// UpdateSession writes every mutable column of a session together with
// the calls the change owes, in one transaction, so a state is never
// recorded without the rows that report it; false means no such session,
// and nothing was written. Rows under keys already present are left as
// they are.
func (l *Linear) UpdateSession(ctx context.Context, record LinearSession, rows ...LinearOutboxRow) (bool, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.UpdateLinearSession(ctx, gen.UpdateLinearSessionParams{
		Prompt:          nullString(record.Prompt),
		State:           record.State,
		ThreadID:        nullString(record.ThreadID),
		TurnID:          nullString(record.TurnID),
		NoticedStatus:   nullString(record.NoticedStatus),
		CompletedSeenAt: nullTime(record.CompletedSeenAt),
		Outcome:         nullString(record.Outcome),
		UpdatedAt:       formatTime(record.UpdatedAt),
		ID:              record.ID,
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, tx.Rollback()
	}
	if err := enqueueAll(ctx, queries, rows); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

func enqueueAll(ctx context.Context, queries *gen.Queries, rows []LinearOutboxRow) error {
	for _, row := range rows {
		if _, err := queries.InsertLinearOutbox(ctx, insertOutboxParams(row)); err != nil {
			return err
		}
	}
	return nil
}

func insertOutboxParams(row LinearOutboxRow) gen.InsertLinearOutboxParams {
	return gen.InsertLinearOutboxParams{
		ID:            row.ID,
		SessionID:     row.SessionID,
		Kind:          row.Kind,
		Body:          row.Body,
		NextAttemptAt: formatTime(row.NextAttemptAt),
		CreatedAt:     formatTime(row.CreatedAt),
	}
}

// GetSession reads one session.
func (l *Linear) GetSession(ctx context.Context, id string) (LinearSession, error) {
	row, err := l.reads.GetLinearSession(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return LinearSession{}, ErrLinearSessionNotFound
	}
	if err != nil {
		return LinearSession{}, err
	}
	return linearSessionFrom(row)
}

// OpenSessions lists every session not yet done, oldest first.
func (l *Linear) OpenSessions(ctx context.Context) ([]LinearSession, error) {
	rows, err := l.reads.ListOpenLinearSessions(ctx)
	if err != nil {
		return nil, err
	}
	records := make([]LinearSession, 0, len(rows))
	for _, row := range rows {
		record, err := linearSessionFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// CountSessions reports how many sessions are open and how many exist.
func (l *Linear) CountSessions(ctx context.Context) (open, total int, err error) {
	row, err := l.reads.CountLinearSessions(ctx)
	if err != nil {
		return 0, 0, err
	}
	return int(row.Open), int(row.Total), nil
}

// Enqueue stores an owed call and reports true; false means the key is
// already there — sent, failed, or pending — and nothing was added.
func (l *Linear) Enqueue(ctx context.Context, row LinearOutboxRow) (bool, error) {
	n, err := l.writes.InsertLinearOutbox(ctx, insertOutboxParams(row))
	return n > 0, err
}

// Due lists unsent, unfailed rows whose next attempt is at or before now.
func (l *Linear) Due(ctx context.Context, now time.Time, limit int) ([]LinearOutboxRow, error) {
	rows, err := l.reads.ListDueLinearOutbox(ctx, gen.ListDueLinearOutboxParams{NextAttemptAt: formatTime(now), Limit: int64(limit)})
	if err != nil {
		return nil, err
	}
	records := make([]LinearOutboxRow, 0, len(rows))
	for _, row := range rows {
		record, err := linearOutboxFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Pending counts rows still owed.
func (l *Linear) Pending(ctx context.Context) (int, error) {
	n, err := l.reads.CountPendingLinearOutbox(ctx)
	return int(n), err
}

// Sent records a row Linear accepted.
func (l *Linear) Sent(ctx context.Context, id string, at time.Time) error {
	_, err := l.writes.MarkLinearOutboxSent(ctx, gen.MarkLinearOutboxSentParams{SentAt: nullString(formatTime(at)), ID: id})
	return err
}

// Failed records a row Linear refused for good.
func (l *Linear) Failed(ctx context.Context, id string, attempts int, reason string) error {
	_, err := l.writes.MarkLinearOutboxFailed(ctx, gen.MarkLinearOutboxFailedParams{Failed: nullString(reason), Attempts: int64(attempts), ID: id})
	return err
}

// Retry records a failed attempt and when to try again.
func (l *Linear) Retry(ctx context.Context, id string, attempts int, next time.Time) error {
	_, err := l.writes.RetryLinearOutbox(ctx, gen.RetryLinearOutboxParams{Attempts: int64(attempts), NextAttemptAt: formatTime(next), ID: id})
	return err
}

// Prune drops sent rows, and refused rows created, before cutoff.
func (l *Linear) Prune(ctx context.Context, cutoff time.Time) error {
	_, err := l.writes.PruneLinearOutbox(ctx, nullString(formatTime(cutoff)))
	return err
}

func linearSessionFrom(row gen.LinearSession) (LinearSession, error) {
	record := LinearSession{
		ID:            row.ID,
		Prompt:        row.Prompt.String,
		State:         row.State,
		ThreadID:      row.ThreadID.String,
		TurnID:        row.TurnID.String,
		NoticedStatus: row.NoticedStatus.String,
		Outcome:       row.Outcome.String,
	}
	var err error
	if record.CompletedSeenAt, err = parseNullTime(row.CompletedSeenAt); err != nil {
		return LinearSession{}, fmt.Errorf("linear session %s completed_seen_at: %w", row.ID, err)
	}
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return LinearSession{}, fmt.Errorf("linear session %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return LinearSession{}, fmt.Errorf("linear session %s updated_at: %w", row.ID, err)
	}
	return record, nil
}

func linearOutboxFrom(row gen.LinearOutbox) (LinearOutboxRow, error) {
	record := LinearOutboxRow{
		ID:        row.ID,
		SessionID: row.SessionID,
		Kind:      row.Kind,
		Body:      row.Body,
		Attempts:  int(row.Attempts),
		Failed:    row.Failed.String,
	}
	var err error
	if record.NextAttemptAt, err = parseTime(row.NextAttemptAt); err != nil {
		return LinearOutboxRow{}, fmt.Errorf("linear outbox %s next_attempt_at: %w", row.ID, err)
	}
	if record.SentAt, err = parseNullTime(row.SentAt); err != nil {
		return LinearOutboxRow{}, fmt.Errorf("linear outbox %s sent_at: %w", row.ID, err)
	}
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return LinearOutboxRow{}, fmt.Errorf("linear outbox %s created_at: %w", row.ID, err)
	}
	return record, nil
}
