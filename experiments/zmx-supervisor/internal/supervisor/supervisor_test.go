package supervisor

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/agentstatus"
	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/terminal"
)

type fakeTerminal struct {
	sessions map[string]terminal.Session
	listErr  error
	killed   []string
	lastSend []byte
	history  []byte
}

func newFakeTerminal() *fakeTerminal {
	return &fakeTerminal{sessions: make(map[string]terminal.Session)}
}

func (f *fakeTerminal) Create(_ context.Context, options terminal.CreateOptions) error {
	if _, exists := f.sessions[options.Name]; exists {
		return errors.New("already exists")
	}
	f.sessions[options.Name] = terminal.Session{Name: options.Name, Reachable: true, PID: len(f.sessions) + 100}
	return nil
}

func (f *fakeTerminal) List(context.Context) ([]terminal.Session, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := make([]terminal.Session, 0, len(f.sessions))
	for _, session := range f.sessions {
		result = append(result, session)
	}
	return result, nil
}

func (f *fakeTerminal) Send(_ context.Context, _ string, input []byte) error {
	f.lastSend = append([]byte(nil), input...)
	return nil
}

func (f *fakeTerminal) History(context.Context, string) ([]byte, error) {
	return append([]byte(nil), f.history...), nil
}

func TestAgentStatusUsesScreenThenProcessEvidence(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	adapter := newFakeTerminal()
	adapter.history = []byte("Codex\n›")
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := newTestSupervisor(t, adapter, store, &clock)
	running, err := host.Create(ctx, CreateRequest{Name: "agent", Kind: "codex", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if running.AgentStatus == nil || running.AgentStatus.State != agentstatus.StateIdle ||
		running.AgentStatus.Evidence.Source != agentstatus.SourceScreen {
		t.Fatalf("running agent status = %#v", running.AgentStatus)
	}

	delete(adapter.sessions, running.ZmxName)
	exitCode := 4
	exitedAt := clock.Add(time.Second)
	if err := WriteExitMarker(store.ExitPath(running.ID), ExitMarker{
		SessionID: running.ID, StartedAt: clock, ExitedAt: &exitedAt, ExitCode: &exitCode,
	}); err != nil {
		t.Fatal(err)
	}
	failed := snapshot(t, host, "agent")
	if failed.AgentStatus == nil || failed.AgentStatus.State != agentstatus.StateFailed ||
		failed.AgentStatus.Evidence.Source != agentstatus.SourceProcess {
		t.Fatalf("failed agent status = %#v", failed.AgentStatus)
	}
}

func TestAgentStatusReportsUnavailableWithIncompleteInventory(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	adapter := newFakeTerminal()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := newTestSupervisor(t, adapter, store, &clock)
	if _, err := host.Create(ctx, CreateRequest{Name: "agent", Kind: "claude", CWD: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	adapter.listErr = errors.New("zmx unavailable")
	snapshots, err := host.Reconcile(ctx)
	if err == nil {
		t.Fatal("reconcile succeeded with an incomplete inventory")
	}
	if len(snapshots) != 1 || snapshots[0].AgentStatus == nil ||
		snapshots[0].AgentStatus.State != agentstatus.StateUnavailable {
		t.Fatalf("disconnected snapshots = %#v", snapshots)
	}
}

func (f *fakeTerminal) Attach(context.Context, string, *os.File, *os.File, io.Writer) error {
	return nil
}

func (f *fakeTerminal) Kill(_ context.Context, name string) error {
	delete(f.sessions, name)
	f.killed = append(f.killed, name)
	return nil
}

func TestRestartRecoveryAndEvidenceBasedStates(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	adapter := newFakeTerminal()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := newTestSupervisor(t, adapter, store, &clock)
	running, err := host.Create(ctx, CreateRequest{Name: "shell", Kind: "shell", CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if running.State != StateRunning || running.DaemonPID == 0 {
		t.Fatalf("created snapshot = %#v", running)
	}

	// A fresh Go host adopts the same daemon PID from persisted metadata.
	restarted := newTestSupervisor(t, adapter, store, &clock)
	recovered := snapshot(t, restarted, "shell")
	if recovered.State != StateRunning || recovered.DaemonPID != running.DaemonPID || recovered.ID != running.ID {
		t.Fatalf("recovered snapshot = %#v, created = %#v", recovered, running)
	}

	entry := adapter.sessions[running.ZmxName]
	entry.Reachable = false
	adapter.sessions[running.ZmxName] = entry
	disconnected := snapshot(t, restarted, "shell")
	if disconnected.State != StateDisconnected {
		t.Fatalf("unreachable snapshot = %#v", disconnected)
	}
	entry.Reachable = true
	adapter.sessions[running.ZmxName] = entry

	delete(adapter.sessions, running.ZmxName)
	missing := snapshot(t, restarted, "shell")
	if missing.State != StateMissing {
		t.Fatalf("first absence = %#v", missing)
	}
	clock = clock.Add(31 * time.Second)
	stale := snapshot(t, restarted, "shell")
	if stale.State != StateStale {
		t.Fatalf("prolonged absence = %#v", stale)
	}
}

func TestExitMarkerDistinguishesExitedFromMissing(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	adapter := newFakeTerminal()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := newTestSupervisor(t, adapter, store, &clock)
	running, err := host.Create(ctx, CreateRequest{
		Name: "job", Kind: "process", Command: []string{"sleep", "10"}, CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	delete(adapter.sessions, running.ZmxName)
	exitCode := 7
	exitedAt := clock.Add(time.Second)
	if err := WriteExitMarker(store.ExitPath(running.ID), ExitMarker{
		SessionID: running.ID, PID: 456, StartedAt: clock, ExitedAt: &exitedAt, ExitCode: &exitCode,
	}); err != nil {
		t.Fatal(err)
	}
	exited := snapshot(t, host, "job")
	if exited.State != StateExited || exited.Exit == nil || exited.Exit.ExitCode == nil || *exited.Exit.ExitCode != 7 {
		t.Fatalf("exit snapshot = %#v", exited)
	}
	result, err := host.Cleanup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.Forgotten, []string{"job"}) {
		t.Fatalf("cleanup result = %#v", result)
	}
	if _, err := os.Stat(store.ExitPath(running.ID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("exit marker still exists: %v", err)
	}
}

func TestCleanupKillsOnlyReachableOrphansAndRefusesIncompleteInventory(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	adapter := newFakeTerminal()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := newTestSupervisor(t, adapter, store, &clock)
	adapter.sessions["orphan-live"] = terminal.Session{Name: "orphan-live", Reachable: true, PID: 99}
	adapter.sessions["orphan-down"] = terminal.Session{Name: "orphan-down", Reachable: false}
	result, err := host.Cleanup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(result.KilledOrphans, []string{"orphan-live"}) {
		t.Fatalf("cleanup result = %#v", result)
	}
	if _, exists := adapter.sessions["orphan-down"]; !exists {
		t.Fatal("cleanup killed a temporarily unreachable orphan")
	}

	adapter.listErr = errors.New("temporary zmx failure")
	if _, err := host.Cleanup(ctx); err == nil {
		t.Fatal("cleanup succeeded without a complete inventory")
	}
	if len(adapter.killed) != 1 {
		t.Fatalf("unexpected kills after unavailable inventory: %#v", adapter.killed)
	}
}

func TestStopRecordsIntentBeforeKilling(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	adapter := newFakeTerminal()
	store, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	host := newTestSupervisor(t, adapter, store, &clock)
	running, err := host.Create(ctx, CreateRequest{
		Name: "job", Kind: "claude", CWD: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := host.Stop(ctx, "job"); err != nil {
		t.Fatal(err)
	}
	stopped := snapshot(t, host, "job")
	if stopped.State != StateExited || stopped.Reason != "terminated deliberately" {
		t.Fatalf("stopped snapshot = %#v", stopped)
	}
	if !slices.Contains(adapter.killed, running.ZmxName) {
		t.Fatalf("kill calls = %#v", adapter.killed)
	}
	loaded, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != 1 || loaded[0].StopRequestedAt == nil {
		t.Fatalf("persisted records = %#v", loaded)
	}
	exitCode := 129
	exitedAt := clock.Add(time.Second)
	if err := WriteExitMarker(store.ExitPath(running.ID), ExitMarker{
		SessionID: running.ID, StartedAt: clock, ExitedAt: &exitedAt, ExitCode: &exitCode,
	}); err != nil {
		t.Fatal(err)
	}
	stopped = snapshot(t, host, "job")
	if stopped.Reason != "terminated deliberately" || stopped.AgentStatus == nil ||
		stopped.AgentStatus.State != agentstatus.StateCompleted {
		t.Fatalf("stopped snapshot with marker = %#v", stopped)
	}
}

func newTestSupervisor(t *testing.T, adapter terminal.Terminal, store *Store, clock *time.Time) *Supervisor {
	t.Helper()
	host, err := New(Config{
		Terminal: adapter, Store: store, Executable: filepath.Join("/", "fake", "atc-zmx"),
		StaleAfter: 30 * time.Second, Now: func() time.Time { return *clock },
	})
	if err != nil {
		t.Fatal(err)
	}
	return host
}

func snapshot(t *testing.T, host *Supervisor, name string) Snapshot {
	t.Helper()
	snapshots, err := host.Reconcile(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	result, err := snapshotByName(snapshots, name)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
