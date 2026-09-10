package zmx

// Real-supervisor regression tests (ATC-319): real systemd user manager,
// real zmx, isolated ATC state. They prove the contract at the boundary
// that caused the incident — a systemd service's stop kills its control
// group — rather than any unit's inventory reads. The dedicated CI job
// sets ATC_SUPERVISOR_TESTS=require so a missing prerequisite fails the
// job instead of skipping the test.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/placement"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/terminals"
)

func init() { serveMode = serveTerminal }

// requireSupervisor returns a real driver on a host with a reachable
// systemd user manager, or skips (fails under require).
func requireSupervisor(t *testing.T) (*Driver, *placement.Host) {
	t.Helper()
	host := placement.Detect()
	if err := host.Preflight(context.Background()); !host.Scoped() || err != nil {
		reason := "no systemd on this host"
		if err != nil {
			reason = err.Error()
		}
		unavailable(t, "supervisor", reason)
	}
	driver := newRealDriver(t)
	if !driver.placement.Scoped() {
		t.Fatal("driver detected no placement on a scoped host")
	}
	t.Setenv("SHELL", "/bin/sh")
	return driver, host
}

// listPIDs reads the daemon pid of every session from `zmx list`.
func listPIDs(t *testing.T, driver *Driver) map[string]int {
	t.Helper()
	executable, err := driver.zmx()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "list")
	cmd.Env = Env(driver.socketDir, true)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("zmx list: %v", err)
	}
	pids := map[string]int{}
	for line := range strings.SplitSeq(string(out), "\n") {
		var name string
		pid := 0
		for field := range strings.SplitSeq(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "→")), "\t") {
			key, value, _ := strings.Cut(field, "=")
			switch key {
			case "name":
				name = value
			case "pid":
				pid, _ = strconv.Atoi(value)
			}
		}
		if name != "" && pid != 0 {
			pids[name] = pid
		}
	}
	return pids
}

func cgroupOf(t *testing.T, pid int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "cgroup"))
	if err != nil {
		t.Fatalf("cgroup of %d: %v", pid, err)
	}
	return strings.TrimSpace(string(data))
}

func alive(pid int) bool {
	if err := syscall.Kill(pid, 0); err != nil {
		return false
	}
	state, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
	if err != nil {
		return false
	}
	fields := strings.Fields(string(state[strings.LastIndexByte(string(state), ')')+1:]))
	return len(fields) > 0 && fields[0] != "Z"
}

func waitFor(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func reachable(driver *Driver, id string) func() bool {
	return func() bool {
		sessions, err := driver.Inventory(context.Background())
		if err != nil {
			return false
		}
		_, ok := lookupSession(sessions, id)
		return ok
	}
}

func systemctl(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("systemctl --user %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// history is the session's scrollback.
func history(t *testing.T, driver *Driver, id string) string {
	t.Helper()
	executable, err := driver.zmx()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "history", id)
	cmd.Env = Env(driver.socketDir, true)
	out, _ := cmd.Output()
	return string(out)
}

// send types raw input into the session's PTY.
func send(t *testing.T, driver *Driver, id, text string) {
	t.Helper()
	executable, err := driver.zmx()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "send", id, text)
	cmd.Env = Env(driver.socketDir, true)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("zmx send: %v\n%s", err, out)
	}
}

// The incident, reproduced and refuted: a systemd service (KillMode=
// control-group, exactly like atc.server.service) creates a terminal
// through the real driver; stopping the service leaves the session
// running under its own daemon pid, in its own scope, still taking input
// and producing output. Deleting it afterwards ends the scope.
func TestRealSupervisorSessionSurvivesServiceStop(t *testing.T) {
	driver, host := requireSupervisor(t)
	ctx := context.Background()
	const id = "term-svcaa"
	unit := "atc-test-" + strconv.FormatInt(time.Now().UnixNano(), 36) + ".service"
	t.Cleanup(func() {
		_ = driver.Kill(context.Background(), id)
		_ = exec.Command("systemctl", "--user", "stop", unit).Run()
	})

	launch := exec.Command("systemd-run", "--user", "--unit="+unit, "--quiet", "--collect", "--no-ask-password",
		"--property=KillMode=control-group", "--setenv=PATH="+os.Getenv("PATH"), "--setenv=SHELL=/bin/sh",
		"--", testBinary(t), "__serve", "--socket", driver.socketDir, "--report", driver.reportDir, "--id", id,
		"--selection", driver.runtime.selectionFile)
	if out, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("starting the service: %v\n%s", err, out)
	}
	waitFor(t, "the service's terminal", 15*time.Second, reachable(driver, id))
	pid := listPIDs(t, driver)[id]
	if pid == 0 {
		t.Fatal("zmx list reports no daemon pid")
	}
	cgroup := cgroupOf(t, pid)
	if !strings.Contains(cgroup, "/"+driver.unit(id)) || strings.Contains(cgroup, unit) {
		t.Fatalf("daemon %d runs in %q, want the terminal's own scope and not the service", pid, cgroup)
	}
	// The stop must hit a live service whose own process is in its
	// cgroup, or the test proves nothing about the boundary.
	if state := systemctl(t, "show", "--property=ActiveState", "--value", unit); state != "active" {
		t.Fatalf("service is %s before stop, want active", state)
	}
	helper, _ := strconv.Atoi(systemctl(t, "show", "--property=MainPID", "--value", unit))
	if helper == 0 || !strings.Contains(cgroupOf(t, helper), "/"+unit) {
		t.Fatalf("service main pid %d is not in the service's cgroup", helper)
	}

	systemctl(t, "stop", unit)
	if alive(helper) {
		t.Fatal("the service's own process survived its stop")
	}
	if state := systemctl(t, "show", "--property=ActiveState", "--value", unit); state != "inactive" {
		t.Fatalf("service is %s after stop", state)
	}
	if !alive(pid) {
		t.Fatal("the daemon died with the service")
	}
	sessions, err := driver.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := lookupSession(sessions, id); !ok {
		t.Fatalf("session unreachable after the service stopped: %v", sessions)
	}
	if got := listPIDs(t, driver)[id]; got != pid {
		t.Fatalf("daemon pid changed from %d to %d: the session was replaced, not preserved", pid, got)
	}

	// Still interactive: input typed after the stop produces output.
	marker := "survived-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	send(t, driver, id, "printf 'out:%s\\n' "+marker+"\n")
	waitFor(t, "output after the service stopped", 10*time.Second, func() bool {
		return strings.Contains(history(t, driver, id), "out:"+marker)
	})

	if err := driver.Kill(ctx, id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if active, err := host.Active(ctx, driver.unit(id)); err != nil || active {
		t.Errorf("scope after Kill active=%v err=%v; want inactive", active, err)
	}
	if alive(pid) {
		t.Error("daemon survived Kill")
	}
}

// Deleting a terminal ends every process its launch owns — including a
// background process that left the session's process group with setsid,
// which `zmx kill` alone leaves running.
func TestRealSupervisorKillEndsBackgroundProcesses(t *testing.T) {
	driver, host := requireSupervisor(t)
	ctx := context.Background()
	const id = "term-bgxaa"
	t.Cleanup(func() { _ = driver.Kill(context.Background(), id) })
	pidFile := filepath.Join(t.TempDir(), "pid")
	command := "setsid sh -c 'echo $$ > " + pidFile + "; exec sleep 300' & sleep 300"
	if err := driver.Create(ctx, id, terminals.CreateSpec{Directory: "/", Command: command}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	var background int
	waitFor(t, "the background process", 10*time.Second, func() bool {
		data, err := os.ReadFile(pidFile)
		if err != nil {
			return false
		}
		background, _ = strconv.Atoi(strings.TrimSpace(string(data)))
		return background != 0
	})
	if !alive(background) {
		t.Fatal("background process not running")
	}
	if !strings.Contains(cgroupOf(t, background), "/"+driver.unit(id)) {
		t.Fatalf("background process runs in %q, want the terminal's scope", cgroupOf(t, background))
	}

	if err := driver.Kill(ctx, id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if alive(background) {
		t.Error("background process survived the terminal's deletion")
	}
	if active, err := host.Active(ctx, driver.unit(id)); err != nil || active {
		t.Errorf("scope after Kill active=%v err=%v; want inactive", active, err)
	}
	if sessions, err := driver.Inventory(ctx); err != nil || len(sessions) != 0 {
		t.Errorf("inventory after Kill = %v, %v; want empty", sessions, err)
	}
}

// A scope that outlived its session (the socket gone, processes not) is
// found by Leftovers — never by the inventory — and Kill ends it. A live
// session's scope is not a leftover.
func TestRealSupervisorLeftoverScopeIsReaped(t *testing.T) {
	driver, host := requireSupervisor(t)
	ctx := context.Background()
	const orphan, live = "term-leftx", "term-livex"
	t.Cleanup(func() {
		_ = driver.Kill(context.Background(), orphan)
		_ = driver.Kill(context.Background(), live)
	})
	argv := host.Wrap(driver.unit(orphan), "ATC terminal "+orphan, []string{"sleep", "300"})
	cmd := exec.Command(argv[0], argv[1:]...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	go func() { _ = cmd.Wait() }()
	if err := driver.Create(ctx, live, terminals.CreateSpec{Directory: "/", Command: "sleep 300"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	waitFor(t, "the orphan scope", 5*time.Second, func() bool {
		active, _ := host.Active(ctx, driver.unit(orphan))
		return active
	})

	inventory, err := driver.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	leftovers, err := driver.Leftovers(ctx, inventory)
	if err != nil {
		t.Fatal(err)
	}
	if len(leftovers) != 1 || leftovers[0] != orphan {
		t.Fatalf("Leftovers = %v, want [%s]", leftovers, orphan)
	}
	if err := driver.Kill(ctx, orphan); err != nil {
		t.Fatalf("Kill(orphan): %v", err)
	}
	if alive(cmd.Process.Pid) {
		t.Error("orphan scope's process survived Kill")
	}
	if leftovers, err := driver.Leftovers(ctx, inventory); err != nil || len(leftovers) != 0 {
		t.Errorf("Leftovers after Kill = %v, %v; want none", leftovers, err)
	}
	if ok := reachable(driver, live)(); !ok {
		t.Error("reaping the orphan disturbed the live session")
	}
}

// A user manager that exists but cannot be reached refuses the launch
// before anything runs: the error is ErrNotLaunched with the manager's
// diagnostic, and no session appears in the server's own cgroup.
func TestRealSupervisorUnreachableManagerIsNotLaunched(t *testing.T) {
	driver, _ := requireSupervisor(t)
	t.Setenv("XDG_RUNTIME_DIR", "/nonexistent/runtime")
	t.Setenv("DBUS_SESSION_BUS_ADDRESS", "unix:path=/nonexistent/bus")
	const id = "term-noman"
	t.Cleanup(func() { _ = driver.Kill(context.Background(), id) })
	err := driver.Create(context.Background(), id, terminals.CreateSpec{Directory: "/", Command: "sleep 300"})
	if !errors.Is(err, terminals.ErrNotLaunched) || !strings.Contains(err.Error(), "user manager unreachable") {
		t.Fatalf("Create = %v, want ErrNotLaunched naming the unreachable manager", err)
	}
	if sessions, err := driver.Inventory(context.Background()); err != nil || len(sessions) != 0 {
		t.Errorf("inventory after refused launch = %v, %v; want empty", sessions, err)
	}
}

// Across a server restart — a fresh terminals service over the same
// durable state and the same driver — the terminal is reconciled under
// its original identity with its original daemon, and reads running.
func TestRealSupervisorRestartReconcilesIdentity(t *testing.T) {
	driver, host := requireSupervisor(t)
	ctx := context.Background()
	db, err := store.Open(ctx, filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	home, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	newService := func() *terminals.Service {
		service := terminals.NewService(terminals.Options{
			Repository: db.Terminals(), Driver: driver, Spaces: db.Spaces(), HomeDir: home,
			ReportDir: driver.reportDir, Hub: events.NewHub(64),
			Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		})
		if err := service.Load(ctx); err != nil {
			t.Fatal(err)
		}
		service.Reconcile(ctx)
		return service
	}

	first := newService()
	terminal, err := first.Create(ctx, api.TerminalCreateParams{Command: "sleep 300"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	t.Cleanup(func() { _ = driver.Kill(context.Background(), terminal.ID) })
	if terminal.Status != api.TerminalRunning {
		t.Fatalf("created terminal is %s, want running", terminal.Status)
	}
	pid := listPIDs(t, driver)[terminal.ID]

	// The restart: the first service is simply gone; nothing stopped the
	// session because nothing owns it but its scope.
	second := newService()
	got, err := second.Get(terminal.ID)
	if err != nil {
		t.Fatalf("Get after restart: %v", err)
	}
	if got.Status != api.TerminalRunning || got.ID != terminal.ID {
		t.Fatalf("after restart: %+v, want %s running", got, terminal.ID)
	}
	if now := listPIDs(t, driver)[terminal.ID]; now != pid {
		t.Fatalf("daemon pid %d became %d across the restart", pid, now)
	}
	if err := second.Delete(ctx, terminal.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if active, err := host.Active(ctx, driver.unit(terminal.ID)); err != nil || active {
		t.Errorf("scope after Delete active=%v err=%v; want inactive", active, err)
	}
	if listed := second.List(""); len(listed) != 0 {
		t.Errorf("List after Delete = %+v, want none", listed)
	}
}

// serveTerminal is the body of `<test-binary> __serve`: the workload of
// the transient service in TestRealSupervisorSessionSurvivesServiceStop.
// It creates one terminal through the real driver, as the ATC server
// would, then holds the service alive until its stop signal.
func serveTerminal(args []string) int {
	var socketDir, reportDir, id, selectionFile string
	for i := 0; i+1 < len(args); i += 2 {
		switch args[i] {
		case "--socket":
			socketDir = args[i+1]
		case "--report":
			reportDir = args[i+1]
		case "--id":
			id = args[i+1]
		case "--selection":
			selectionFile = args[i+1]
		}
	}
	self, err := os.Executable()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	// The subprocess resolves the runtime the parent already activated;
	// it installs nothing of its own.
	runtime := NewRuntime(RuntimeOptions{SelectionFile: selectionFile, Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	driver, err := New(Options{SocketDir: socketDir, ReportDir: reportDir, MonitorExecutable: self, Runtime: runtime,
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if err := driver.Create(context.Background(), id, terminals.CreateSpec{Directory: "/"}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Println("created", id)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM)
	<-stop
	return 0
}
