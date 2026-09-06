package linear

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
)

// Restarts at every boundary against the same store: accepted work is
// kept, a start never dispatched is owed again, a start that may have
// been dispatched is never repeated, a watched Turn resumes, and owed
// Linear calls are sent.
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
	f.starter.set(func(s *fakeStarter) { s.block = release })
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
	if _, err := repo.InsertSession(ctx, store.LinearSession{ID: "sess-recorded", State: stateStarting, ThreadID: recordedID, TurnID: recordedTurn, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	f.starter.set(func(s *fakeStarter) { s.block = nil })
	f.linear.set(func(l *fakeLinear) { l.failNext = 0 })
	startsBefore := f.starter.count()
	f.start()
	// The never-dispatched start is owed again — one more start, no more.
	f.startedThread("sess-accepted")
	// The recorded one is not started again; Linear hears it is uncertain,
	// and the recorded Thread is watched.
	acts := f.waitActivities("sess-recorded", 1)
	contains(t, acts[0].Body, "restarted while starting")
	if session := f.session("sess-recorded"); session.State != stateStarted || session.ThreadID != recordedID {
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

	// A restart while T3 is disconnected: the Thread reads unknown, which
	// ends nothing; the answer arrives once T3 reports again.
	f.process(t, "dlv-3", createdEvent("sess-outage", now))
	_, outageProvider := f.startedThread("sess-outage")
	f.report(outageProvider, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	f.threads.ReleaseIntegration(ctx, providerT3)
	f.restart()
	f.waitActivities("sess-outage", 2)
	time.Sleep(40 * time.Millisecond)
	if session := f.session("sess-outage"); session.State != stateStarted {
		t.Fatalf("outage session = %+v, want still started", session)
	}
	f.report(outageProvider, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "After the outage."})
	acts = f.waitActivities("sess-outage", 3)
	if acts[2].Body != "After the outage." {
		t.Errorf("outage result = %+v", acts[2])
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
