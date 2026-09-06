package threads

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
)

var turnIDPattern = regexp.MustCompile(`^turn-[23456789bcdfghjkmnpqrstvwxyz]{10}$`)

// The one ranking: error beats live evidence, a question beats an
// approval, and idle needs every source at rest; no evidence is unknown.
func TestRank(t *testing.T) {
	cases := []struct {
		name     string
		evidence []api.ThreadStatus
		want     api.ThreadStatus
	}{
		{"no evidence", nil, api.ThreadUnknown},
		{"one idle", []api.ThreadStatus{api.ThreadIdle}, api.ThreadIdle},
		{"all idle", []api.ThreadStatus{api.ThreadIdle, api.ThreadIdle}, api.ThreadIdle},
		{"idle needs every source at rest", []api.ThreadStatus{api.ThreadIdle, api.ThreadUnknown}, api.ThreadUnknown},
		{"working over unknown", []api.ThreadStatus{api.ThreadUnknown, api.ThreadWorking}, api.ThreadWorking},
		{"approval over working", []api.ThreadStatus{api.ThreadWorking, api.ThreadWaitingForPermission}, api.ThreadWaitingForPermission},
		{"question over approval", []api.ThreadStatus{api.ThreadWaitingForPermission, api.ThreadWaitingForInput, api.ThreadWorking}, api.ThreadWaitingForInput},
		{"error over everything", []api.ThreadStatus{api.ThreadWaitingForInput, api.ThreadError, api.ThreadWorking}, api.ThreadError},
		{"unrecognized is unknown", []api.ThreadStatus{api.ThreadIdle, api.ThreadStatus("starting")}, api.ThreadUnknown},
	}
	for _, c := range cases {
		if got := Rank(c.evidence...); got != c.want {
			t.Errorf("%s: Rank(%v) = %s, want %s", c.name, c.evidence, got, c.want)
		}
	}
}

// turn reads a thread's latest turn, failing when there is none.
func (f *fixture) turn(t *testing.T, id string) api.ThreadTurn {
	t.Helper()
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.LatestTurn == nil {
		t.Fatalf("thread %s has no latest turn", id)
	}
	return *thread.LatestTurn
}

// status applies one status observation to the Claude-style conversation
// sess-1.
func (f *fixture) status(t *testing.T, status api.ThreadStatus, detail string, turn *TurnObservation) {
	t.Helper()
	if err := f.service.ObserveStatus(context.Background(), StatusObservation{
		IntegrationID: "claude", ProviderID: "sess-1", Status: status, StatusDetail: detail, Turn: turn,
	}); err != nil {
		t.Fatal(err)
	}
}

// The turn lifecycle for an Integration without provider turn ids
// (Claude): a prompt mints a running turn, a stop ends it, a new prompt
// replaces it, idle without an end leaves it unknown, a faulted session
// fails it — and every turn change publishes thread.updated.
func TestTurnLifecycle(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()
	id, err := f.service.ObserveSession(ctx, f.observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if thread, _ := f.service.Get(id); thread.LatestTurn != nil {
		t.Fatalf("latest turn before any turn = %+v; want absent", thread.LatestTurn)
	}
	f.drain()

	// A prompt: running, minted, started as best ATC knows (now).
	f.status(t, api.ThreadWorking, "", &TurnObservation{State: api.TurnRunning})
	first := f.turn(t, id)
	if !turnIDPattern.MatchString(first.ID) || first.State != api.TurnRunning || first.StartedAt.IsZero() || first.CompletedAt != nil || first.Error != "" {
		t.Fatalf("minted turn = %+v", first)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on mint = %v", got)
	}

	// The same claim again changes nothing: no event.
	f.status(t, api.ThreadWorking, "", nil)
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on an unchanged turn = %v", got)
	}

	// Stop: the running turn completes; a turn-only change publishes.
	f.status(t, api.ThreadWorking, "", &TurnObservation{State: api.TurnCompleted})
	completed := f.turn(t, id)
	if completed.ID != first.ID || completed.State != api.TurnCompleted || completed.CompletedAt == nil || !completed.StartedAt.Equal(first.StartedAt) {
		t.Errorf("completed turn = %+v; want %s completed with an end time", completed, first.ID)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on completion = %v", got)
	}
	f.status(t, api.ThreadIdle, "", nil)
	if got := f.turn(t, id); got.State != api.TurnCompleted {
		t.Errorf("idle after completion changed the turn to %s", got.State)
	}

	// A new prompt is a new turn; a second prompt replaces a running one.
	f.status(t, api.ThreadWorking, "", &TurnObservation{State: api.TurnRunning})
	second := f.turn(t, id)
	f.status(t, api.ThreadWorking, "", &TurnObservation{State: api.TurnRunning})
	third := f.turn(t, id)
	if second.ID == first.ID || third.ID == second.ID || third.State != api.TurnRunning {
		t.Errorf("turns after two prompts: %s, %s, %s", first.ID, second.ID, third.ID)
	}

	// Idle with no end signal (an interrupt Claude never reports): unknown.
	f.status(t, api.ThreadIdle, "", nil)
	if got := f.turn(t, id); got.ID != third.ID || got.State != api.TurnUnknown || got.CompletedAt != nil {
		t.Errorf("turn after idle without an end = %+v; want %s unknown", got, third.ID)
	}
	// A stop with no running turn is still a turn ATC observed ending.
	f.status(t, api.ThreadIdle, "", &TurnObservation{State: api.TurnFailed, Error: "limit"})
	if got := f.turn(t, id); got.ID == third.ID || got.State != api.TurnFailed || got.Error != "limit" {
		t.Errorf("stop without a running turn = %+v; want a fresh failed turn", got)
	}

	// The session faults mid-turn: status error with the provider's text,
	// the turn failed with the same text. Recovery clears the detail and
	// leaves the turn as it ended.
	f.status(t, api.ThreadWorking, "", &TurnObservation{State: api.TurnRunning})
	running := f.turn(t, id)
	f.status(t, api.ThreadError, "session broke", nil)
	thread, _ := f.service.Get(id)
	if thread.Status != api.ThreadError || thread.StatusDetail != "session broke" ||
		thread.LatestTurn.ID != running.ID || thread.LatestTurn.State != api.TurnFailed || thread.LatestTurn.Error != "session broke" || thread.LatestTurn.CompletedAt == nil {
		t.Errorf("faulted session = status %s detail %q turn %+v", thread.Status, thread.StatusDetail, thread.LatestTurn)
	}
	f.status(t, api.ThreadIdle, "", nil)
	thread, _ = f.service.Get(id)
	if thread.Status != api.ThreadIdle || thread.StatusDetail != "" || thread.LatestTurn.State != api.TurnFailed {
		t.Errorf("recovered session = status %s detail %q turn %+v", thread.Status, thread.StatusDetail, thread.LatestTurn)
	}
	// A turn failure never writes statusDetail; a detail without error
	// status is dropped.
	f.status(t, api.ThreadIdle, "stray detail", &TurnObservation{State: api.TurnFailed, Error: "boom"})
	if thread, _ = f.service.Get(id); thread.StatusDetail != "" {
		t.Errorf("statusDetail = %q without error status", thread.StatusDetail)
	}
}

// A running turn is a live claim: ignored for a thread nothing holds,
// coerced to unknown when the holder leaves or ATC restarts, while a
// finished turn persists as recorded.
func TestTurnCoercion(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()
	id, err := f.service.ObserveSession(ctx, f.observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.status(t, api.ThreadWorking, "", &TurnObservation{State: api.TurnRunning})
	running := f.turn(t, id)

	f.service.Deactivate(ctx, "term-aaaaa")
	thread, _ := f.service.Get(id)
	if thread.Status != api.ThreadUnknown || thread.LatestTurn.ID != running.ID || thread.LatestTurn.State != api.TurnUnknown {
		t.Errorf("after deactivate = status %s turn %+v; want both unknown", thread.Status, thread.LatestTurn)
	}
	// Inactive: a running turn is ignored, an ended one is recorded.
	f.status(t, "", "", &TurnObservation{State: api.TurnRunning})
	if got := f.turn(t, id); got.State != api.TurnUnknown || got.ID != running.ID {
		t.Errorf("running turn for an inactive thread landed: %+v", got)
	}
	f.status(t, api.ThreadIdle, "", &TurnObservation{State: api.TurnCompleted})
	if got := f.turn(t, id); got.State != api.TurnCompleted {
		t.Errorf("ended turn for an inactive thread = %+v", got)
	}

	// Restart with a running turn persisted: the reload coerces and
	// persists it; a finished one is untouched.
	if _, err := f.service.ObserveSession(ctx, f.observation("term-aaaaa", "sess-1")); err != nil {
		t.Fatal(err)
	}
	f.status(t, api.ThreadWorking, "", &TurnObservation{State: api.TurnRunning})
	other, err := f.service.ObserveSession(ctx, SessionObservation{
		IntegrationID: "claude", AppID: "claude/tui", AgentID: "claude", ProviderID: "sess-2", TerminalID: "term-aaaaa", InitialDirectory: f.dir("proj-aaaaa"),
		Status: api.ThreadIdle, Turn: &TurnObservation{State: api.TurnCompleted},
	})
	if err != nil {
		t.Fatal(err)
	}
	reloaded := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Projects: f.store.Projects(), Hub: events.NewHubAt(8, 1), Now: f.clock.Now})
	if err := reloaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get(id); got.LatestTurn == nil || got.LatestTurn.State != api.TurnUnknown {
		t.Errorf("running turn after reload = %+v; want unknown", got.LatestTurn)
	}
	if got, _ := reloaded.Get(other); got.LatestTurn == nil || got.LatestTurn.State != api.TurnCompleted {
		t.Errorf("finished turn after reload = %+v; want completed", got.LatestTurn)
	}
	records, err := f.store.Threads().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.ID == id && record.Turn.State != string(api.TurnUnknown) {
			t.Errorf("database still claims a %s turn after reload", record.Turn.State)
		}
	}
}

// A submission (ATC-289, ATC-302, ATC-307) is a pending turn beside the
// latest one: minted with the thread provisionally working, bound to the
// first provider turn reported that is not the turn the thread held at
// submission — which, re-reported, updates the latest turn as it always
// did and touches the submission not at all — and refused while one is
// pending. A fault fails it; a loss of observation leaves it pending,
// and a new server process over the same store still binds it.
func TestSubmitTurnBinding(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	observe := func(turn *TurnObservation, status api.ThreadStatus) string {
		t.Helper()
		id, err := f.service.ObserveExternal(ctx, ExternalObservation{
			IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: status, Title: "T", Turn: turn,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	if _, err := f.service.SubmitTurn(ctx, "thrd-nope"); !errors.Is(err, ErrNotFound) {
		t.Errorf("submit to an unknown thread = %v", err)
	}
	prior := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	id := observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnCompleted, StartedAt: prior, CompletedAt: prior.Add(time.Minute)}, api.ThreadIdle)
	before := f.turn(t, id)
	f.drain()

	submitted, err := f.service.SubmitTurn(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(id)
	if !turnIDPattern.MatchString(submitted) || thread.Status != api.ThreadWorking || thread.PendingTurn == nil || thread.PendingTurn.ID != submitted ||
		thread.PendingTurn.SubmittedAt.IsZero() || thread.LatestTurn == nil || thread.LatestTurn.ID != before.ID {
		t.Fatalf("after submit: id %q, thread status %s pending %+v latest %+v", submitted, thread.Status, thread.PendingTurn, thread.LatestTurn)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on submit = %v", got)
	}
	if _, err := f.service.SubmitTurn(ctx, id); !errors.Is(err, ErrTurnPending) {
		t.Errorf("second submission while pending = %v; want ErrTurnPending", err)
	}

	// T3 re-reports the shape it had — the old turn, the session idle —
	// which describes the thread before the submission: the latest turn
	// stands, the status follows the provider, the submission pends.
	observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnCompleted, StartedAt: prior, CompletedAt: prior.Add(time.Minute)}, api.ThreadIdle)
	thread, _ = f.service.Get(id)
	if thread.LatestTurn.ID != before.ID || thread.LatestTurn.State != api.TurnCompleted || thread.PendingTurn == nil || thread.PendingTurn.ID != submitted || thread.Status != api.ThreadIdle {
		t.Errorf("re-report of the prior turn: latest %+v pending %+v status %s", thread.LatestTurn, thread.PendingTurn, thread.Status)
	}
	if _, err := f.service.SubmitTurn(ctx, id); !errors.Is(err, ErrTurnPending) {
		t.Errorf("still pending = %v; want ErrTurnPending", err)
	}

	// The provider starts a turn: bound, provider timestamps in.
	started := prior.Add(2 * time.Minute)
	observe(&TurnObservation{ProviderID: "pt-2", State: api.TurnRunning, StartedAt: started}, api.ThreadWorking)
	thread, _ = f.service.Get(id)
	bound := f.turn(t, id)
	if bound.ID != submitted || bound.State != api.TurnRunning || !bound.StartedAt.Equal(started) || bound.CompletedAt != nil || thread.PendingTurn != nil {
		t.Errorf("bound turn = %+v pending %+v; want %s running from %v, nothing pending", bound, thread.PendingTurn, submitted, started)
	}
	observe(&TurnObservation{ProviderID: "pt-2", State: api.TurnCompleted, StartedAt: started, CompletedAt: started.Add(time.Minute)}, api.ThreadIdle)
	if got := f.turn(t, id); got.ID != submitted || got.State != api.TurnCompleted || got.CompletedAt == nil || !got.CompletedAt.Equal(started.Add(time.Minute)) {
		t.Errorf("bound turn's outcome = %+v", got)
	}
	next, err := f.service.SubmitTurn(ctx, id)
	if err != nil || next == submitted {
		t.Fatalf("submission after binding = %q, %v", next, err)
	}

	// Binding needs a provider turn id: a turn reported without one is
	// the provider's own, a fresh id, and the submission stays pending.
	observe(&TurnObservation{State: api.TurnRunning}, api.ThreadWorking)
	thread, _ = f.service.Get(id)
	if thread.LatestTurn.ID == next || thread.LatestTurn.State != api.TurnRunning || thread.PendingTurn == nil || thread.PendingTurn.ID != next {
		t.Errorf("turn without provider id while pending: latest %+v pending %+v", thread.LatestTurn, thread.PendingTurn)
	}
	// A fault fails the pending submission — the provider cannot start
	// it — as the latest turn, with the fault text.
	observe(nil, api.ThreadError)
	thread, _ = f.service.Get(id)
	if thread.PendingTurn != nil || thread.LatestTurn.ID != next || thread.LatestTurn.State != api.TurnFailed {
		t.Errorf("pending turn on a fault: latest %+v pending %+v; want %s failed", thread.LatestTurn, thread.PendingTurn, next)
	}
	observe(&TurnObservation{ProviderID: "pt-3", State: api.TurnRunning}, api.ThreadWorking)
	if got := f.turn(t, id); got.ID == next || got.State != api.TurnRunning {
		t.Errorf("provider-started turn = %+v; want a fresh id", got)
	}

	// A submission pending through a loss of observation stays pending
	// beside the coerced latest turn: the provider reconnecting — or a
	// new server process over the same store — still binds its first new
	// turn to the submitted id (ATC-302), and the prior turn re-reported
	// still binds nothing.
	pending, err := f.service.SubmitTurn(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.service.ReleaseIntegration(ctx, "t3code")
	thread, _ = f.service.Get(id)
	if thread.PendingTurn == nil || thread.PendingTurn.ID != pending || thread.LatestTurn.State != api.TurnUnknown || thread.Status != api.ThreadUnknown {
		t.Errorf("after release: pending %+v latest %+v status %s", thread.PendingTurn, thread.LatestTurn, thread.Status)
	}
	if _, err := f.service.SubmitTurn(ctx, id); !errors.Is(err, ErrTurnPending) {
		t.Errorf("submission after the release = %v; want ErrTurnPending", err)
	}
	restarted := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Projects: f.store.Projects(), Hub: f.hub, Now: f.clock.Now})
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := restarted.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadIdle, Title: "T",
		Turn: &TurnObservation{ProviderID: "pt-3", State: api.TurnCompleted},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := restarted.Get(id); got.PendingTurn == nil || got.PendingTurn.ID != pending || got.LatestTurn == nil || got.LatestTurn.State != api.TurnCompleted {
		t.Errorf("prior turn re-reported after restart = latest %+v pending %+v; want %s still pending, the prior completed", got.LatestTurn, got.PendingTurn, pending)
	}
	if _, err := restarted.ObserveExternal(ctx, ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: api.ThreadIdle, Title: "T",
		Turn: &TurnObservation{ProviderID: "pt-4", State: api.TurnCompleted, Response: "bound after restart"},
	}); err != nil {
		t.Fatal(err)
	}
	if got, _ := restarted.Get(id); got.PendingTurn != nil || got.LatestTurn == nil || got.LatestTurn.ID != pending || got.LatestTurn.State != api.TurnCompleted || got.LatestTurn.Response != "bound after restart" {
		t.Errorf("first new turn after restart = %+v; want it bound to %s", got.LatestTurn, pending)
	}
	if _, err := restarted.SubmitTurn(ctx, id); err != nil {
		t.Errorf("submission after binding = %v", err)
	}
}

// A submission while the provider runs a turn (ATC-307): the running
// turn keeps its place and its end is recorded — a follow-up the
// provider queues binds to the next turn it starts — and a fault fails
// the running turn and the pending submission alike.
func TestSubmitTurnBesideRunningTurn(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	observe := func(turn *TurnObservation, status api.ThreadStatus) string {
		t.Helper()
		id, err := f.service.ObserveExternal(ctx, ExternalObservation{
			IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: status, Title: "T", Turn: turn,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	id := observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnRunning}, api.ThreadWorking)
	running := f.turn(t, id)
	queued, err := f.service.SubmitTurn(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	// The running turn ends with its reply while the submission pends.
	observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnCompleted, Response: "first reply"}, api.ThreadIdle)
	thread, _ := f.service.Get(id)
	if thread.LatestTurn.ID != running.ID || thread.LatestTurn.State != api.TurnCompleted || thread.LatestTurn.Response != "first reply" || thread.PendingTurn == nil || thread.PendingTurn.ID != queued {
		t.Errorf("running turn's end under a pending submission: latest %+v pending %+v", thread.LatestTurn, thread.PendingTurn)
	}
	observe(&TurnObservation{ProviderID: "pt-2", State: api.TurnRunning}, api.ThreadWorking)
	if got := f.turn(t, id); got.ID != queued || got.State != api.TurnRunning {
		t.Errorf("next turn = %+v; want it bound to %s", got, queued)
	}

	second, err := f.service.SubmitTurn(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	observe(nil, api.ThreadError)
	thread, _ = f.service.Get(id)
	if thread.PendingTurn != nil || thread.LatestTurn.ID != second || thread.LatestTurn.State != api.TurnFailed {
		t.Errorf("fault under a running turn and a pending submission: latest %+v pending %+v", thread.LatestTurn, thread.PendingTurn)
	}
}

// Re-matching by provider turn id across a loss of observation: the same
// turn still running keeps its id and state, the same turn now finished
// keeps its id and takes the outcome, a different turn is a fresh id.
// Idle before the end signal (Codex's ordering) leaves the turn unknown
// until the end arrives for the same provider turn.
func TestTurnRematchAfterReconnect(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	observe := func(turn *TurnObservation, status api.ThreadStatus) string {
		t.Helper()
		id, err := f.service.ObserveExternal(ctx, ExternalObservation{
			IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: status, Title: "T", Turn: turn,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	started := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	id := observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnRunning, StartedAt: started}, api.ThreadWorking)
	first := f.turn(t, id)

	f.service.ReleaseIntegration(ctx, "t3code")
	if got := f.turn(t, id); got.ID != first.ID || got.State != api.TurnUnknown {
		t.Fatalf("after release = %+v; want %s unknown", got, first.ID)
	}
	observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnRunning, StartedAt: started}, api.ThreadWorking)
	if got := f.turn(t, id); got.ID != first.ID || got.State != api.TurnRunning {
		t.Errorf("same turn still running = %+v; want %s running", got, first.ID)
	}

	f.service.ReleaseIntegration(ctx, "t3code")
	observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnInterrupted, StartedAt: started, CompletedAt: started.Add(time.Minute)}, api.ThreadIdle)
	if got := f.turn(t, id); got.ID != first.ID || got.State != api.TurnInterrupted || got.CompletedAt == nil {
		t.Errorf("same turn now finished = %+v; want %s interrupted", got, first.ID)
	}

	f.service.ReleaseIntegration(ctx, "t3code")
	if got := f.turn(t, id); got.State != api.TurnInterrupted {
		t.Errorf("finished turn coerced on release: %+v", got)
	}
	observe(&TurnObservation{ProviderID: "pt-2", State: api.TurnCompleted, StartedAt: started, CompletedAt: started.Add(time.Hour)}, api.ThreadIdle)
	second := f.turn(t, id)
	if second.ID == first.ID || second.State != api.TurnCompleted {
		t.Errorf("different turn = %+v; want a fresh id, completed", second)
	}

	// Idle arrives before the end: unknown, then the end for the same
	// provider turn records the outcome on the same id.
	observe(&TurnObservation{ProviderID: "pt-3", State: api.TurnRunning}, api.ThreadWorking)
	third := f.turn(t, id)
	observe(nil, api.ThreadIdle)
	if got := f.turn(t, id); got.ID != third.ID || got.State != api.TurnUnknown {
		t.Errorf("idle before the end = %+v; want %s unknown", got, third.ID)
	}
	observe(&TurnObservation{ProviderID: "pt-3", State: api.TurnCompleted}, api.ThreadIdle)
	if got := f.turn(t, id); got.ID != third.ID || got.State != api.TurnCompleted {
		t.Errorf("late end = %+v; want %s completed", got, third.ID)
	}
	// A provider that says the turn runs while the session is at rest is
	// believed about the turn.
	observe(&TurnObservation{ProviderID: "pt-4", State: api.TurnRunning}, api.ThreadIdle)
	if got := f.turn(t, id); got.State != api.TurnRunning {
		t.Errorf("reported running with idle status = %+v", got)
	}
}

// The latest turn's response (ATC-303): recorded only with an ended
// state, never with a running one; an ended turn gains or changes it
// after the fact through the same provider turn — publishing only on a
// change — while a late result for a turn no longer latest, a result for
// a running turn, an empty text, and an unmapped identity are dropped; a
// newer turn starts without one; presence never changes the rest of the
// turn; and it survives a reload.
func TestTurnResponse(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()
	observe := func(turn *TurnObservation, status api.ThreadStatus) string {
		t.Helper()
		id, err := f.service.ObserveExternal(ctx, ExternalObservation{
			IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.dir("proj-aaaaa"), Status: status, Title: "T", Turn: turn,
		})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	recover := func(threadID, turnID, response string) {
		t.Helper()
		if err := f.service.ObserveTurnResponse(ctx, threadID, turnID, response); err != nil {
			t.Fatal(err)
		}
	}
	started := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	ended := started.Add(time.Minute)

	// A running turn never carries one, whatever the observation says.
	id := observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnRunning, StartedAt: started, Response: "early"}, api.ThreadWorking)
	if got := f.turn(t, id); got.Response != "" {
		t.Errorf("running turn = %+v; want no response", got)
	}
	f.drain()

	// The end carrying the response records it, one event; the same
	// report again, or one without a response, changes nothing.
	completed := &TurnObservation{ProviderID: "pt-1", State: api.TurnCompleted, StartedAt: started, CompletedAt: ended, Response: "All done."}
	observe(completed, api.ThreadIdle)
	first := f.turn(t, id)
	if first.State != api.TurnCompleted || first.Response != "All done." {
		t.Fatalf("completed turn = %+v; want the response", first)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on completion = %v", got)
	}
	observe(completed, api.ThreadIdle)
	bare := *completed
	bare.Response = ""
	observe(&bare, api.ThreadIdle)
	if got := f.turn(t, id); got.Response != "All done." {
		t.Errorf("re-reports changed the response: %+v", got)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on unchanged re-reports = %v", got)
	}

	// After the fact: the same text is silent, a new text replaces and
	// publishes, and nothing else about the turn moves.
	recover(id, "pt-1", "All done.")
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on an identical recovery = %v", got)
	}
	recover(id, "pt-1", "All done, and the tests pass.")
	replaced := f.turn(t, id)
	if replaced.Response != "All done, and the tests pass." {
		t.Errorf("replaced response = %+v", replaced)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on a changed recovery = %v", got)
	}
	replaced.Response = first.Response
	if diff := cmp.Diff(first, replaced); diff != "" {
		t.Errorf("recovery changed more than the response (-before +after):\n%s", diff)
	}
	// Dropped: a stale turn, an empty text, an unknown thread.
	recover(id, "pt-0", "stale")
	recover(id, "pt-1", "")
	recover("thrd-nope", "pt-1", "unknown")
	if got := f.turn(t, id); got.Response != "All done, and the tests pass." {
		t.Errorf("dropped recoveries changed the response: %+v", got)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("events on dropped recoveries = %v", got)
	}

	// A newer turn starts without one; a late result for the earlier
	// turn is dropped, as is one for the turn while it runs; its own end
	// — interrupted here — takes one after the fact.
	observe(&TurnObservation{ProviderID: "pt-2", State: api.TurnRunning, StartedAt: ended}, api.ThreadWorking)
	second := f.turn(t, id)
	if second.ID == first.ID || second.Response != "" {
		t.Fatalf("newer turn = %+v; want a fresh id with no response", second)
	}
	recover(id, "pt-1", "late for the first turn")
	recover(id, "pt-2", "too early for the second")
	if got := f.turn(t, id); got.ID != second.ID || got.State != api.TurnRunning || got.Response != "" {
		t.Errorf("late and early results landed: %+v", got)
	}
	observe(&TurnObservation{ProviderID: "pt-2", State: api.TurnInterrupted, StartedAt: ended, CompletedAt: ended.Add(time.Minute)}, api.ThreadIdle)
	f.drain()
	recover(id, "pt-2", "Stopped before the summary.")
	if got := f.turn(t, id); got.ID != second.ID || got.State != api.TurnInterrupted || got.Response != "Stopped before the summary." {
		t.Errorf("interrupted turn after recovery = %+v", got)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on recovery = %v", got)
	}
	// An empty message at the end is absent.
	observe(&TurnObservation{ProviderID: "pt-3", State: api.TurnRunning}, api.ThreadWorking)
	observe(&TurnObservation{ProviderID: "pt-3", State: api.TurnCompleted, Response: ""}, api.ThreadIdle)
	if got := f.turn(t, id); got.State != api.TurnCompleted || got.Response != "" {
		t.Errorf("empty response = %+v; want absent", got)
	}

	// Without provider ids (Claude): the stop carries it onto the running
	// turn.
	claude, err := f.service.ObserveSession(ctx, f.observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.status(t, api.ThreadWorking, "", &TurnObservation{State: api.TurnRunning})
	running := f.turn(t, claude)
	f.status(t, api.ThreadIdle, "", &TurnObservation{State: api.TurnCompleted, Response: "Claude says so."})
	if got := f.turn(t, claude); got.ID != running.ID || got.State != api.TurnCompleted || got.Response != "Claude says so." {
		t.Errorf("Claude stop = %+v; want %s completed with the message", got, running.ID)
	}

	// Persisted with the turn: a reload serves it.
	reloaded := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Projects: f.store.Projects(), Hub: events.NewHubAt(8, 1), Now: f.clock.Now})
	if err := reloaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if got, _ := reloaded.Get(claude); got.LatestTurn == nil || got.LatestTurn.Response != "Claude says so." {
		t.Errorf("response after reload = %+v", got.LatestTurn)
	}
}
