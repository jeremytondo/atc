package terminals

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/exitmarker"
	"github.com/jeremytondo/atc/internal/store"
)

// fakeAdapter is a hand-written in-memory session backend. Create births a
// reachable session unless told otherwise; Kill removes it unless killErr
// is set (the "zmx unhealthy" condition).
type fakeAdapter struct {
	mu        sync.Mutex
	sessions  map[string]bool // name → reachable
	invErr    error
	createErr error
	killErr   error
	killed    []string
	// onCreate observes the create, e.g. to assert the record already
	// exists or to plant a fast-failure marker instead of a session.
	onCreate func(id string, spec CreateSpec)
}

func newFakeAdapter() *fakeAdapter {
	return &fakeAdapter{sessions: map[string]bool{}}
}

func (a *fakeAdapter) Inventory(context.Context) ([]Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.invErr != nil {
		return nil, a.invErr
	}
	inventory := make([]Session, 0, len(a.sessions))
	for name, reachable := range a.sessions {
		inventory = append(inventory, Session{Name: name, Reachable: reachable})
	}
	return inventory, nil
}

func (a *fakeAdapter) Create(_ context.Context, id string, spec CreateSpec) error {
	a.mu.Lock()
	createErr, onCreate := a.createErr, a.onCreate
	a.mu.Unlock()
	if onCreate != nil {
		onCreate(id, spec)
	}
	if createErr != nil {
		return createErr
	}
	a.mu.Lock()
	a.sessions[id] = true
	a.mu.Unlock()
	return nil
}

func (a *fakeAdapter) Kill(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.killed = append(a.killed, id)
	if a.killErr != nil {
		return a.killErr
	}
	delete(a.sessions, id)
	return nil
}

func (a *fakeAdapter) killedNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.killed...)
}

func (a *fakeAdapter) set(name string, reachable bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[name] = reachable
}

func (a *fakeAdapter) remove(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, name)
}

func (a *fakeAdapter) setInvErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.invErr = err
}

// fixture wires a Service over a real temp-file store, the fake adapter, a
// real marker directory, and a fake clock, with one project (rooted at a
// real temp directory) for terminals to belong to.
type fixture struct {
	service    *Service
	adapter    *fakeAdapter
	hub        *events.Hub
	markers    string
	clock      *fakeClock
	projectID  string
	projectDir string
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
	adapter := newFakeAdapter()
	markers := t.TempDir()
	clock := &fakeClock{now: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
	projectDir := t.TempDir()
	projectID := "proj-aaaaa"
	if ok, err := s.Projects().Insert(context.Background(), store.ProjectRecord{
		ID: projectID, Name: "p", Directory: projectDir, CreatedAt: clock.Now(), UpdatedAt: clock.Now(),
	}); err != nil || !ok {
		t.Fatalf("planting project = %v, %v", ok, err)
	}
	service := NewService(Options{
		Repository: s.Terminals(),
		Adapter:    adapter,
		Projects:   s.Projects(),
		MarkerDir:  markers,
		Hub:        events.NewHub(64),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        clock.Now,
	})
	service.verifyInterval = time.Millisecond
	return &fixture{service: service, adapter: adapter, hub: service.hub, markers: markers,
		clock: clock, projectID: projectID, projectDir: projectDir}
}

// create is Create against the fixture's project.
func (f *fixture) create(ctx context.Context, params api.TerminalCreateParams) (api.Terminal, error) {
	if params.ProjectID == "" {
		params.ProjectID = f.projectID
	}
	return f.service.Create(ctx, params)
}

func plantExitMarker(t *testing.T, dir, id string, code int, exited bool) {
	t.Helper()
	marker := exitmarker.Marker{TerminalID: id, PID: 1, StartedAt: time.Now()}
	if exited {
		now := time.Now().UTC()
		marker.ExitedAt = &now
		marker.Code = &code
	}
	if err := exitmarker.Write(exitmarker.Path(dir, id), marker); err != nil {
		t.Fatal(err)
	}
}

var idFormat = regexp.MustCompile(`^term-[23456789bcdfghjkmnpqrstvwxyz]{5}$`)

func TestCreateHappyPath(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	// Record-first invariant: when the backend is asked to start the
	// session, the record must already be durable.
	var recordExisted bool
	f.adapter.onCreate = func(id string, _ CreateSpec) {
		records, err := f.service.repository.List(ctx)
		if err != nil {
			t.Error(err)
		}
		for _, record := range records {
			if record.ID == id {
				recordExisted = true
			}
		}
	}

	terminal, err := f.create(ctx, api.TerminalCreateParams{App: "hx"})
	if err != nil {
		t.Fatal(err)
	}
	if !idFormat.MatchString(terminal.ID) {
		t.Errorf("ID %q does not match the permanent format", terminal.ID)
	}
	if !recordExisted {
		t.Error("session started before the record was persisted")
	}
	want := terminal
	want.Name, want.ProjectID, want.Directory, want.App, want.Status = "hx", f.projectID, f.projectDir, "hx", api.TerminalRunning
	if diff := cmp.Diff(want, terminal); diff != "" {
		t.Errorf("terminal (-want +got):\n%s", diff)
	}
}

func TestCreateDefaults(t *testing.T) {
	f := newFixture(t)
	terminal, err := f.create(context.Background(), api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Name != "Shell" || terminal.Directory != f.projectDir || terminal.App != "" {
		t.Errorf("defaults = %q %q %q, want Shell %q \"\"", terminal.Name, terminal.Directory, terminal.App, f.projectDir)
	}
}

// A fast-failing app never becomes a session; the wrapper's marker is the
// evidence and create reports exited with it — no separate error path.
func TestCreateFastFailingApp(t *testing.T) {
	f := newFixture(t)
	f.adapter.createErr = errors.New("client exited before the session settled")
	f.adapter.onCreate = func(id string, _ CreateSpec) {
		plantExitMarker(t, f.markers, id, 127, true)
	}
	terminal, err := f.create(context.Background(), api.TerminalCreateParams{App: "no-such-tool"})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != api.TerminalExited {
		t.Fatalf("status = %s, want exited", terminal.Status)
	}
	if terminal.ExitCode == nil || *terminal.ExitCode != 127 {
		t.Errorf("exitCode = %v, want 127", terminal.ExitCode)
	}
}

// The session never appears and no evidence lands: the window closes and
// the terminal reports missing rather than inventing an error.
func TestCreateNeverSettles(t *testing.T) {
	f := newFixture(t)
	f.adapter.createErr = errors.New("session never settled")
	terminal, err := f.create(context.Background(), api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != api.TerminalMissing {
		t.Errorf("status = %s, want missing", terminal.Status)
	}
}

// The reconciliation decision table over (inventory result, marker state,
// stop intent) → status.
func TestReconcileDecisionTable(t *testing.T) {
	invErr := errors.New("zmx unavailable")
	code3 := 3
	for name, tc := range map[string]struct {
		present      bool
		reachable    bool
		invErr       error
		markerExited bool
		markerStart  bool
		stopIntent   bool
		want         api.TerminalStatus
		wantCode     *int
	}{
		"present reachable → running":                   {present: true, reachable: true, want: api.TerminalRunning},
		"present unresponsive → unreachable":            {present: true, want: api.TerminalUnreachable},
		"inventory failure → unreachable":               {present: true, reachable: true, invErr: invErr, want: api.TerminalUnreachable},
		"absent with evidence → exited":                 {markerExited: true, want: api.TerminalExited, wantCode: &code3},
		"absent, stop intent → exited, code suppressed": {markerExited: true, stopIntent: true, want: api.TerminalExited},
		"absent, start-only marker → missing":           {markerStart: true, want: api.TerminalMissing},
		"absent, no evidence → missing":                 {want: api.TerminalMissing},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			ctx := context.Background()
			terminal, err := f.create(ctx, api.TerminalCreateParams{})
			if err != nil {
				t.Fatal(err)
			}
			id := terminal.ID

			if tc.stopIntent {
				if ok, err := f.service.repository.RecordStopIntent(ctx, id, f.clock.Now()); err != nil || !ok {
					t.Fatal(err)
				}
				f.service.mu.Lock()
				now := f.clock.Now()
				f.service.view[id].record.StopRequestedAt = &now
				f.service.mu.Unlock()
			}
			if tc.markerExited || tc.markerStart {
				plantExitMarker(t, f.markers, id, code3, tc.markerExited)
			}
			if tc.present {
				f.adapter.set(id, tc.reachable)
			} else {
				f.adapter.remove(id)
			}
			f.adapter.setInvErr(tc.invErr)

			f.service.Reconcile(ctx)
			got, err := f.service.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Errorf("status = %s, want %s", got.Status, tc.want)
			}
			if diff := cmp.Diff(tc.wantCode, got.ExitCode); diff != "" {
				t.Errorf("exitCode (-want +got):\n%s", diff)
			}
		})
	}
}

// Exit evidence is durable truth: once recorded, later inventory failures
// or marker removal never resurrect the terminal, and it stays listed
// until deleted.
func TestExitedIsStickyAndStaysListed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	terminal, err := f.create(ctx, api.TerminalCreateParams{App: "build"})
	if err != nil {
		t.Fatal(err)
	}
	f.adapter.remove(terminal.ID)
	plantExitMarker(t, f.markers, terminal.ID, 3, true)
	f.service.Reconcile(ctx)

	if err := exitmarker.Remove(f.markers, terminal.ID); err != nil {
		t.Fatal(err)
	}
	f.adapter.setInvErr(errors.New("zmx down"))
	f.service.Reconcile(ctx)
	f.adapter.setInvErr(nil)
	f.service.Reconcile(ctx)

	list := f.service.List("")
	if len(list) != 1 || list[0].Status != api.TerminalExited || list[0].ExitCode == nil || *list[0].ExitCode != 3 {
		t.Errorf("list = %+v, want one exited terminal with code 3", list)
	}
}

func TestUpdateName(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	terminal, err := f.create(ctx, api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	renamed, err := f.service.UpdateName(ctx, terminal.ID, "build watcher")
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "build watcher" {
		t.Errorf("name = %q", renamed.Name)
	}
	if _, err := f.service.UpdateName(ctx, "term-zzzzz", "x"); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateName(absent) = %v, want ErrNotFound", err)
	}
}

// Delete under an unhealthy backend: the kill fails, the record is removed
// anyway, and the surviving session is reaped by a later reconcile once it
// is reachable and recordless — never while the inventory is down.
func TestDeleteBestEffortAndOrphanReaping(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	terminal, err := f.create(ctx, api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	id := terminal.ID

	f.adapter.mu.Lock()
	f.adapter.killErr = errors.New("session still present after inventory passes")
	f.adapter.mu.Unlock()
	if err := f.service.Delete(ctx, id); err != nil {
		t.Fatalf("Delete under failing kill = %v, want nil", err)
	}
	if _, err := f.service.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}

	// Inventory down: cleanup must refuse to act.
	f.adapter.mu.Lock()
	f.adapter.killErr = nil
	f.adapter.killed = nil
	f.adapter.invErr = errors.New("zmx down")
	f.adapter.mu.Unlock()
	f.service.reconcile(ctx, true)
	if killed := f.adapter.killedNames(); len(killed) != 0 {
		t.Fatalf("cleanup acted without a complete inventory: %v", killed)
	}

	// The request-path Reconcile never reaps: kill verification is bounded
	// but slow, and must not block HTTP handlers.
	f.adapter.setInvErr(nil)
	f.service.Reconcile(ctx)
	if killed := f.adapter.killedNames(); len(killed) != 0 {
		t.Fatalf("request-path reconcile reaped: %v", killed)
	}

	// The background pass with the session reachable: provably ours, reaped.
	f.service.reconcile(ctx, true)
	if killed := f.adapter.killedNames(); len(killed) != 1 || killed[0] != id {
		t.Fatalf("orphan not reaped: killed = %v", killed)
	}

	// An unreachable recordless session is preserved.
	f.adapter.set("term-ghost", false)
	f.adapter.mu.Lock()
	f.adapter.killed = nil
	f.adapter.mu.Unlock()
	f.service.reconcile(ctx, true)
	if killed := f.adapter.killedNames(); len(killed) != 0 {
		t.Fatalf("cleanup killed an unreachable session: %v", killed)
	}
}

func TestDeleteAbsentIsNotFound(t *testing.T) {
	f := newFixture(t)
	if err := f.service.Delete(context.Background(), "term-zzzzz"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Delete(absent) = %v, want ErrNotFound", err)
	}
}

// A second delete of the same terminal is a 404, not a second success with
// a duplicate deleted event.
func TestSecondDeleteIsNotFound(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	terminal, err := f.create(ctx, api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	sub := f.hub.Subscribe(0, false)
	defer sub.Close()
	if err := f.service.Delete(ctx, terminal.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Delete(ctx, terminal.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second Delete = %v, want ErrNotFound", err)
	}
	deleted := 0
	for {
		select {
		case change := <-sub.C:
			if change.Type == api.EventTerminalDeleted {
				deleted++
			}
			continue
		default:
		}
		break
	}
	if deleted != 1 {
		t.Errorf("terminal.deleted published %d times, want 1", deleted)
	}
}

// A delete whose request context is already cancelled still completes: the
// user's intent, once durable, must not be abandoned by a disconnect.
func TestDeleteSurvivesCancelledContext(t *testing.T) {
	f := newFixture(t)
	terminal, err := f.create(context.Background(), api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := f.service.Delete(cancelled, terminal.ID); err != nil {
		t.Fatalf("Delete with cancelled context = %v, want nil", err)
	}
	if _, err := f.service.Get(terminal.ID); !errors.Is(err, ErrNotFound) {
		t.Errorf("record survived a cancelled-context delete: %v", err)
	}
}

// Exit evidence predating the record belongs to an earlier incarnation of
// a reused ID and is never adopted.
func TestStaleMarkerFromEarlierIncarnationIgnored(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	terminal, err := f.create(ctx, api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	f.adapter.remove(terminal.ID)
	// The marker's exit predates the record's creation (fixture clock
	// starts 2026-08-27T12:00) — a leftover from a dead incarnation.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	code := 3
	marker := exitmarker.Marker{TerminalID: terminal.ID, PID: 1, StartedAt: old, ExitedAt: &old, Code: &code}
	if err := exitmarker.Write(exitmarker.Path(f.markers, terminal.ID), marker); err != nil {
		t.Fatal(err)
	}
	f.service.Reconcile(ctx)
	got, err := f.service.Get(terminal.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != api.TerminalMissing {
		t.Errorf("status = %s, want missing (stale evidence rejected)", got.Status)
	}
}

// The view rebuilds from the database after a restart and the startup
// reconcile settles statuses before anything reads.
func TestLoadRebuildsView(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	running, err := f.create(ctx, api.TerminalCreateParams{Name: "keep"})
	if err != nil {
		t.Fatal(err)
	}
	gone, err := f.create(ctx, api.TerminalCreateParams{Name: "gone"})
	if err != nil {
		t.Fatal(err)
	}
	f.adapter.remove(gone.ID)

	// A "restarted" service over the same database and backend.
	restarted := NewService(Options{
		Repository: f.service.repository,
		Adapter:    f.adapter,
		Projects:   f.service.projects,
		MarkerDir:  f.markers,
		Hub:        events.NewHub(64),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        f.clock.Now,
	})
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.Reconcile(ctx)

	byName := map[string]api.TerminalStatus{}
	for _, terminal := range restarted.List("") {
		byName[terminal.Name] = terminal.Status
	}
	want := map[string]api.TerminalStatus{"keep": api.TerminalRunning, "gone": api.TerminalMissing}
	if diff := cmp.Diff(want, byName); diff != "" {
		t.Errorf("statuses after restart (-want +got):\n%s", diff)
	}
	_ = running
}

// Change events flow for create, status change, rename, and delete.
func TestEventsEmitted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sub := f.hub.Subscribe(0, false)
	defer sub.Close()

	terminal, err := f.create(ctx, api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.UpdateName(ctx, terminal.ID, "renamed"); err != nil {
		t.Fatal(err)
	}
	f.adapter.remove(terminal.ID)
	f.service.Reconcile(ctx) // running → missing
	if err := f.service.Delete(ctx, terminal.ID); err != nil {
		t.Fatal(err)
	}

	// created; updated when the create settles to running; updated for the
	// rename; updated for the missing transition; deleted.
	var types []string
	for len(types) < 5 {
		change, ok := <-sub.C
		if !ok {
			t.Fatal("subscription dropped")
		}
		if change.ID != terminal.ID || change.Resource != "terminal" {
			t.Fatalf("change = %+v", change)
		}
		types = append(types, change.Type)
	}
	want := []string{
		api.EventTerminalCreated, api.EventTerminalUpdated, api.EventTerminalUpdated,
		api.EventTerminalUpdated, api.EventTerminalDeleted,
	}
	if diff := cmp.Diff(want, types); diff != "" {
		t.Errorf("event types (-want +got):\n%s", diff)
	}
}

// The ATC-256 create contract: the project must exist, and its directory
// must exist at that moment — refused before any record is written.
func TestCreateRequiresLiveProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.service.Create(ctx, api.TerminalCreateParams{ProjectID: "proj-zzzzz"}); !errors.Is(err, ErrProjectUnknown) {
		t.Errorf("Create(unknown project) = %v, want ErrProjectUnknown", err)
	}

	if err := os.RemoveAll(f.projectDir); err != nil {
		t.Fatal(err)
	}
	if _, err := f.create(ctx, api.TerminalCreateParams{}); !errors.Is(err, ErrProjectDirectoryMissing) {
		t.Errorf("Create(vanished directory) = %v, want ErrProjectDirectoryMissing", err)
	}

	// Refused means refused: no terminal row was written and nothing is
	// listed.
	records, err := f.service.repository.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 || len(f.service.List("")) != 0 {
		t.Errorf("refused creates left records: %+v", records)
	}
}

// List's project filter scopes the view; unfiltered returns everything.
func TestListFiltersByProject(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	otherDir := t.TempDir()
	if ok, err := f.service.projects.Insert(ctx, store.ProjectRecord{
		ID: "proj-bbbbb", Name: "other", Directory: otherDir,
		CreatedAt: f.clock.Now(), UpdatedAt: f.clock.Now(),
	}); err != nil || !ok {
		t.Fatalf("planting project = %v, %v", ok, err)
	}
	mine, err := f.create(ctx, api.TerminalCreateParams{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.service.Create(ctx, api.TerminalCreateParams{ProjectID: "proj-bbbbb", Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if all := f.service.List(""); len(all) != 2 {
		t.Errorf("unfiltered list = %+v, want both terminals", all)
	}
	filtered := f.service.List("proj-bbbbb")
	if len(filtered) != 1 || filtered[0].ID != other.ID {
		t.Errorf("filtered list = %+v, want only %s", filtered, other.ID)
	}
	if other.Directory != otherDir || mine.Directory != f.projectDir {
		t.Errorf("directories not copied from projects: %q %q", mine.Directory, other.Directory)
	}
}
