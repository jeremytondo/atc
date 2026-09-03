package threads

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"
	"time"

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

// The submission seam: acceptance mints a running turn and returns its
// id; a second submission while it is unbound is refused; the provider
// re-reporting the turn that preceded the submission changes nothing;
// the first turn the provider starts binds to the submitted id and its
// timestamps take over; after binding a submission is accepted again.
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
	if !turnIDPattern.MatchString(submitted) || thread.Status != api.ThreadWorking || thread.LatestTurn == nil ||
		thread.LatestTurn.ID != submitted || thread.LatestTurn.State != api.TurnRunning || thread.LatestTurn.ID == before.ID {
		t.Fatalf("after submit: id %q, thread status %s turn %+v", submitted, thread.Status, thread.LatestTurn)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events on submit = %v", got)
	}
	if _, err := f.service.SubmitTurn(ctx, id); !errors.Is(err, ErrTurnPending) {
		t.Errorf("second submission while unbound = %v; want ErrTurnPending", err)
	}
	if got := f.turn(t, id); got.ID != submitted || got.State != api.TurnRunning {
		t.Errorf("refused submission touched the turn: %+v", got)
	}

	// T3 re-reports the shape it had — the old turn, the session idle —
	// which describes the thread before the submission: not the submitted
	// turn starting, and not the submitted turn ending.
	observe(&TurnObservation{ProviderID: "pt-1", State: api.TurnCompleted, StartedAt: prior, CompletedAt: prior.Add(time.Minute)}, api.ThreadIdle)
	if got := f.turn(t, id); got.ID != submitted || got.State != api.TurnRunning {
		t.Errorf("re-report of the prior turn touched the submitted turn: %+v", got)
	}
	if _, err := f.service.SubmitTurn(ctx, id); !errors.Is(err, ErrTurnPending) {
		t.Errorf("still unbound = %v; want ErrTurnPending", err)
	}

	// The provider starts a turn: bound, provider timestamps in.
	started := prior.Add(2 * time.Minute)
	observe(&TurnObservation{ProviderID: "pt-2", State: api.TurnRunning, StartedAt: started}, api.ThreadWorking)
	bound := f.turn(t, id)
	if bound.ID != submitted || bound.State != api.TurnRunning || !bound.StartedAt.Equal(started) || bound.CompletedAt != nil {
		t.Errorf("bound turn = %+v; want %s running from %v", bound, submitted, started)
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
	if got := f.turn(t, id); got.ID == next || got.State != api.TurnRunning {
		t.Errorf("turn without provider id while pending = %+v; want a fresh id", got)
	}
	if _, err := f.service.SubmitTurn(ctx, id); err != nil {
		t.Errorf("submission after the pending turn was replaced = %v", err)
	}
	// A fault ends a pending turn like any running one.
	observe(nil, api.ThreadError)
	if got := f.turn(t, id); got.State != api.TurnFailed {
		t.Errorf("pending turn on a fault = %+v; want failed", got)
	}
	observe(&TurnObservation{ProviderID: "pt-3", State: api.TurnRunning}, api.ThreadWorking)
	if got := f.turn(t, id); got.ID == next || got.State != api.TurnRunning {
		t.Errorf("provider-started turn = %+v; want a fresh id", got)
	}

	// A submitted turn left unbound coerces with the hold and is not
	// pending afterwards.
	pending, err := f.service.SubmitTurn(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	f.service.ReleaseIntegration(ctx, "t3code")
	if got := f.turn(t, id); got.ID != pending || got.State != api.TurnUnknown {
		t.Errorf("unbound turn after release = %+v; want %s unknown", got, pending)
	}
	if _, err := f.service.SubmitTurn(ctx, id); err != nil {
		t.Errorf("submission after the pending turn coerced = %v", err)
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
