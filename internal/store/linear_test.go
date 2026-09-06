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
	seen := at(5)
	records := []LinearSession{
		{ID: "sess-a", Prompt: "hi", State: "accepted", CreatedAt: at(0), UpdatedAt: at(0)},
		{ID: "sess-b", State: "started", ThreadID: "thrd-a", TurnID: "turn-a", NoticedStatus: "waiting_for_input", CompletedSeenAt: &seen, CreatedAt: at(1), UpdatedAt: at(1)},
		{ID: "sess-c", State: "done", Outcome: "responded", CreatedAt: at(2), UpdatedAt: at(2)},
	}
	for _, record := range records {
		if ok, err := linear.InsertSession(ctx, record); err != nil || !ok {
			t.Fatalf("InsertSession = %v, %v", ok, err)
		}
	}
	if ok, err := linear.InsertSession(ctx, records[0]); err != nil || ok {
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
	// A session moves together with the calls it owes; a known key is
	// left alone, and an unknown session writes nothing at all.
	updated := records[0]
	updated.State, updated.Prompt, updated.ThreadID, updated.TurnID, updated.UpdatedAt = "started", "", "thrd-b", "turn-b", at(3)
	row := LinearOutboxRow{ID: "sess-a/started", SessionID: "sess-a", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(3), CreatedAt: at(3)}
	if ok, err := linear.UpdateSession(ctx, updated, row, row); err != nil || !ok {
		t.Fatalf("UpdateSession = %v, %v", ok, err)
	}
	if got, _ := linear.GetSession(ctx, "sess-a"); !cmp.Equal(updated, got) {
		t.Errorf("after update = %+v", got)
	}
	if due, _ := linear.Due(ctx, at(10), 10); len(due) != 1 || due[0].ID != "sess-a/started" {
		t.Errorf("owed after update = %+v, want the one row", due)
	}
	if ok, err := linear.UpdateSession(ctx, LinearSession{ID: "sess-nope", State: "done", UpdatedAt: at(3)}, LinearOutboxRow{ID: "sess-nope/x", SessionID: "sess-nope", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(3), CreatedAt: at(3)}); err != nil || ok {
		t.Errorf("UpdateSession(unknown) = %v, %v", ok, err)
	}
	if due, _ := linear.Due(ctx, at(10), 10); len(due) != 1 {
		t.Errorf("an unknown session's update queued rows: %+v", due)
	}
	// A repeated insert of a known session still adds the rows it owes
	// under absent keys.
	if ok, err := linear.InsertSession(ctx, records[0], LinearOutboxRow{ID: "sess-a/ack", SessionID: "sess-a", Kind: "activity", Body: []byte(`{}`), NextAttemptAt: at(4), CreatedAt: at(4)}); err != nil || ok {
		t.Errorf("repeated InsertSession = %v, %v; want false", ok, err)
	}
	if due, _ := linear.Due(ctx, at(10), 10); len(due) != 2 {
		t.Errorf("owed after repeated insert = %+v, want two rows", due)
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
	if err := linear.Retry(ctx, "sess-a/ack", 1, at(20)); err != nil {
		t.Fatal(err)
	}
	due, _ = linear.Due(ctx, at(15), 10)
	if len(due) != 1 || due[0].ID != "sess-a/links" {
		t.Errorf("Due after retry = %+v", due)
	}
	if err := linear.Sent(ctx, "sess-a/links", at(16)); err != nil {
		t.Fatal(err)
	}
	if err := linear.Failed(ctx, "sess-a/ack", 2, "refused"); err != nil {
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
	if ok, _ := linear.Enqueue(ctx, rows[0]); !ok {
		t.Error("Enqueue of the pruned refused key did not insert")
	}
	_ = time.Second
}
