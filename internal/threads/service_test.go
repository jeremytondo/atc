package threads

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/store"
)

// fakeTerminals is the hand-written read seam into the terminals domain:
// the sweep asks it whether a terminal is still running.
type fakeTerminals struct {
	mu       sync.Mutex
	statuses map[string]api.TerminalStatus
}

func (f *fakeTerminals) Get(id string) (api.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	status, ok := f.statuses[id]
	if !ok {
		return api.Terminal{}, errors.New("terminal not found")
	}
	return api.Terminal{ID: id, Status: status}, nil
}

func (f *fakeTerminals) set(id string, status api.TerminalStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses[id] = status
}

func (f *fakeTerminals) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.statuses, id)
}

type fixture struct {
	service   *Service
	store     *store.Store
	terminals *fakeTerminals
	hub       *events.Hub
	sub       *events.Subscription
	clock     *fakeClock
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond) // strictly monotonic
	return c.now
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	terminals := &fakeTerminals{statuses: map[string]api.TerminalStatus{}}
	hub := events.NewHubAt(64, 1)
	clock := &fakeClock{now: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
	service := NewService(Options{
		Repository: s.Threads(),
		Terminals:  terminals,
		Hub:        hub,
		Now:        clock.Now,
	})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	f := &fixture{service: service, store: s, terminals: terminals, hub: hub, clock: clock}
	f.sub = hub.Subscribe(0, false)
	t.Cleanup(f.sub.Close)
	return f
}

// plant registers a project (and optionally a running terminal) so thread
// rows satisfy their foreign keys.
func (f *fixture) plant(t *testing.T, projectID string, terminalIDs ...string) {
	t.Helper()
	ctx := context.Background()
	now := f.clock.Now()
	if ok, err := f.store.Projects().Insert(ctx, store.ProjectRecord{
		ID: projectID, Name: "p", Directory: "/" + projectID, CreatedAt: now, UpdatedAt: now,
	}); err != nil || !ok {
		t.Fatalf("planting project = %v, %v", ok, err)
	}
	for _, id := range terminalIDs {
		if ok, err := f.store.Terminals().Insert(ctx, store.TerminalRecord{
			ID: id, ProjectID: projectID, Name: "tui", Directory: "/" + projectID,
			Agent: "claude", CreatedAt: now, UpdatedAt: now,
		}); err != nil || !ok {
			t.Fatalf("planting terminal = %v, %v", ok, err)
		}
		f.terminals.set(id, api.TerminalRunning)
	}
}

// drain collects every event published so far as "type id" strings.
func (f *fixture) drain() []string {
	var got []string
	for {
		select {
		case change := <-f.sub.C:
			got = append(got, change.Type+" "+change.ID)
		default:
			return got
		}
	}
}

func observation(terminal, providerID string) SessionObservation {
	return SessionObservation{
		Agent:      "claude",
		ProviderID: providerID,
		TerminalID: terminal,
		ProjectID:  "proj-aaaaa",
		Status:     api.ThreadIdle,
	}
}

func TestObserveSessionCreatesOnce(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Status:   api.ThreadWorking,
		Metadata: Metadata{Title: "fix the build", Model: "claude-opus-5", Cwd: "/proj-aaaaa", PermissionMode: "default"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(id, "thrd-") {
		t.Errorf("thread id = %q; want thrd- prefix", id)
	}

	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Agent != "claude" || thread.ProjectID != "proj-aaaaa" || thread.TerminalID != "term-aaaaa" {
		t.Errorf("thread = %+v; wrong identity fields", thread)
	}
	if thread.Status != api.ThreadWorking || thread.Title != "fix the build" || thread.Model != "claude-opus-5" {
		t.Errorf("thread = %+v; wrong observed fields", thread)
	}
	if thread.LastEvidenceAt == nil {
		t.Error("LastEvidenceAt not recorded")
	}
	if got := f.service.ActiveThreadID("term-aaaaa"); got != id {
		t.Errorf("ActiveThreadID = %q; want %q", got, id)
	}
	want := []string{"thread.created " + id, "terminal.updated term-aaaaa"}
	if diff := cmp.Diff(want, f.drain()); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}

	// The same provider conversation reattaches; it never mints a duplicate.
	again, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Errorf("reattach minted %q; want %q", again, id)
	}
	if got := f.service.List("", "", true); len(got) != 1 {
		t.Errorf("List after reattach = %d threads; want 1", len(got))
	}
}

func TestSwitchingConversationsMovesActive(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	first, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Status: api.ThreadWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.drain()

	// /clear: a new conversation appears in the same terminal. The old
	// thread persists inactive; its unverifiable live status coerces.
	second, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-2"))
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("new conversation reused the old thread")
	}
	if got := f.service.ActiveThreadID("term-aaaaa"); got != second {
		t.Errorf("ActiveThreadID = %q; want %q", got, second)
	}
	old, err := f.service.Get(first)
	if err != nil {
		t.Fatal(err)
	}
	if old.Status != api.ThreadUnknown {
		t.Errorf("displaced thread status = %s; want unknown (working is unverifiable once unobserved)", old.Status)
	}
	want := []string{
		"thread.created " + second,
		"thread.updated " + first,
		"terminal.updated term-aaaaa",
	}
	if diff := cmp.Diff(want, f.drain()); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}

	// /resume of the first conversation reactivates the existing thread.
	resumed, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if resumed != first {
		t.Errorf("resume minted %q; want %q", resumed, first)
	}
	if got := f.service.ActiveThreadID("term-aaaaa"); got != first {
		t.Errorf("ActiveThreadID after resume = %q; want %q", got, first)
	}
}

func TestLastObserverWins(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa", "term-bbbbb")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	again, err := f.service.ObserveSession(ctx, observation("term-bbbbb", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if again != id {
		t.Fatalf("second observer minted %q; want %q", again, id)
	}
	if got := f.service.ActiveThreadID("term-bbbbb"); got != id {
		t.Errorf("new observer ActiveThreadID = %q; want %q", got, id)
	}
	if got := f.service.ActiveThreadID("term-aaaaa"); got != "" {
		t.Errorf("old observer still holds %q; want released", got)
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.TerminalID != "term-bbbbb" {
		t.Errorf("TerminalID = %q; want the last observer", thread.TerminalID)
	}
}

func TestObserveStatusTransitionsAndSilence(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.drain()

	if err := f.service.ObserveStatus(ctx, StatusObservation{
		Agent: "claude", ProviderID: "sess-1", Status: api.ThreadWorking,
	}); err != nil {
		t.Fatal(err)
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Status != api.ThreadWorking {
		t.Errorf("status = %s; want working", thread.Status)
	}
	if diff := cmp.Diff([]string{"thread.updated " + id}, f.drain()); diff != "" {
		t.Errorf("transition events (-want +got):\n%s", diff)
	}

	// An evidence-only refresh updates lastEvidenceAt silently: no event,
	// no updatedAt bump — it rides along on the next fetch.
	before := thread
	if err := f.service.ObserveStatus(ctx, StatusObservation{
		Agent: "claude", ProviderID: "sess-1", Status: api.ThreadWorking,
	}); err != nil {
		t.Fatal(err)
	}
	after, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("evidence-only refresh published %v; want nothing", got)
	}
	if !after.LastEvidenceAt.After(*before.LastEvidenceAt) {
		t.Error("evidence-only refresh did not advance LastEvidenceAt")
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("evidence-only refresh bumped UpdatedAt")
	}

	// A failed turn: idle with the detail in lastError.
	detail := "provider rejected the request"
	if err := f.service.ObserveStatus(ctx, StatusObservation{
		Agent: "claude", ProviderID: "sess-1", Status: api.ThreadIdle, LastError: &detail,
	}); err != nil {
		t.Fatal(err)
	}
	thread, err = f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Status != api.ThreadIdle || thread.LastError != detail {
		t.Errorf("failed turn: status=%s lastError=%q; want idle with detail", thread.Status, thread.LastError)
	}

	// Evidence for an unmapped conversation is dropped, never minted.
	if err := f.service.ObserveStatus(ctx, StatusObservation{
		Agent: "claude", ProviderID: "sess-unknown", Status: api.ThreadWorking,
	}); err != nil {
		t.Fatal(err)
	}
	if got := f.service.List("", "", true); len(got) != 1 {
		t.Errorf("unmapped status evidence minted a thread: %d threads", len(got))
	}
}

func TestUserTitleWinsOverObservation(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Metadata: Metadata{Title: "observed default"},
	})
	if err != nil {
		t.Fatal(err)
	}

	title := "my title"
	if _, err := f.service.Update(ctx, id, api.ThreadUpdateParams{Title: &title}); err != nil {
		t.Fatal(err)
	}
	if err := f.service.ObserveStatus(ctx, StatusObservation{
		Agent: "claude", ProviderID: "sess-1", Metadata: Metadata{Title: "later observation"},
	}); err != nil {
		t.Fatal(err)
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Title != "my title" {
		t.Errorf("title = %q; observation overwrote the user's title", thread.Title)
	}
}

func TestArchiveAndDeleteRefusedWhileActive(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}

	archived := true
	if _, err := f.service.Update(ctx, id, api.ThreadUpdateParams{Archived: &archived}); !errors.Is(err, ErrActive) {
		t.Fatalf("archive active = %v; want ErrActive", err)
	} else if !strings.Contains(err.Error(), "term-aaaaa") {
		t.Errorf("refusal %q does not name the holding terminal", err)
	}
	if err := f.service.Delete(ctx, id); !errors.Is(err, ErrActive) {
		t.Fatalf("delete active = %v; want ErrActive", err)
	}

	// Once inactive, both verbs work.
	f.service.Deactivate(ctx, "term-aaaaa")
	f.drain()
	thread, err := f.service.Update(ctx, id, api.ThreadUpdateParams{Archived: &archived})
	if err != nil {
		t.Fatal(err)
	}
	if !thread.Archived || thread.ArchivedAt == nil {
		t.Errorf("archived thread = %+v; want archived with timestamp", thread)
	}
	if diff := cmp.Diff([]string{"thread.updated " + id}, f.drain()); diff != "" {
		t.Errorf("archive events (-want +got):\n%s", diff)
	}

	// Hidden by default, present on opt-in; unarchive clears the timestamp.
	if got := f.service.List("", "", false); len(got) != 0 {
		t.Errorf("default list shows archived thread: %v", got)
	}
	if got := f.service.List("", "", true); len(got) != 1 {
		t.Errorf("opt-in list = %d threads; want 1", len(got))
	}
	unarchived := false
	thread, err = f.service.Update(ctx, id, api.ThreadUpdateParams{Archived: &unarchived})
	if err != nil {
		t.Fatal(err)
	}
	if thread.Archived || thread.ArchivedAt != nil {
		t.Errorf("unarchived thread = %+v; want timestamp cleared", thread)
	}

	if err := f.service.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Get(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v; want ErrNotFound", err)
	}

	// The identity mapping died with the record: the same provider
	// conversation observed again deliberately mints a fresh thread.
	fresh, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if fresh == id {
		t.Error("deleted thread id was resurrected")
	}
}

// Resuming an archived conversation inside the TUI cannot be refused, so
// the session observation restores the invariant instead: active means
// unarchived.
func TestReattachUnarchives(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.service.Deactivate(ctx, "term-aaaaa")
	archived := true
	if _, err := f.service.Update(ctx, id, api.ThreadUpdateParams{Archived: &archived}); err != nil {
		t.Fatal(err)
	}
	f.drain()

	// The same conversation observed open again: unarchived, back in the
	// default list, and announced.
	if _, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1")); err != nil {
		t.Fatal(err)
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Archived || thread.ArchivedAt != nil {
		t.Errorf("reattached thread = %+v; want unarchived with timestamp cleared", thread)
	}
	if got := f.service.List("", "", false); len(got) != 1 {
		t.Errorf("default list after reattach = %d threads; want 1", len(got))
	}
	if diff := cmp.Diff([]string{
		"thread.updated " + id,
		"terminal.updated term-aaaaa",
	}, f.drain()); diff != "" {
		t.Errorf("reattach events (-want +got):\n%s", diff)
	}
}

func TestUpdateUnknownAndNoopPatch(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	if _, err := f.service.Update(ctx, "thrd-zzzzz", api.ThreadUpdateParams{}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(unknown) = %v; want ErrNotFound", err)
	}
	if err := f.service.Delete(ctx, "thrd-zzzzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(unknown) = %v; want ErrNotFound", err)
	}

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	before, _ := f.service.Get(id)
	after, err := f.service.Update(ctx, id, api.ThreadUpdateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("empty patch published %v; want nothing", got)
	}
	if !after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Error("empty patch bumped UpdatedAt")
	}
}

func TestSweepDeactivatesWhenTerminalLeaves(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Status: api.ThreadWaitingForInput,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.drain()

	// Running terminal: the sweep leaves everything alone.
	f.service.Sweep(ctx)
	if got := f.service.ActiveThreadID("term-aaaaa"); got != id {
		t.Fatalf("sweep deactivated a running terminal's thread")
	}

	// The TUI exits without provider evidence (kill -9): the sweep is the
	// backstop. The thread persists, inactive, its live status coerced.
	f.terminals.set("term-aaaaa", api.TerminalExited)
	f.service.Sweep(ctx)
	if got := f.service.ActiveThreadID("term-aaaaa"); got != "" {
		t.Errorf("ActiveThreadID after exit = %q; want none", got)
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Status != api.ThreadUnknown {
		t.Errorf("status after exit = %s; want unknown", thread.Status)
	}
	if thread.TerminalID != "term-aaaaa" {
		t.Errorf("TerminalID = %q; an exited (not deleted) terminal keeps the linkage", thread.TerminalID)
	}
	want := []string{"thread.updated " + id, "terminal.updated term-aaaaa"}
	if diff := cmp.Diff(want, f.drain()); diff != "" {
		t.Errorf("sweep events (-want +got):\n%s", diff)
	}
}

// A merely unreachable terminal (inventory hiccup) is no evidence the TUI
// left; only exited, missing, or deleted deactivate.
func TestSweepLeavesUnreachableAlone(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Status: api.ThreadWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.terminals.set("term-aaaaa", api.TerminalUnreachable)
	f.service.Sweep(ctx)
	if got := f.service.ActiveThreadID("term-aaaaa"); got != id {
		t.Errorf("unreachable terminal lost its active thread")
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Status != api.ThreadWorking {
		t.Errorf("status = %s; an unreachable terminal must not coerce", thread.Status)
	}
}

// Delayed live evidence for a conversation nothing displays must not
// revive it; idle evidence is always honest.
func TestLiveStatusIgnoredWhileInactive(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.service.Deactivate(ctx, "term-aaaaa")
	if err := f.service.ObserveStatus(ctx, StatusObservation{
		Agent: "claude", ProviderID: "sess-1", Status: api.ThreadWorking,
	}); err != nil {
		t.Fatal(err)
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Status != api.ThreadIdle {
		t.Errorf("status = %s; delayed working revived an inactive thread", thread.Status)
	}
}

// Evidence refreshes persist silently, so a restart never rewinds
// lastEvidenceAt.
func TestEvidenceSurvivesReload(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	if err := f.service.ObserveStatus(ctx, StatusObservation{
		Agent: "claude", ProviderID: "sess-1", Status: api.ThreadIdle,
	}); err != nil {
		t.Fatal(err)
	}
	before, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}

	reloaded := NewService(Options{
		Repository: f.store.Threads(),
		Terminals:  f.terminals,
		Hub:        events.NewHubAt(8, 1),
		Now:        f.clock.Now,
	})
	if err := reloaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	after, err := reloaded.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if after.LastEvidenceAt == nil || !after.LastEvidenceAt.Equal(*before.LastEvidenceAt) {
		t.Errorf("LastEvidenceAt after reload = %v; want %v", after.LastEvidenceAt, before.LastEvidenceAt)
	}
}

func TestIdlePersistsWhenInactive(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.drain()
	f.terminals.set("term-aaaaa", api.TerminalExited)
	f.service.Sweep(ctx)
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.Status != api.ThreadIdle {
		t.Errorf("status = %s; idle must persist when the thread goes inactive", thread.Status)
	}
	// Only the projection changed, so only the terminal event fires.
	if diff := cmp.Diff([]string{"terminal.updated term-aaaaa"}, f.drain()); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

func TestTerminalRemovedClearsLinkage(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Status: api.ThreadWorking,
	})
	if err != nil {
		t.Fatal(err)
	}
	f.drain()

	// The API layer deletes the terminal, then notifies. The row-level
	// SET NULL already happened; the view converges here.
	if ok, err := f.store.Terminals().Delete(ctx, "term-aaaaa"); err != nil || !ok {
		t.Fatalf("terminal delete = %v, %v", ok, err)
	}
	f.terminals.remove("term-aaaaa")
	f.service.TerminalRemoved(ctx, "term-aaaaa")

	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.TerminalID != "" {
		t.Errorf("TerminalID = %q; want cleared", thread.TerminalID)
	}
	if thread.Status != api.ThreadUnknown {
		t.Errorf("status = %s; want unknown", thread.Status)
	}
	if got := f.service.ActiveThreadID("term-aaaaa"); got != "" {
		t.Errorf("ActiveThreadID = %q; want none", got)
	}
	if diff := cmp.Diff([]string{"thread.updated " + id}, f.drain()); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

func TestProjectRemovedDropsThreads(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	f.service.Deactivate(ctx, "term-aaaaa")
	f.drain()

	// The API layer deletes the project (terminal already gone), then
	// notifies; the schema cascade removed the rows.
	if ok, err := f.store.Terminals().Delete(ctx, "term-aaaaa"); err != nil || !ok {
		t.Fatalf("terminal delete = %v, %v", ok, err)
	}
	f.service.TerminalRemoved(ctx, "term-aaaaa")
	if ok, err := f.store.Projects().Delete(ctx, "proj-aaaaa"); err != nil || !ok {
		t.Fatalf("project delete = %v, %v", ok, err)
	}
	f.drain()
	f.service.ProjectRemoved("proj-aaaaa")

	if _, err := f.service.Get(id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after project removal = %v; want ErrNotFound", err)
	}
	if diff := cmp.Diff([]string{"thread.deleted " + id}, f.drain()); diff != "" {
		t.Errorf("events (-want +got):\n%s", diff)
	}
}

func TestListFilters(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	f.plant(t, "proj-bbbbb", "term-bbbbb")
	ctx := context.Background()

	first, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-2", TerminalID: "term-bbbbb", ProjectID: "proj-bbbbb",
	})
	if err != nil {
		t.Fatal(err)
	}

	ids := func(threads []api.Thread) []string {
		var got []string
		for _, thread := range threads {
			got = append(got, thread.ID)
		}
		return got
	}
	if diff := cmp.Diff([]string{first, second}, ids(f.service.List("", "", false))); diff != "" {
		t.Errorf("unfiltered list (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{first}, ids(f.service.List("proj-aaaaa", "", false))); diff != "" {
		t.Errorf("project filter (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{second}, ids(f.service.List("", "term-bbbbb", false))); diff != "" {
		t.Errorf("terminal filter (-want +got):\n%s", diff)
	}
}

// Restart discipline: idle and error persist; live statuses are claims
// about an observation that no longer exists and coerce to unknown, in
// the database too.
func TestLoadCoercesLiveStatuses(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()

	idle, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	working, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-2", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Status: api.ThreadWaitingForPermission,
	})
	if err != nil {
		t.Fatal(err)
	}

	reloaded := NewService(Options{
		Repository: f.store.Threads(),
		Terminals:  f.terminals,
		Hub:        events.NewHubAt(8, 1),
		Now:        f.clock.Now,
	})
	if err := reloaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	got, err := reloaded.Get(idle)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.ThreadIdle {
		t.Errorf("idle thread after reload = %s; want idle", got.Status)
	}
	got, err = reloaded.Get(working)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.ThreadUnknown {
		t.Errorf("live thread after reload = %s; want unknown", got.Status)
	}
	if reloaded.ActiveThreadID("term-aaaaa") != "" {
		t.Error("active projection survived a reload; it must be re-derived from evidence")
	}

	// The coercion is persisted: a third load finds unknown already stored.
	records, err := f.store.Threads().List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range records {
		if record.ID == working && record.Status != string(api.ThreadUnknown) {
			t.Errorf("database still claims %s after reload", record.Status)
		}
	}
}

func TestReattachUpdatesMetadataAndTerminalOnly(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	f.plant(t, "proj-bbbbb", "term-bbbbb")
	ctx := context.Background()

	id, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Metadata: Metadata{Cwd: "/proj-aaaaa"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Resumed from a terminal in another project: the terminal linkage and
	// provider-reported cwd move, the project deliberately does not.
	if _, err := f.service.ObserveSession(ctx, SessionObservation{
		Agent: "claude", ProviderID: "sess-1", TerminalID: "term-bbbbb", ProjectID: "proj-bbbbb",
		Metadata: Metadata{Cwd: "/proj-bbbbb"},
	}); err != nil {
		t.Fatal(err)
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	if thread.ProjectID != "proj-aaaaa" {
		t.Errorf("ProjectID = %q; first observation is immutable", thread.ProjectID)
	}
	if thread.TerminalID != "term-bbbbb" || thread.Cwd != "/proj-bbbbb" {
		t.Errorf("thread = %+v; want moved terminal and cwd", thread)
	}
}
