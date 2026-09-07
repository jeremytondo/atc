package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// Answers and stops (ATC-308): the durable rows behind the two thread
// operations that resolve only on provider evidence. Both follow the
// message pattern — recorded before dispatch under an ATC identity,
// updated as the outcome is learned, pruned to a thread's newest few
// with the unresolved ones spared — and both are read whole at boot.

// ThreadAnswerRecord is one answer submitted through ATC to a structured
// request. Questions, Answers, and ProviderAnswers are opaque JSON the
// threads domain encodes: the request's questions as they stood, the
// answers in ATC's wire form, and the answers in the provider's form
// (what a retry re-presents). Reply is the conversational reply when the
// answer was one (ATC-309), empty otherwise. Delivery is accepted or
// uncertain; State is sent, resolved, failed, or superseded; Detail
// explains a failure or a supersession.
type ThreadAnswerRecord struct {
	ID                string
	ThreadID          string
	RequestID         string
	ProviderRequestID string
	Questions         string
	Answers           string
	ProviderAnswers   string
	Reply             string
	Delivery          string
	State             string
	Detail            string
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

// ThreadStopRecord is one stop operation: the client's idempotency key
// (empty, stored NULL, for none), its state (stopping, stopped,
// finished, failed), delivery (accepted, uncertain), the scope it was
// accepted with — the ATC turn running, the ATC turn pending, each
// empty for none, and the thread's status — and its outcome detail.
type ThreadStopRecord struct {
	ID           string
	ThreadID     string
	Key          string
	State        string
	Delivery     string
	ScopeTurn    string
	ScopePending string
	ScopeStatus  string
	Detail       string
	CreatedAt    time.Time
	UpdatedAt    time.Time
	ResolvedAt   *time.Time
}

// SubmitAnswer persists an answer and prunes the thread's settled
// answers past keep, in one transaction. False reports an id collision
// (the caller re-rolls); a deleted thread surfaces as
// ErrForeignKeyViolation.
func (t *Threads) SubmitAnswer(ctx context.Context, answer ThreadAnswerRecord, keep int) (bool, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.InsertThreadAnswer(ctx, gen.InsertThreadAnswerParams{
		ID: answer.ID, ThreadID: answer.ThreadID, RequestID: answer.RequestID, ProviderRequestID: answer.ProviderRequestID,
		Questions: answer.Questions, Answers: answer.Answers, ProviderAnswers: answer.ProviderAnswers, Reply: nullString(answer.Reply),
		Delivery: answer.Delivery, State: answer.State, Detail: nullString(answer.Detail),
		CreatedAt: formatTime(answer.CreatedAt), UpdatedAt: formatTime(answer.UpdatedAt),
	})
	if err != nil {
		return false, foreignKeyError(err)
	}
	if n == 0 {
		return false, nil
	}
	if err := queries.PruneThreadAnswers(ctx, gen.PruneThreadAnswersParams{ThreadID: answer.ThreadID, Offset: int64(keep)}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// UpdateAnswer records an answer's delivery and state; false means no
// such answer.
func (t *Threads) UpdateAnswer(ctx context.Context, answer ThreadAnswerRecord) (bool, error) {
	n, err := t.writes.UpdateThreadAnswer(ctx, gen.UpdateThreadAnswerParams{
		Delivery: answer.Delivery, State: answer.State, Detail: nullString(answer.Detail), UpdatedAt: formatTime(answer.UpdatedAt), ID: answer.ID,
	})
	return n > 0, err
}

// SupersedeAnswers marks every answer of the thread still awaiting
// evidence superseded, with the detail; it reports how many.
func (t *Threads) SupersedeAnswers(ctx context.Context, threadID, detail string, at time.Time) (int64, error) {
	return t.writes.SupersedeThreadAnswers(ctx, gen.SupersedeThreadAnswersParams{Detail: nullString(detail), UpdatedAt: formatTime(at), ThreadID: threadID})
}

// ListAnswers returns every retained answer in creation order.
func (t *Threads) ListAnswers(ctx context.Context) ([]ThreadAnswerRecord, error) {
	rows, err := t.reads.ListThreadAnswers(ctx)
	if err != nil {
		return nil, err
	}
	answers := make([]ThreadAnswerRecord, 0, len(rows))
	for _, row := range rows {
		answer, err := answerFrom(row)
		if err != nil {
			return nil, err
		}
		answers = append(answers, answer)
	}
	return answers, nil
}

func answerFrom(row gen.ThreadAnswer) (ThreadAnswerRecord, error) {
	answer := ThreadAnswerRecord{
		ID: row.ID, ThreadID: row.ThreadID, RequestID: row.RequestID, ProviderRequestID: row.ProviderRequestID,
		Questions: row.Questions, Answers: row.Answers, ProviderAnswers: row.ProviderAnswers, Reply: row.Reply.String,
		Delivery: row.Delivery, State: row.State, Detail: row.Detail.String,
	}
	var err error
	if answer.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return ThreadAnswerRecord{}, fmt.Errorf("thread answer %s created_at: %w", row.ID, err)
	}
	if answer.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return ThreadAnswerRecord{}, fmt.Errorf("thread answer %s updated_at: %w", row.ID, err)
	}
	return answer, nil
}

// ErrStopKeyTaken reports a key the thread already has a stop for.
var ErrStopKeyTaken = errors.New("stop key already used")

// SubmitStop persists a stop and prunes the thread's resolved stops past
// keep, in one transaction. False reports an id collision; a deleted
// thread surfaces as ErrForeignKeyViolation, a key already recorded for
// the thread as ErrStopKeyTaken.
func (t *Threads) SubmitStop(ctx context.Context, stop ThreadStopRecord, keep int) (bool, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.InsertThreadStop(ctx, gen.InsertThreadStopParams{
		ID: stop.ID, ThreadID: stop.ThreadID, Key: nullString(stop.Key), State: stop.State, Delivery: stop.Delivery,
		ScopeTurn: nullString(stop.ScopeTurn), ScopePending: nullString(stop.ScopePending), ScopeStatus: stop.ScopeStatus,
		Detail: nullString(stop.Detail), CreatedAt: formatTime(stop.CreatedAt), UpdatedAt: formatTime(stop.UpdatedAt), ResolvedAt: nullTime(stop.ResolvedAt),
	})
	if err != nil {
		if uniqueViolation(err, "thread_stops.thread_id, thread_stops.key") {
			return false, fmt.Errorf("%w: %s", ErrStopKeyTaken, stop.Key)
		}
		return false, foreignKeyError(err)
	}
	if n == 0 {
		return false, nil
	}
	if err := queries.PruneThreadStops(ctx, gen.PruneThreadStopsParams{ThreadID: stop.ThreadID, Offset: int64(keep)}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// UpdateStop records a stop's state and delivery; false means no such
// stop.
func (t *Threads) UpdateStop(ctx context.Context, stop ThreadStopRecord) (bool, error) {
	n, err := t.writes.UpdateThreadStop(ctx, updateStopParams(stop))
	return n > 0, err
}

func updateStopParams(stop ThreadStopRecord) gen.UpdateThreadStopParams {
	return gen.UpdateThreadStopParams{
		State: stop.State, Delivery: stop.Delivery, Detail: nullString(stop.Detail),
		UpdatedAt: formatTime(stop.UpdatedAt), ResolvedAt: nullTime(stop.ResolvedAt), ID: stop.ID,
	}
}

// ResolveStop commits a stop's resolution in one transaction: the thread
// record as the resolution left it, the stop's outcome, the withdrawal
// of every message still uncertain on the turns named — a replay must
// not deliver them into work that was stopped — and the supersession of
// every answer still awaiting evidence, with the details given. False
// means the thread or the stop is gone.
func (t *Threads) ResolveStop(ctx context.Context, record ThreadRecord, stop ThreadStopRecord, withdraw []string, withdrawal, supersession string) (bool, error) {
	tx, err := t.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.UpdateThread(ctx, updateThreadParams(record))
	if err != nil {
		return false, foreignKeyError(err)
	}
	if n == 0 {
		return false, nil
	}
	if n, err = queries.UpdateThreadStop(ctx, updateStopParams(stop)); err != nil {
		return false, err
	}
	if n == 0 {
		return false, nil
	}
	for _, turnID := range withdraw {
		if _, err := queries.WithdrawThreadMessages(ctx, gen.WithdrawThreadMessagesParams{
			Detail: nullString(withdrawal), UpdatedAt: formatTime(stop.UpdatedAt), ThreadID: record.ID, TurnID: turnID,
		}); err != nil {
			return false, err
		}
	}
	if _, err := queries.SupersedeThreadAnswers(ctx, gen.SupersedeThreadAnswersParams{
		Detail: nullString(supersession), UpdatedAt: formatTime(stop.UpdatedAt), ThreadID: record.ID,
	}); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// ListStops returns every retained stop in creation order.
func (t *Threads) ListStops(ctx context.Context) ([]ThreadStopRecord, error) {
	rows, err := t.reads.ListThreadStops(ctx)
	if err != nil {
		return nil, err
	}
	stops := make([]ThreadStopRecord, 0, len(rows))
	for _, row := range rows {
		stop, err := stopFrom(row)
		if err != nil {
			return nil, err
		}
		stops = append(stops, stop)
	}
	return stops, nil
}

func stopFrom(row gen.ThreadStop) (ThreadStopRecord, error) {
	stop := ThreadStopRecord{
		ID: row.ID, ThreadID: row.ThreadID, Key: row.Key.String, State: row.State, Delivery: row.Delivery,
		ScopeTurn: row.ScopeTurn.String, ScopePending: row.ScopePending.String, ScopeStatus: row.ScopeStatus, Detail: row.Detail.String,
	}
	var err error
	if stop.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return ThreadStopRecord{}, fmt.Errorf("thread stop %s created_at: %w", row.ID, err)
	}
	if stop.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return ThreadStopRecord{}, fmt.Errorf("thread stop %s updated_at: %w", row.ID, err)
	}
	if stop.ResolvedAt, err = parseNullTime(row.ResolvedAt); err != nil {
		return ThreadStopRecord{}, fmt.Errorf("thread stop %s resolved_at: %w", row.ID, err)
	}
	return stop, nil
}
