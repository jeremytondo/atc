package threads

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
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
		Adapter:    "claude",
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
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
		Adapter: "claude", ProviderID: "sess-1", Status: api.ThreadWorking,
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
		Adapter: "claude", ProviderID: "sess-1", Status: api.ThreadWorking,
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
		Adapter: "claude", ProviderID: "sess-1", Status: api.ThreadIdle, LastError: &detail,
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
		Adapter: "claude", ProviderID: "sess-unknown", Status: api.ThreadWorking,
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
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
		Adapter: "claude", ProviderID: "sess-1", Metadata: Metadata{Title: "later observation"},
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
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
		Adapter: "claude", ProviderID: "sess-1", Status: api.ThreadWorking,
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
		Adapter: "claude", ProviderID: "sess-1", Status: api.ThreadIdle,
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-2", TerminalID: "term-bbbbb", ProjectID: "proj-bbbbb",
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-2", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
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
		Adapter: "claude", Agent: "claude", ProviderID: "sess-1", TerminalID: "term-aaaaa", ProjectID: "proj-aaaaa",
		Metadata: Metadata{Cwd: "/proj-aaaaa"},
	})
	if err != nil {
		t.Fatal(err)
	}

	// Resumed from a terminal in another project: the terminal linkage and
	// provider-reported cwd move, the project deliberately does not.
	if _, err := f.service.ObserveSession(ctx, SessionObservation{
		Adapter: "claude", Agent: "claude", ProviderID: "sess-1", TerminalID: "term-bbbbb", ProjectID: "proj-bbbbb",
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

// fakeResumer is the hand-written launch seam behind open: it plants a
// running terminal record the way the agents side would create one and
// records every request. gate, when set, holds the resume until released
// so a concurrent open can be caught behind the decision; onResume runs
// inside the launch (a client cancelling mid-launch).
type fakeResumer struct {
	mu       sync.Mutex
	f        *fixture
	requests []ResumeRequest
	gate     chan struct{}
	fail     error
	onResume func()
}

func (r *fakeResumer) Resume(_ context.Context, req ResumeRequest) (api.Terminal, error) {
	r.mu.Lock()
	r.requests = append(r.requests, req)
	n := len(r.requests)
	gate, fail, onResume := r.gate, r.fail, r.onResume
	r.mu.Unlock()
	if gate != nil {
		<-gate
	}
	if onResume != nil {
		onResume()
	}
	if fail != nil {
		return api.Terminal{}, fail
	}
	id := fmt.Sprintf("term-resm%d", n)
	now := r.f.clock.Now()
	if ok, err := r.f.store.Terminals().Insert(context.Background(), store.TerminalRecord{
		ID: id, ProjectID: req.ProjectID, Name: "resume", Directory: "/" + req.ProjectID,
		Agent: req.Agent, CreatedAt: now, UpdatedAt: now,
	}); err != nil || !ok {
		return api.Terminal{}, fmt.Errorf("planting resume terminal = %v: %w", ok, err)
	}
	r.f.terminals.set(id, api.TerminalRunning)
	return api.Terminal{ID: id, ProjectID: req.ProjectID, Agent: req.Agent, Status: api.TerminalRunning}, nil
}

// observed plants a project with a running terminal showing conversation
// s1 (cwd recorded) and returns the thread id — the starting point every
// open case varies from.
func (f *fixture) observed(t *testing.T) string {
	t.Helper()
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	o := observation("term-aaaaa", "s1")
	o.Metadata.Cwd = "/proj-aaaaa/sub"
	id, err := f.service.ObserveSession(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// dormant makes the observed thread's terminal exit and sweeps it.
func (f *fixture) dormant(t *testing.T) {
	t.Helper()
	f.terminals.set("term-aaaaa", api.TerminalExited)
	f.service.Sweep(context.Background())
}

// Open resolves a thread to exactly one terminal: reuse wherever a
// terminal may still hold the conversation, resume only when it is
// definitively dormant — and the decision itself records the linkage.
func TestOpenDecision(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		// arrange varies the observed thread's terminal state.
		arrange     func(t *testing.T, f *fixture, id string)
		wantCreated bool
	}{
		{name: "actively shown terminal is reused", arrange: func(*testing.T, *fixture, string) {}},
		{
			// A restart: the projection is empty, the terminal runs on.
			name:    "last terminal running with unknown contents is reused",
			arrange: func(_ *testing.T, f *fixture, _ string) { f.service.Deactivate(ctx, "term-aaaaa") },
		},
		{
			name: "last terminal unreachable still counts as up",
			arrange: func(_ *testing.T, f *fixture, _ string) {
				f.service.Deactivate(ctx, "term-aaaaa")
				f.terminals.set("term-aaaaa", api.TerminalUnreachable)
			},
		},
		{
			// The TUI switched to another conversation (/new): the terminal
			// is known to show something else.
			name: "last terminal showing another conversation means dormant",
			arrange: func(t *testing.T, f *fixture, _ string) {
				if _, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "s2")); err != nil {
					t.Fatal(err)
				}
			},
			wantCreated: true,
		},
		{
			name:        "exited terminal means dormant: resume and link",
			arrange:     func(t *testing.T, f *fixture, _ string) { f.dormant(t) },
			wantCreated: true,
		},
		{
			name: "deleted terminal means dormant",
			arrange: func(t *testing.T, f *fixture, _ string) {
				f.terminals.remove("term-aaaaa")
				if _, err := f.store.Terminals().Delete(ctx, "term-aaaaa"); err != nil {
					t.Fatal(err)
				}
				f.service.TerminalRemoved(ctx, "term-aaaaa")
			},
			wantCreated: true,
		},
		{
			name: "archived thread is unarchived",
			arrange: func(t *testing.T, f *fixture, id string) {
				f.dormant(t)
				archived := true
				if _, err := f.service.Update(ctx, id, api.ThreadUpdateParams{Archived: &archived}); err != nil {
					t.Fatal(err)
				}
			},
			wantCreated: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			resumer := &fakeResumer{f: f}
			id := f.observed(t)
			tc.arrange(t, f, id)
			f.drain()

			terminal, created, err := f.service.Open(ctx, id, resumer.Resume)
			if err != nil {
				t.Fatal(err)
			}
			wantTerminal := "term-aaaaa"
			if tc.wantCreated {
				wantTerminal = "term-resm1"
			}
			if terminal.ID != wantTerminal || created != tc.wantCreated {
				t.Errorf("Open = %s, created %v; want %s, %v", terminal.ID, created, wantTerminal, tc.wantCreated)
			}
			thread, err := f.service.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if thread.TerminalID != terminal.ID || thread.Archived {
				t.Errorf("after open: terminal %q archived %v; want linked to %s, unarchived", thread.TerminalID, thread.Archived, terminal.ID)
			}
			if !tc.wantCreated {
				if len(resumer.requests) != 0 {
					t.Errorf("reuse reached the resumer: %+v", resumer.requests)
				}
				return
			}
			// The resume carries the identity, project, and recorded cwd,
			// and the linkage publishes.
			want := ResumeRequest{Adapter: "claude", Agent: "claude", ProviderID: "s1", ProjectID: "proj-aaaaa", Directory: "/proj-aaaaa/sub"}
			if diff := cmp.Diff([]ResumeRequest{want}, resumer.requests); diff != "" {
				t.Errorf("resume requests (-want +got):\n%s", diff)
			}
			if got := f.drain(); len(got) != 1 || got[0] != "thread.updated "+id {
				t.Errorf("events after resume = %v", got)
			}
		})
	}
}

// Refusals: an unknown id, a failed resume (the record stays as it was),
// and a dormant open with no resumer to launch.
func TestOpenRefusals(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	resumer := &fakeResumer{f: f}
	if _, _, err := f.service.Open(ctx, "thrd-zzzzz", resumer.Resume); !errors.Is(err, ErrNotFound) || len(resumer.requests) != 0 {
		t.Errorf("Open(unknown) = %v, requests %+v; want ErrNotFound and no resume", err, resumer.requests)
	}

	id := f.observed(t)
	f.dormant(t)
	f.drain()
	resumer.fail = errors.New("agent unavailable")
	if _, _, err := f.service.Open(ctx, id, resumer.Resume); err == nil || err.Error() != "agent unavailable" {
		t.Errorf("Open(failed resume) = %v", err)
	}
	if thread, _ := f.service.Get(id); thread.TerminalID != "term-aaaaa" {
		t.Errorf("failed resume changed linkage to %q", thread.TerminalID)
	}
	if got := f.drain(); len(got) != 0 {
		t.Errorf("failed resume published %v", got)
	}

	if _, _, err := f.service.Open(ctx, id, nil); !errors.Is(err, ErrResumeUnavailable) {
		t.Errorf("Open(dormant, no resumer) = %v, want ErrResumeUnavailable", err)
	}
}

// Two concurrent opens of a dormant thread converge on one terminal: the
// second waits for the first's resume rather than racing it, then finds
// the linkage that decision recorded — before any hook evidence — and
// reuses. Meanwhile the launch does not hold up evidence for other
// terminals, and the thread cannot be archived or deleted from under it.
func TestConcurrentOpensConverge(t *testing.T) {
	ctx := context.Background()
	f := newFixture(t)
	resumer := &fakeResumer{f: f, gate: make(chan struct{})}
	id := f.observed(t)
	f.dormant(t)

	type result struct {
		terminal api.Terminal
		created  bool
		err      error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			terminal, created, err := f.service.Open(ctx, id, resumer.Resume)
			results <- result{terminal, created, err}
		}()
	}
	deadline := time.After(5 * time.Second)
	for {
		resumer.mu.Lock()
		started := len(resumer.requests)
		resumer.mu.Unlock()
		if started == 1 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("the first open never reached the resumer")
		case <-time.After(time.Millisecond):
		}
	}
	// With the launch in flight, other evidence still commits...
	f.plant(t, "proj-bbbbb", "term-bbbbb")
	if _, err := f.service.ObserveSession(ctx, SessionObservation{
		Adapter: "claude", Agent: "claude", ProviderID: "s9", TerminalID: "term-bbbbb", ProjectID: "proj-bbbbb",
	}); err != nil {
		t.Errorf("evidence during a resume launch: %v", err)
	}
	// ...but the thread being opened is spoken for.
	archived := true
	if _, err := f.service.Update(ctx, id, api.ThreadUpdateParams{Archived: &archived}); !errors.Is(err, ErrActive) {
		t.Errorf("archive during open = %v, want ErrActive", err)
	}
	if err := f.service.Delete(ctx, id); !errors.Is(err, ErrActive) {
		t.Errorf("delete during open = %v, want ErrActive", err)
	}
	close(resumer.gate)

	var got []result
	for range 2 {
		got = append(got, <-results)
	}
	for _, r := range got {
		if r.err != nil {
			t.Fatal(r.err)
		}
	}
	if got[0].terminal.ID != got[1].terminal.ID || got[0].terminal.ID != "term-resm1" {
		t.Errorf("opens diverged: %s and %s", got[0].terminal.ID, got[1].terminal.ID)
	}
	if got[0].created == got[1].created {
		t.Errorf("created flags = %v, %v; want exactly one create", got[0].created, got[1].created)
	}
	if len(resumer.requests) != 1 {
		t.Errorf("resumes = %d, want 1", len(resumer.requests))
	}
}

// A client that gives up mid-launch (the CLI interrupted while the TUI
// settles) still gets the resume linked: the terminal is running, and an
// unlinked one would let the next open start a second writer.
func TestOpenLinksDespiteCancel(t *testing.T) {
	f := newFixture(t)
	id := f.observed(t)
	f.dormant(t)
	ctx, cancel := context.WithCancel(context.Background())
	resumer := &fakeResumer{f: f, onResume: cancel}

	terminal, created, err := f.service.Open(ctx, id, resumer.Resume)
	if err != nil || !created || terminal.ID != "term-resm1" {
		t.Fatalf("Open under cancel = %+v, %v, %v", terminal, created, err)
	}
	if thread, _ := f.service.Get(id); thread.TerminalID != "term-resm1" {
		t.Errorf("linkage after cancel = %q, want term-resm1", thread.TerminalID)
	}
	// Persisted, not just in the view: a reload still sees it.
	records, err := f.store.Threads().List(context.Background())
	if err != nil || len(records) != 1 || records[0].TerminalID == nil || *records[0].TerminalID != "term-resm1" {
		t.Errorf("persisted records = %+v, %v", records, err)
	}
}

// Adapter-held threads (ATC-285): an observation mints a record with no
// terminal, later ones bring it into line with the program — agent, title
// (unless the user's), status, error, metadata — and the hold accepts
// live statuses and refuses archive and delete.
func TestObserveAdapterMintsAndUpdates(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()

	if _, err := f.service.ObserveAdapter(ctx, AdapterObservation{Adapter: "t3code", ProviderID: "t1"}); !errors.Is(err, ErrProjectRequired) {
		t.Fatalf("first observation without a project = %v, want ErrProjectRequired", err)
	}
	id, err := f.service.ObserveAdapter(ctx, AdapterObservation{
		Adapter: "t3code", ProviderID: "t1", ProjectID: "proj-aaaaa",
		Status: api.ThreadWorking, Agent: "codex", Title: "Fix it", LastError: "",
		Metadata: Metadata{Model: "gpt-5", Cwd: "/proj-aaaaa"},
	})
	if err != nil {
		t.Fatal(err)
	}
	thread, err := f.service.Get(id)
	if err != nil {
		t.Fatal(err)
	}
	want := api.Thread{
		ID: id, Adapter: "t3code", Agent: "codex", ProjectID: "proj-aaaaa", Title: "Fix it",
		Model: "gpt-5", Cwd: "/proj-aaaaa", Status: api.ThreadWorking,
		LastEvidenceAt: thread.LastEvidenceAt, CreatedAt: thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
	}
	if diff := cmp.Diff(want, thread); diff != "" {
		t.Errorf("minted (-want +got):\n%s", diff)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.created " + id}) {
		t.Errorf("events = %v", got)
	}

	// Refusals name the adapter; the title is the user's to set.
	archived := true
	if _, err := f.service.Update(ctx, id, api.ThreadUpdateParams{Archived: &archived}); !errors.Is(err, ErrActive) || !strings.Contains(err.Error(), "adapter t3code") {
		t.Errorf("archive held = %v", err)
	}
	if err := f.service.Delete(ctx, id); !errors.Is(err, ErrActive) {
		t.Errorf("delete held = %v", err)
	}
	title := "mine"
	if _, err := f.service.Update(ctx, id, api.ThreadUpdateParams{Title: &title}); err != nil {
		t.Fatal(err)
	}
	f.drain()

	// The program is the source of truth for everything but that title;
	// an identical observation refreshes evidence silently.
	again := AdapterObservation{
		Adapter: "t3code", ProviderID: "t1", Status: api.ThreadError, Agent: "", Title: "T3 renamed",
		LastError: "boom", Metadata: Metadata{Model: "gpt-6"},
	}
	if got, err := f.service.ObserveAdapter(ctx, again); err != nil || got != id {
		t.Fatalf("second observation = %q, %v; want %q", got, err, id)
	}
	thread, _ = f.service.Get(id)
	if thread.Agent != "" || thread.Title != "mine" || thread.Status != api.ThreadError || thread.LastError != "boom" || thread.Model != "gpt-6" || thread.Cwd != "/proj-aaaaa" {
		t.Errorf("updated = %+v", thread)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + id}) {
		t.Errorf("events = %v", got)
	}
	before := thread.UpdatedAt
	if _, err := f.service.ObserveAdapter(ctx, again); err != nil {
		t.Fatal(err)
	}
	thread, _ = f.service.Get(id)
	if got := f.drain(); len(got) != 0 || !thread.UpdatedAt.Equal(before) || !thread.LastEvidenceAt.After(before) {
		t.Errorf("identical observation: events %v, updatedAt %v→%v, lastEvidenceAt %v", got, before, thread.UpdatedAt, thread.LastEvidenceAt)
	}
	if diff := cmp.Diff([]string{"t1"}, f.service.UnarchivedProviderIDs("t3code")); diff != "" {
		t.Errorf("UnarchivedProviderIDs (-want +got):\n%s", diff)
	}
}

// Releasing the adapter coerces the live statuses it vouched for and
// frees the threads; archiving on removal keeps the record and frees it
// too; a later observation unarchives the same record.
func TestAdapterReleaseAndArchive(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa")
	ctx := context.Background()
	observe := func(providerID string, status api.ThreadStatus) string {
		t.Helper()
		id, err := f.service.ObserveAdapter(ctx, AdapterObservation{Adapter: "t3code", ProviderID: providerID, ProjectID: "proj-aaaaa", Status: status})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	live := observe("t-live", api.ThreadWaitingForPermission)
	idle := observe("t-idle", api.ThreadIdle)
	f.drain()

	f.service.ReleaseAdapter(ctx, "t3code")
	if thread, _ := f.service.Get(live); thread.Status != api.ThreadUnknown {
		t.Errorf("live after release = %s, want unknown", thread.Status)
	}
	if thread, _ := f.service.Get(idle); thread.Status != api.ThreadIdle {
		t.Errorf("idle after release = %s, want idle", thread.Status)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + live}) {
		t.Errorf("release events = %v", got)
	}
	archived := true
	if _, err := f.service.Update(ctx, idle, api.ThreadUpdateParams{Archived: &archived}); err != nil {
		t.Errorf("archive after release = %v", err)
	}
	// Live evidence for a released thread is ignored, as for a terminal
	// that left; a fresh adapter observation re-holds it.
	if err := f.service.ObserveStatus(ctx, StatusObservation{Adapter: "t3code", ProviderID: "t-live", Status: api.ThreadWorking}); err != nil {
		t.Fatal(err)
	}
	if thread, _ := f.service.Get(live); thread.Status != api.ThreadUnknown {
		t.Errorf("status evidence revived a released thread: %s", thread.Status)
	}
	observe("t-live", api.ThreadWorking)
	if thread, _ := f.service.Get(live); thread.Status != api.ThreadWorking {
		t.Errorf("re-observed = %s, want working", thread.Status)
	}
	f.drain()

	if err := f.service.ArchiveAdapterThread(ctx, "t3code", "t-live"); err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(live)
	if !thread.Archived || thread.ArchivedAt == nil || thread.Status != api.ThreadUnknown {
		t.Errorf("archived by adapter = %+v", thread)
	}
	if got := f.drain(); !slices.Equal(got, []string{"thread.updated " + live}) {
		t.Errorf("archive events = %v", got)
	}
	if ids := f.service.UnarchivedProviderIDs("t3code"); len(ids) != 0 {
		t.Errorf("UnarchivedProviderIDs after archiving = %v", ids)
	}
	if err := f.service.ArchiveAdapterThread(ctx, "t3code", "t-unknown"); err != nil {
		t.Errorf("archiving an unknown identity = %v", err)
	}
	if err := f.service.Delete(ctx, live); err != nil {
		t.Errorf("delete after archive = %v", err)
	}
	if got := observe("t-idle", api.ThreadIdle); got != idle {
		t.Errorf("re-observation minted %s, want %s back", got, idle)
	}
	if thread, _ := f.service.Get(idle); thread.Archived {
		t.Error("re-observation left the thread archived")
	}
}

// Links derive at read time through the adapter's linker; threads of
// other adapters carry none, and a boot coerces adapter-held live
// statuses like any other.
func TestAdapterLinksAndBootCoercion(t *testing.T) {
	f := newFixture(t)
	f.plant(t, "proj-aaaaa", "term-aaaaa")
	ctx := context.Background()
	f.service.SetLinker("t3code", func(providerID string) *api.ThreadLinks {
		return &api.ThreadLinks{Web: "http://t3/" + providerID, App: "t3code://" + providerID}
	})
	held, err := f.service.ObserveAdapter(ctx, AdapterObservation{Adapter: "t3code", ProviderID: "t1", ProjectID: "proj-aaaaa", Status: api.ThreadWorking})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.service.ObserveSession(ctx, observation("term-aaaaa", "sess-1"))
	if err != nil {
		t.Fatal(err)
	}
	thread, _ := f.service.Get(held)
	if diff := cmp.Diff(&api.ThreadLinks{Web: "http://t3/t1", App: "t3code://t1"}, thread.Links); diff != "" {
		t.Errorf("links (-want +got):\n%s", diff)
	}
	for _, thread := range f.service.List("", "", false) {
		if thread.ID == other && thread.Links != nil {
			t.Errorf("a terminal thread carries links: %+v", thread.Links)
		}
	}

	reloaded := NewService(Options{Repository: f.store.Threads(), Terminals: f.terminals, Hub: f.hub, Now: f.clock.Now})
	if err := reloaded.Load(ctx); err != nil {
		t.Fatal(err)
	}
	if thread, _ := reloaded.Get(held); thread.Status != api.ThreadUnknown || thread.Adapter != "t3code" {
		t.Errorf("after reload = %+v; want unknown, adapter kept", thread)
	}
	if ids := reloaded.UnarchivedProviderIDs("t3code"); !slices.Equal(ids, []string{"t1"}) {
		t.Errorf("identities after reload = %v", ids)
	}
}
