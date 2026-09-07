package linear

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
)

// Restarts at every boundary against the same store: accepted work is
// kept, a start never dispatched is owed again, a start that may have
// been dispatched is never repeated, a watched Turn resumes, a
// submission recorded but not dispatched is dispatched once, one left
// uncertain is reconciled under the same key, a stop still stopping is
// resolved on the evidence, and owed Linear calls are sent.
func TestRestartResumesEveryBoundary(t *testing.T) {
	f := newFixture(t)
	f.start()
	now := f.clock.Now()
	repo := f.store.Linear()
	ctx := context.Background()

	// Started and watched; then Linear goes down, so its acknowledgement
	// and links stay owed.
	f.linear.set(func(l *fakeLinear) { l.failNext = 1000 })
	f.process(t, "dlv-1", createdEvent("sess-watched", now))
	watchedID, watchedProvider := f.startedThread("sess-watched")
	f.report(watchedProvider, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})

	// Accepted, with T3 silent; the server stops before the start lands.
	release := make(chan struct{})
	f.starter.set(func(s *fakeCoordinator) { s.block = release })
	f.process(t, "dlv-2", createdEvent("sess-accepted", now))
	waitFor(t, "start in flight", func() bool { return f.starter.count() == 2 })
	f.stop()
	if session := f.session("sess-accepted"); session.State != stateStarting || session.ThreadID != "" {
		t.Fatalf("stopped mid-start: %+v", session)
	}
	if acts := f.linear.activitiesOf("sess-watched"); len(acts) != 0 {
		t.Fatalf("Linear was down; activities = %+v", acts)
	}

	// Recorded with its thread before dispatch, then the server died: the
	// thread exists in ATC, T3 may or may not have it.
	recordedID, err := f.threads.ObserveExternal(ctx, threads.ExternalObservation{IntegrationID: providerT3, ProviderID: "t3-recorded", InitialDirectory: f.dir, Title: "T"})
	if err != nil {
		t.Fatal(err)
	}
	recordedTurn, err := f.threads.SubmitTurn(ctx, recordedID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.InsertSession(ctx, store.LinearSession{ID: "sess-recorded", State: stateStarting, ThreadID: recordedID, CreatedAt: now, UpdatedAt: now},
		[]store.LinearSubmission{{ID: startKey("sess-recorded"), SessionID: "sess-recorded", Kind: kindStart, State: subSent, TurnID: recordedTurn, OperationID: recordedTurn, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now}}); err != nil {
		t.Fatal(err)
	}

	f.starter.set(func(s *fakeCoordinator) { s.block = nil })
	f.linear.set(func(l *fakeLinear) { l.failNext = 0 })
	startsBefore := f.starter.count()
	f.start()
	// The never-dispatched start is owed again — one more start, no more.
	f.startedThread("sess-accepted")
	// The recorded one is not started again; Linear hears it is uncertain,
	// and the recorded Thread is watched.
	acts := f.waitActivities("sess-recorded", 1)
	contains(t, acts[0].Body, "restarted while starting")
	if session := f.session("sess-recorded"); session.State != stateBound || session.ThreadID != recordedID {
		t.Errorf("recorded session = %+v", session)
	}
	f.report("t3-recorded", api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-r", State: api.TurnCompleted, Response: "Recorded answer."})
	acts = f.waitActivities("sess-recorded", 2)
	if acts[1].Type != contentResponse || acts[1].Body != "Recorded answer." {
		t.Errorf("recorded result = %+v", acts[1])
	}
	// The watched one gets its owed calls, then its outcome.
	acts = f.waitActivities("sess-watched", 2)
	contains(t, acts[0].Body+acts[1].Body, "https://t3.test/env-1/"+watchedProvider)
	waitFor(t, "watched links", func() bool { return len(f.linear.linksOf("sess-watched")) == 2 })
	f.report(watchedProvider, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "Watched answer."})
	acts = f.waitActivities("sess-watched", 3)
	if acts[2].Body != "Watched answer." {
		t.Errorf("watched result = %+v", acts[2])
	}
	if session := f.session("sess-watched"); session.ThreadID != watchedID {
		t.Errorf("watched session moved: %+v", session)
	}
	time.Sleep(40 * time.Millisecond)
	if f.starter.count() != startsBefore+1 {
		t.Errorf("starts after restart = %d, want %d (only the never-dispatched one)", f.starter.count(), startsBefore+1)
	}

	// A message recorded while the server was down is dispatched once
	// after the restart; one left uncertain is reconciled under its key.
	f.stop()
	f.prompt(t, "dlv-3", "sess-watched", "recorded while down")
	f.starter.set(func(s *fakeCoordinator) { s.dispatch = integrations.ErrDeliveryUncertain })
	f.start()
	uncertain := f.waitSubmission("sess-watched", "act-dlv-3", "uncertain", func(s store.LinearSubmission) bool {
		return s.State == subSent && s.Delivery == string(api.MessageUncertain)
	})
	f.stop()
	f.starter.set(func(s *fakeCoordinator) { s.dispatch = nil })
	f.start()
	accepted := f.waitSubmission("sess-watched", "act-dlv-3", "accepted after restart", func(s store.LinearSubmission) bool { return s.Delivery == string(api.MessageAccepted) })
	if accepted.OperationID != uncertain.OperationID || accepted.TurnID != uncertain.TurnID {
		t.Errorf("the restart changed the message: %+v then %+v", uncertain, accepted)
	}
	messages, _, _, _ := f.starter.sent()
	for _, m := range messages {
		if m.Key != "act-dlv-3" {
			t.Errorf("message under another key: %+v", m)
		}
	}
	if len(messages) < 2 {
		t.Errorf("messages = %+v, want the dispatch and its reconciliation", messages)
	}
	f.waitActivities("sess-watched", 4)

	// A stop still stopping across a restart resolves on the evidence
	// that arrives afterwards, and nothing is dispatched twice.
	f.report(watchedProvider, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnRunning})
	f.stopSignal(t, "dlv-4", "sess-watched")
	f.waitSubmission("sess-watched", "act-dlv-4", "stop sent", func(s store.LinearSubmission) bool { return s.State == subSent })
	f.restart()
	time.Sleep(40 * time.Millisecond)
	if _, _, _, stops := f.starter.sent(); len(stops) != 1 {
		t.Errorf("stops after restart = %+v, want the one", stops)
	}
	if _, err := f.threads.ObserveExternal(ctx, threads.ExternalObservation{
		IntegrationID: providerT3, ProviderID: watchedProvider, InitialDirectory: f.dir, Title: "T", Status: api.ThreadIdle,
		Turn: &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnInterrupted}, SessionClosedAt: f.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	stop := f.waitSubmission("sess-watched", "act-dlv-4", "stop resolved", func(s store.LinearSubmission) bool { return s.State == subDone })
	if stop.Outcome != outcomeStopped {
		t.Errorf("stop after restart = %+v", stop)
	}

	// A restart while T3 is disconnected: the Thread reads unknown, which
	// ends nothing and closes no request; the answer arrives once T3
	// reports again.
	f.process(t, "dlv-5", createdEvent("sess-outage", now))
	_, outageProvider := f.startedThread("sess-outage")
	f.ask(outageProvider, "req-1")
	f.waitActivities("sess-outage", 3)
	f.threads.ReleaseIntegration(ctx, providerT3)
	f.restart()
	f.waitActivities("sess-outage", 3)
	time.Sleep(40 * time.Millisecond)
	if session := f.session("sess-outage"); session.State != stateBound {
		t.Fatalf("outage session = %+v, want still bound", session)
	}
	if n := len(f.linear.activitiesOf("sess-outage")); n != 3 {
		t.Errorf("the outage closed a request: %d activities", n)
	}
	requests, _ := f.store.Linear().Requests(ctx, "sess-outage")
	if len(requests) != 1 || requests[0].State != requestOpen {
		t.Errorf("requests after the outage = %+v", requests)
	}
	// T3 is back and reports the question still open: nothing new; then
	// resolved elsewhere and the turn done.
	f.ask(outageProvider, "req-1")
	f.service.wake(f.service.sessionKick)
	time.Sleep(40 * time.Millisecond)
	if n := len(f.linear.activitiesOf("sess-outage")); n != 3 {
		t.Errorf("the reconnect re-presented a request: %d activities", n)
	}
	f.resolveInput(outageProvider, "req-1", []threads.ProviderAnswer{{QuestionID: "color", Values: []string{"Blue"}}})
	acts = f.waitActivities("sess-outage", 4)
	contains(t, acts[3].Body, "resolved elsewhere")
	f.report(outageProvider, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "After the outage."})
	acts = f.waitActivities("sess-outage", 5)
	if acts[4].Body != "After the outage." {
		t.Errorf("outage result = %+v", acts[4])
	}
}

// A session whose start finished while the loop held a stale row is not
// started twice: the fresh row decides.
func TestConcurrentReconcileStartsOnce(t *testing.T) {
	f := newFixture(t)
	f.start()
	now := f.clock.Now()
	for i := range 5 {
		f.process(t, "dlv-"+itoa(int64(i)), createdEvent("sess-"+itoa(int64(i)), now))
		f.service.wake(f.service.sessionKick)
	}
	for i := range 5 {
		f.startedThread("sess-" + itoa(int64(i)))
	}
	for range 20 {
		f.service.wake(f.service.sessionKick)
		time.Sleep(2 * time.Millisecond)
	}
	time.Sleep(40 * time.Millisecond)
	if f.starter.count() != 5 {
		t.Errorf("starts = %d, want 5", f.starter.count())
	}
	for i := range 5 {
		if acts := f.linear.activitiesOf("sess-" + itoa(int64(i))); len(acts) != 2 || strings.Contains(acts[1].Body, "restarted") {
			t.Errorf("sess-%d activities = %+v", i, acts)
		}
	}
}
