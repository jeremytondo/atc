package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

func TestLinearSessionsRoundTrip(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	linear := s.Linear()
	records := []LinearSession{
		{ID: "sess-a", State: "accepted", CreatedAt: at(0), UpdatedAt: at(0)},
		{ID: "sess-b", State: "bound", ThreadID: "thrd-a", CreatedAt: at(1), UpdatedAt: at(1)},
		{ID: "sess-c", State: "done", Outcome: "refused", CreatedAt: at(2), UpdatedAt: at(2)},
	}
	start := LinearSubmission{ID: "sess-a/start", SessionID: "sess-a", Kind: "start", Text: "hi", State: "pending", NextAttemptAt: at(0), CreatedAt: at(0), UpdatedAt: at(0)}
	ack := LinearOutboxRow{ID: "sess-a/ack", SessionID: "sess-a", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(0), CreatedAt: at(0)}
	for i, record := range records {
		var subs []LinearSubmission
		var rows []LinearOutboxRow
		if i == 0 {
			subs, rows = []LinearSubmission{start}, []LinearOutboxRow{ack}
		}
		if ok, err := linear.InsertSession(ctx, record, subs, rows...); err != nil || !ok {
			t.Fatalf("InsertSession = %v, %v", ok, err)
		}
	}
	// A repeated insert changes nothing and adds nothing — not even a
	// row under a key long pruned.
	other := LinearOutboxRow{ID: "sess-a/ack-again", SessionID: "sess-a", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(0), CreatedAt: at(0)}
	if ok, err := linear.InsertSession(ctx, records[0], []LinearSubmission{start, {ID: "sess-a/extra", SessionID: "sess-a", Kind: "message", State: "pending", NextAttemptAt: at(0), CreatedAt: at(0), UpdatedAt: at(0)}}, ack, other); err != nil || ok {
		t.Errorf("duplicate InsertSession = %v, %v; want false", ok, err)
	}
	got, err := linear.GetSession(ctx, "sess-b")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(records[1], got); diff != "" {
		t.Errorf("GetSession mismatch (-want +got):\n%s", diff)
	}
	if _, err := linear.GetSession(ctx, "sess-nope"); !errors.Is(err, ErrLinearSessionNotFound) {
		t.Errorf("GetSession(unknown) = %v", err)
	}
	open, err := linear.OpenSessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(records[:2], open); diff != "" {
		t.Errorf("OpenSessions mismatch (-want +got):\n%s", diff)
	}
	if o, total, err := linear.CountSessions(ctx); err != nil || o != 2 || total != 3 {
		t.Errorf("CountSessions = %d/%d, %v", o, total, err)
	}
	if subs, err := linear.Submissions(ctx, "sess-a", false); err != nil || !cmp.Equal(subs, []LinearSubmission{start}) {
		t.Errorf("Submissions = %+v, %v", subs, err)
	}
	if pending, _ := linear.Pending(ctx); pending != 1 {
		t.Errorf("Pending = %d", pending)
	}

	// One change commits a session, its submissions, its requests, and
	// the calls owed together; a known key or id is left alone.
	seen := at(4)
	updated := records[0]
	updated.State, updated.ThreadID, updated.UpdatedAt = "bound", "thrd-b", at(3)
	started := start
	started.State, started.Text, started.Delivery, started.TurnID, started.OperationID, started.CompletedSeenAt, started.UpdatedAt = "sent", "", "accepted", "turn-1", "turn-1", &seen, at(3)
	message := LinearSubmission{ID: "act-1", SessionID: "sess-a", Kind: "message", Text: "and the tests?", State: "pending", NextAttemptAt: at(3), CreatedAt: at(3), UpdatedAt: at(3)}
	choice := LinearSubmission{ID: "act-2", SessionID: "sess-a", Kind: "choice", Text: "Blue", RequestID: "inpt-1", QuestionID: "q1", Value: "Blue", State: "done", Outcome: "delivered", Detail: "d", Attempts: 1, NextAttemptAt: at(3), CreatedAt: at(3), UpdatedAt: at(3)}
	request := LinearRequest{ID: "inpt-1", SessionID: "sess-a", Kind: "input", Options: `[{"label":"Blue"}]`, State: "open", CreatedAt: at(3), UpdatedAt: at(3)}
	row := LinearOutboxRow{ID: "sess-a/started", SessionID: "sess-a", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(3), CreatedAt: at(3)}
	change := LinearChange{Session: &updated, NewSubmissions: []LinearSubmission{message, choice, start}, Submissions: []LinearSubmission{started}, NewRequests: []LinearRequest{request}, Rows: []LinearOutboxRow{row, row, ack}}
	if err := linear.Record(ctx, change); err != nil {
		t.Fatalf("Record = %v", err)
	}
	if got, _ := linear.GetSession(ctx, "sess-a"); !cmp.Equal(updated, got) {
		t.Errorf("after update = %+v", got)
	}
	if subs, err := linear.Submissions(ctx, "sess-a", false); err != nil || !cmp.Equal(subs, []LinearSubmission{started, message, choice}) {
		t.Errorf("Submissions after change = %+v, %v", subs, err)
	}
	if subs, err := linear.Submissions(ctx, "sess-a", true); err != nil || !cmp.Equal(subs, []LinearSubmission{started, message}) {
		t.Errorf("active Submissions = %+v, %v", subs, err)
	}
	if requests, err := linear.Requests(ctx, "sess-a"); err != nil || !cmp.Equal(requests, []LinearRequest{request}) {
		t.Errorf("Requests = %+v, %v", requests, err)
	}
	if n, _ := linear.PendingSubmissions(ctx); n != 2 {
		t.Errorf("PendingSubmissions = %d", n)
	}
	if pending, _ := linear.Pending(ctx); pending != 2 {
		t.Errorf("Pending after change = %d", pending)
	}
	closed := request
	closed.State, closed.UpdatedAt = "closed", at(5)
	if err := linear.Record(ctx, LinearChange{Requests: []LinearRequest{closed}}); err != nil {
		t.Fatal(err)
	}
	if requests, _ := linear.Requests(ctx, "sess-a"); !cmp.Equal(requests, []LinearRequest{closed}) {
		t.Errorf("Requests after close = %+v", requests)
	}
	// A change naming a missing row writes nothing.
	gone := LinearSession{ID: "sess-nope", State: "bound", UpdatedAt: at(3)}
	if err := linear.Record(ctx, LinearChange{Session: &gone, Rows: []LinearOutboxRow{{ID: "sess-nope/x", SessionID: "sess-nope", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(3), CreatedAt: at(3)}}}); !errors.Is(err, ErrLinearSessionNotFound) {
		t.Errorf("Record(unknown session) = %v", err)
	}
	if err := linear.Record(ctx, LinearChange{Submissions: []LinearSubmission{{ID: "act-nope", NextAttemptAt: at(3), UpdatedAt: at(3)}}}); !errors.Is(err, ErrLinearRowGone) {
		t.Errorf("Record(unknown submission) = %v", err)
	}
	if err := linear.Record(ctx, LinearChange{Requests: []LinearRequest{{ID: "inpt-nope", UpdatedAt: at(3)}}}); !errors.Is(err, ErrLinearRowGone) {
		t.Errorf("Record(unknown request) = %v", err)
	}
	if pending, _ := linear.Pending(ctx); pending != 2 {
		t.Errorf("Pending after refused change = %d", pending)
	}
}

func TestLinearOutboxRoundTrip(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	linear := s.Linear()
	rows := []LinearOutboxRow{
		{ID: "sess-a/ack", SessionID: "sess-a", Kind: "activity", Body: []byte(`{"a":1}`), NextAttemptAt: at(0), CreatedAt: at(0)},
		{ID: "sess-a/links", SessionID: "sess-a", Kind: "links", Body: []byte(`{"l":1}`), NextAttemptAt: at(10), CreatedAt: at(1)},
	}
	for _, row := range rows {
		if ok, err := linear.Enqueue(ctx, row); err != nil || !ok {
			t.Fatalf("Enqueue = %v, %v", ok, err)
		}
	}
	if ok, err := linear.Enqueue(ctx, rows[0]); err != nil || ok {
		t.Errorf("duplicate Enqueue = %v, %v; want false", ok, err)
	}
	due, err := linear.Due(ctx, at(5), 10)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(rows[:1], due); diff != "" {
		t.Errorf("Due mismatch (-want +got):\n%s", diff)
	}
	// A session's rows go in order: while its oldest row backs off, the
	// rows behind it wait; another session's rows do not.
	if err := linear.Retry(ctx, "sess-a/ack", 1, at(20)); err != nil {
		t.Fatal(err)
	}
	otherSession := LinearOutboxRow{ID: "sess-b/ack", SessionID: "sess-b", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(0), CreatedAt: at(2)}
	if ok, err := linear.Enqueue(ctx, otherSession); err != nil || !ok {
		t.Fatal(err)
	}
	due, _ = linear.Due(ctx, at(15), 10)
	if len(due) != 1 || due[0].ID != "sess-b/ack" {
		t.Errorf("Due while the oldest backs off = %+v", due)
	}
	if err := linear.Sent(ctx, "sess-b/ack", at(15)); err != nil {
		t.Fatal(err)
	}
	due, _ = linear.Due(ctx, at(25), 10)
	if len(due) != 1 || due[0].ID != "sess-a/ack" {
		t.Errorf("Due once the backoff ends = %+v", due)
	}
	if err := linear.Sent(ctx, "sess-a/ack", at(26)); err != nil {
		t.Fatal(err)
	}
	if due, _ = linear.Due(ctx, at(26), 10); len(due) != 1 || due[0].ID != "sess-a/links" {
		t.Errorf("Due after the head went = %+v", due)
	}
	if err := linear.Sent(ctx, "sess-a/links", at(16)); err != nil {
		t.Fatal(err)
	}
	refused := LinearOutboxRow{ID: "sess-c/x", SessionID: "sess-c", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(1), CreatedAt: at(1)}
	if ok, err := linear.Enqueue(ctx, refused); err != nil || !ok {
		t.Fatal(err)
	}
	if err := linear.Failed(ctx, "sess-c/x", 2, "refused"); err != nil {
		t.Fatal(err)
	}
	if due, _ = linear.Due(ctx, at(100), 10); len(due) != 0 {
		t.Errorf("Due after sent and failed = %+v", due)
	}
	if pending, _ := linear.Pending(ctx); pending != 0 {
		t.Errorf("Pending = %d", pending)
	}
	// A sent key stays as a receipt until pruned, so re-enqueueing it is
	// ignored; after pruning it is accepted again.
	if ok, _ := linear.Enqueue(ctx, rows[1]); ok {
		t.Error("Enqueue of a sent key inserted")
	}
	if err := linear.Prune(ctx, at(17)); err != nil {
		t.Fatal(err)
	}
	if ok, _ := linear.Enqueue(ctx, rows[1]); !ok {
		t.Error("Enqueue after prune did not insert")
	}
	// A refused row is pruned by its creation time.
	if ok, _ := linear.Enqueue(ctx, refused); !ok {
		t.Error("Enqueue of the pruned refused key did not insert")
	}
	_ = time.Second
}
