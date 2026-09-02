package t3code

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
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

// fixture is the adapter over the real threads and projects domains (a
// store in a temp dir, the real event hub), a fake T3 server, and a fake
// CLI — so every assertion reads what the API would serve.
type fixture struct {
	t           *testing.T
	server      *fakeT3
	cli         *fakeCLI
	home        string
	sessionPath string
	threads     *threads.Service
	projects    *projects.Service
	hub         *events.Hub
	sub         *events.Subscription
	observer    *Observer
	alive       bool
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
	projectService := projects.NewService(projects.Options{Repository: db.Projects(), Terminals: db.Terminals(), Hub: hub})
	threadService := threads.NewService(threads.Options{Repository: db.Threads(), Terminals: noTerminals{}, Hub: hub})
	if err := threadService.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	server := newFakeT3(t)
	f := &fixture{
		t: t, server: server, cli: &fakeCLI{server: server},
		home: t.TempDir(), sessionPath: filepath.Join(t.TempDir(), "t3code-session.json"),
		threads: threadService, projects: projectService, hub: hub, alive: true,
	}
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

// start builds the observer with shrunk cadences and runs it until the
// test ends.
func (f *fixture) start() {
	f.t.Helper()
	f.observer = New(Options{
		Home: f.home, SessionPath: f.sessionPath,
		Threads: f.threads, Projects: f.projects, Hub: f.hub,
		RunCLI:       f.cli.run,
		ProcessAlive: func(int) bool { return f.alive },
	})
	f.observer.pollInterval = 20 * time.Millisecond
	f.observer.backoffMin = 20 * time.Millisecond
	f.observer.backoffMax = 100 * time.Millisecond
	f.observer.authRetry = 300 * time.Millisecond
	f.threads.SetLinker(ID, f.observer.Links)
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	go func() {
		defer close(f.done)
		f.observer.Run(ctx)
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

func (f *fixture) waitState(state api.AgentAdapterConnectionState) api.AgentAdapterConnection {
	f.t.Helper()
	waitFor(f.t, "adapter state "+string(state), func() bool { return f.observer.Connection().State == state })
	return f.observer.Connection()
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
// appears with every mapped field, and the adapter reports connected —
// once.
func TestSnapshotMirrorsThreads(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	project := f.project(workspace, "mine")
	f.writeRuntime(f.server.origin())
	f.server.setInitial(func(*uint64) []any {
		return []any{snapshotItem(7, []any{projectItem("p1", "T3 title", workspace)}, []any{
			threadItem("t1", "p1", "Fix the build", withSession("running", "codex")),
		}), synchronizedItem()}
	})
	f.events()
	f.start()

	connection := f.waitState(api.AdapterConnected)
	if !strings.Contains(connection.Detail, "1 threads mirrored") || connection.Since.IsZero() {
		t.Errorf("connected = %+v", connection)
	}
	thread := f.waitStatus("t1", api.ThreadWorking)
	want := api.Thread{
		ID: thread.ID, Adapter: "t3code", Agent: "codex", ProjectID: project.ID,
		Title: "Fix the build", Model: "gpt-5", Cwd: workspace, Status: api.ThreadWorking,
		LastEvidenceAt: thread.LastEvidenceAt, CreatedAt: thread.CreatedAt, UpdatedAt: thread.UpdatedAt,
		Links: &api.ThreadLinks{Web: f.server.origin() + "/env-1/t1", App: "t3code://threads/env-1/t1"},
	}
	if diff := cmp.Diff(want, thread); diff != "" {
		t.Errorf("thread (-want +got):\n%s", diff)
	}
	if thread.TerminalID != "" {
		t.Errorf("a T3 thread has terminal %q", thread.TerminalID)
	}

	// Pairing left one session, persisted 0600, and the exchange asked for
	// exactly the read scope (the fake refuses anything else).
	if f.cli.count("auth pairing create") != 1 {
		t.Errorf("pairing create calls = %v", f.cli.calls)
	}
	stored := f.readSession()
	if stored.Origin != f.server.origin() || stored.Token != "token-1" || stored.Label != "atc" || stored.SessionID != "sess-1" {
		t.Errorf("session = %+v", stored)
	}
	if info, err := os.Stat(f.sessionPath); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("session file mode = %v, %v; want 0600", info, err)
	}

	got := f.events()
	if n := count(got, "agent_adapter.updated t3code"); n != 1 {
		t.Errorf("agent_adapter.updated published %d times: %v", n, got)
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
	f.writeRuntime(f.server.origin())
	f.server.setInitial(func(*uint64) []any {
		return []any{snapshotItem(1, []any{projectItem("p1", "T3", workspace)}, []any{
			threadItem("t1", "p1", "One", withSession("running", "codex")),
		}), synchronizedItem()}
	})
	f.start()
	f.waitStatus("t1", api.ThreadWorking)

	steps := []struct {
		sequence uint64
		opts     []threadOpt
		want     api.ThreadStatus
	}{
		{2, []threadOpt{withSession("running", "codex"), pending(true, false)}, api.ThreadWaitingForPermission},
		{3, []threadOpt{withSession("running", "codex"), pending(false, true)}, api.ThreadWaitingForInput},
		{4, []threadOpt{withSession("error", "codex"), lastError("boom")}, api.ThreadError},
		{5, []threadOpt{withSession("idle", "codex")}, api.ThreadIdle},
		{6, []threadOpt{withSession("ready", "codex"), liveness("monitoring")}, api.ThreadWorking},
		{7, []threadOpt{withSession("stopped", "codex")}, api.ThreadIdle},
		{8, []threadOpt{withSession("hibernating", "codex")}, api.ThreadUnknown},
		{9, []threadOpt{liveness("dreaming")}, api.ThreadUnknown},
		{10, nil, api.ThreadIdle},
	}
	for _, step := range steps {
		f.server.push(upserted(step.sequence, threadItem("t1", "p1", "One", step.opts...)))
		thread := f.waitStatus("t1", step.want)
		if step.want == api.ThreadError && thread.LastError != "boom" {
			t.Errorf("lastError = %q after the error step", thread.LastError)
		}
		if step.want == api.ThreadIdle && step.sequence == 5 && thread.LastError != "" {
			t.Errorf("lastError = %q; want cleared once the session reports none", thread.LastError)
		}
	}
	if thread := f.thread("t1"); thread.Agent != "" {
		t.Errorf("agent = %q; want empty with no session", thread.Agent)
	}

	// A stale sequence is ignored: the status stays where sequence 10
	// left it.
	f.server.push(upserted(4, threadItem("t1", "p1", "One", withSession("error", "codex"))))
	f.server.push(upserted(11, threadItem("t1", "p1", "Renamed", withSession("idle", "claude"))))
	thread := f.thread("t1")
	waitFor(t, "sequence 11", func() bool { thread = f.thread("t1"); return thread.Title == "Renamed" })
	if thread.Status != api.ThreadIdle || thread.Agent != "claude" {
		t.Errorf("after replay = %+v; want idle from sequence 10, agent claude from 11", thread)
	}
}

// thread-removed archives; a later upsert of the same identity brings the
// same record back. Meanwhile archive and delete are refused only while
// T3 still reports the thread; a title change always works.
func TestRemovedArchivesAndVerbs(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.origin())
	f.server.setInitial(func(*uint64) []any {
		return []any{snapshotItem(1, []any{projectItem("p1", "T3", workspace)}, []any{
			threadItem("t1", "p1", "One", withSession("running", "codex")),
		}), synchronizedItem()}
	})
	f.start()
	thread := f.waitStatus("t1", api.ThreadWorking)
	ctx := context.Background()

	archived := true
	if _, err := f.threads.Update(ctx, thread.ID, api.ThreadUpdateParams{Archived: &archived}); !errors.Is(err, threads.ErrActive) || !strings.Contains(err.Error(), "t3code") {
		t.Errorf("archive while reported = %v; want ErrActive naming the adapter", err)
	}
	if err := f.threads.Delete(ctx, thread.ID); !errors.Is(err, threads.ErrActive) {
		t.Errorf("delete while reported = %v; want ErrActive", err)
	}
	title := "my title"
	if _, err := f.threads.Update(ctx, thread.ID, api.ThreadUpdateParams{Title: &title}); err != nil {
		t.Errorf("title patch while reported = %v", err)
	}

	f.server.push(removed(2, "t1"))
	waitFor(t, "archive", func() bool { return f.thread("t1").Archived })
	got := f.thread("t1")
	if got.Status != api.ThreadUnknown || got.ArchivedAt == nil {
		t.Errorf("after removal = %+v; want unknown and archived", got)
	}

	// T3 reports it again (unarchived there): same record, back in the
	// default list, the user's title kept.
	f.server.push(upserted(3, threadItem("t1", "p1", "T3 renamed it", withSession("idle", "codex"))))
	waitFor(t, "unarchive", func() bool { return !f.thread("t1").Archived })
	got = f.thread("t1")
	if got.ID != thread.ID || got.Title != "my title" || got.Status != api.ThreadIdle {
		t.Errorf("after re-upsert = %+v; want the same record, user title, idle", got)
	}

	// Removed again: now archive and delete are the user's to do.
	f.server.push(removed(4, "t1"))
	waitFor(t, "second archive", func() bool { return f.thread("t1").Archived })
	unarchived := false
	if _, err := f.threads.Update(ctx, thread.ID, api.ThreadUpdateParams{Archived: &unarchived}); err != nil {
		t.Errorf("unarchive after removal = %v", err)
	}
	if err := f.threads.Delete(ctx, thread.ID); err != nil {
		t.Errorf("delete after removal = %v", err)
	}
}

// A dropped socket: live statuses coerce to unknown and the adapter
// reports connecting once; the reconnect buys a new ticket, resumes after
// the last applied sequence, ignores the replayed one, applies the new
// one, re-establishes every hold — and mints no duplicate.
func TestReconnectResumes(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.origin())
	f.server.setInitial(func(after *uint64) []any {
		if after == nil {
			return []any{snapshotItem(5, []any{projectItem("p1", "T3", workspace)}, []any{
				threadItem("t1", "p1", "One", withSession("running", "codex")),
				threadItem("t2", "p1", "Two", withSession("idle", "codex")),
			}), synchronizedItem()}
		}
		// A replay: one event at the cursor (already applied), one new.
		return []any{
			upserted(5, threadItem("t1", "p1", "One", withSession("error", "codex"))),
			upserted(6, threadItem("t2", "p1", "Two", withSession("running", "codex"))),
			synchronizedItem(),
		}
	})
	f.start()
	one := f.waitStatus("t1", api.ThreadWorking)
	two := f.waitStatus("t2", api.ThreadIdle)
	f.events()

	f.server.dropConns()
	f.waitState(api.AdapterConnecting)
	f.waitStatus("t1", api.ThreadUnknown)
	if got := f.thread("t2"); got.Status != api.ThreadIdle {
		t.Errorf("idle thread after drop = %s; idle persists", got.Status)
	}

	f.waitState(api.AdapterConnected)
	f.waitStatus("t1", api.ThreadWorking)
	f.waitStatus("t2", api.ThreadWorking)
	subscriptions := f.server.subscriptions()
	if len(subscriptions) != 2 || subscriptions[0] != nil || subscriptions[1] == nil || *subscriptions[1] != 5 {
		t.Errorf("subscriptions = %v; want a fresh one then afterSequence 5", subscriptions)
	}
	if tickets, exchanges := f.server.counts(); tickets != 2 || exchanges != 1 {
		t.Errorf("tickets = %d, exchanges = %d; want a ticket per connection and one pairing", tickets, exchanges)
	}
	got := f.events()
	if count(got, "agent_adapter.updated t3code") != 2 {
		t.Errorf("adapter events over a drop and reconnect = %v; want connecting then connected", got)
	}
	if count(got, "thread.created "+one.ID)+count(got, "thread.created "+two.ID) != 0 {
		t.Errorf("reconnect minted a duplicate: %v", got)
	}
	if got := f.thread("t1"); got.ID != one.ID {
		t.Errorf("t1 is now %s, was %s", got.ID, one.ID)
	}
}

// When T3 answers a resume with a fresh snapshot instead of a replay, it
// is diff-applied: threads it lacks archive, new ones appear, the rest
// update in place.
func TestSnapshotFallbackDiffApplies(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.origin())
	f.server.setInitial(func(after *uint64) []any {
		if after == nil {
			return []any{snapshotItem(5, []any{projectItem("p1", "T3", workspace)}, []any{
				threadItem("t1", "p1", "One", withSession("running", "codex")),
				threadItem("t2", "p1", "Two", withSession("idle", "codex")),
			}), synchronizedItem()}
		}
		return []any{snapshotItem(40, []any{projectItem("p1", "T3", workspace)}, []any{
			threadItem("t2", "p1", "Two", withSession("running", "codex")),
			threadItem("t3", "p1", "Three"),
		}), synchronizedItem()}
	})
	f.start()
	one := f.waitStatus("t1", api.ThreadWorking)
	f.waitStatus("t2", api.ThreadIdle)

	f.server.dropConns()
	f.waitStatus("t2", api.ThreadWorking)
	f.waitStatus("t3", api.ThreadIdle)
	waitFor(t, "t1 archived", func() bool { return f.thread("t1").Archived })
	if got := f.thread("t1"); got.ID != one.ID || got.Status != api.ThreadUnknown {
		t.Errorf("t1 after fallback = %+v", got)
	}
	// The next resume starts from the fallback's sequence.
	f.server.dropConns()
	waitFor(t, "third subscription", func() bool { return len(f.server.subscriptions()) == 3 })
	if after := f.server.subscriptions()[2]; after == nil || *after != 40 {
		t.Errorf("resume after fallback = %v; want 40", after)
	}
}

// Project association: exact match, nearest ancestor, auto-create named
// from T3's title, and a skip counted in the detail when the workspace
// has no local directory — re-evaluated once it does.
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
	f.writeRuntime(f.server.origin())
	f.server.setInitial(func(*uint64) []any {
		return []any{snapshotItem(1, []any{
			projectItem("p-exact", "Exact", exact),
			projectItem("p-nested", "Nested", nested),
			projectItem("p-fresh", "Fresh Workspace", fresh),
			projectItem("p-missing", "Missing", missing),
		}, []any{
			threadItem("t-exact", "p-exact", "A"),
			threadItem("t-nested", "p-nested", "B", worktree(filepath.Join(nested, "wt"))),
			threadItem("t-fresh", "p-fresh", "C"),
			threadItem("t-missing", "p-missing", "D"),
		}), synchronizedItem()}
	})
	f.start()
	connection := f.waitState(api.AdapterConnected)

	if got := f.thread("t-exact"); got.ProjectID != exactProject.ID || got.Cwd != exact {
		t.Errorf("exact = %+v; want project %s", got, exactProject.ID)
	}
	if got := f.thread("t-nested"); got.ProjectID != parentProject.ID || got.Cwd != filepath.Join(nested, "wt") {
		t.Errorf("nested = %+v; want the nearest ancestor %s (not %s) and the worktree cwd", got, parentProject.ID, rootProject.ID)
	}
	created := f.thread("t-fresh")
	list, err := f.projects.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var freshProject api.Project
	for _, project := range list {
		if project.ID == created.ProjectID {
			freshProject = project
		}
	}
	if freshProject.Directory != fresh || freshProject.Name != "Fresh Workspace" {
		t.Errorf("auto-created project = %+v; want %s named from T3", freshProject, fresh)
	}
	if len(list) != 4 {
		t.Errorf("projects = %d; want the three planted plus one created", len(list))
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
	f.server.push(upserted(2, threadItem("t-missing", "p-missing", "D")))
	if got := f.thread("t-missing"); got.Cwd != missing {
		t.Errorf("re-evaluated = %+v", got)
	}
	waitFor(t, "skip cleared", func() bool { return strings.HasSuffix(f.observer.Connection().Detail, "4 threads mirrored") })
}

// A stored session T3 no longer honors: one re-pairing that revokes the
// old session first, then connected. A second rejection is an auth
// failure, not a loop.
func TestRejectedSessionRepairsOnce(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.writeRuntime(f.server.origin())
	if err := saveSession(f.sessionPath, &session{Origin: f.server.origin(), Token: "stale", Label: "atc", SessionID: "sess-stale"}); err != nil {
		t.Fatal(err)
	}
	f.server.setInitial(func(*uint64) []any {
		return []any{snapshotItem(1, []any{projectItem("p1", "T3", workspace)}, []any{threadItem("t1", "p1", "One")}), synchronizedItem()}
	})
	f.start()
	f.waitState(api.AdapterConnected)
	f.thread("t1")

	if f.cli.count("auth session revoke sess-stale") != 1 || f.cli.count("auth pairing create") != 1 {
		t.Errorf("CLI calls = %v; want one revoke of the stale session and one pairing", f.cli.calls)
	}
	if stored := f.readSession(); stored.Token != "token-1" || stored.SessionID != "sess-1" {
		t.Errorf("session after re-pair = %+v", stored)
	}
	if tickets, _ := f.server.counts(); tickets != 2 {
		t.Errorf("ticket calls = %d; want the rejected one and the good one", tickets)
	}
}

// Auth failures report auth_failed with the reason and are not retried in
// a tight loop: a scope refusal, a missing CLI (the real runner over an
// empty T3 home), and a missing node.
func TestAuthFailures(t *testing.T) {
	t.Run("scope denied", func(t *testing.T) {
		f := newFixture(t)
		f.writeRuntime(f.server.origin())
		f.server.mu.Lock()
		f.server.scopeDenied = true
		f.server.mu.Unlock()
		f.start()
		connection := f.waitState(api.AdapterAuthFailed)
		if !strings.Contains(connection.Detail, "refused the orchestration:read scope") {
			t.Errorf("detail = %q", connection.Detail)
		}
		time.Sleep(150 * time.Millisecond)
		if _, exchanges := f.server.counts(); exchanges != 1 {
			t.Errorf("exchanges = %d; want one pairing, then a wait", exchanges)
		}
		// After the wait, pairing is tried again.
		waitFor(t, "second pairing", func() bool { _, exchanges := f.server.counts(); return exchanges == 2 })
	})
	t.Run("wider scope granted", func(t *testing.T) {
		f := newFixture(t)
		f.writeRuntime(f.server.origin())
		f.server.mu.Lock()
		f.server.grantScope = "orchestration:read orchestration:write"
		f.server.mu.Unlock()
		f.start()
		if connection := f.waitState(api.AdapterAuthFailed); !strings.Contains(connection.Detail, "granted scope") {
			t.Errorf("detail = %q", connection.Detail)
		}
	})
	t.Run("cli missing", func(t *testing.T) {
		f := newFixture(t)
		f.writeRuntime(f.server.origin())
		_, f.cli.fail = runNodeCLI(f.home)(context.Background(), "auth", "pairing", "create")
		var auth *authError
		if !errors.As(f.cli.fail, &auth) || !strings.Contains(f.cli.fail.Error(), "T3 Code CLI not found") {
			t.Fatalf("real runner over an empty home = %v; want an auth error naming the CLI", f.cli.fail)
		}
		f.start()
		if connection := f.waitState(api.AdapterAuthFailed); !strings.Contains(connection.Detail, "T3 Code CLI not found") {
			t.Errorf("detail = %q", connection.Detail)
		}
	})
	t.Run("node missing", func(t *testing.T) {
		f := newFixture(t)
		f.writeRuntime(f.server.origin())
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
		_, f.cli.fail = runNodeCLI(f.home)(context.Background(), "auth", "pairing", "create")
		if f.cli.fail == nil || !strings.Contains(f.cli.fail.Error(), "node not found") {
			t.Fatalf("real runner without node = %v", f.cli.fail)
		}
		f.start()
		if connection := f.waitState(api.AdapterAuthFailed); !strings.Contains(connection.Detail, "node not found") {
			t.Errorf("detail = %q", connection.Detail)
		}
	})
}

// No runtime file is the quiet unavailable state, polled slowly; the
// file appearing connects, its process dying disconnects.
func TestNotRunning(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.server.setInitial(func(*uint64) []any {
		return []any{snapshotItem(1, []any{projectItem("p1", "T3", workspace)}, []any{threadItem("t1", "p1", "One", withSession("running", "codex"))}), synchronizedItem()}
	})
	f.start()
	connection := f.waitState(api.AdapterUnavailable)
	if !strings.Contains(connection.Detail, "not running") || !strings.Contains(connection.Detail, runtimeFile(f.home)) {
		t.Errorf("detail = %q", connection.Detail)
	}
	time.Sleep(60 * time.Millisecond)
	if got := f.events(); count(got, "agent_adapter.updated t3code") != 0 {
		t.Errorf("polling for a runtime file published %v", got)
	}
	if _, exchanges := f.server.counts(); exchanges != 0 {
		t.Error("pairing was attempted with no T3 running")
	}

	f.writeRuntime(f.server.origin())
	f.waitState(api.AdapterConnected)
	f.waitStatus("t1", api.ThreadWorking)

	// The recorded process dies: unavailable, statuses coerced.
	f.alive = false
	f.server.dropConns()
	connection = f.waitState(api.AdapterUnavailable)
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
	f.writeRuntime(f.server.origin())
	f.server.setInitial(func(after *uint64) []any {
		return []any{snapshotItem(1, []any{projectItem("p1", "T3", workspace)}, []any{threadItem("t1", "p1", "One", withSession("idle", "codex"))}), synchronizedItem()}
	})
	f.start()
	f.waitStatus("t1", api.ThreadIdle)

	// An upsert missing a required field: reported, not applied.
	broken := threadItem("t1", "p1", "Broken", withSession("running", "codex"))
	delete(broken, "hasPendingApprovals")
	f.server.push(upserted(2, broken))
	connection := f.waitState(api.AdapterUnavailable)
	if !strings.Contains(connection.Detail, "shell schema") || !strings.Contains(connection.Detail, "pending-action flags") {
		t.Errorf("detail = %q", connection.Detail)
	}
	if got := f.thread("t1"); got.Title != "One" || got.Status != api.ThreadIdle {
		t.Errorf("broken payload applied: %+v", got)
	}
	// Recovery takes a fresh snapshot, not a resume.
	f.waitState(api.AdapterConnected)
	subscriptions := f.server.subscriptions()
	if len(subscriptions) < 2 || subscriptions[len(subscriptions)-1] != nil {
		t.Errorf("subscriptions after a schema failure = %v; want a fresh one last", subscriptions)
	}

	// A protocol defect likewise.
	f.server.raw(`{"_tag":"Defect","requestId":"1","defect":"boom"}`)
	if connection := f.waitState(api.AdapterUnavailable); !strings.Contains(connection.Detail, "protocol") {
		t.Errorf("detail = %q", connection.Detail)
	}
	f.waitState(api.AdapterConnected)
}

// T3 forgetting a project touches nothing in ATC.
func TestProjectRemovedLeavesATCProjects(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.writeRuntime(f.server.origin())
	f.server.setInitial(func(*uint64) []any {
		return []any{snapshotItem(1, []any{projectItem("p1", "Workspace", workspace)}, []any{threadItem("t1", "p1", "One")}), synchronizedItem()}
	})
	f.start()
	thread := f.thread("t1")
	f.server.push(removed(2, "t1"), projectRemoved(3, "p1"))
	waitFor(t, "archive", func() bool { return f.thread("t1").Archived })
	list, err := f.projects.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 || list[0].ID != thread.ProjectID || list[0].Name != "Workspace" {
		t.Errorf("projects after T3 removed its project = %+v", list)
	}
	// And a project T3 announces later can carry threads.
	other := t.TempDir()
	f.server.push(projectUpserted(4, projectItem("p2", "Other", other)), upserted(5, threadItem("t2", "p2", "Two")))
	if got := f.thread("t2"); got.Cwd != other {
		t.Errorf("thread under a later project = %+v", got)
	}
}

// Links derive from the live connection; the environment id comes from
// the descriptor at connect time.
func TestLinksFollowTheEnvironment(t *testing.T) {
	f := newFixture(t)
	f.writeRuntime(f.server.origin())
	f.server.mu.Lock()
	f.server.environmentID = "env-xyz"
	f.server.mu.Unlock()
	f.start()
	if links := f.observer.Links("t1"); links != nil {
		t.Errorf("links before connecting = %+v", links)
	}
	f.waitState(api.AdapterConnected)
	want := &api.ThreadLinks{Web: f.server.origin() + "/env-xyz/t1", App: "t3code://threads/env-xyz/t1"}
	if diff := cmp.Diff(want, f.observer.Links("t1")); diff != "" {
		t.Errorf("links (-want +got):\n%s", diff)
	}
}

func TestProjectStatusTable(t *testing.T) {
	item := func(opts ...threadOpt) threadShell {
		data, _ := json.Marshal(threadItem("t", "p", "T", opts...))
		var thread threadShell
		if err := json.Unmarshal(data, &thread); err != nil {
			t.Fatal(err)
		}
		return thread
	}
	cases := []struct {
		name string
		opts []threadOpt
		want api.ThreadStatus
	}{
		{"approval outranks running", []threadOpt{withSession("running", "codex"), pending(true, true)}, api.ThreadWaitingForPermission},
		{"input outranks running", []threadOpt{withSession("running", "codex"), pending(false, true)}, api.ThreadWaitingForInput},
		{"starting", []threadOpt{withSession("starting", "codex")}, api.ThreadWorking},
		{"error outranks liveness", []threadOpt{withSession("error", "codex"), liveness("working")}, api.ThreadError},
		{"interrupted with liveness", []threadOpt{withSession("interrupted", "codex"), liveness("working")}, api.ThreadWorking},
		{"no session with liveness", []threadOpt{liveness("monitoring")}, api.ThreadWorking},
		{"no session, null liveness", []threadOpt{liveness(nil)}, api.ThreadIdle},
		{"no session, no liveness", nil, api.ThreadIdle},
		{"unknown session status", []threadOpt{withSession("paused", "codex")}, api.ThreadUnknown},
		{"unknown liveness", []threadOpt{withSession("idle", "codex"), liveness("napping")}, api.ThreadUnknown},
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

func TestAdapterRegistration(t *testing.T) {
	f := newFixture(t)
	f.observer = New(Options{Home: f.home, SessionPath: f.sessionPath, Threads: f.threads, Projects: f.projects, Hub: f.hub, RunCLI: f.cli.run})
	adapter := Adapter(f.observer)
	if adapter.ID != ID || adapter.Name != "T3 Code" || adapter.Connection == nil {
		t.Errorf("registration = %+v", adapter)
	}
	var agents []string
	for _, spec := range adapter.Agents {
		if spec.TUI != nil {
			t.Errorf("%s is launchable through T3 Code", spec.ID)
		}
		agents = append(agents, spec.ID)
	}
	if diff := cmp.Diff([]string{"claude", "codex", "cursor", "grok", "opencode"}, agents); diff != "" {
		t.Errorf("agents (-want +got):\n%s", diff)
	}
	if connection := adapter.Connection(); connection.State != api.AdapterUnavailable {
		t.Errorf("connection before running = %+v; want unavailable (no T3 home)", connection)
	}
}
