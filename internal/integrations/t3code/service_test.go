package t3code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
)

// noTerminals is the threads domain's terminal seam: T3 threads never
// have one.
type noTerminals struct{}

func (noTerminals) Get(string) (api.Terminal, error) {
	return api.Terminal{}, errors.New("no terminals")
}

// fixture is the Integration over the real threads and projects domains (a
// store in a temp dir, the real event hub), a fake T3 server, and a fake
// CLI — so every assertion reads what the API would serve.
type fixture struct {
	t           *testing.T
	server      *t3codetest.Server
	cli         *t3codetest.CLI
	home        string
	sessionPath string
	threads     *threads.Service
	projects    *projects.Service
	hub         *events.Hub
	sub         *events.Subscription
	service     *Service
	alive       atomic.Bool
	authRetry   time.Duration
	cancel      context.CancelFunc
	done        chan struct{}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hub := events.NewHubAt(256, 1)
	projectService := projects.NewService(projects.Options{Repository: db.Projects(), Hub: hub})
	threadService := threads.NewService(threads.Options{Repository: db.Threads(), Terminals: noTerminals{}, Projects: db.Projects(), Hub: hub})
	if err := threadService.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := t3codetest.NewServer(t)
	f := &fixture{
		t: t, server: server, cli: t3codetest.NewCLI(server),
		home: t.TempDir(), sessionPath: filepath.Join(t.TempDir(), "t3code-session.json"),
		threads: threadService, projects: projectService, hub: hub,
		authRetry: 300 * time.Millisecond,
	}
	f.alive.Store(true)
	f.sub = hub.Subscribe(0, false)
	t.Cleanup(f.sub.Close)
	return f
}

// writeRuntime plants T3's runtime state pointing at the fake server.
func (f *fixture) writeRuntime(origin string) {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(runtimeFile(f.home)), 0o700); err != nil {
		f.t.Fatal(err)
	}
	data, _ := json.Marshal(map[string]any{"version": 1, "pid": os.Getpid(), "port": 0, "origin": origin, "startedAt": "2026-09-01T00:00:00Z"})
	if err := os.WriteFile(runtimeFile(f.home), data, 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// start builds the service with shrunk cadences and runs it until the
// test ends.
func (f *fixture) start() {
	f.t.Helper()
	f.service = New(Options{
		Home: f.home, SessionPath: f.sessionPath,
		Threads: f.threads, Hub: f.hub,
		RunCLI:       f.cli.Run,
		ProcessAlive: func(int) bool { return f.alive.Load() },
	})
	f.service.pollInterval = 20 * time.Millisecond
	f.service.backoffMin = 20 * time.Millisecond
	f.service.backoffMax = 100 * time.Millisecond
	f.service.authRetry = f.authRetry
	f.service.responseRetry = 20 * time.Millisecond
	f.threads.SetLinker(ID, f.service.Links)
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		f.service.Run(ctx)
	}()
	f.t.Cleanup(func() {
		cancel()
		<-f.done
	})
}

func waitFor(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func (f *fixture) waitState(state api.IntegrationConnectionState) api.IntegrationConnection {
	f.t.Helper()
	waitFor(f.t, "integration state "+string(state), func() bool { return f.service.Connection().State == state })
	return f.service.Connection()
}

// thread resolves a T3 thread id to its ATC record, waiting for it.
func (f *fixture) thread(providerID string) api.Thread {
	f.t.Helper()
	var id string
	waitFor(f.t, "thread "+providerID, func() bool {
		var ok bool
		id, _, ok = f.threads.LookupIdentity(ID, providerID)
		return ok
	})
	thread, err := f.threads.Get(id)
	if err != nil {
		f.t.Fatal(err)
	}
	return thread
}

// waitStatus waits for a T3 thread to reach a status.
func (f *fixture) waitStatus(providerID string, status api.ThreadStatus) api.Thread {
	f.t.Helper()
	var thread api.Thread
	waitFor(f.t, providerID+" to be "+string(status), func() bool {
		thread = f.thread(providerID)
		return thread.Status == status
	})
	return thread
}

// events drains the hub as "type id" strings.
func (f *fixture) events() []string {
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

func count(items []string, want string) int {
	n := 0
	for _, item := range items {
		if item == want {
			n++
		}
	}
	return n
}

// project registers an ATC project over the projects service.
func (f *fixture) project(dir, name string) api.Project {
	f.t.Helper()
	project, err := f.projects.Create(context.Background(), api.ProjectCreateParams{Directory: dir, Name: name})
	if err != nil {
		f.t.Fatal(err)
	}
	return project
}

func (f *fixture) readSession() session {
	f.t.Helper()
	s, err := loadSession(f.sessionPath)
	if err != nil || s == nil {
		f.t.Fatalf("session file: %v, %v", s, err)
	}
	return *s
}

// The acceptance path: T3 running, never paired, one thread in a
// directory that is an ATC project. ATC pairs by itself, the thread
// appears with every mapped field, and the Integration reports connected —
// once.
func TestSnapshotMirrorsThreads(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	project := f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(7, []any{t3codetest.ProjectItem("p1", "T3 title", workspace)}, []any{
			t3codetest.ThreadItem("t1", "p1", "Fix the build", t3codetest.WithSession("running", "codex")),
		}), t3codetest.SynchronizedItem()}
	})
	f.events()
	f.start()

	connection := f.waitState(api.IntegrationConnected)
	if !strings.Contains(connection.Detail, "1 threads mirrored") || connection.Since.IsZero() {
		t.Errorf("connected = %+v", connection)
	}
	thread := f.waitStatus("t1", api.ThreadWorking)
	want := api.Thread{
		ID: thread.ID, IntegrationID: "t3code", AgentID: "codex", ProjectID: project.ID, InitialDirectory: workspace,
		Title: "Fix the build", Model: "gpt-5", Cwd: workspace, Status: api.ThreadWorking,
		LastEvidenceAt: thread.LastEvidenceAt, CreatedAt: thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
		Links: &api.ThreadLinks{Web: f.server.Origin() + "/env-1/t1", App: "t3code://threads/env-1/t1"},
	}
	if diff := cmp.Diff(want, thread); diff != "" {
		t.Errorf("thread (-want +got):\n%s", diff)
	}
	if thread.TerminalID != "" {
		t.Errorf("a T3 thread has terminal %q", thread.TerminalID)
	}

	// Pairing left one session, persisted 0600, and the exchange asked for
	// exactly the scope set (the fake refuses anything else).
	if f.cli.Count("auth pairing create") != 1 {
		t.Errorf("pairing create calls = %v", f.cli.Calls())
	}
	stored := f.readSession()
	if stored.Origin != f.server.Origin() || stored.Token != "token-1" || stored.Label != "atc" || stored.SessionID != "sess-1" {
		t.Errorf("session = %+v", stored)
	}
	if info, err := os.Stat(f.sessionPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("session file mode = %v, %v; want 0600", info, err)
	}

	got := f.events()
	if n := count(got, "integration.updated t3code"); n != 1 {
		t.Errorf("integration.updated published %d times: %v", n, got)
	}
	if n := count(got, "thread.created "+thread.ID); n != 1 {
		t.Errorf("thread.created published %d times: %v", n, got)
	}
}

// Ordered upserts drive the whole status projection; a replayed sequence
// is ignored.
func TestUpsertsDriveStatus(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
			t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex")),
		}), t3codetest.SynchronizedItem()}
	})
	f.start()
	f.waitStatus("t1", api.ThreadWorking)

	steps := []struct {
		sequence uint64
		opts     []t3codetest.ThreadOpt
		want     api.ThreadStatus
	}{
		{2, []t3codetest.ThreadOpt{t3codetest.WithSession("running", "codex"), t3codetest.Pending(true, false)}, api.ThreadWaitingForPermission},
		{3, []t3codetest.ThreadOpt{t3codetest.WithSession("running", "codex"), t3codetest.Pending(false, true)}, api.ThreadWaitingForInput},
		{4, []t3codetest.ThreadOpt{t3codetest.WithSession("error", "codex"), t3codetest.LastError("boom")}, api.ThreadError},
		{5, []t3codetest.ThreadOpt{t3codetest.WithSession("idle", "codex")}, api.ThreadIdle},
		{6, []t3codetest.ThreadOpt{t3codetest.WithSession("ready", "codex"), t3codetest.Liveness("monitoring")}, api.ThreadWorking},
		{7, []t3codetest.ThreadOpt{t3codetest.WithSession("stopped", "codex")}, api.ThreadIdle},
		{8, []t3codetest.ThreadOpt{t3codetest.WithSession("hibernating", "codex")}, api.ThreadUnknown},
		{9, []t3codetest.ThreadOpt{t3codetest.Liveness("dreaming")}, api.ThreadUnknown},
		{10, nil, api.ThreadIdle},
	}
	for _, step := range steps {
		f.server.Push(t3codetest.Upserted(step.sequence, t3codetest.ThreadItem("t1", "p1", "One", step.opts...)))
		thread := f.waitStatus("t1", step.want)
		if step.want == api.ThreadError && thread.StatusDetail != "boom" {
			t.Errorf("statusDetail = %q after the error step", thread.StatusDetail)
		}
		if step.want != api.ThreadError && thread.StatusDetail != "" {
			t.Errorf("statusDetail = %q at %s; present only with error", thread.StatusDetail, step.want)
		}
	}
	if thread := f.thread("t1"); thread.AgentID != "" {
		t.Errorf("agent = %q; want empty with no session", thread.AgentID)
	}

	// A stale sequence is ignored: the status stays where sequence 10
	// left it.
	f.server.Push(t3codetest.Upserted(4, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("error", "codex"))))
	f.server.Push(t3codetest.Upserted(11, t3codetest.ThreadItem("t1", "p1", "Renamed", t3codetest.WithSession("idle", "claudeAgent"))))
	thread := f.thread("t1")
	waitFor(t, "sequence 11", func() bool { thread = f.thread("t1"); return thread.Title == "Renamed" })
	if thread.Status != api.ThreadIdle || thread.AgentID != "claudeAgent" {
		t.Errorf("after replay = %+v; want idle from sequence 10, agent claudeAgent (as T3 names it) from 11", thread)
	}
}

// thread-removed archives; a later upsert of the same identity brings the
// same record back. Meanwhile archive and delete are refused only while
// T3 still reports the thread; a title change always works.
func TestRemovedArchivesAndVerbs(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
			t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex")),
		}), t3codetest.SynchronizedItem()}
	})
	f.start()
	thread := f.waitStatus("t1", api.ThreadWorking)
	ctx := context.Background()

	archived := true
	if _, err := f.threads.Update(ctx, thread.ID, api.ThreadUpdateParams{Archived: api.Some(archived)}); !errors.Is(err, threads.ErrActive) || !strings.Contains(err.Error(), "t3code") {
		t.Errorf("archive while reported = %v; want ErrActive naming the integration", err)
	}
	if err := f.threads.Delete(ctx, thread.ID); !errors.Is(err, threads.ErrActive) {
		t.Errorf("delete while reported = %v; want ErrActive", err)
	}
	title := "my title"
	if _, err := f.threads.Update(ctx, thread.ID, api.ThreadUpdateParams{Title: api.Some(title)}); err != nil {
		t.Errorf("title patch while reported = %v", err)
	}

	f.server.Push(t3codetest.Removed(2, "t1"))
	waitFor(t, "archive", func() bool { return f.thread("t1").Archived })
	got := f.thread("t1")
	if got.Status != api.ThreadUnknown || got.ArchivedAt == nil {
		t.Errorf("after removal = %+v; want unknown and archived", got)
	}

	// T3 reports it again (unarchived there): same record, back in the
	// default list, the user's title kept.
	f.server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "T3 renamed it", t3codetest.WithSession("idle", "codex"))))
	waitFor(t, "unarchive", func() bool { return !f.thread("t1").Archived })
	got = f.thread("t1")
	if got.ID != thread.ID || got.Title != "my title" || got.Status != api.ThreadIdle {
		t.Errorf("after re-upsert = %+v; want the same record, user title, idle", got)
	}

	// Removed again: now archive and delete are the user's to do.
	f.server.Push(t3codetest.Removed(4, "t1"))
	waitFor(t, "second archive", func() bool { return f.thread("t1").Archived })
	unarchived := false
	if _, err := f.threads.Update(ctx, thread.ID, api.ThreadUpdateParams{Archived: api.Some(unarchived)}); err != nil {
		t.Errorf("unarchive after removal = %v", err)
	}
	if err := f.threads.Delete(ctx, thread.ID); err != nil {
		t.Errorf("delete after removal = %v", err)
	}
}

// T3 settlement is ATC archival (ATC-292): a settled thread ATC knows
// archives, one it never saw is not minted, and unsettling brings the
// same record back — in the initial snapshot, live upserts, and a
// reconnect snapshot alike.
func TestSettledThreadsAreArchived(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(after *uint64) []any {
		if after == nil {
			return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
				t3codetest.ThreadItem("t-live", "p1", "Live", t3codetest.WithSession("running", "codex")),
				t3codetest.ThreadItem("t-history", "p1", "Old", t3codetest.SettledOverride("settled")),
			}), t3codetest.SynchronizedItem()}
		}
		return []any{t3codetest.SnapshotItem(9, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
			t3codetest.ThreadItem("t-live", "p1", "Live", t3codetest.WithSession("idle", "codex"), t3codetest.SettledOverride("settled")),
			t3codetest.ThreadItem("t-history", "p1", "Old", t3codetest.SettledOverride("settled")),
		}), t3codetest.SynchronizedItem()}
	})
	f.start()
	live := f.waitStatus("t-live", api.ThreadWorking)
	connection := f.waitState(api.IntegrationConnected)
	if !strings.Contains(connection.Detail, "1 threads mirrored, 1 settled") {
		t.Errorf("detail = %q", connection.Detail)
	}
	if _, _, ok := f.threads.LookupIdentity(ID, "t-history"); ok {
		t.Error("a settled thread ATC never observed was recorded")
	}

	// Settled live: archived, coerced, hold released.
	f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem("t-live", "p1", "Live", t3codetest.WithSession("idle", "codex"), t3codetest.SettledOverride("settled"))))
	waitFor(t, "archive on settle", func() bool { return f.thread("t-live").Archived })
	if got := f.thread("t-live"); got.ID != live.ID || got.Status != api.ThreadUnknown {
		t.Errorf("settled = %+v", got)
	}
	unarchived := false
	if _, err := f.threads.Update(context.Background(), live.ID, api.ThreadUpdateParams{Archived: api.Some(unarchived)}); err != nil {
		t.Errorf("unarchive while settled = %v; want allowed (T3 no longer reports it active)", err)
	}

	// Reactivated (explicitly, then by clearing the override): the same
	// record returns and holds again.
	f.server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t-live", "p1", "Live", t3codetest.WithSession("running", "codex"), t3codetest.SettledOverride("active"))))
	restored := f.waitStatus("t-live", api.ThreadWorking)
	if restored.ID != live.ID || restored.Archived {
		t.Errorf("reactivated = %+v; want %s unarchived", restored, live.ID)
	}
	archived := true
	if _, err := f.threads.Update(context.Background(), live.ID, api.ThreadUpdateParams{Archived: api.Some(archived)}); !errors.Is(err, threads.ErrActive) {
		t.Errorf("archive while active again = %v; want ErrActive", err)
	}
	f.server.Push(t3codetest.Upserted(4, t3codetest.ThreadItem("t-live", "p1", "Live", t3codetest.WithSession("idle", "codex"), t3codetest.SettledOverride("settled"))))
	waitFor(t, "second settle", func() bool { return f.thread("t-live").Archived })
	f.server.Push(t3codetest.Upserted(5, t3codetest.ThreadItem("t-live", "p1", "Live", t3codetest.WithSession("idle", "codex"), t3codetest.SettledOverride(nil))))
	waitFor(t, "unsettle by null", func() bool { return !f.thread("t-live").Archived })
	if got := f.thread("t-live"); got.ID != restored.ID || got.Status != api.ThreadIdle {
		t.Errorf("unsettled = %+v; want %s idle", got, restored.ID)
	}

	// A reconnect snapshot that reports it settled archives it again.
	f.server.DropConns()
	waitFor(t, "archive on reconnect snapshot", func() bool { return f.thread("t-live").Archived })
	if connection := f.waitState(api.IntegrationConnected); !strings.Contains(connection.Detail, "0 threads mirrored, 2 settled") {
		t.Errorf("detail after reconnect = %q", connection.Detail)
	}
}

// A dropped socket: live statuses coerce to unknown and the Integration
// reports connecting once; the reconnect buys a new ticket, resumes after
// the last applied sequence, ignores the replayed one, applies the new
// one, re-establishes every hold — and mints no duplicate.
func TestReconnectResumes(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(after *uint64) []any {
		if after == nil {
			return []any{t3codetest.SnapshotItem(5, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
				t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex")),
				t3codetest.ThreadItem("t2", "p1", "Two", t3codetest.WithSession("idle", "codex")),
			}), t3codetest.SynchronizedItem()}
		}
		// A replay: one event at the cursor (already applied), one new.
		return []any{
			t3codetest.Upserted(5, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("error", "codex"))),
			t3codetest.Upserted(6, t3codetest.ThreadItem("t2", "p1", "Two", t3codetest.WithSession("running", "codex"))),
			t3codetest.SynchronizedItem(),
		}
	})
	f.start()
	one := f.waitStatus("t1", api.ThreadWorking)
	two := f.waitStatus("t2", api.ThreadIdle)
	f.events()

	f.server.DropConns()
	f.waitState(api.IntegrationConnecting)
	f.waitStatus("t1", api.ThreadUnknown)
	if got := f.thread("t2"); got.Status != api.ThreadIdle {
		t.Errorf("idle thread after drop = %s; idle persists", got.Status)
	}

	f.waitState(api.IntegrationConnected)
	f.waitStatus("t1", api.ThreadWorking)
	f.waitStatus("t2", api.ThreadWorking)
	subscriptions := f.server.Subscriptions()
	if len(subscriptions) != 2 || subscriptions[0] != nil || subscriptions[1] == nil || *subscriptions[1] != 5 {
		t.Errorf("subscriptions = %v; want a fresh one then afterSequence 5", subscriptions)
	}
	if tickets, exchanges := f.server.Counts(); tickets != 2 || exchanges != 1 {
		t.Errorf("tickets = %d, exchanges = %d; want a ticket per connection and one pairing", tickets, exchanges)
	}
	got := f.events()
	if count(got, "integration.updated t3code") != 2 {
		t.Errorf("integration events over a drop and reconnect = %v; want connecting then connected", got)
	}
	if count(got, "thread.created "+one.ID)+count(got, "thread.created "+two.ID) != 0 {
		t.Errorf("reconnect minted a duplicate: %v", got)
	}
	if got := f.thread("t1"); got.ID != one.ID {
		t.Errorf("t1 is now %s, was %s", got.ID, one.ID)
	}
}

// During a replay the Integration is still connecting: holds are only back
// once the marker arrives, so connected is reported then.
func TestReplayConnectsAtTheMarker(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(after *uint64) []any {
		if after == nil {
			return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
				t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex")),
			}), t3codetest.SynchronizedItem()}
		}
		// The replay's events without its marker: the marker is pushed by
		// the test.
		return []any{t3codetest.Upserted(2, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex")))}
	})
	f.start()
	f.waitStatus("t1", api.ThreadWorking)
	f.server.DropConns()
	f.waitState(api.IntegrationConnecting)
	f.waitStatus("t1", api.ThreadIdle)
	if state := f.service.Connection().State; state != api.IntegrationConnecting {
		t.Errorf("state after a replayed event = %s; want connecting until synchronized", state)
	}
	f.server.Push(t3codetest.SynchronizedItem())
	f.waitState(api.IntegrationConnected)
	archived := true
	if _, err := f.threads.Update(context.Background(), f.thread("t1").ID, api.ThreadUpdateParams{Archived: api.Some(archived)}); !errors.Is(err, threads.ErrActive) {
		t.Errorf("archive after the marker = %v; want ErrActive (hold restored)", err)
	}
}

// When T3 answers a resume with a fresh snapshot instead of a replay, it
// is diff-applied: threads it lacks archive, new ones appear, the rest
// update in place.
func TestSnapshotFallbackDiffApplies(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(after *uint64) []any {
		if after == nil {
			return []any{t3codetest.SnapshotItem(5, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
				t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex")),
				t3codetest.ThreadItem("t2", "p1", "Two", t3codetest.WithSession("idle", "codex")),
			}), t3codetest.SynchronizedItem()}
		}
		return []any{t3codetest.SnapshotItem(40, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
			t3codetest.ThreadItem("t2", "p1", "Two", t3codetest.WithSession("running", "codex")),
			t3codetest.ThreadItem("t3", "p1", "Three"),
		}), t3codetest.SynchronizedItem()}
	})
	f.start()
	one := f.waitStatus("t1", api.ThreadWorking)
	f.waitStatus("t2", api.ThreadIdle)

	f.server.DropConns()
	f.waitStatus("t2", api.ThreadWorking)
	f.waitStatus("t3", api.ThreadIdle)
	waitFor(t, "t1 archived", func() bool { return f.thread("t1").Archived })
	if got := f.thread("t1"); got.ID != one.ID || got.Status != api.ThreadUnknown {
		t.Errorf("t1 after fallback = %+v", got)
	}
	// The next resume starts from the fallback's sequence.
	f.server.DropConns()
	waitFor(t, "third subscription", func() bool { return len(f.server.Subscriptions()) == 3 })
	if after := f.server.Subscriptions()[2]; after == nil || *after != 40 {
		t.Errorf("resume after fallback = %v; want 40", after)
	}
}

// Directory evidence (ATC-295): the workspace root is the thread's
// origin, which the threads domain classifies — exact match, nearest
// ancestor, none — while T3 never creates a project; a thread whose
// workspace has no local directory is skipped and counted in the detail,
// re-evaluated once the directory exists.
func TestProjectAssociation(t *testing.T) {
	f := newFixture(t)
	root := t.TempDir()
	exact := filepath.Join(root, "exact")
	parent := filepath.Join(root, "parent")
	nested := filepath.Join(parent, "child", "grand")
	elsewhere := t.TempDir()
	fresh := filepath.Join(elsewhere, "fresh")
	missing := filepath.Join(elsewhere, "missing")
	for _, dir := range []string{exact, nested, fresh} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	exactProject := f.project(exact, "exact")
	rootProject := f.project(root, "root")
	parentProject := f.project(parent, "parent")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{
			t3codetest.ProjectItem("p-exact", "Exact", exact),
			t3codetest.ProjectItem("p-nested", "Nested", nested),
			t3codetest.ProjectItem("p-fresh", "Fresh Workspace", fresh),
			t3codetest.ProjectItem("p-missing", "Missing", missing),
		}, []any{
			t3codetest.ThreadItem("t-exact", "p-exact", "A"),
			t3codetest.ThreadItem("t-nested", "p-nested", "B", t3codetest.Worktree(filepath.Join(nested, "wt"))),
			t3codetest.ThreadItem("t-fresh", "p-fresh", "C"),
			t3codetest.ThreadItem("t-missing", "p-missing", "D"),
		}), t3codetest.SynchronizedItem()}
	})
	f.start()
	connection := f.waitState(api.IntegrationConnected)

	if got := f.thread("t-exact"); got.ProjectID != exactProject.ID || got.InitialDirectory != exact || got.Cwd != exact {
		t.Errorf("exact = %+v; want project %s", got, exactProject.ID)
	}
	if got := f.thread("t-nested"); got.ProjectID != parentProject.ID || got.InitialDirectory != nested || got.Cwd != filepath.Join(nested, "wt") {
		t.Errorf("nested = %+v; want the nearest ancestor %s (not %s), the workspace origin, and the worktree cwd", got, parentProject.ID, rootProject.ID)
	}
	if got := f.thread("t-fresh"); got.ProjectID != "" || got.InitialDirectory != fresh {
		t.Errorf("fresh = %+v; want recorded unassigned with its origin", got)
	}
	list, err := f.projects.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 3 {
		t.Errorf("projects = %d; want only the three planted — T3 creates none", len(list))
	}
	if _, _, ok := f.threads.LookupIdentity(ID, "t-missing"); ok {
		t.Error("a thread with no local directory was recorded")
	}
	if !strings.Contains(connection.Detail, "3 threads mirrored; 1 skipped (t-missing: workspace "+missing) {
		t.Errorf("detail = %q", connection.Detail)
	}

	// The directory appears and T3 reports the thread again: it joins.
	if err := os.MkdirAll(missing, 0o755); err != nil {
		t.Fatal(err)
	}
	f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem("t-missing", "p-missing", "D")))
	if got := f.thread("t-missing"); got.Cwd != missing {
		t.Errorf("re-evaluated = %+v", got)
	}
	waitFor(t, "skip cleared", func() bool { return strings.HasSuffix(f.service.Connection().Detail, "4 threads mirrored") })
}

// A stored session T3 no longer honors: one re-pairing that revokes the
// old session first, then connected. A second rejection is an auth
// failure, not a loop.
func TestRejectedSessionRepairsOnce(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	if err := saveSession(f.sessionPath, &session{Origin: f.server.Origin(), Token: "stale", Label: "atc", SessionID: "sess-stale", Scope: scope}); err != nil {
		t.Fatal(err)
	}
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{t3codetest.ThreadItem("t1", "p1", "One")}), t3codetest.SynchronizedItem()}
	})
	f.start()
	f.waitState(api.IntegrationConnected)
	f.thread("t1")

	if f.cli.Count("auth session revoke sess-stale") != 1 || f.cli.Count("auth pairing create") != 1 {
		t.Errorf("CLI calls = %v; want one revoke of the stale session and one pairing", f.cli.Calls())
	}
	if stored := f.readSession(); stored.Token != "token-1" || stored.SessionID != "sess-1" {
		t.Errorf("session after re-pair = %+v", stored)
	}
	if tickets, _ := f.server.Counts(); tickets != 2 {
		t.Errorf("ticket calls = %d; want the rejected one and the good one", tickets)
	}
}

// Auth failures report auth_failed with the reason and are not retried in
// a tight loop: a scope refusal, a missing CLI (the real runner over an
// empty T3 home), and a missing node.
func TestAuthFailures(t *testing.T) {
	t.Run("scope denied", func(t *testing.T) {
		f := newFixture(t)
		f.writeRuntime(f.server.Origin())
		f.server.SetScopeDenied(true)
		f.start()
		connection := f.waitState(api.IntegrationAuthFailed)
		if !strings.Contains(connection.Detail, "refused the scope") {
			t.Errorf("detail = %q", connection.Detail)
		}
		time.Sleep(150 * time.Millisecond)
		if _, exchanges := f.server.Counts(); exchanges != 1 {
			t.Errorf("exchanges = %d; want one pairing, then a wait", exchanges)
		}
		// After the wait, pairing is tried again.
		waitFor(t, "second pairing", func() bool { _, exchanges := f.server.Counts(); return exchanges == 2 })
	})
	t.Run("subscription refused", func(t *testing.T) {
		// T3 checks the subscription's scope inside the stream, not at the
		// ticket: the refusal there retires the session the same way.
		f := newFixture(t)
		f.writeRuntime(f.server.Origin())
		f.server.SetStreamDenied(true)
		f.start()
		connection := f.waitState(api.IntegrationAuthFailed)
		if !strings.Contains(connection.Detail, "refused the scope") || !strings.Contains(connection.Detail, "subscription refused") {
			t.Errorf("detail = %q", connection.Detail)
		}
		if f.cli.Count("auth session revoke sess-1") != 1 {
			t.Errorf("CLI calls = %v; want the refused session revoked", f.cli.Calls())
		}
		waitFor(t, "second pairing", func() bool { _, exchanges := f.server.Counts(); return exchanges == 2 })
	})
	t.Run("exchange unavailable", func(t *testing.T) {
		// A T3 that cannot answer the exchange is a reconnect, not a
		// pairing failure.
		f := newFixture(t)
		f.writeRuntime(f.server.Origin())
		f.server.SetExchangeStatus(http.StatusInternalServerError)
		f.start()
		waitFor(t, "a reported exchange failure", func() bool {
			connection := f.service.Connection()
			return connection.State == api.IntegrationConnecting && strings.Contains(connection.Detail, "pairing exchange")
		})
		waitFor(t, "retries", func() bool { _, exchanges := f.server.Counts(); return exchanges >= 3 })
		f.server.SetExchangeStatus(0)
		f.waitState(api.IntegrationConnected)
	})
	t.Run("session not persisted", func(t *testing.T) {
		// A session that cannot be stored would be paired over at every
		// restart: it is retired and the failure reported.
		f := newFixture(t)
		f.writeRuntime(f.server.Origin())
		f.sessionPath = filepath.Join(f.home, "userdata", "server-runtime.json", "session.json")
		f.start()
		if connection := f.waitState(api.IntegrationAuthFailed); !strings.Contains(connection.Detail, "persisting the T3 Code session") {
			t.Errorf("detail = %q", connection.Detail)
		}
		if f.cli.Count("auth session revoke sess-1") != 1 {
			t.Errorf("CLI calls = %v; want the unpersisted session revoked", f.cli.Calls())
		}
	})
	t.Run("wider scope granted", func(t *testing.T) {
		f := newFixture(t)
		f.writeRuntime(f.server.Origin())
		f.server.SetGrantScope("orchestration:read orchestration:write")
		f.start()
		if connection := f.waitState(api.IntegrationAuthFailed); !strings.Contains(connection.Detail, "granted scope") {
			t.Errorf("detail = %q", connection.Detail)
		}
	})
	t.Run("cli missing", func(t *testing.T) {
		f := newFixture(t)
		f.writeRuntime(f.server.Origin())
		_, fail := runNodeCLI(f.home)(context.Background(), "auth", "pairing", "create")
		f.cli.SetFail(fail)
		var auth *authError
		if !errors.As(fail, &auth) || !strings.Contains(fail.Error(), "T3 Code CLI not found") {
			t.Fatalf("real runner over an empty home = %v; want an auth error naming the CLI", fail)
		}
		f.start()
		if connection := f.waitState(api.IntegrationAuthFailed); !strings.Contains(connection.Detail, "T3 Code CLI not found") {
			t.Errorf("detail = %q", connection.Detail)
		}
	})
	t.Run("node missing", func(t *testing.T) {
		f := newFixture(t)
		f.writeRuntime(f.server.Origin())
		// A T3 install without node on PATH.
		if err := os.MkdirAll(filepath.Dir(serviceStateFile(f.home)), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(serviceStateFile(f.home), []byte(`{"protocol":2,"activeVersion":"1.0.0"}`), 0o600); err != nil {
			t.Fatal(err)
		}
		bin := filepath.Join(f.home, "runtime", "versions", "1.0.0", "node_modules", "t3", "dist", "bin.mjs")
		if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(bin, []byte("// t3"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", t.TempDir())
		_, fail := runNodeCLI(f.home)(context.Background(), "auth", "pairing", "create")
		f.cli.SetFail(fail)
		if fail == nil || !strings.Contains(fail.Error(), "node not found") {
			t.Fatalf("real runner without node = %v", fail)
		}
		f.start()
		if connection := f.waitState(api.IntegrationAuthFailed); !strings.Contains(connection.Detail, "node not found") {
			t.Errorf("detail = %q", connection.Detail)
		}
	})
}

// An auth failure waits before pairing again — unless T3 restarts, which
// is a new run worth trying at once, even when the file changes between
// polls.
func TestAuthRetryGateClearsOnRestart(t *testing.T) {
	f := newFixture(t)
	f.writeRuntime(f.server.Origin())
	f.server.SetScopeDenied(true)
	f.authRetry = time.Hour
	f.start()
	f.waitState(api.IntegrationAuthFailed)
	f.server.SetScopeDenied(false)
	// Same origin, new pid: a restarted T3.
	data, _ := json.Marshal(map[string]any{"pid": os.Getpid() + 1, "origin": f.server.Origin()})
	if err := os.WriteFile(runtimeFile(f.home), data, 0o644); err != nil {
		t.Fatal(err)
	}
	f.waitState(api.IntegrationConnected)
}

// No runtime file is the quiet unavailable state, polled slowly; the
// file appearing connects, its process dying disconnects.
func TestNotRunning(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"))}), t3codetest.SynchronizedItem()}
	})
	f.start()
	connection := f.waitState(api.IntegrationUnavailable)
	if !strings.Contains(connection.Detail, "not running") || !strings.Contains(connection.Detail, runtimeFile(f.home)) {
		t.Errorf("detail = %q", connection.Detail)
	}
	time.Sleep(60 * time.Millisecond)
	if got := f.events(); count(got, "integration.updated t3code") != 0 {
		t.Errorf("polling for a runtime file published %v", got)
	}
	if _, exchanges := f.server.Counts(); exchanges != 0 {
		t.Error("pairing was attempted with no T3 running")
	}

	f.writeRuntime(f.server.Origin())
	f.waitState(api.IntegrationConnected)
	f.waitStatus("t1", api.ThreadWorking)

	// The recorded process dies: unavailable, statuses coerced.
	f.alive.Store(false)
	f.server.DropConns()
	connection = f.waitState(api.IntegrationUnavailable)
	if !strings.Contains(connection.Detail, "is gone") {
		t.Errorf("detail after death = %q", connection.Detail)
	}
	f.waitStatus("t1", api.ThreadUnknown)
}

// A payload ATC cannot read is reported as unavailable with the reason
// and applied not at all; the next connection starts from a fresh
// snapshot.
func TestSchemaFailure(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(after *uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"))}), t3codetest.SynchronizedItem()}
	})
	f.start()
	f.waitStatus("t1", api.ThreadIdle)

	// An upsert missing a required field: reported, not applied.
	broken := t3codetest.ThreadItem("t1", "p1", "Broken", t3codetest.WithSession("running", "codex"))
	delete(broken, "hasPendingApprovals")
	f.server.Push(t3codetest.Upserted(2, broken))
	connection := f.waitState(api.IntegrationUnavailable)
	if !strings.Contains(connection.Detail, "shell schema") || !strings.Contains(connection.Detail, "pending-action flags") {
		t.Errorf("detail = %q", connection.Detail)
	}
	if got := f.thread("t1"); got.Title != "One" || got.Status != api.ThreadIdle {
		t.Errorf("broken payload applied: %+v", got)
	}
	// Recovery takes a fresh snapshot, not a resume.
	f.waitState(api.IntegrationConnected)
	subscriptions := f.server.Subscriptions()
	if len(subscriptions) < 2 || subscriptions[len(subscriptions)-1] != nil {
		t.Errorf("subscriptions after a schema failure = %v; want a fresh one last", subscriptions)
	}

	// A protocol defect likewise.
	f.server.Raw(`{"_tag":"Defect","requestId":"SUB","defect":"boom"}`)
	if connection := f.waitState(api.IntegrationUnavailable); !strings.Contains(connection.Detail, "protocol") {
		t.Errorf("detail = %q", connection.Detail)
	}
	f.waitState(api.IntegrationConnected)
}

// T3 forgetting a project touches nothing in ATC.
func TestProjectRemovedLeavesATCProjects(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "Workspace", workspace)}, []any{t3codetest.ThreadItem("t1", "p1", "One")}), t3codetest.SynchronizedItem()}
	})
	f.start()
	thread := f.thread("t1")
	f.server.Push(t3codetest.Removed(2, "t1"), t3codetest.ProjectRemoved(3, "p1"))
	waitFor(t, "archive", func() bool { return f.thread("t1").Archived })
	list, err := f.projects.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 0 || thread.ProjectID != "" || thread.InitialDirectory != workspace {
		t.Errorf("after T3 removed its project: projects %+v, thread %+v; want none and an unassigned thread", list, thread)
	}
	// And a project T3 announces later can carry threads.
	other := t.TempDir()
	f.server.Push(t3codetest.ProjectUpserted(4, t3codetest.ProjectItem("p2", "Other", other)), t3codetest.Upserted(5, t3codetest.ThreadItem("t2", "p2", "Two")))
	if got := f.thread("t2"); got.Cwd != other {
		t.Errorf("thread under a later project = %+v", got)
	}
}

// Links derive from the live connection; the environment id comes from
// the descriptor at connect time.
func TestLinksFollowTheEnvironment(t *testing.T) {
	f := newFixture(t)
	f.writeRuntime(f.server.Origin())
	f.server.SetEnvironmentID("env-xyz")
	f.start()
	if links := f.service.Links("t1"); links != nil {
		t.Errorf("links before connecting = %+v", links)
	}
	f.waitState(api.IntegrationConnected)
	want := &api.ThreadLinks{Web: f.server.Origin() + "/env-xyz/t1", App: "t3code://threads/env-xyz/t1"}
	if diff := cmp.Diff(want, f.service.Links("t1")); diff != "" {
		t.Errorf("links (-want +got):\n%s", diff)
	}
}

func TestProjectStatusTable(t *testing.T) {
	item := func(opts ...t3codetest.ThreadOpt) threadShell {
		data, _ := json.Marshal(t3codetest.ThreadItem("t", "p", "T", opts...))
		var thread threadShell
		if err := json.Unmarshal(data, &thread); err != nil {
			t.Fatal(err)
		}
		return thread
	}
	cases := []struct {
		name string
		opts []t3codetest.ThreadOpt
		want api.ThreadStatus
	}{
		{"input outranks approval and running", []t3codetest.ThreadOpt{t3codetest.WithSession("running", "codex"), t3codetest.Pending(true, true)}, api.ThreadWaitingForInput},
		{"approval outranks running", []t3codetest.ThreadOpt{t3codetest.WithSession("running", "codex"), t3codetest.Pending(true, false)}, api.ThreadWaitingForPermission},
		{"input outranks running", []t3codetest.ThreadOpt{t3codetest.WithSession("running", "codex"), t3codetest.Pending(false, true)}, api.ThreadWaitingForInput},
		{"starting", []t3codetest.ThreadOpt{t3codetest.WithSession("starting", "codex")}, api.ThreadWorking},
		{"error outranks liveness", []t3codetest.ThreadOpt{t3codetest.WithSession("error", "codex"), t3codetest.Liveness("working")}, api.ThreadError},
		{"interrupted with liveness", []t3codetest.ThreadOpt{t3codetest.WithSession("interrupted", "codex"), t3codetest.Liveness("working")}, api.ThreadWorking},
		{"no session with liveness", []t3codetest.ThreadOpt{t3codetest.Liveness("monitoring")}, api.ThreadWorking},
		{"no session, null liveness", []t3codetest.ThreadOpt{t3codetest.Liveness(nil)}, api.ThreadIdle},
		{"no session, no liveness", nil, api.ThreadIdle},
		{"unknown session status", []t3codetest.ThreadOpt{t3codetest.WithSession("paused", "codex")}, api.ThreadUnknown},
		{"unknown liveness", []t3codetest.ThreadOpt{t3codetest.WithSession("idle", "codex"), t3codetest.Liveness("napping")}, api.ThreadUnknown},
	}
	for _, c := range cases {
		if got := projectStatus(item(c.opts...)); got != c.want {
			t.Errorf("%s: got %s, want %s", c.name, got, c.want)
		}
	}
}

func TestDiscover(t *testing.T) {
	home := t.TempDir()
	if _, err := discover(home, func(int) bool { return true }); !errors.Is(err, errNotRunning) {
		t.Errorf("no runtime file = %v; want errNotRunning", err)
	}
	if err := os.MkdirAll(filepath.Dir(runtimeFile(home)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile(home), []byte(`{"pid":42,"origin":"http://127.0.0.1:1"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(home, func(int) bool { return false }); !errors.Is(err, errNotRunning) {
		t.Errorf("dead pid = %v; want errNotRunning", err)
	}
	state, err := discover(home, func(pid int) bool { return pid == 42 })
	if err != nil || state.Origin != "http://127.0.0.1:1" {
		t.Errorf("live = %+v, %v", state, err)
	}
	if err := os.WriteFile(runtimeFile(home), []byte(`{"pid":0}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := discover(home, func(int) bool { return true }); err == nil || errors.Is(err, errNotRunning) {
		t.Errorf("unreadable = %v; want a distinct error", err)
	}
}

func TestIntegrationRegistration(t *testing.T) {
	f := newFixture(t)
	f.service = New(Options{Home: f.home, SessionPath: f.sessionPath, Threads: f.threads, Hub: f.hub, RunCLI: f.cli.Run})
	integration := Integration(f.service)
	if integration.ID != ID || integration.Name != "T3 Code" || integration.Connection == nil || integration.Executable != nil {
		t.Errorf("registration = %+v", integration)
	}
	var agentIDs []string
	for _, agent := range integration.Agents {
		agentIDs = append(agentIDs, agent.ID)
	}
	if diff := cmp.Diff([]string{"claudeAgent", "codex", "cursor", "grok", "opencode"}, agentIDs); diff != "" {
		t.Errorf("agents (-want +got):\n%s", diff)
	}
	// Both Apps are handoffs — nothing T3 owns runs in an ATC terminal —
	// and each supports every agent T3 drives.
	var appIDs []string
	for _, app := range integration.Apps {
		if app.Terminal != nil || !app.Handoff {
			t.Errorf("%s = %+v; want a handoff App", app.ID, app)
		}
		if diff := cmp.Diff(agentIDs, app.Agents); diff != "" {
			t.Errorf("%s agents (-want +got):\n%s", app.ID, diff)
		}
		appIDs = append(appIDs, app.ID)
	}
	if diff := cmp.Diff([]string{"web", "desktop"}, appIDs); diff != "" {
		t.Errorf("apps (-want +got):\n%s", diff)
	}
	if connection := integration.Connection(); connection.State != api.IntegrationUnavailable {
		t.Errorf("connection before running = %+v; want unavailable (no T3 home)", connection)
	}
}

// A session file that is not a session is paired over, not reported.
func TestUnreadableSessionFileIsRepaired(t *testing.T) {
	f := newFixture(t)
	f.writeRuntime(f.server.Origin())
	if err := os.WriteFile(f.sessionPath, []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.waitState(api.IntegrationConnected)
	if stored := f.readSession(); stored.Token != "token-1" {
		t.Errorf("session after pairing over garbage = %+v", stored)
	}
}

// T3's latestTurn is decoded, not projected: each state maps directly,
// error is a failed turn with the session's text, the T3 turn id
// re-matches across a dropped subscription (same turn finished keeps
// the ATC id; a different turn is a fresh one), and a running turn
// ends unknown while the subscription is down.
func TestLatestTurnMirrors(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	reconnects := 0
	f.server.SetInitial(func(after *uint64) []any {
		if after == nil {
			return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
				t3codetest.ThreadItem("t1", "p1", "One"),
			}), t3codetest.SynchronizedItem()}
		}
		reconnects++
		switch reconnects {
		case 1:
			// The turn that was running finished while ATC was away.
			return []any{t3codetest.Upserted(10, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"),
				t3codetest.LatestTurn("tu-3b", "completed", "2026-09-01T00:00:03Z", "2026-09-01T00:00:04Z"))), t3codetest.SynchronizedItem()}
		default:
			// A different turn altogether.
			return []any{t3codetest.Upserted(20, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"),
				t3codetest.LatestTurn("tu-5", "interrupted", "2026-09-01T00:00:05Z", "2026-09-01T00:00:06Z"))), t3codetest.SynchronizedItem()}
		}
	})
	f.start()
	if thread := f.waitStatus("t1", api.ThreadIdle); thread.LatestTurn != nil {
		t.Fatalf("latest turn with none reported = %+v", thread.LatestTurn)
	}
	turn := func() api.ThreadTurn {
		t.Helper()
		thread := f.thread("t1")
		if thread.LatestTurn == nil {
			t.Fatal("no latest turn")
		}
		return *thread.LatestTurn
	}
	waitTurn := func(state api.TurnState) api.ThreadTurn {
		t.Helper()
		var got api.ThreadTurn
		waitFor(t, "turn "+string(state), func() bool {
			thread := f.thread("t1")
			if thread.LatestTurn == nil {
				return false
			}
			got = *thread.LatestTurn
			return got.State == state
		})
		return got
	}

	// Running with startedAt null: requestedAt stands in.
	f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("tu-1", "running", nil, nil))))
	first := waitTurn(api.TurnRunning)
	if !first.StartedAt.Equal(time.Date(2026, 9, 1, 0, 0, 1, 0, time.UTC)) || first.CompletedAt != nil {
		t.Errorf("running turn = %+v; want started at requestedAt", first)
	}
	f.server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"), t3codetest.LatestTurn("tu-1", "completed", "2026-09-01T00:00:02Z", "2026-09-01T00:00:03Z"))))
	done := waitTurn(api.TurnCompleted)
	if done.ID != first.ID || !done.StartedAt.Equal(time.Date(2026, 9, 1, 0, 0, 2, 0, time.UTC)) || done.CompletedAt == nil || !done.CompletedAt.Equal(time.Date(2026, 9, 1, 0, 0, 3, 0, time.UTC)) {
		t.Errorf("completed turn = %+v; want %s with T3's timestamps", done, first.ID)
	}
	if f.thread("t1").Status != api.ThreadIdle {
		t.Errorf("status after completion = %s", f.thread("t1").Status)
	}

	// A T3 turn ending in error: the thread is idle, the turn failed with
	// the session's error text, and no statusDetail.
	f.server.Push(t3codetest.Upserted(4, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"), t3codetest.LastError("provider refused"), t3codetest.LatestTurn("tu-2", "error", "2026-09-01T00:00:02Z", "2026-09-01T00:00:03Z"))))
	failed := waitTurn(api.TurnFailed)
	if failed.ID == first.ID || failed.Error != "provider refused" {
		t.Errorf("failed turn = %+v; want a fresh id with the session's error", failed)
	}
	if thread := f.thread("t1"); thread.Status != api.ThreadIdle || thread.StatusDetail != "" {
		t.Errorf("thread after a failed turn = status %s detail %q", thread.Status, thread.StatusDetail)
	}
	// The session itself faulting mid-turn: error status with the text,
	// and the running turn failed with it too.
	f.server.Push(t3codetest.Upserted(5, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("error", "codex"), t3codetest.LastError("session died"), t3codetest.LatestTurn("tu-3", "running", "2026-09-01T00:00:03Z", nil))))
	f.waitStatus("t1", api.ThreadError)
	faulted := waitTurn(api.TurnFailed)
	if thread := f.thread("t1"); thread.StatusDetail != "session died" || faulted.Error != "session died" || faulted.ID == failed.ID {
		t.Errorf("faulted session = detail %q turn %+v", thread.StatusDetail, faulted)
	}
	// The session recovers on a new turn: the detail clears, and the
	// turn ATC failed stays failed — an ended turn is final.
	f.server.Push(t3codetest.Upserted(6, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("tu-3", "running", "2026-09-01T00:00:03Z", nil))))
	if thread := f.waitStatus("t1", api.ThreadWorking); thread.StatusDetail != "" || thread.LatestTurn.ID != faulted.ID || thread.LatestTurn.State != api.TurnFailed {
		t.Errorf("recovered session = detail %q turn %+v; want the failed turn kept", thread.StatusDetail, thread.LatestTurn)
	}
	f.server.Push(t3codetest.Upserted(7, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("tu-3b", "running", "2026-09-01T00:00:03Z", nil))))
	running := waitTurn(api.TurnRunning)

	// Subscription drops: the running turn ends unknown; on reconnect
	// T3 reports the same turn finished — same ATC id, reported outcome.
	f.server.DropConns()
	f.waitStatus("t1", api.ThreadUnknown)
	if got := turn(); got.ID != running.ID || got.State != api.TurnUnknown {
		t.Errorf("turn while disconnected = %+v; want %s unknown", got, running.ID)
	}
	f.waitState(api.IntegrationConnected)
	rematched := waitTurn(api.TurnCompleted)
	if rematched.ID != running.ID || rematched.CompletedAt == nil {
		t.Errorf("same turn finished after reconnect = %+v; want %s completed", rematched, running.ID)
	}

	// A different turn after the next drop: fresh id with its state.
	f.server.Push(t3codetest.Upserted(11, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("tu-4", "running", "2026-09-01T00:00:05Z", nil))))
	fourth := waitTurn(api.TurnRunning)
	f.server.DropConns()
	f.waitStatus("t1", api.ThreadUnknown)
	f.waitState(api.IntegrationConnected)
	fifth := waitTurn(api.TurnInterrupted)
	if fifth.ID == fourth.ID {
		t.Errorf("a different turn kept the id %s", fourth.ID)
	}

	// A malformed turn is a schema failure like any other.
	f.server.Push(t3codetest.Upserted(21, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"), t3codetest.LatestTurn("tu-6", "paused", nil, nil))))
	if got := waitTurn(api.TurnUnknown); got.ID == fifth.ID {
		t.Errorf("unrecognized state reused the id: %+v", got)
	}
	f.server.Push(t3codetest.Upserted(22, t3codetest.ThreadItem("t1", "p1", "One", func(m map[string]any) {
		m["latestTurn"] = map[string]any{"turnId": "tu-7", "state": "running", "requestedAt": "yesterday", "startedAt": nil, "completedAt": nil}
	})))
	if connection := f.waitState(api.IntegrationUnavailable); !strings.Contains(connection.Detail, "schema") {
		t.Errorf("malformed turn = %+v", connection)
	}
}

// The turn decode, state by state: T3's timestamps carry over, requestedAt
// stands in for a null startedAt, error is failed with the session's
// text, and anything unrecognized is unknown.
func TestTurnObservationTable(t *testing.T) {
	requested := time.Date(2026, 9, 1, 0, 0, 1, 0, time.UTC)
	started := requested.Add(time.Second)
	completed := started.Add(time.Second)
	cases := []struct {
		name string
		turn *latestTurnShell
		want *threads.TurnObservation
	}{
		{"none", nil, nil},
		{"running before pickup", &latestTurnShell{TurnID: "tu", State: "running", RequestedAt: requested},
			&threads.TurnObservation{ProviderID: "tu", State: api.TurnRunning, StartedAt: requested}},
		{"running", &latestTurnShell{TurnID: "tu", State: "running", RequestedAt: requested, StartedAt: &started},
			&threads.TurnObservation{ProviderID: "tu", State: api.TurnRunning, StartedAt: started}},
		{"completed", &latestTurnShell{TurnID: "tu", State: "completed", RequestedAt: requested, StartedAt: &started, CompletedAt: &completed},
			&threads.TurnObservation{ProviderID: "tu", State: api.TurnCompleted, StartedAt: started, CompletedAt: completed}},
		{"interrupted", &latestTurnShell{TurnID: "tu", State: "interrupted", RequestedAt: requested, CompletedAt: &completed},
			&threads.TurnObservation{ProviderID: "tu", State: api.TurnInterrupted, StartedAt: requested, CompletedAt: completed}},
		{"error", &latestTurnShell{TurnID: "tu", State: "error", RequestedAt: requested, CompletedAt: &completed},
			&threads.TurnObservation{ProviderID: "tu", State: api.TurnFailed, StartedAt: requested, CompletedAt: completed, Error: "boom"}},
		{"unrecognized", &latestTurnShell{TurnID: "tu", State: "paused", RequestedAt: requested},
			&threads.TurnObservation{ProviderID: "tu", State: api.TurnUnknown, StartedAt: requested}},
	}
	for _, c := range cases {
		if diff := cmp.Diff(c.want, turnObservation(c.turn, "boom")); diff != "" {
			t.Errorf("%s (-want +got):\n%s", c.name, diff)
		}
	}
}

// The latest turn's response (ATC-303): a turn T3 reports ended triggers
// one read of the thread detail snapshot, the message the turn names is
// the response, and thread.updated publishes once for it; a thread first
// seen already ended backfills at the snapshot; a message still
// streaming, or a turn without one named, is retried the bounded number
// of times and then left absent with the turn unchanged; a snapshot
// showing T3 already on another turn is dropped after one read; a
// reconnect reads nothing for a response already recorded.
func TestTurnResponseRecovery(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.Origin())
	ended := func(id, title, turnID string, opts ...t3codetest.ThreadOpt) map[string]any {
		opts = append([]t3codetest.ThreadOpt{t3codetest.WithSession("idle", "codex"),
			t3codetest.LatestTurn(turnID, "completed", "2026-09-01T00:00:02Z", "2026-09-01T00:00:03Z")}, opts...)
		return t3codetest.ThreadItem(id, "p1", title, opts...)
	}
	zero := ended("t0", "Zero", "tu-0", t3codetest.AssistantMessage("m0"))
	f.server.SetThreadDetail("t0", t3codetest.ThreadDetailItem(zero, t3codetest.MessageItem("u0", "user", "start", "tu-0", false), t3codetest.MessageItem("m0", "assistant", "Zero is done.", "tu-0", false)))
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
			zero, t3codetest.ThreadItem("t1", "p1", "One"),
		}), t3codetest.SynchronizedItem()}
	})
	f.start()
	waitResponse := func(providerID, want string) api.Thread {
		t.Helper()
		var thread api.Thread
		waitFor(t, providerID+" response", func() bool {
			thread = f.thread(providerID)
			return thread.LatestTurn != nil && thread.LatestTurn.Response == want
		})
		return thread
	}
	waitReads := func(providerID string, want int) {
		t.Helper()
		waitFor(t, fmt.Sprintf("%d detail reads of %s", want, providerID), func() bool { return f.server.DetailReads(providerID) >= want })
		if got := f.server.DetailReads(providerID); got != want {
			t.Errorf("detail reads of %s = %d; want %d", providerID, got, want)
		}
	}
	// Backfill: first seen already ended, recovered from one read.
	backfilled := waitResponse("t0", "Zero is done.")
	waitReads("t0", 1)
	if backfilled.LatestTurn.State != api.TurnCompleted || backfilled.Status != api.ThreadIdle {
		t.Errorf("backfilled thread = status %s turn %+v", backfilled.Status, backfilled.LatestTurn)
	}
	one := f.waitStatus("t1", api.ThreadIdle)
	f.events()

	// The normal end: running carries nothing; the completion names the
	// message, one read recovers it, and the Thread updates once for the
	// completion and once for the response.
	f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("tu-1", "running", "2026-09-01T00:00:02Z", nil))))
	if thread := f.waitStatus("t1", api.ThreadWorking); thread.LatestTurn == nil || thread.LatestTurn.Response != "" {
		t.Errorf("running turn = %+v", thread.LatestTurn)
	}
	completed := ended("t1", "One", "tu-1", t3codetest.AssistantMessage("m1"))
	f.server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(completed,
		t3codetest.MessageItem("u1", "user", "do it", "tu-1", false),
		t3codetest.MessageItem("m1", "assistant", "One is **done**.", "tu-1", false)))
	f.server.Push(t3codetest.Upserted(3, completed))
	recovered := waitResponse("t1", "One is **done**.")
	waitReads("t1", 1)
	if recovered.LatestTurn.State != api.TurnCompleted || recovered.Status != api.ThreadIdle {
		t.Errorf("recovered thread = status %s turn %+v", recovered.Status, recovered.LatestTurn)
	}
	if got := f.events(); count(got, "thread.updated "+one.ID) != 3 {
		// Running, completed, response.
		t.Errorf("events = %v; want three updates of %s", got, one.ID)
	}
	// Still streaming at every attempt: bounded reads, then absent, the
	// turn untouched.
	streaming := ended("t1", "One", "tu-2", t3codetest.AssistantMessage("m2"))
	f.server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(streaming, t3codetest.MessageItem("m2", "assistant", "partial", "tu-2", true)))
	f.server.Push(t3codetest.Upserted(5, streaming))
	waitReads("t1", 1+responseReads)
	time.Sleep(50 * time.Millisecond)
	if thread := f.thread("t1"); thread.LatestTurn == nil || thread.LatestTurn.State != api.TurnCompleted || thread.LatestTurn.Response != "" || f.server.DetailReads("t1") != 1+responseReads {
		t.Errorf("after a streaming message = %+v, %d reads", thread.LatestTurn, f.server.DetailReads("t1"))
	}
	// No message named: bounded reads, then absent.
	unnamed := ended("t1", "One", "tu-3")
	f.server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(unnamed, t3codetest.MessageItem("m3", "assistant", "unnamed", "tu-3", false)))
	f.server.Push(t3codetest.Upserted(6, unnamed))
	waitReads("t1", 1+2*responseReads)
	time.Sleep(50 * time.Millisecond)
	if thread := f.thread("t1"); thread.LatestTurn.Response != "" || f.server.DetailReads("t1") != 1+2*responseReads {
		t.Errorf("after an unnamed message = %+v, %d reads", thread.LatestTurn, f.server.DetailReads("t1"))
	}
	// T3 already on another turn when the read lands: dropped after one
	// read; that turn's own end recovers its own.
	f.server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(ended("t1", "One", "tu-5", t3codetest.AssistantMessage("m5")), t3codetest.MessageItem("m5", "assistant", "five", "tu-5", false)))
	f.server.Push(t3codetest.Upserted(7, ended("t1", "One", "tu-4", t3codetest.AssistantMessage("m4"))))
	waitReads("t1", 2+2*responseReads)
	time.Sleep(50 * time.Millisecond)
	if thread := f.thread("t1"); thread.LatestTurn.Response != "" || f.server.DetailReads("t1") != 2+2*responseReads {
		t.Errorf("after T3 moved on = %+v, %d reads", thread.LatestTurn, f.server.DetailReads("t1"))
	}
	// An interrupted turn takes a response too; the message's empty text
	// is absent. Messages are matched by id, not position.
	interrupted := t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"),
		t3codetest.LatestTurn("tu-6", "interrupted", "2026-09-01T00:00:02Z", "2026-09-01T00:00:03Z"), t3codetest.AssistantMessage("m6"))
	f.server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(interrupted,
		t3codetest.MessageItem("m6", "assistant", "Stopped early.", "tu-6", false),
		t3codetest.MessageItem("m7", "assistant", "later message", "tu-6", false)))
	f.server.Push(t3codetest.Upserted(8, interrupted))
	if thread := waitResponse("t1", "Stopped early."); thread.LatestTurn.State != api.TurnInterrupted {
		t.Errorf("interrupted turn = %+v", thread.LatestTurn)
	}

	// Attempts exhausted on a message T3 was still streaming, then the
	// connection drops: the replay re-reports the same turn, every
	// connection is a fresh chance, and the message — final by then — is
	// recovered with one more read. The response already recorded for the
	// other thread is not read again.
	slow := ended("t1", "One", "tu-7", t3codetest.AssistantMessage("m8"))
	f.server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(slow, t3codetest.MessageItem("m8", "assistant", "Slow answer.", "tu-7", true)))
	f.server.Push(t3codetest.Upserted(9, slow))
	waitReads("t1", 3+3*responseReads)
	time.Sleep(50 * time.Millisecond)
	if thread := f.thread("t1"); thread.LatestTurn.Response != "" || f.server.DetailReads("t1") != 3+3*responseReads {
		t.Errorf("after exhausting the attempts = %+v, %d reads", thread.LatestTurn, f.server.DetailReads("t1"))
	}
	f.server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(slow, t3codetest.MessageItem("m8", "assistant", "Slow answer.", "tu-7", false)))
	reads0 := f.server.DetailReads("t0")
	f.server.SetInitial(func(after *uint64) []any {
		if after == nil {
			t.Error("reconnect asked for a snapshot")
		}
		return []any{t3codetest.Upserted(10, slow), t3codetest.SynchronizedItem()}
	})
	subscriptions := len(f.server.Subscriptions())
	f.server.DropConns()
	waitFor(t, "a resubscription", func() bool { return len(f.server.Subscriptions()) > subscriptions })
	f.waitState(api.IntegrationConnected)
	waitResponse("t1", "Slow answer.")
	waitReads("t1", 4+3*responseReads)
	time.Sleep(50 * time.Millisecond)
	if f.server.DetailReads("t0") != reads0 {
		t.Errorf("reads of t0 after reconnect = %d; want %d", f.server.DetailReads("t0"), reads0)
	}
}
