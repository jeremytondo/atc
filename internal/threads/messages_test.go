package threads

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jeremytondo/atc/internal/api"
)

var messageIDPattern = regexp.MustCompile(`^msg-[23456789bcdfghjkmnpqrstvwxyz]{10}$`)

// A message on an idle thread (ATC-307) mints the pending turn it will
// start, delivery uncertain until the provider answers; delivery is then
// recorded as accepted, a replay of the key returns the same message and
// mints nothing, and a second message while one pends is refused.
func TestSubmitMessageStartsATurn(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id, err := f.service.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadIdle, Title: "T",
		Turn: &TurnObservation{ProviderID: "pt-1", State: api.TurnCompleted},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	for _, text := range []string{"", "  \n\t"} {
		if _, err := f.service.SubmitMessage(ctx, id, Submission{Text: text}); !errors.Is(err, ErrMessageInvalid) {
			t.Errorf("blank text %q = %v; want ErrMessageInvalid", text, err)
		}
	}
	if _, err := f.service.SubmitMessage(ctx, "thrd-nope", Submission{Text: "hi"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown thread = %v", err)
	}

	message, err := f.service.SubmitMessage(ctx, id, Submission{Text: "  continue ", Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	if !messageIDPattern.MatchString(message.ID) || thread.PendingTurn == nil || message.TurnID != thread.PendingTurn.ID || thread.Status != api.ThreadWorking {
		t.Fatalf("message = %+v; thread pending %+v status %s", message, thread.PendingTurn, thread.Status)
	}
	want := api.ThreadMessage{ID: message.ID, ThreadID: id, Key: "k1", Text: "  continue ", TurnID: thread.PendingTurn.ID, Delivery: api.MessageUncertain, CreatedAt: message.CreatedAt}
	if diff := cmp.Diff(want, message); diff != "" {
		t.Errorf("message (-want +got):\n%s", diff)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on submit = %v", got)
	}
	if _, err := f.service.SubmitMessage(ctx, id, Submission{Text: "another", Key: "k2"}); !errors.Is(err, ErrTurnPending) {
		t.Errorf("second message while pending = %v; want ErrTurnPending", err)
	}

	delivered, err := f.service.MessageDelivered(ctx, id, message.ID)
	if err != nil || delivered.Delivery != api.MessageAccepted || delivered.ID != message.ID {
		t.Fatalf("MessageDelivered = %+v, %v", delivered, err)
	}
	if _, err := f.service.MessageDelivered(ctx, id, "msg-nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("MessageDelivered(unknown) = %v", err)
	}
	// The same key returns the recorded message, whatever the text says
	// now, and sends nothing: still one pending turn, no event.
	replay, err := f.service.SubmitMessage(ctx, id, Submission{Text: "different", Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(delivered, replay, cmpopts.EquateApproxTime(0)); diff != "" {
		t.Errorf("replay (-want +got):\n%s", diff)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on replay = %v", got)
	}
	// The key is scoped to the thread.
	other, err := f.service.ObserveExternal(ctx, ExternalObservation{IntegrationID: "t3code", ProviderID: "t2", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadIdle, Title: "U"})
	if err != nil {
		t.Fatal(err)
	}
	if elsewhere, err := f.service.SubmitMessage(ctx, other, Submission{Text: "elsewhere", Key: "k1"}); err != nil || elsewhere.ID == message.ID || elsewhere.ThreadID != other {
		t.Errorf("same key on another thread = %+v, %v", elsewhere, err)
	}
}

// A rejected message (ATC-307): recorded for good with the provider's
// reason, the pending turn it would have started withdrawn and the
// status it displaced restored; the key replays the rejection and sends
// nothing. A rejection after a restart restores nothing better than
// unknown.
func TestSubmitMessageRejected(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id, err := f.service.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadError, StatusDetail: "provider down", Title: "T",
		Turn: &TurnObservation{ProviderID: "pt-1", State: api.TurnFailed},
	})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := f.service.Get(id)
	f.drain()
	message, err := f.service.SubmitMessage(ctx, id, Submission{Text: "retry", Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.MessageRejected(ctx, id, message.ID, "no session"); err != nil {
		t.Fatal(err)
	}
	after, _ := f.service.Get(id)
	if after.PendingTurn != nil || after.Status != before.Status || after.StatusDetail != before.StatusDetail || after.LatestTurn.ID != before.LatestTurn.ID {
		t.Errorf("after rejection = status %s %q pending %+v latest %+v; want the pre-submission shape %s %q", after.Status, after.StatusDetail, after.PendingTurn, after.LatestTurn, before.Status, before.StatusDetail)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id, "thread.updated " + id}) {
		t.Errorf("events = %v; want the submission and its withdrawal", got)
	}
	_, err = f.service.SubmitMessage(ctx, id, Submission{Text: "retry", Key: "k1"})
	if !errors.Is(err, ErrMessageRejected) || !strings.Contains(err.Error(), "no session") {
		t.Errorf("replay of a rejected key = %v; want ErrMessageRejected with the reason", err)
	}
	if err := f.service.MessageRejected(ctx, id, message.ID, "again"); err != nil {
		t.Errorf("rejecting twice = %v", err)
	}
	if _, err := f.service.SubmitMessage(ctx, id, Submission{Text: "fresh", Key: "k2"}); err != nil {
		t.Errorf("a fresh message after the withdrawal = %v", err)
	}

	// Rejected by a new server process: the pending turn is withdrawn,
	// the status honestly unknown.
	restarted := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Projects: f.store.Projects(), Hub: f.hub, Now: f.clock.Now})
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	pending, _ := restarted.Get(id)
	if pending.PendingTurn == nil {
		t.Fatal("pending turn did not survive the reload")
	}
	fresh, err := restarted.SubmitMessage(ctx, id, Submission{Text: "fresh", Key: "k2"})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.MessageRejected(ctx, id, fresh.ID, "still no session"); err != nil {
		t.Fatal(err)
	}
	if got, _ := restarted.Get(id); got.PendingTurn != nil || got.Status != api.ThreadUnknown {
		t.Errorf("rejection after a restart = status %s pending %+v; want unknown, nothing pending", got.Status, got.PendingTurn)
	}
}

// A steering submission (ATC-307) directs the turn the provider is
// running: no pending turn, the message's turn is the running one, and
// a second steer directs the same turn. Without a running turn a
// steering submission starts a turn like any other.
func TestSubmitMessageSteers(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id, err := f.service.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadWorking, Title: "T",
		Turn: &TurnObservation{ProviderID: "pt-1", State: api.TurnRunning},
	})
	if err != nil {
		t.Fatal(err)
	}
	running := f.turn(t, id)
	f.drain()
	first, err := f.service.SubmitMessage(ctx, id, Submission{Text: "also do this", Steers: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.SubmitMessage(ctx, id, Submission{Text: "and this", Steers: true})
	if err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	if first.TurnID != running.ID || second.TurnID != running.ID || thread.PendingTurn != nil || first.ID == second.ID {
		t.Errorf("steers = %+v, %+v; pending %+v; want both directing %s", first, second, thread.PendingTurn, running.ID)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on a steer = %v; want none, the thread is unchanged", got)
	}
	// Rejecting a steer withdraws nothing.
	if err := f.service.MessageRejected(ctx, id, first.ID, "no"); err != nil {
		t.Fatal(err)
	}
	if got := f.turn(t, id); got.ID != running.ID || got.State != api.TurnRunning {
		t.Errorf("running turn after a rejected steer = %+v", got)
	}
	// The running turn ends: the steered messages' turn has its reply.
	if _, err := f.service.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadIdle, Title: "T",
		Turn: &TurnObservation{ProviderID: "pt-1", State: api.TurnCompleted, Response: "did both"},
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.turn(t, id); got.ID != running.ID || got.Response != "did both" {
		t.Errorf("steered turn's end = %+v", got)
	}
	// Nothing running: a steering submission starts a turn.
	third, err := f.service.SubmitMessage(ctx, id, Submission{Text: "next", Steers: true})
	if err != nil {
		t.Fatal(err)
	}
	if thread, _ := f.service.Get(id); thread.PendingTurn == nil || third.TurnID != thread.PendingTurn.ID {
		t.Errorf("steer on an idle thread = %+v; pending %+v", third, thread.PendingTurn)
	}
}
