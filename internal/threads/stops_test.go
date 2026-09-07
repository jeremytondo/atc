package threads

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
)

var stopIDPattern = regexp.MustCompile(`^stop-[23456789bcdfghjkmnpqrstvwxyz]{10}$`)

// running observes a T3 thread with provider turn turnID running.
func (f *fixture) running(t *testing.T, providerID, turnID string) string {
	t.Helper()
	id, err := f.service.ObserveExternal(context.Background(), ExternalObservation{
		IntegrationID: "t3code", ProviderID: providerID, InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadWorking, Title: "T",
		Turn: &TurnObservation{ProviderID: turnID, State: api.TurnRunning},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	return id
}

// closed is the program's report of the session closed at `at` with the
// turn in the given state.
func closed(providerID, turnID string, state api.TurnState, at time.Time) ExternalObservation {
	return ExternalObservation{
		IntegrationID: "t3code", ProviderID: providerID, Status: api.ThreadIdle, Title: "T",
		Turn: &TurnObservation{ProviderID: turnID, State: state}, SessionClosedAt: at,
	}
}

// A stop on a thread at rest resolves at once as finished, with nothing
// to dispatch: nothing was running, nothing was submitted, nothing was
// blocked — and it never claims an interruption.
func TestStopIdleFinishesWithoutDispatch(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.external(t, "t1", api.ThreadIdle)
	req, stop, err := f.service.BeginStop(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if req.Dispatch || stop.State != api.StopFinished || stop.ResolvedAt == nil || !stopIDPattern.MatchString(stop.ID) || !strings.Contains(stop.Detail, "nothing was running") {
		t.Errorf("stop on an idle thread = %+v, %+v", req, stop)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on an immediate finish = %v", got)
	}
	if thread, _ := f.service.Get(id); thread.Stop != nil {
		t.Errorf("thread shows a resolved stop: %+v", thread.Stop)
	}
	if got, err := f.service.Stop(id, stop.ID); err != nil || got.State != api.StopFinished {
		t.Errorf("Stop = %+v, %v", got, err)
	}
	if _, err := f.service.Stop(id, "stop-nope"); !errors.Is(err, ErrStopNotFound) {
		t.Errorf("unknown stop = %v", err)
	}
	if _, _, err := f.service.BeginStop(ctx, "thrd-nope", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown thread = %v", err)
	}
	// A message still goes through: nothing is stopping.
	if _, err := f.service.SubmitMessage(ctx, id, Submission{Text: "go"}); err != nil {
		t.Errorf("message after a finished stop = %v", err)
	}
}

// A stop on running work (ATC-308): recorded with its scope and shown
// on the thread; a second stop meanwhile is the first; messages,
// answers, and decisions are refused while it is stopping; the
// program's session closed before the stop was accepted proves nothing;
// closed no earlier than that confirms it — stopped, the turn
// interrupted, the pending approval and question closed for good even
// when the program keeps presenting them, the answer in flight
// superseded — and the thread takes work again.
func TestStopRunningConfirmedOnSessionClose(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.running(t, "t1", "pt-1")
	requested := f.clock.Now()
	approval := ApprovalObservation{RequestID: "apr-1", Kind: api.ApprovalCommand, Summary: "Run?", Options: approveOrDeny, RequestedAt: requested}
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{approval}); err != nil {
		t.Fatal(err)
	}
	question := twoQuestions("req-1", requested)
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{question}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	approvalID, requestID := thread.Approvals[0].ID, thread.InputRequests[0].ID
	answers := []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2", Choices: []string{"go"}}}
	answer, err := f.service.BeginAnswer(ctx, id, requestID, answers)
	if err != nil {
		t.Fatal(err)
	}
	f.drain()

	req, stop, err := f.service.BeginStop(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if !req.Dispatch || req.StopID != stop.ID || stop.State != api.StopStopping || stop.Delivery != api.MessageUncertain || req.CreatedAt != stop.CreatedAt {
		t.Fatalf("stop = %+v, %+v", req, stop)
	}
	if req.CreatedAt.Truncate(time.Millisecond) != req.CreatedAt {
		t.Errorf("CreatedAt %v is not at millisecond precision", req.CreatedAt)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a stop = %v", got)
	}
	if thread, _ = f.service.Get(id); thread.Stop == nil || thread.Stop.ID != stop.ID {
		t.Errorf("thread does not show the stop: %+v", thread.Stop)
	}
	// The same stop again, re-dispatched while uncertain, not once
	// delivered.
	if again, got, err := f.service.BeginStop(ctx, id, ""); err != nil || got.ID != stop.ID || !again.Dispatch {
		t.Errorf("second stop while uncertain = %+v, %+v, %v", again, got, err)
	}
	if delivered, err := f.service.StopDelivered(ctx, id, stop.ID); err != nil || delivered.Delivery != api.MessageAccepted || delivered.State != api.StopStopping {
		t.Errorf("StopDelivered = %+v, %v", delivered, err)
	}
	if again, got, err := f.service.BeginStop(ctx, id, ""); err != nil || got.ID != stop.ID || again.Dispatch {
		t.Errorf("second stop once delivered = %+v, %+v, %v", again, got, err)
	}
	f.drain()
	// Nothing new while stopping.
	if _, err := f.service.SubmitMessage(ctx, id, Submission{Text: "more", Key: "k1"}); !errors.Is(err, ErrThreadStopping) || !strings.Contains(err.Error(), stop.ID) {
		t.Errorf("message while stopping = %v", err)
	}
	if _, err := f.service.BeginDecision(id, approvalID, api.DecisionApprove); !errors.Is(err, ErrThreadStopping) {
		t.Errorf("decision while stopping = %v", err)
	}
	if _, err := f.service.BeginAnswer(ctx, id, requestID, answers); !errors.Is(err, ErrThreadStopping) {
		t.Errorf("answer while stopping = %v", err)
	}
	// A session closed before the stop was accepted is old news; the turn
	// still running says the same.
	if _, err := f.service.ObserveExternal(ctx, closed("t1", "pt-1", api.TurnRunning, stop.CreatedAt.Add(-time.Second))); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); thread.Stop == nil {
		t.Fatal("an older session close confirmed the stop")
	}
	if got := f.drain(); len(got) != 1 {
		t.Errorf("events on old evidence = %v", got)
	}
	// Closed at the stop's own instant: confirmed.
	if _, err := f.service.ObserveExternal(ctx, closed("t1", "pt-1", api.TurnInterrupted, stop.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	if thread.Stop != nil || thread.Status != api.ThreadIdle || thread.LatestTurn.State != api.TurnInterrupted || len(thread.Approvals) != 0 || len(thread.InputRequests) != 0 {
		t.Errorf("thread after confirmation = %+v", thread)
	}
	got, err := f.service.Stop(id, stop.ID)
	if err != nil || got.State != api.StopStopped || got.ResolvedAt == nil || !strings.Contains(got.Detail, "closed the session") || !strings.Contains(got.Detail, "interrupted") {
		t.Errorf("confirmed stop = %+v, %v", got, err)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on confirmation = %v", got)
	}
	request, _ := f.service.InputRequest(id, requestID)
	if request.Status != api.InputRequestResolved || request.Resolution != api.InputResolvedByStop || request.Answer.State != api.InputAnswerSuperseded || !strings.Contains(request.Answer.Detail, stop.ID) {
		t.Errorf("request after the stop = %+v (answer %+v)", request, request.Answer)
	}
	if _, err := f.service.BeginDecision(id, approvalID, api.DecisionApprove); !errors.Is(err, ErrApprovalResolved) {
		t.Errorf("decision after the stop = %v", err)
	}
	// The same answers again recover the superseded answer, its outcome
	// on the record; other answers are refused: the request is closed.
	if req, err := f.service.BeginAnswer(ctx, id, requestID, answers); err != nil || req.AnswerID != answer.AnswerID || req.Dispatch {
		t.Errorf("same answers after the stop = %+v, %v", req, err)
	}
	if _, err := f.service.BeginAnswer(ctx, id, requestID, []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Blue"}}, {QuestionID: "q2", Choices: []string{"go"}}}); !errors.Is(err, ErrInputResolved) {
		t.Errorf("other answers after the stop = %v", err)
	}
	// The program still presents the stale requests: they stay closed.
	if err := f.service.ObserveApprovals(ctx, "t3code", "t1", []ApprovalObservation{approval}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t1", []InputObservation{question}, nil); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); len(thread.Approvals) != 0 || len(thread.InputRequests) != 0 {
		t.Errorf("stale presentation revived closed requests: %+v %+v", thread.Approvals, thread.InputRequests)
	}
	// Work goes on: a message is accepted, and a later stop is a new one
	// scoped to the new work.
	message, err := f.service.SubmitMessage(ctx, id, Submission{Text: "continue", Key: "k2"})
	if err != nil {
		t.Fatalf("message after the stop = %v", err)
	}
	later, next, err := f.service.BeginStop(ctx, id, "k-later")
	if err != nil || next.ID == stop.ID || !later.Dispatch || next.State != api.StopStopping || next.Key != "k-later" {
		t.Errorf("a later stop = %+v, %+v, %v", later, next, err)
	}
	if thread, _ = f.service.Get(id); thread.PendingTurn == nil || thread.PendingTurn.ID != message.TurnID {
		t.Errorf("pending turn before the later stop = %+v", thread.PendingTurn)
	}
	// The key recovers its stop in any state, never stopping later work:
	// once this one resolves, the same key returns it resolved and the
	// new work goes on; a different key is a new stop.
	if _, err := f.service.StopFailed(ctx, id, next.ID, "refused"); err != nil {
		t.Fatal(err)
	}
	if recovered, got, found, err := f.service.RecoverStop(id, "k-later"); err != nil || !found || got.ID != next.ID || got.State != api.StopFailed || recovered.Dispatch {
		t.Errorf("RecoverStop by key = %+v, %+v, %v, %v", recovered, got, found, err)
	}
	if again, got, err := f.service.BeginStop(ctx, id, "k-later"); err != nil || got.ID != next.ID || again.Dispatch {
		t.Errorf("stop by the same key after failure = %+v, %+v, %v", again, got, err)
	}
	if _, got, err := f.service.BeginStop(ctx, id, "k-newer"); err != nil || got.ID == next.ID || got.State != api.StopStopping {
		t.Errorf("stop by a new key = %+v, %v", got, err)
	}
	if _, _, found, err := f.service.RecoverStop(id, "k-unknown"); err != nil || !found {
		t.Errorf("RecoverStop by an unknown key while one is stopping = %v, %v; want the stopping one", found, err)
	}
}

// A stop covers a submitted turn that never started (ATC-308): on
// confirmation the pending slot clears — the provider's turn stays the
// latest, so the next submission binds against it — the stop names the
// withdrawn turn, and the message still uncertain on it is withdrawn,
// so a replay of its key delivers nothing.
func TestStopWithdrawsPendingTurn(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.running(t, "t1", "pt-1")
	follow, err := f.service.SubmitMessage(ctx, id, Submission{Text: "then this", Key: "k1"})
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	req, stop, err := f.service.BeginStop(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.StopDelivered(ctx, id, stop.ID); err != nil {
		t.Fatal(err)
	}
	// A replay while stopping is refused rather than re-sent.
	if _, err := f.service.SubmitMessage(ctx, id, Submission{Text: "then this", Key: "k1"}); !errors.Is(err, ErrThreadStopping) {
		t.Errorf("replay while stopping = %v", err)
	}
	if _, err := f.service.ObserveExternal(ctx, closed("t1", "pt-1", api.TurnInterrupted, req.CreatedAt.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	if thread.PendingTurn != nil || thread.LatestTurn == nil || thread.LatestTurn.ID == follow.TurnID || thread.LatestTurn.State != api.TurnInterrupted {
		t.Errorf("turns after the stop = latest %+v pending %+v", thread.LatestTurn, thread.PendingTurn)
	}
	got, _ := f.service.Stop(id, stop.ID)
	if got.State != api.StopStopped || !strings.Contains(got.Detail, "submitted turn "+follow.TurnID+" withdrawn before it started") {
		t.Errorf("stop = %+v", got)
	}
	// The provider re-reporting its interrupted turn does not bind the
	// next submission: it is the turn the thread held, not a new one.
	next, err := f.service.SubmitMessage(ctx, id, Submission{Text: "again", Key: "k2"})
	if err != nil {
		t.Fatalf("message after the stop = %v", err)
	}
	if _, err := f.service.ObserveExternal(ctx, closed("t1", "pt-1", api.TurnInterrupted, req.CreatedAt.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	if thread, _ = f.service.Get(id); thread.PendingTurn == nil || thread.PendingTurn.ID != next.TurnID {
		t.Errorf("the provider's old turn bound the new submission: latest %+v pending %+v", thread.LatestTurn, thread.PendingTurn)
	}
	if _, err := f.service.SubmitMessage(ctx, id, Submission{Text: "then this", Key: "k1"}); !errors.Is(err, ErrMessageWithdrawn) || !strings.Contains(err.Error(), stop.ID) {
		t.Errorf("replay after the stop = %v", err)
	}
	if _, err := f.service.MessageByKey(ctx, id, "k1"); !errors.Is(err, ErrMessageWithdrawn) {
		t.Errorf("MessageByKey after the stop = %v", err)
	}
}

// Work that ended on its own before the stop was accepted is reported
// as finished, never as interrupted; a message accepted for the running
// turn stays accepted. A completion the provider dates after the
// acceptance is the stop's doing: stopped.
func TestStopFinishedWhenWorkCompleted(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.running(t, "t1", "pt-1")
	steer, err := f.service.SubmitMessage(ctx, id, Submission{Text: "steer", Key: "k1", Steers: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.MessageDelivered(ctx, id, steer.ID); err != nil {
		t.Fatal(err)
	}
	req, stop, err := f.service.BeginStop(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	before := closed("t1", "pt-1", api.TurnCompleted, req.CreatedAt)
	before.Turn.CompletedAt = req.CreatedAt.Add(-time.Second)
	if _, err := f.service.ObserveExternal(ctx, before); err != nil {
		t.Fatal(err)
	}
	got, _ := f.service.Stop(id, stop.ID)
	if got.State != api.StopFinished || !strings.Contains(got.Detail, "had completed") {
		t.Errorf("stop after natural completion = %+v", got)
	}
	if replay, err := f.service.SubmitMessage(ctx, id, Submission{Text: "steer", Key: "k1"}); err != nil || replay.Delivery != api.MessageAccepted {
		t.Errorf("accepted message after the stop = %+v, %v", replay, err)
	}
	// Running again, stopped, and completed only after the acceptance.
	if _, err := f.service.ObserveExternal(ctx, ExternalObservation{IntegrationID: "t3code", ProviderID: "t1", Status: api.ThreadWorking, Title: "T", Turn: &TurnObservation{ProviderID: "pt-2", State: api.TurnRunning}}); err != nil {
		t.Fatal(err)
	}
	req, stop, err = f.service.BeginStop(ctx, id, "")
	if err != nil || !req.Dispatch {
		t.Fatalf("second stop = %+v, %v", req, err)
	}
	after := closed("t1", "pt-2", api.TurnCompleted, req.CreatedAt)
	after.Turn.CompletedAt = req.CreatedAt
	if _, err := f.service.ObserveExternal(ctx, after); err != nil {
		t.Fatal(err)
	}
	if got, _ = f.service.Stop(id, stop.ID); got.State != api.StopStopped || !strings.Contains(got.Detail, "after the stop was accepted") {
		t.Errorf("stop with a completion dated after it = %+v", got)
	}
}

// The program refusing the stop fails it for good and lifts the
// restriction; the refusal never claims anything about the work.
func TestStopFailedLiftsRestriction(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.running(t, "t1", "pt-1")
	_, stop, err := f.service.BeginStop(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	failed, err := f.service.StopFailed(ctx, id, stop.ID, "T3 Code rejected the stop: no such thread")
	if err != nil || failed.State != api.StopFailed || failed.ResolvedAt == nil || !strings.Contains(failed.Detail, "no such thread") {
		t.Errorf("StopFailed = %+v, %v", failed, err)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on failure = %v", got)
	}
	thread, _ := f.service.Get(id)
	if thread.Stop != nil || thread.LatestTurn.State != api.TurnRunning {
		t.Errorf("thread after a failed stop = %+v", thread)
	}
	if _, err := f.service.SubmitMessage(ctx, id, Submission{Text: "go on", Steers: true}); err != nil {
		t.Errorf("message after a failed stop = %v", err)
	}
	// Failing it again changes nothing.
	if again, err := f.service.StopFailed(ctx, id, stop.ID, "later"); err != nil || again.Detail != failed.Detail {
		t.Errorf("second failure = %+v, %v", again, err)
	}
}

// A stop survives a restart (ATC-308): the reloaded service refuses
// work for it, still resolves it on the program's evidence, and keeps
// the original scope — a turn that started later is not what it stops.
func TestStopSurvivesRestart(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.running(t, "t1", "pt-1")
	req, stop, err := f.service.BeginStop(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.StopDelivered(ctx, id, stop.ID); err != nil {
		t.Fatal(err)
	}

	reloaded := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Projects: f.store.Projects(), Hub: events.NewHubAt(8, 1), Now: f.clock.Now})
	if err := reloaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	thread, _ := reloaded.Get(id)
	if thread.Stop == nil || thread.Stop.ID != stop.ID || thread.Stop.Delivery != api.MessageAccepted {
		t.Fatalf("stop after reload = %+v", thread.Stop)
	}
	if _, err := reloaded.SubmitMessage(ctx, id, Submission{Text: "x"}); !errors.Is(err, ErrThreadStopping) {
		t.Errorf("message after reload = %v", err)
	}
	if again, got, err := reloaded.BeginStop(ctx, id, ""); err != nil || got.ID != stop.ID || again.Dispatch {
		t.Errorf("stop after reload = %+v, %+v, %v", again, got, err)
	}
	// The program reports the session closed at the stop, its turn
	// interrupted: confirmed, with the boot-coerced turn as it stands.
	if _, err := reloaded.ObserveExternal(ctx, closed("t1", "pt-1", api.TurnInterrupted, req.CreatedAt)); err != nil {
		t.Fatal(err)
	}
	got, _ := reloaded.Stop(id, stop.ID)
	if got.State != api.StopStopped {
		t.Errorf("stop after evidence = %+v", got)
	}
	if thread, _ = reloaded.Get(id); thread.Stop != nil || thread.LatestTurn.State != api.TurnInterrupted {
		t.Errorf("thread after evidence = %+v", thread)
	}
}

// The program dropping the thread resolves a stop being confirmed:
// nothing runs on a conversation it no longer reports.
func TestArchiveResolvesStop(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.running(t, "t1", "pt-1")
	_, stop, err := f.service.BeginStop(ctx, id, "")
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.ArchiveExternalThread(ctx, "t3code", "t1"); err != nil {
		t.Fatal(err)
	}
	got, _ := f.service.Stop(id, stop.ID)
	thread, _ := f.service.Get(id)
	if got.State != api.StopStopped || !strings.Contains(got.Detail, "dropped the thread") || !thread.Archived || thread.Stop != nil {
		t.Errorf("stop after the drop = %+v; thread %+v", got, thread)
	}
	// Without a stop, an answer awaiting evidence on a dropped thread is
	// superseded too.
	other := f.external(t, "t2", api.ThreadWaitingForInput)
	if _, err := f.service.ObserveInputs(ctx, "t3code", "t2", []InputObservation{twoQuestions("req-1", f.clock.Now())}, nil); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(other)
	if _, err := f.service.BeginAnswer(ctx, other, thread.InputRequests[0].ID, []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Red"}}, {QuestionID: "q2", Choices: []string{"go"}}}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ArchiveExternalThread(ctx, "t3code", "t2"); err != nil {
		t.Fatal(err)
	}
	if request, _ := f.service.InputRequest(other, thread.InputRequests[0].ID); request.Answer == nil || request.Answer.State != api.InputAnswerSuperseded || len(f.service.UnresolvedAnswers("t3code")) != 0 {
		t.Errorf("answer after the drop = %+v; unresolved %v", request, f.service.UnresolvedAnswers("t3code"))
	}
}

// A stop on a thread blocked with no turn known — the status live, the
// work waiting — is stopped, not finished, once the program closes the
// session.
func TestStopBlockedWithoutTurn(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	id := f.external(t, "t1", api.ThreadWaitingForPermission)
	req, stop, err := f.service.BeginStop(ctx, id, "")
	if err != nil || !req.Dispatch {
		t.Fatalf("stop on a blocked thread = %+v, %v", req, err)
	}
	if _, err := f.service.ObserveExternal(ctx, ExternalObservation{IntegrationID: "t3code", ProviderID: "t1", Status: api.ThreadIdle, Title: "T", SessionClosedAt: req.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	if got, _ := f.service.Stop(id, stop.ID); got.State != api.StopStopped || !strings.Contains(got.Detail, "waiting_for_permission") {
		t.Errorf("stop = %+v", got)
	}
}
