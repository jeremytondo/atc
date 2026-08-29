package codex

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// fakeProcess scripts the supervision seams: which pids are alive, which
// carry the app-server argv token, whether the socket answers, and what
// spawning yields.
type fakeProcess struct {
	mu         sync.Mutex
	answering  bool
	alive      map[int]bool
	ours       map[int]bool
	spawnPID   int
	spawnErr   error
	spawned    int
	terminated []int
	// onSpawn runs before the spawn returns, e.g. to make the socket
	// answer.
	onSpawn func()
}

func (f *fakeProcess) probe(context.Context, string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.answering
}

func (f *fakeProcess) processAlive(pid int) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.alive[pid]
}

func (f *fakeProcess) hasArgvToken(pid int, _ string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ours[pid]
}

func (f *fakeProcess) spawn(context.Context) (int, error) {
	f.mu.Lock()
	onSpawn := f.onSpawn
	f.mu.Unlock()
	if onSpawn != nil {
		onSpawn()
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.spawned++
	return f.spawnPID, f.spawnErr
}

func (f *fakeProcess) terminate(pid int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminated = append(f.terminated, pid)
	delete(f.alive, pid)
	return nil
}

func (f *fakeProcess) set(fn func(f *fakeProcess)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	fn(f)
}

func newTestSupervisor(t *testing.T, process *fakeProcess) *Supervisor {
	t.Helper()
	dir := t.TempDir()
	s := NewSupervisor(SupervisorOptions{
		CodexHome:    filepath.Join(dir, "codex-home"),
		IdentityFile: filepath.Join(dir, "codex-app-server.json"),
		LogFile:      filepath.Join(dir, "codex-app-server.log"),
		SpawnDir:     dir,
		Probe:        process.probe,
		Spawn:        process.spawn,
		ProcessAlive: process.processAlive,
		HasArgvToken: process.hasArgvToken,
		Terminate:    process.terminate,
	})
	// Waiting windows shrink so failure paths resolve in test time.
	s.readyWindow, s.adoptWindow = 50*time.Millisecond, 50*time.Millisecond
	return s
}

func writeIdentity(t *testing.T, s *Supervisor, pid int) {
	t.Helper()
	if err := s.writeIdentity(pid); err != nil {
		t.Fatal(err)
	}
}

func TestAdoptOrStartDecisionTable(t *testing.T) {
	ctx := context.Background()

	t.Run("answering server with our verified pid is re-adopted", func(t *testing.T) {
		process := &fakeProcess{answering: true, alive: map[int]bool{42: true}, ours: map[int]bool{42: true}}
		s := newTestSupervisor(t, process)
		writeIdentity(t, s, 42)
		socket, err := s.Ensure(ctx)
		if err != nil || socket != s.socketPath() {
			t.Fatalf("Ensure = %q, %v", socket, err)
		}
		if s.OwnedPID() != 42 || process.spawned != 0 {
			t.Errorf("pid = %d, spawned = %d; want re-adoption", s.OwnedPID(), process.spawned)
		}
	})

	t.Run("answering server with a recycled pid is adopted as foreign", func(t *testing.T) {
		// The persisted pid is alive but its command line no longer carries
		// the token: never trusted, never signaled.
		process := &fakeProcess{answering: true, alive: map[int]bool{42: true}, ours: map[int]bool{}}
		s := newTestSupervisor(t, process)
		writeIdentity(t, s, 42)
		if _, err := s.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		if s.OwnedPID() != 0 {
			t.Errorf("recycled pid trusted: %d", s.OwnedPID())
		}
		if len(process.terminated) != 0 {
			t.Errorf("recycled pid signaled: %v", process.terminated)
		}
		if _, ok := s.readIdentity(); ok {
			t.Error("stale identity survived adoption")
		}
	})

	t.Run("silent socket with our dead pid spawns fresh", func(t *testing.T) {
		process := &fakeProcess{alive: map[int]bool{}, ours: map[int]bool{}, spawnPID: 77}
		process.onSpawn = func() {
			process.set(func(f *fakeProcess) { f.answering = true; f.alive[77] = true })
		}
		s := newTestSupervisor(t, process)
		writeIdentity(t, s, 42)
		if _, err := s.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		if s.OwnedPID() != 77 || process.spawned != 1 {
			t.Errorf("pid = %d, spawned = %d; want a fresh start", s.OwnedPID(), process.spawned)
		}
		if pid, ok := s.readIdentity(); !ok || pid != 77 {
			t.Errorf("identity = %d, %v; want the new pid persisted", pid, ok)
		}
	})

	t.Run("losing the start race adopts the winner", func(t *testing.T) {
		// Our spawn's pid exits ("address already in use") while the socket
		// answers — another client's server won.
		process := &fakeProcess{alive: map[int]bool{}, ours: map[int]bool{}, spawnPID: 77}
		process.onSpawn = func() {
			process.set(func(f *fakeProcess) { f.answering = true })
		}
		s := newTestSupervisor(t, process)
		if _, err := s.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		if s.OwnedPID() != 0 {
			t.Errorf("raced pid claimed as ours: %d", s.OwnedPID())
		}
		if _, ok := s.readIdentity(); ok {
			t.Error("raced identity survived")
		}
	})

	t.Run("a spawn that never answers is reaped", func(t *testing.T) {
		process := &fakeProcess{alive: map[int]bool{77: true}, ours: map[int]bool{77: true}, spawnPID: 77}
		s := newTestSupervisor(t, process)
		if _, err := s.Ensure(ctx); err == nil {
			t.Fatal("Ensure succeeded with nothing answering")
		}
		process.mu.Lock()
		terminated := append([]int(nil), process.terminated...)
		process.mu.Unlock()
		if len(terminated) != 1 || terminated[0] != 77 {
			t.Errorf("terminated = %v; want the failed spawn reaped", terminated)
		}
	})

	t.Run("adopt-only never spawns", func(t *testing.T) {
		process := &fakeProcess{alive: map[int]bool{}, ours: map[int]bool{}, spawnPID: 77}
		s := newTestSupervisor(t, process)
		if _, ok := s.Adopt(ctx); ok {
			t.Error("Adopt claimed success with nothing answering")
		}
		if process.spawned != 0 {
			t.Errorf("Adopt spawned %d servers", process.spawned)
		}
		// With something answering, adopt-only connects.
		process.set(func(f *fakeProcess) { f.answering = true })
		if _, ok := s.Adopt(ctx); !ok {
			t.Error("Adopt missed an answering server")
		}
	})

	t.Run("corrupt identity is never trusted", func(t *testing.T) {
		process := &fakeProcess{answering: true, alive: map[int]bool{}, ours: map[int]bool{}}
		s := newTestSupervisor(t, process)
		if err := os.WriteFile(s.opts.IdentityFile, []byte("{broken"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := s.Ensure(ctx); err != nil {
			t.Fatal(err)
		}
		if s.OwnedPID() != 0 {
			t.Errorf("corrupt identity yielded pid %d", s.OwnedPID())
		}
	})
}

func TestEnsureFastPathAndSpawnFailure(t *testing.T) {
	ctx := context.Background()
	process := &fakeProcess{answering: true, alive: map[int]bool{}, ours: map[int]bool{}}
	s := newTestSupervisor(t, process)
	if _, err := s.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	// The fast path re-probes but never re-acquires.
	if _, err := s.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	if process.spawned != 0 {
		t.Errorf("fast path spawned %d servers", process.spawned)
	}

	// The server goes away and spawning fails: the error surfaces.
	process.set(func(f *fakeProcess) { f.answering = false; f.spawnErr = errors.New("no codex") })
	if _, err := s.Ensure(ctx); err == nil {
		t.Fatal("Ensure succeeded with a failing spawn")
	}
}
