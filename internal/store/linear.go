package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// LinearSession is one linear_sessions row in domain terms (ATC-302,
// ATC-309): a Linear Agent Session and the ATC Thread it is bound to.
// The Linear Integration owns the state vocabulary; this package stores
// it.
type LinearSession struct {
	// ID is Linear's Agent Session id.
	ID    string
	State string
	// ThreadID binds the session to the exact ATC Thread; empty until
	// the record exists.
	ThreadID  string
	Outcome   string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// LinearSubmission is one linear_submissions row: a Linear input ATC
// acts on, what it targets, the ATC operation it became, and how it
// stands.
type LinearSubmission struct {
	// ID is Linear's activity id, or the session's start key.
	ID        string
	SessionID string
	Kind      string
	// Text is the user's text, verbatim; for a start, the first prompt
	// until the start is over.
	Text string
	// RequestID, QuestionID, and Value name the exact ATC request and
	// choice a selection targets; empty otherwise.
	RequestID  string
	QuestionID string
	Value      string
	State      string
	// Delivery is "" until dispatched, then accepted or uncertain.
	Delivery string
	// OperationID is the ATC operation the submission became (message,
	// answer, stop id); TurnID the execution it watches.
	OperationID string
	TurnID      string
	// Attempts counts dispatches that could not reach the program;
	// NextAttemptAt is when the next may run.
	Attempts      int
	NextAttemptAt time.Time
	// CompletedSeenAt is when the watched turn was first seen completed
	// without a response; nil otherwise.
	CompletedSeenAt *time.Time
	Outcome         string
	Detail          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// LinearRequest is one linear_requests row: a request presented to a
// session with the options offered (JSON the Integration encodes).
type LinearRequest struct {
	// ID is the ATC request id (aprv-…, inpt-…).
	ID        string
	SessionID string
	Kind      string
	Options   string
	State     string
	CreatedAt time.Time
	UpdatedAt time.Time
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

// LinearChange is one transactional write of the Integration's state:
// a session to update, submissions and requests to insert (a known id
// inserts nothing) or update (an unknown id fails the write), and the
// calls owed for the change (a known key is left as it is). Everything
// commits together, so a state is never recorded without the rows that
// report it, and never the reverse.
type LinearChange struct {
	Session        *LinearSession
	NewSubmissions []LinearSubmission
	Submissions    []LinearSubmission
	NewRequests    []LinearRequest
	Requests       []LinearRequest
	Rows           []LinearOutboxRow
}

// ErrLinearSessionNotFound reports a session id with no row.
var ErrLinearSessionNotFound = errors.New("linear session not found")

// ErrLinearRowGone reports an update to a submission or request whose
// row does not exist.
var ErrLinearRowGone = errors.New("linear row not found")

// Linear is the Linear Integration's repository: sessions, their
// submissions and presented requests, and the outbox. Reads go to the
// read pool, mutations to the single-writer pool; db is that pool's
// handle, for the writes that move several rows together.
type Linear struct {
	reads  *gen.Queries
	writes *gen.Queries
	db     *sql.DB
}

// Linear returns the Linear Integration's repository.
func (s *Store) Linear() *Linear {
	return &Linear{reads: gen.New(s.reads), writes: gen.New(s.writes), db: s.writes}
}

// InsertSession stores a new session together with its first
// submissions and the calls it owes, in one transaction, and reports
// whether the session was new. A known session id inserts nothing at
// all — the session and everything it came with were one transaction —
// so a repeated delivery changes nothing, however long ago the first
// was recorded.
func (l *Linear) InsertSession(ctx context.Context, record LinearSession, submissions []LinearSubmission, rows ...LinearOutboxRow) (bool, error) {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.InsertLinearSession(ctx, gen.InsertLinearSessionParams{
		ID:        record.ID,
		State:     record.State,
		ThreadID:  nullString(record.ThreadID),
		Outcome:   nullString(record.Outcome),
		CreatedAt: formatTime(record.CreatedAt),
		UpdatedAt: formatTime(record.UpdatedAt),
	})
	if err != nil {
		return false, err
	}
	if n == 0 {
		return false, tx.Rollback()
	}
	if err := apply(ctx, queries, LinearChange{NewSubmissions: submissions, Rows: rows}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// Record commits one change. ErrLinearSessionNotFound when the session
// to update is gone, ErrLinearRowGone when a submission or request to
// update is; nothing is written then.
func (l *Linear) Record(ctx context.Context, change LinearChange) error {
	tx, err := l.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	if session := change.Session; session != nil {
		n, err := queries.UpdateLinearSession(ctx, gen.UpdateLinearSessionParams{
			State:     session.State,
			ThreadID:  nullString(session.ThreadID),
			Outcome:   nullString(session.Outcome),
			UpdatedAt: formatTime(session.UpdatedAt),
			ID:        session.ID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: %s", ErrLinearSessionNotFound, session.ID)
		}
	}
	if err := apply(ctx, queries, change); err != nil {
		return err
	}
	return tx.Commit()
}

// apply writes a change's submissions, requests, and rows.
func apply(ctx context.Context, queries *gen.Queries, change LinearChange) error {
	for _, sub := range change.NewSubmissions {
		if _, err := queries.InsertLinearSubmission(ctx, gen.InsertLinearSubmissionParams{
			ID: sub.ID, SessionID: sub.SessionID, Kind: sub.Kind, Text: nullString(sub.Text),
			RequestID: nullString(sub.RequestID), QuestionID: nullString(sub.QuestionID), Value: nullString(sub.Value),
			State: sub.State, Delivery: sub.Delivery, OperationID: nullString(sub.OperationID), TurnID: nullString(sub.TurnID),
			Attempts: int64(sub.Attempts), NextAttemptAt: formatTime(sub.NextAttemptAt), CompletedSeenAt: nullTime(sub.CompletedSeenAt),
			Outcome: nullString(sub.Outcome), Detail: nullString(sub.Detail), CreatedAt: formatTime(sub.CreatedAt), UpdatedAt: formatTime(sub.UpdatedAt),
		}); err != nil {
			return err
		}
	}
	for _, sub := range change.Submissions {
		n, err := queries.UpdateLinearSubmission(ctx, gen.UpdateLinearSubmissionParams{
			Text: nullString(sub.Text), State: sub.State, Delivery: sub.Delivery, OperationID: nullString(sub.OperationID), TurnID: nullString(sub.TurnID),
			Attempts: int64(sub.Attempts), NextAttemptAt: formatTime(sub.NextAttemptAt), CompletedSeenAt: nullTime(sub.CompletedSeenAt),
			Outcome: nullString(sub.Outcome), Detail: nullString(sub.Detail), UpdatedAt: formatTime(sub.UpdatedAt), ID: sub.ID,
		})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: submission %s", ErrLinearRowGone, sub.ID)
		}
	}
	for _, request := range change.NewRequests {
		if _, err := queries.InsertLinearRequest(ctx, gen.InsertLinearRequestParams{
			ID: request.ID, SessionID: request.SessionID, Kind: request.Kind, Options: request.Options, State: request.State,
			CreatedAt: formatTime(request.CreatedAt), UpdatedAt: formatTime(request.UpdatedAt),
		}); err != nil {
			return err
		}
	}
	for _, request := range change.Requests {
		n, err := queries.UpdateLinearRequest(ctx, gen.UpdateLinearRequestParams{State: request.State, UpdatedAt: formatTime(request.UpdatedAt), ID: request.ID})
		if err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("%w: request %s", ErrLinearRowGone, request.ID)
		}
	}
	for _, row := range change.Rows {
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

// Submissions lists a session's submissions in creation order: every
// one, or only those not yet done.
func (l *Linear) Submissions(ctx context.Context, sessionID string, activeOnly bool) ([]LinearSubmission, error) {
	var rows []gen.LinearSubmission
	var err error
	if activeOnly {
		rows, err = l.reads.ListActiveLinearSubmissions(ctx, sessionID)
	} else {
		rows, err = l.reads.ListLinearSubmissions(ctx, sessionID)
	}
	if err != nil {
		return nil, err
	}
	records := make([]LinearSubmission, 0, len(rows))
	for _, row := range rows {
		record, err := linearSubmissionFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// PendingSubmissions counts submissions not yet done, across sessions.
func (l *Linear) PendingSubmissions(ctx context.Context) (int, error) {
	n, err := l.reads.CountPendingLinearSubmissions(ctx)
	return int(n), err
}

// Requests lists a session's presented requests in creation order.
func (l *Linear) Requests(ctx context.Context, sessionID string) ([]LinearRequest, error) {
	rows, err := l.reads.ListLinearRequests(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	records := make([]LinearRequest, 0, len(rows))
	for _, row := range rows {
		record, err := linearRequestFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Enqueue stores an owed call and reports true; false means the key is
// already there — sent, failed, or pending — and nothing was added.
func (l *Linear) Enqueue(ctx context.Context, row LinearOutboxRow) (bool, error) {
	n, err := l.writes.InsertLinearOutbox(ctx, insertOutboxParams(row))
	return n > 0, err
}

// Due lists the rows due now — each session's oldest outstanding row,
// when its next attempt is at or before now — so a session's calls go
// out in order and a row backing off holds the rows behind it.
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
		ID:       row.ID,
		State:    row.State,
		ThreadID: row.ThreadID.String,
		Outcome:  row.Outcome.String,
	}
	var err error
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return LinearSession{}, fmt.Errorf("linear session %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return LinearSession{}, fmt.Errorf("linear session %s updated_at: %w", row.ID, err)
	}
	return record, nil
}

func linearSubmissionFrom(row gen.LinearSubmission) (LinearSubmission, error) {
	record := LinearSubmission{
		ID: row.ID, SessionID: row.SessionID, Kind: row.Kind, Text: row.Text.String,
		RequestID: row.RequestID.String, QuestionID: row.QuestionID.String, Value: row.Value.String,
		State: row.State, Delivery: row.Delivery, OperationID: row.OperationID.String, TurnID: row.TurnID.String,
		Attempts: int(row.Attempts), Outcome: row.Outcome.String, Detail: row.Detail.String,
	}
	var err error
	if record.NextAttemptAt, err = parseTime(row.NextAttemptAt); err != nil {
		return LinearSubmission{}, fmt.Errorf("linear submission %s next_attempt_at: %w", row.ID, err)
	}
	if record.CompletedSeenAt, err = parseNullTime(row.CompletedSeenAt); err != nil {
		return LinearSubmission{}, fmt.Errorf("linear submission %s completed_seen_at: %w", row.ID, err)
	}
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return LinearSubmission{}, fmt.Errorf("linear submission %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return LinearSubmission{}, fmt.Errorf("linear submission %s updated_at: %w", row.ID, err)
	}
	return record, nil
}

func linearRequestFrom(row gen.LinearRequest) (LinearRequest, error) {
	record := LinearRequest{ID: row.ID, SessionID: row.SessionID, Kind: row.Kind, Options: row.Options, State: row.State}
	var err error
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return LinearRequest{}, fmt.Errorf("linear request %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return LinearRequest{}, fmt.Errorf("linear request %s updated_at: %w", row.ID, err)
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
