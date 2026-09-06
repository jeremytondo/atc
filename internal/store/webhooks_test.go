package store

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func delivery(id, deliveryID string, second int) WebhookDelivery {
	return WebhookDelivery{
		ID: id, IntegrationID: "probe", Route: "/probe", DeliveryID: deliveryID,
		Payload: []byte(`{"n":` + id + `}`), NextAttemptAt: at(second), AcceptedAt: at(second),
	}
}

// Acceptance deduplicates on the Integration-scoped identity, another
// Integration's identical identity is a separate delivery, and the
// pending capacity refuses without evicting.
func TestWebhooksAcceptDeduplicatesAndBounds(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	inbox := s.Webhooks()

	if ok, err := inbox.Accept(ctx, delivery("1", "evt-a", 0), 2); err != nil || !ok {
		t.Fatalf("Accept(first) = %v, %v; want true", ok, err)
	}
	if ok, err := inbox.Accept(ctx, delivery("2", "evt-a", 1), 2); err != nil || ok {
		t.Fatalf("Accept(redelivery) = %v, %v; want false, nil", ok, err)
	}
	other := delivery("3", "evt-a", 2)
	other.IntegrationID = "other"
	if ok, err := inbox.Accept(ctx, other, 2); err != nil || !ok {
		t.Fatalf("Accept(other integration) = %v, %v; want true", ok, err)
	}
	if _, err := inbox.Accept(ctx, delivery("4", "evt-b", 3), 2); !errors.Is(err, ErrInboxFull) {
		t.Fatalf("Accept(over capacity) = %v, want ErrInboxFull", err)
	}
	// A redelivery of an existing delivery is still acknowledged at
	// capacity: nothing new is stored, so it cannot be refused.
	if ok, err := inbox.Accept(ctx, delivery("5", "evt-a", 4), 2); err != nil || ok {
		t.Fatalf("Accept(redelivery at capacity) = %v, %v; want false, nil", ok, err)
	}
	pending, err := inbox.Pending(ctx)
	if err != nil || pending != 2 {
		t.Fatalf("Pending = %d, %v; want 2", pending, err)
	}
	due, err := inbox.Due(ctx, at(10), 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []WebhookDelivery{delivery("1", "evt-a", 0), other}
	if diff := cmp.Diff(want, due); diff != "" {
		t.Errorf("Due mismatch (-want +got):\n%s", diff)
	}
}

// Completion drops the payload but keeps the receipt (so the identity
// still deduplicates); a failure schedules the retry and only due rows
// are listed; pruning removes receipts by age and count, never pending
// rows.
func TestWebhooksProcessingLifecycle(t *testing.T) {
	s, _ := openStore(t)
	ctx := context.Background()
	inbox := s.Webhooks()
	for i, id := range []string{"1", "2", "3"} {
		if ok, err := inbox.Accept(ctx, delivery(id, "evt-"+id, i), 10); err != nil || !ok {
			t.Fatal(ok, err)
		}
	}

	if ok, err := inbox.Fail(ctx, "1", 1, at(60)); err != nil || !ok {
		t.Fatalf("Fail = %v, %v; want true", ok, err)
	}
	due, err := inbox.Due(ctx, at(30), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(due); !cmp.Equal(got, []string{"2", "3"}) {
		t.Errorf("Due before retry time = %v, want [2 3]", got)
	}
	due, err = inbox.Due(ctx, at(60), 10)
	if err != nil {
		t.Fatal(err)
	}
	if got := ids(due); !cmp.Equal(got, []string{"2", "3", "1"}) {
		t.Errorf("Due at retry time = %v, want [2 3 1] (retry scheduled last)", got)
	}
	if due[2].Attempts != 1 {
		t.Errorf("attempts = %d, want 1", due[2].Attempts)
	}

	if ok, err := inbox.Complete(ctx, "2", at(70)); err != nil || !ok {
		t.Fatalf("Complete = %v, %v; want true", ok, err)
	}
	if ok, err := inbox.Complete(ctx, "2", at(71)); err != nil || ok {
		t.Fatalf("Complete(again) = %v, %v; want false", ok, err)
	}
	if ok, err := inbox.Accept(ctx, delivery("9", "evt-2", 72), 10); err != nil || ok {
		t.Fatalf("Accept(redelivery of completed) = %v, %v; want false (receipt deduplicates)", ok, err)
	}
	var payload []byte
	if err := s.reads.QueryRowContext(ctx, "SELECT payload FROM webhook_deliveries WHERE id = '2'").Scan(&payload); err != nil {
		t.Fatal(err)
	}
	if payload != nil {
		t.Errorf("completed payload = %q, want NULL", payload)
	}

	if ok, err := inbox.Complete(ctx, "3", at(80)); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Age prune removes 2 (completed at 70) but not 3 (completed at 80).
	if err := inbox.Prune(ctx, at(75), 100); err != nil {
		t.Fatal(err)
	}
	if ok, err := inbox.Accept(ctx, delivery("9", "evt-2", 90), 10); err != nil || !ok {
		t.Fatalf("Accept after receipt pruned = %v, %v; want true", ok, err)
	}
	// Count prune keeps no receipts; the pending rows survive.
	if err := inbox.Prune(ctx, at(0), 0); err != nil {
		t.Fatal(err)
	}
	pending, err := inbox.Pending(ctx)
	if err != nil || pending != 2 {
		t.Fatalf("Pending after prune = %d, %v; want 2 (1 and 9)", pending, err)
	}
	var receipts int
	if err := s.reads.QueryRowContext(ctx, "SELECT COUNT(*) FROM webhook_deliveries WHERE state = 'done'").Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	if receipts != 0 {
		t.Errorf("receipts after count prune = %d, want 0", receipts)
	}
}

// Accepted deliveries survive a reopen with their identity and payload.
func TestWebhooksSurviveReopen(t *testing.T) {
	s, path := openStore(t)
	ctx := context.Background()
	if ok, err := s.Webhooks().Accept(ctx, delivery("1", "evt-1", 0), 10); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reopened.Close() }()
	due, err := reopened.Webhooks().Due(ctx, at(0), 10)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]WebhookDelivery{delivery("1", "evt-1", 0)}, due); diff != "" {
		t.Errorf("Due after reopen mismatch (-want +got):\n%s", diff)
	}
}

func ids(records []WebhookDelivery) []string {
	out := make([]string, 0, len(records))
	for _, r := range records {
		out = append(out, r.ID)
	}
	return out
}
