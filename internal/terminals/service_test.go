package terminals

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/terminals/monitor/report"
)

// fakeDriver is a hand-written in-memory session backend. Create births a
// reachable session unless told otherwise; Kill removes it unless killErr
// is set (the "zmx unhealthy" condition).
type fakeDriver struct {
	mu        sync.Mutex
	sessions  map[string]bool // name → reachable
	invErr    error
	createErr error
	killErr   error
	killed    []string
	// onCreate observes the create, e.g. to assert the record already
	// exists or to plant a fast-failure report instead of a session.
	onCreate func(id string, spec CreateSpec)
	// commands records what each session was created with.
	commands map[string]string
}

func (a *fakeDriver) createdCommand(id string) string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.commands[id]
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{sessions: map[string]bool{}}
}

func (a *fakeDriver) Inventory(context.Context) ([]Session, error) {
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

func (a *fakeDriver) Create(_ context.Context, id string, spec CreateSpec) error {
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
	if a.commands == nil {
		a.commands = map[string]string{}
	}
	a.commands[id] = spec.Command
	a.mu.Unlock()
	return nil
}

func (a *fakeDriver) Kill(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.killed = append(a.killed, id)
	if a.killErr != nil {
		return a.killErr
	}
	delete(a.sessions, id)
	return nil
}

func (a *fakeDriver) killedNames() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.killed...)
}

func (a *fakeDriver) set(name string, reachable bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[name] = reachable
}

func (a *fakeDriver) remove(name string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, name)
}

func (a *fakeDriver) setInvErr(err error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.invErr = err
}

// fixture wires a Service over a real temp-file store, the fake driver, a
// real report directory, and a fake clock. home is the fixture's "server
// user's home", a real temp directory the Default space is rooted at.
type fixture struct {
	service *Service
	store   *store.Store
	driver  *fakeDriver
	hub     *events.Hub
	reports string
	clock   *fakeClock
	home    string
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
	driver := newFakeDriver()
	reports := t.TempDir()
	clock := &fakeClock{now: time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)}
	home := canonicalTempDir(t)
	service := NewService(Options{
		Repository: s.Terminals(),
		Driver:     driver,
		Spaces:     s.Spaces(),
		HomeDir:    home,
		ReportDir:  reports,
		Hub:        events.NewHub(64),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        clock.Now,
	})
	service.verifyInterval = time.Millisecond
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return &fixture{service: service, store: s, driver: driver, hub: service.hub, reports: reports, clock: clock, home: home}
}

// canonicalTempDir is a temp directory in canonical form — on macOS the
// temp root is a symlink, and directories are stored resolved.
func canonicalTempDir(t *testing.T) string {
	t.Helper()
	dir, err := paths.CanonicalDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

// create is Create in the Default space.
func (f *fixture) create(ctx context.Context, params api.TerminalCreateParams) (api.Terminal, error) {
	return f.service.Create(ctx, params)
}

// defaultSpace is the fixture's Default space.
func (f *fixture) defaultSpace(t *testing.T) api.Space {
	t.Helper()
	for _, space := range f.service.ListSpaces() {
		if space.IsDefault {
			return space
		}
	}
	t.Fatal("no Default space")
	return api.Space{}
}

// plantReport writes a monitor report: start-only, or exit evidence with
// code; process is the observed foreground program ("" for none yet).
func plantReport(t *testing.T, dir, id string, code int, exited bool, process string) {
	t.Helper()
	rep := report.Report{TerminalID: id, PID: 1, StartedAt: time.Now(), Process: process}
	if exited {
		now := time.Now().UTC()
		rep.ExitedAt = &now
		rep.Code = &code
	}
	if err := report.Write(report.Path(dir, id), rep); err != nil {
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
	f.driver.onCreate = func(id string, _ CreateSpec) {
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

	terminal, err := f.create(ctx, api.TerminalCreateParams{Command: "hx"})
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
	want.Name, want.SpaceID, want.Directory, want.Command, want.Process, want.Status = "", f.defaultSpace(t).ID, f.home, "hx", "hx", api.TerminalRunning
	if diff := cmp.Diff(want, terminal); diff != "" {
		t.Errorf("terminal (-want +got):\n%s", diff)
	}
}

// Defaults: the Default space, its directory, and no name — an unnamed
// terminal is labelled by its process (ATC-317); an explicit directory
// is stored canonical, and a blank name is no name.
func TestCreateDefaults(t *testing.T) {
	f := newFixture(t)
	terminal, err := f.create(context.Background(), api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Name != "" || terminal.Process != "shell" || terminal.Directory != f.home || terminal.Command != "" || terminal.SpaceID != f.defaultSpace(t).ID {
		t.Errorf("defaults = %+v, want the Default space, %q, no name, and process shell", terminal, f.home)
	}
	sub := filepath.Join(f.home, "sub dir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.home, "link")
	if err := os.Symlink(sub, link); err != nil {
		t.Fatal(err)
	}
	explicit, err := f.create(context.Background(), api.TerminalCreateParams{Directory: link, Name: "  "})
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Directory != sub || explicit.Name != "" {
		t.Errorf("explicit directory = %+v, want canonical %q and no name", explicit, sub)
	}
}

// process before the monitor's first observation comes from creation-time
// knowledge — App, then command, then shell — and an observation, once
// made, outlives both the fallback and the session.
func TestProcessFallbackAndObservation(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	app, err := f.service.CreateForApp(ctx, api.TerminalCreateParams{}, AppLaunch{
		AppID:   "codex/tui",
		Compose: func(string, string) (string, error) { return "node /opt/codex.js", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err := f.create(ctx, api.TerminalCreateParams{Command: "git log | less"})
	if err != nil {
		t.Fatal(err)
	}
	path, err := f.create(ctx, api.TerminalCreateParams{Command: "/usr/bin/hx ."})
	if err != nil {
		t.Fatal(err)
	}
	plain, err := f.create(ctx, api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{app.ID: "codex", command.ID: "git", path.ID: "hx", plain.ID: "shell"} {
		got, err := f.service.Get(id)
		if err != nil {
			t.Fatal(err)
		}
		if got.Process != want {
			t.Errorf("fallback process for %s = %q, want %q", id, got.Process, want)
		}
	}

	// An observation replaces the fallback, sticks when the report goes
	// away, and is kept by an exited terminal.
	plantReport(t, f.reports, plain.ID, 0, false, "nvim")
	f.service.Reconcile(ctx)
	if got, _ := f.service.Get(plain.ID); got.Process != "nvim" || got.Status != api.TerminalRunning {
		t.Errorf("observed = %+v, want running with process nvim", got)
	}
	if err := report.Remove(f.reports, plain.ID); err != nil {
		t.Fatal(err)
	}
	f.service.Reconcile(ctx)
	if got, _ := f.service.Get(plain.ID); got.Process != "nvim" {
		t.Errorf("after the report vanished = %q, want the observation kept", got.Process)
	}
	f.driver.remove(plain.ID)
	plantReport(t, f.reports, plain.ID, 0, true, "nvim")
	f.service.Reconcile(ctx)
	if got, _ := f.service.Get(plain.ID); got.Process != "nvim" || got.Status != api.TerminalExited {
		t.Errorf("exited = %+v, want exited with process nvim", got)
	}
}

// A fast-failing command never becomes a session; the monitor's report is
// the evidence and create reports exited with it — no separate error path.
func TestCreateFastFailingCommand(t *testing.T) {
	f := newFixture(t)
	f.driver.createErr = errors.New("client exited before the session settled")
	f.driver.onCreate = func(id string, _ CreateSpec) {
		plantReport(t, f.reports, id, 127, true, "")
	}
	terminal, err := f.create(context.Background(), api.TerminalCreateParams{Command: "no-such-tool"})
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
	f.driver.createErr = errors.New("session never settled")
	terminal, err := f.create(context.Background(), api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if terminal.Status != api.TerminalMissing {
		t.Errorf("status = %s, want missing", terminal.Status)
	}
}

// The reconciliation decision table over (inventory result, report state,
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
		// process is what the planted report observed; wantProcess what
		// the resource shows (the fallback "shell" without a report).
		process     string
		want        api.TerminalStatus
		wantCode    *int
		wantProcess string
	}{
		"present reachable → running":                   {present: true, reachable: true, want: api.TerminalRunning, wantProcess: "shell"},
		"present reachable, observed → running":         {present: true, reachable: true, markerStart: true, process: "nvim", want: api.TerminalRunning, wantProcess: "nvim"},
		"present unresponsive → unreachable":            {present: true, want: api.TerminalUnreachable, wantProcess: "shell"},
		"inventory failure → unreachable":               {present: true, reachable: true, invErr: invErr, markerStart: true, process: "nvim", want: api.TerminalUnreachable, wantProcess: "nvim"},
		"absent with evidence → exited":                 {markerExited: true, process: "hx", want: api.TerminalExited, wantCode: &code3, wantProcess: "hx"},
		"absent, stop intent → exited, code suppressed": {markerExited: true, stopIntent: true, want: api.TerminalExited, wantProcess: "shell"},
		"absent, start-only report → missing":           {markerStart: true, process: "less", want: api.TerminalMissing, wantProcess: "less"},
		"absent, no evidence → missing":                 {want: api.TerminalMissing, wantProcess: "shell"},
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
				plantReport(t, f.reports, id, code3, tc.markerExited, tc.process)
			}
			if tc.present {
				f.driver.set(id, tc.reachable)
			} else {
				f.driver.remove(id)
			}
			f.driver.setInvErr(tc.invErr)

			f.service.Reconcile(ctx)
			got, err := f.service.Get(id)
			if err != nil {
				t.Fatal(err)
			}
			if got.Status != tc.want {
				t.Errorf("status = %s, want %s", got.Status, tc.want)
			}
			if got.Process != tc.wantProcess {
				t.Errorf("process = %q, want %q", got.Process, tc.wantProcess)
			}
			if diff := cmp.Diff(tc.wantCode, got.ExitCode); diff != "" {
				t.Errorf("exitCode (-want +got):\n%s", diff)
			}
		})
	}
}

// Exit evidence is durable truth: once recorded, later inventory failures
// or report removal never resurrect the terminal, and it stays listed
// until deleted.
func TestExitedIsStickyAndStaysListed(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	terminal, err := f.create(ctx, api.TerminalCreateParams{Command: "build"})
	if err != nil {
		t.Fatal(err)
	}
	f.driver.remove(terminal.ID)
	plantReport(t, f.reports, terminal.ID, 3, true, "")
	f.service.Reconcile(ctx)

	if err := report.Remove(f.reports, terminal.ID); err != nil {
		t.Fatal(err)
	}
	f.driver.setInvErr(errors.New("zmx down"))
	f.service.Reconcile(ctx)
	f.driver.setInvErr(nil)
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
	renamed, err := f.service.Update(ctx, terminal.ID, api.TerminalUpdateParams{Name: api.Some("build watcher")})
	if err != nil {
		t.Fatal(err)
	}
	if renamed.Name != "build watcher" {
		t.Errorf("name = %q", renamed.Name)
	}
	// A merge patch: an empty patch changes nothing, an empty name is
	// refused, and null clears the name so the process labels the
	// terminal again.
	if same, err := f.service.Update(ctx, terminal.ID, api.TerminalUpdateParams{}); err != nil || same.Name != "build watcher" {
		t.Errorf("empty patch = %+v, %v", same, err)
	}
	if _, err := f.service.Update(ctx, terminal.ID, api.TerminalUpdateParams{Name: api.Some("  ")}); !errors.Is(err, ErrInvalidUpdate) {
		t.Errorf("blank name = %v, want ErrInvalidUpdate", err)
	}
	cleared, err := f.service.Update(ctx, terminal.ID, api.TerminalUpdateParams{Name: api.Clear[string]()})
	if err != nil || cleared.Name != "" || cleared.Process != "shell" {
		t.Errorf("null name = %+v, %v; want no name and process shell", cleared, err)
	}
	if _, err := f.service.Update(ctx, "term-zzzzz", api.TerminalUpdateParams{Name: api.Some("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Update(absent) = %v, want ErrNotFound", err)
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

	f.driver.mu.Lock()
	f.driver.killErr = errors.New("session still present after inventory passes")
	f.driver.mu.Unlock()
	if err := f.service.Delete(ctx, id); err != nil {
		t.Fatalf("Delete under failing kill = %v, want nil", err)
	}
	if _, err := f.service.Get(id); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}

	// Inventory down: cleanup must refuse to act.
	f.driver.mu.Lock()
	f.driver.killErr = nil
	f.driver.killed = nil
	f.driver.invErr = errors.New("zmx down")
	f.driver.mu.Unlock()
	f.service.reconcile(ctx, true)
	if killed := f.driver.killedNames(); len(killed) != 0 {
		t.Fatalf("cleanup acted without a complete inventory: %v", killed)
	}

	// The request-path Reconcile never reaps: kill verification is bounded
	// but slow, and must not block HTTP handlers.
	f.driver.setInvErr(nil)
	f.service.Reconcile(ctx)
	if killed := f.driver.killedNames(); len(killed) != 0 {
		t.Fatalf("request-path reconcile reaped: %v", killed)
	}

	// The background pass with the session reachable: provably ours, reaped.
	f.service.reconcile(ctx, true)
	if killed := f.driver.killedNames(); len(killed) != 1 || killed[0] != id {
		t.Fatalf("orphan not reaped: killed = %v", killed)
	}

	// An unreachable recordless session is preserved.
	f.driver.set("term-ghost", false)
	f.driver.mu.Lock()
	f.driver.killed = nil
	f.driver.mu.Unlock()
	f.service.reconcile(ctx, true)
	if killed := f.driver.killedNames(); len(killed) != 0 {
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
	f.driver.remove(terminal.ID)
	// The rep's exit predates the record's creation (fixture clock
	// starts 2026-08-27T12:00) — a leftover from a dead incarnation.
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	code := 3
	rep := report.Report{TerminalID: terminal.ID, PID: 1, StartedAt: old, ExitedAt: &old, Code: &code}
	if err := report.Write(report.Path(f.reports, terminal.ID), rep); err != nil {
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
	f.driver.remove(gone.ID)
	plantReport(t, f.reports, running.ID, 0, false, "nvim")

	// A "restarted" service over the same database and backend.
	restarted := NewService(Options{
		Repository: f.service.repository,
		Driver:     f.driver,
		Spaces:     f.service.spaces,
		HomeDir:    f.home,
		ReportDir:  f.reports,
		Hub:        events.NewHub(64),
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        f.clock.Now,
	})
	if err := restarted.Load(ctx); err != nil {
		t.Fatal(err)
	}
	restarted.Reconcile(ctx)
	// The Default space is stable across restarts: one, the same id.
	if spaces := restarted.ListSpaces(); len(spaces) != 1 || spaces[0].ID != f.defaultSpace(t).ID || !spaces[0].IsDefault {
		t.Errorf("spaces after restart = %+v, want the one Default space", spaces)
	}
	// A home that is no usable directory fails the boot.
	broken := NewService(Options{
		Repository: f.service.repository, Driver: f.driver, Spaces: f.service.spaces,
		HomeDir: filepath.Join(f.home, "nope"), ReportDir: f.reports, Hub: events.NewHub(64), Now: f.clock.Now,
	})
	if err := broken.Load(ctx); err == nil {
		t.Error("Load with an unusable home succeeded")
	}

	// Statuses settle from the inventory; the observed process is rebuilt
	// from the monitor's report, which outlives the server.
	byName := map[string][2]string{}
	for _, terminal := range restarted.List("") {
		byName[terminal.Name] = [2]string{string(terminal.Status), terminal.Process}
	}
	want := map[string][2]string{"keep": {"running", "nvim"}, "gone": {"missing", "shell"}}
	if diff := cmp.Diff(want, byName); diff != "" {
		t.Errorf("view after restart (-want +got):\n%s", diff)
	}
}

// Change events flow for create, status change, rename, and delete — and
// not for a process observation, which a busy shell changes constantly.
func TestEventsEmitted(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sub := f.hub.Subscribe(0, false)
	defer sub.Close()

	terminal, err := f.create(ctx, api.TerminalCreateParams{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Update(ctx, terminal.ID, api.TerminalUpdateParams{Name: api.Some("renamed")}); err != nil {
		t.Fatal(err)
	}
	plantReport(t, f.reports, terminal.ID, 0, false, "nvim")
	f.service.Reconcile(ctx) // process only: no event
	if got, _ := f.service.Get(terminal.ID); got.Process != "nvim" {
		t.Fatalf("process = %q, want nvim", got.Process)
	}
	f.driver.remove(terminal.ID)
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
	select {
	case change := <-sub.C:
		t.Errorf("unexpected extra event %+v", change)
	default:
	}
}

// The create contract: the space must exist and not be deleting, and the
// directory must exist at that moment — refused before any record is
// written.
func TestCreateRequiresLiveSpaceAndDirectory(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()

	if _, err := f.service.Create(ctx, api.TerminalCreateParams{SpaceID: "spce-zzzzz"}); !errors.Is(err, ErrSpaceNotFound) {
		t.Errorf("Create(unknown space) = %v, want ErrSpaceNotFound", err)
	}
	if _, err := f.create(ctx, api.TerminalCreateParams{Directory: filepath.Join(f.home, "nope")}); !errors.Is(err, ErrDirectoryInvalid) {
		t.Errorf("Create(missing directory) = %v, want ErrDirectoryInvalid", err)
	}
	gone := canonicalTempDir(t)
	space, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Directory: gone})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := f.create(ctx, api.TerminalCreateParams{SpaceID: space.ID}); !errors.Is(err, ErrDirectoryInvalid) {
		t.Errorf("Create(vanished space directory) = %v, want ErrDirectoryInvalid", err)
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

// List's space filter scopes the view; unfiltered returns everything. A
// terminal in a selected space starts in that space's directory.
func TestListFiltersBySpace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	otherDir := canonicalTempDir(t)
	space, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Directory: otherDir, Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	mine, err := f.create(ctx, api.TerminalCreateParams{Name: "mine"})
	if err != nil {
		t.Fatal(err)
	}
	other, err := f.service.Create(ctx, api.TerminalCreateParams{SpaceID: space.ID, Name: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if all := f.service.List(""); len(all) != 2 {
		t.Errorf("unfiltered list = %+v, want both terminals", all)
	}
	filtered := f.service.List(space.ID)
	if len(filtered) != 1 || filtered[0].ID != other.ID {
		t.Errorf("filtered list = %+v, want only %s", filtered, other.ID)
	}
	if other.Directory != otherDir || mine.Directory != f.home {
		t.Errorf("directories not taken from spaces: %q %q", mine.Directory, other.Directory)
	}
}

// An App launch's Prepare runs with the resolved directory before any
// record exists and outside the commit lock; a refusal there creates
// nothing, and a create that fails after it aborts the preparation.
// Compose then sees the minted id and the same directory, and the
// composed command stays off the wire.
func TestCreateForAppPreparesBeforeTheRecord(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	var prepared, composedDir, composedID string
	aborted, prepares := false, 0
	launch := AppLaunch{
		AppID: "alpha/tui",
		Prepare: func(_ context.Context, directory string) (func(), error) {
			prepared = directory
			prepares++
			if records, err := f.service.repository.List(ctx); err != nil || len(records) != prepares-1 {
				t.Errorf("records at prepare time = %v, %v; want the earlier creates' only", records, err)
			}
			// The commit lock is free: a concurrent mutation must not wait
			// on preparation.
			f.service.ops.Lock()
			f.service.ops.Unlock() //nolint:staticcheck // the empty critical section is the assertion
			return func() { aborted = true }, nil
		},
		Compose: func(terminalID, directory string) (string, error) {
			composedID, composedDir = terminalID, directory
			return "alpha --for " + terminalID, nil
		},
	}
	terminal, err := f.service.CreateForApp(ctx, api.TerminalCreateParams{}, launch)
	if err != nil {
		t.Fatal(err)
	}
	if prepared != f.home || composedDir != f.home || composedID != terminal.ID ||
		terminal.Command != "" || terminal.AppID != "alpha/tui" || aborted {
		t.Errorf("prepared %q, composed (%q, %q), terminal %+v, aborted %v", prepared, composedID, composedDir, terminal, aborted)
	}
	if got := f.driver.createdCommand(terminal.ID); got != "alpha --for "+terminal.ID {
		t.Errorf("driver ran %q, want the composed command", got)
	}

	refused := launch
	refused.Prepare = func(context.Context, string) (func(), error) { return nil, errors.New("not now") }
	if _, err := f.service.CreateForApp(ctx, api.TerminalCreateParams{}, refused); err == nil || err.Error() != "not now" {
		t.Fatalf("refused prepare: err = %v", err)
	}
	if records, _ := f.service.repository.List(ctx); len(records) != 1 {
		t.Errorf("a refused preparation left records: %v", records)
	}

	failing := launch
	failing.Compose = func(string, string) (string, error) { return "", errors.New("cannot compose") }
	if _, err := f.service.CreateForApp(ctx, api.TerminalCreateParams{}, failing); err == nil {
		t.Fatal("create succeeded though compose failed")
	}
	if !aborted {
		t.Error("a failed create did not abort the preparation")
	}
}

// Spaces (ATC-296): the Default space exists at boot, rooted at the
// server user's home, and refuses update and deletion; regular spaces
// canonicalize their directory, default the name from its basename, may
// share directories, and are editable — a directory change affects only
// later terminals.
func TestSpacesModel(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sub := f.hub.Subscribe(0, false)
	defer sub.Close()

	def := f.defaultSpace(t)
	if def.Name != DefaultSpaceName || def.Directory != f.home || !strings.HasPrefix(def.ID, "spce-") {
		t.Errorf("Default space = %+v", def)
	}
	if _, err := f.service.UpdateSpace(ctx, def.ID, api.SpaceUpdateParams{Name: api.Some("x")}); !errors.Is(err, ErrDefaultSpace) {
		t.Errorf("update Default = %v, want ErrDefaultSpace", err)
	}
	if err := f.service.DeleteSpace(ctx, def.ID, nil); !errors.Is(err, ErrDefaultSpace) {
		t.Errorf("delete Default = %v, want ErrDefaultSpace", err)
	}

	// Create: home by default; a symlinked path stores canonical; the name
	// defaults to the basename; the same directory twice is fine.
	dir := filepath.Join(f.home, "work dir")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.home, "link")
	if err := os.Symlink(dir, link); err != nil {
		t.Fatal(err)
	}
	homeSpace, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Name: "  named  "})
	if err != nil || homeSpace.Directory != f.home || homeSpace.Name != "named" || homeSpace.IsDefault {
		t.Fatalf("CreateSpace(no directory) = %+v, %v", homeSpace, err)
	}
	linked, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Directory: link})
	if err != nil || linked.Directory != dir || linked.Name != "work dir" {
		t.Fatalf("CreateSpace(link) = %+v, %v", linked, err)
	}
	if _, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Directory: dir}); err != nil {
		t.Errorf("CreateSpace(duplicate directory) = %v, want allowed", err)
	}
	if _, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Directory: filepath.Join(f.home, "nope")}); !errors.Is(err, ErrSpaceDirectoryInvalid) {
		t.Errorf("CreateSpace(missing) = %v, want ErrSpaceDirectoryInvalid", err)
	}
	if list := f.service.ListSpaces(); len(list) != 4 || list[0].ID != def.ID {
		t.Errorf("ListSpaces = %+v, want Default first then three", list)
	}

	// Update: rename, move; existing terminals keep their directory, a
	// later terminal takes the new one; null and empty are refused.
	before, err := f.service.Create(ctx, api.TerminalCreateParams{SpaceID: linked.ID})
	if err != nil {
		t.Fatal(err)
	}
	moved := canonicalTempDir(t)
	updated, err := f.service.UpdateSpace(ctx, linked.ID, api.SpaceUpdateParams{Name: api.Some("moved"), Directory: api.Some(moved)})
	if err != nil || updated.Name != "moved" || updated.Directory != moved {
		t.Fatalf("UpdateSpace = %+v, %v", updated, err)
	}
	after, err := f.service.Create(ctx, api.TerminalCreateParams{SpaceID: linked.ID})
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := f.service.Get(before.ID); got.Directory != dir || after.Directory != moved {
		t.Errorf("directories after move: existing %q, later %q; want %q, %q", got.Directory, after.Directory, dir, moved)
	}
	if same, err := f.service.UpdateSpace(ctx, linked.ID, api.SpaceUpdateParams{}); err != nil || same.Name != "moved" {
		t.Errorf("empty patch = %+v, %v", same, err)
	}
	for name, params := range map[string]api.SpaceUpdateParams{
		"null name":         {Name: api.Clear[string]()},
		"empty name":        {Name: api.Some(" ")},
		"null directory":    {Directory: api.Clear[string]()},
		"missing directory": {Directory: api.Some(filepath.Join(f.home, "nope"))},
	} {
		if _, err := f.service.UpdateSpace(ctx, linked.ID, params); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if _, err := f.service.UpdateSpace(ctx, "spce-zzzzz", api.SpaceUpdateParams{Name: api.Some("x")}); !errors.Is(err, ErrSpaceNotFound) {
		t.Errorf("update unknown = %v, want ErrSpaceNotFound", err)
	}

	var events []string
	for len(sub.C) > 0 {
		change := <-sub.C
		if strings.HasPrefix(change.Type, "space.") {
			events = append(events, change.Type+" "+change.ID)
		}
	}
	want := []string{"space.created " + homeSpace.ID, "space.created " + linked.ID, "space.created " + f.service.ListSpaces()[3].ID, "space.updated " + linked.ID}
	if diff := cmp.Diff(want, events); diff != "" {
		t.Errorf("space events (-want +got):\n%s", diff)
	}
}

// A move changes the space and nothing else: the session, directory, and
// App intent stay; a move into an unknown or deleting space is refused;
// null is refused.
func TestTerminalMoveBetweenSpaces(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	other, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Directory: canonicalTempDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	launched, err := f.service.CreateForApp(ctx, api.TerminalCreateParams{Name: "tui"}, AppLaunch{
		AppID:   "alpha/tui",
		Compose: func(string, string) (string, error) { return "alpha", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	moved, err := f.service.Update(ctx, launched.ID, api.TerminalUpdateParams{SpaceID: api.Some(other.ID)})
	if err != nil {
		t.Fatal(err)
	}
	want := launched
	want.SpaceID, want.UpdatedAt = other.ID, moved.UpdatedAt
	if diff := cmp.Diff(want, moved); diff != "" {
		t.Errorf("moved (-want +got):\n%s", diff)
	}
	if f.driver.createdCommand(launched.ID) != "alpha" || len(f.driver.killedNames()) != 0 {
		t.Error("a move touched the session")
	}
	if _, err := f.service.Update(ctx, launched.ID, api.TerminalUpdateParams{SpaceID: api.Some("spce-zzzzz")}); !errors.Is(err, ErrSpaceNotFound) {
		t.Errorf("move to unknown = %v, want ErrSpaceNotFound", err)
	}
	if _, err := f.service.Update(ctx, launched.ID, api.TerminalUpdateParams{SpaceID: api.Clear[string]()}); !errors.Is(err, ErrInvalidUpdate) {
		t.Errorf("null space = %v, want ErrInvalidUpdate", err)
	}
	// A move names its space exactly; "" is unknown, not Default.
	if _, err := f.service.Update(ctx, launched.ID, api.TerminalUpdateParams{SpaceID: api.Some("")}); !errors.Is(err, ErrSpaceNotFound) {
		t.Errorf("move to \"\" = %v, want ErrSpaceNotFound", err)
	}
}

// Space deletion runs every terminal in the space through the supplied
// deletion workflow — running, unreachable, exited, and missing alike —
// then removes the space; a create or move into the space meanwhile is
// refused, as is a move out of it; every terminal is attempted even when
// one fails, and a failure leaves the space marked so a retry finishes
// the job.
func TestDeleteSpace(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	space, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Directory: canonicalTempDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, name := range []string{"running", "unreachable", "exited", "missing"} {
		terminal, err := f.service.Create(ctx, api.TerminalCreateParams{SpaceID: space.ID, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, terminal.ID)
	}
	f.driver.set(ids[1], false)
	f.driver.remove(ids[2])
	plantReport(t, f.reports, ids[2], 0, true, "")
	f.driver.remove(ids[3])
	f.service.Reconcile(ctx)
	elsewhere, err := f.service.Create(ctx, api.TerminalCreateParams{Name: "elsewhere"})
	if err != nil {
		t.Fatal(err)
	}

	// The workflow sees every terminal, in id order, and can observe the
	// space refusing members in and out while it runs.
	var deleted []string
	var refusedCreate, refusedMove, refusedLeave error
	workflow := func(ctx context.Context, id string) error {
		deleted = append(deleted, id)
		if len(deleted) == 1 {
			_, refusedCreate = f.service.Create(ctx, api.TerminalCreateParams{SpaceID: space.ID})
			_, refusedMove = f.service.Update(ctx, elsewhere.ID, api.TerminalUpdateParams{SpaceID: api.Some(space.ID)})
			_, refusedLeave = f.service.Update(ctx, ids[3], api.TerminalUpdateParams{SpaceID: api.Some(elsewhere.SpaceID)})
		}
		return f.service.Delete(ctx, id)
	}
	if err := f.service.DeleteSpace(ctx, space.ID, workflow); err != nil {
		t.Fatalf("DeleteSpace = %v", err)
	}
	sorted := slices.Clone(ids)
	slices.Sort(sorted)
	if diff := cmp.Diff(sorted, deleted); diff != "" {
		t.Errorf("deleted (-want +got):\n%s", diff)
	}
	if !errors.Is(refusedCreate, ErrSpaceDeleting) || !errors.Is(refusedMove, ErrSpaceDeleting) || !errors.Is(refusedLeave, ErrSpaceDeleting) {
		t.Errorf("during deletion: create = %v, move in = %v, move out = %v; want ErrSpaceDeleting", refusedCreate, refusedMove, refusedLeave)
	}
	if _, err := f.service.GetSpace(space.ID); !errors.Is(err, ErrSpaceNotFound) {
		t.Errorf("space after delete = %v, want gone", err)
	}
	if list := f.service.List(""); len(list) != 1 || list[0].ID != elsewhere.ID {
		t.Errorf("terminals after delete = %+v, want only the other space's", list)
	}
	if err := f.service.DeleteSpace(ctx, space.ID, workflow); !errors.Is(err, ErrSpaceNotFound) {
		t.Errorf("second delete = %v, want ErrSpaceNotFound", err)
	}

	// A failing terminal delete does not stop the others: every terminal
	// is attempted, the failures are reported together, the space stays
	// (marked), and the retry deletes what remains.
	again, err := f.service.CreateSpace(ctx, api.SpaceCreateParams{Directory: canonicalTempDir(t)})
	if err != nil {
		t.Fatal(err)
	}
	for range 3 {
		if _, err := f.service.Create(ctx, api.TerminalCreateParams{SpaceID: again.ID}); err != nil {
			t.Fatal(err)
		}
	}
	calls := 0
	failing := func(ctx context.Context, id string) error {
		calls++
		if calls <= 2 {
			return fmt.Errorf("persistence down for %s", id)
		}
		return f.service.Delete(ctx, id)
	}
	err = f.service.DeleteSpace(ctx, again.ID, failing)
	if err == nil || strings.Count(err.Error(), "persistence down") != 2 || calls != 3 {
		t.Fatalf("DeleteSpace with failing deletes = %v after %d calls; want both failures reported and all three attempted", err, calls)
	}
	if list := f.service.List(again.ID); len(list) != 2 {
		t.Errorf("terminals after the partial failure = %+v, want the two that failed", list)
	}
	if _, err := f.service.Create(ctx, api.TerminalCreateParams{SpaceID: again.ID}); !errors.Is(err, ErrSpaceDeleting) {
		t.Errorf("create after a failed deletion = %v, want ErrSpaceDeleting (the mark holds)", err)
	}
	if err := f.service.DeleteSpace(ctx, again.ID, failing); err != nil {
		t.Fatalf("retry = %v", err)
	}
	if list := f.service.List(again.ID); len(list) != 0 {
		t.Errorf("terminals after retry = %+v", list)
	}
}
