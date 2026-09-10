package zmx

import (
	"context"
	"flag"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/terminals/monitor"
	"github.com/jeremytondo/atc/internal/terminals/report"
)

// TestMain doubles as the monitor executable for the real-zmx integration
// tests: re-exec'd as `<test-binary> __child --report … --id … --dir …
// [--command …]`, it runs the real monitor exactly the way cmd/atc does.
// serveMode is the body of `<test-binary> __serve`, the workload of the
// real-supervisor service test; set by the Linux-only test file.
var serveMode func(args []string) int

func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__serve" && serveMode != nil {
		os.Exit(serveMode(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "__child" {
		flags := flag.NewFlagSet("__child", flag.ExitOnError)
		rep := flags.String("report", "", "")
		id := flags.String("id", "", "")
		dir := flags.String("dir", "", "")
		command := flags.String("command", "", "")
		_ = flags.Parse(os.Args[2:])
		os.Exit(monitor.Run(monitor.Options{
			ReportPath: *rep, TerminalID: *id, Directory: *dir, Command: *command,
		}))
	}
	os.Exit(m.Run())
}

func TestParseList(t *testing.T) {
	for name, tc := range map[string]struct {
		output string
		want   []terminals.Session
	}{
		"empty": {"", nil},
		"healthy rows": {
			"name=term-x7k2f\tpid=123\tclients=0\tcreated=1756\n" +
				"name=term-abcde\tpid=456\tclients=1\tcreated=1756\tstart_dir=/home\tcmd=hx .\n",
			[]terminals.Session{{Name: "term-x7k2f", Reachable: true}, {Name: "term-abcde", Reachable: true}},
		},
		"unreachable row has err and no pid": {
			"name=term-x7k2f\terr=Timeout\tstatus=unreachable\n",
			[]terminals.Session{{Name: "term-x7k2f", Reachable: false}},
		},
		"current-session arrow prefix": {
			"→ name=term-x7k2f\tpid=1\tclients=1\tcreated=1\n  name=term-abcde\tpid=2\tclients=0\tcreated=1\n",
			[]terminals.Session{{Name: "term-x7k2f", Reachable: true}, {Name: "term-abcde", Reachable: true}},
		},
		"garbage lines skipped": {
			"no sessions found in /tmp/x\n\nname=term-x7k2f\tpid=1\tclients=0\tcreated=1\n",
			[]terminals.Session{{Name: "term-x7k2f", Reachable: true}},
		},
	} {
		if diff := cmp.Diff(tc.want, parseList(tc.output)); diff != "" {
			t.Errorf("%s (-want +got):\n%s", name, diff)
		}
	}
}

// The environment contract: ZMX_DIR forced, the two traps scrubbed, TERM
// pinned for session spawning but kept for real-TTY attach.
func TestEnvContract(t *testing.T) {
	t.Setenv("ZMX_DIR", "/somewhere/else")
	t.Setenv("ZMX_SESSION", "operator-session")
	t.Setenv("ZMX_SESSION_PREFIX", "d.")
	t.Setenv("TERM", "screen-256color")

	toMap := func(env []string) map[string]string {
		m := map[string]string{}
		for _, entry := range env {
			name, value, _ := strings.Cut(entry, "=")
			m[name] = value
		}
		return m
	}

	spawn := toMap(Env("/private/dir", true))
	if spawn["ZMX_DIR"] != "/private/dir" || spawn["TERM"] != sessionTerm {
		t.Errorf("spawn env = ZMX_DIR:%q TERM:%q", spawn["ZMX_DIR"], spawn["TERM"])
	}
	for _, trap := range []string{"ZMX_SESSION", "ZMX_SESSION_PREFIX"} {
		if _, present := spawn[trap]; present {
			t.Errorf("%s not scrubbed", trap)
		}
	}

	attach := toMap(Env("/private/dir", false))
	if attach["TERM"] != "screen-256color" {
		t.Errorf("attach env TERM = %q, want the user's kept", attach["TERM"])
	}
	if attach["ZMX_DIR"] != "/private/dir" {
		t.Errorf("attach env ZMX_DIR = %q", attach["ZMX_DIR"])
	}
}

// A socket directory too deep for sun_path fails boot with the remedy.
func TestNewRejectsDeepSocketDir(t *testing.T) {
	deep := filepath.Join(t.TempDir(), strings.Repeat("d", 120))
	_, err := New(Options{SocketDir: deep, ReportDir: t.TempDir(), MonitorExecutable: "/bin/true", Runtime: stubRuntime(t)})
	if err == nil || !strings.Contains(err.Error(), "move your state dir") {
		t.Errorf("New(deep dir) = %v, want the socket-path guard error", err)
	}
}

func TestNewTightensPermissiveSocketDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{SocketDir: dir, ReportDir: t.TempDir(), MonitorExecutable: "/bin/true", Runtime: stubRuntime(t)}); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("socket dir mode = %o, want 0700", mode)
	}
}

// unavailable skips a real-tooling test, or fails it when the dedicated
// CI job demands the real thing (ATC_SUPERVISOR_TESTS=require).
func unavailable(t *testing.T, what, reason string) {
	t.Helper()
	if os.Getenv("ATC_SUPERVISOR_TESTS") == "require" {
		t.Fatalf("real %s tests required but unavailable: %s", what, reason)
	}
	t.Skipf("real %s tests unavailable: %s", what, reason)
}

// stubRuntime is a Runtime that installs nothing and activates nothing:
// New's guard requires one, but the socket-path and permission tests
// never resolve an executable through it.
func stubRuntime(t *testing.T) *Runtime {
	t.Helper()
	return NewRuntime(RuntimeOptions{
		RuntimeDir:    t.TempDir(),
		SelectionFile: filepath.Join(t.TempDir(), "runtime.json"),
	})
}

// newRealRuntime installs the managed release executable (the ATC-324
// testing seam: the real installer against the real assets) into
// throwaway storage and activates it, so the driver resolves the version
// this build ships. A failed install skips, or fails under the CI job
// that requires the real tooling.
func newRealRuntime(t *testing.T) *Runtime {
	t.Helper()
	rt := NewRuntime(RuntimeOptions{
		RuntimeDir:    t.TempDir(),
		SelectionFile: filepath.Join(mkShortTempDir(t), "runtime.json"),
		Logger:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	executable, err := rt.Install(context.Background(), rt.Desired())
	if err != nil {
		unavailable(t, "zmx", "installing the managed zmx: "+err.Error())
	}
	sel, err := selectionFor(rt.Desired(), executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeSelection(rt.selectionFile, sel); err != nil {
		t.Fatal(err)
	}
	rt.load()
	return rt
}

// Integration against a real zmx in a private, throwaway socket directory
// (never the developer's real sessions — repo doctrine). /tmp keeps the
// socket-path budget; TempDir on macOS does not.
func newRealDriver(t *testing.T) *Driver {
	t.Helper()
	driver, err := New(Options{
		SocketDir:         mkShortTempDir(t),
		ReportDir:         t.TempDir(),
		MonitorExecutable: testBinary(t),
		Runtime:           newRealRuntime(t),
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return driver
}

func mkShortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "atc-zmx-*")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func testBinary(t *testing.T) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return self
}

func TestRealZmxLifecycle(t *testing.T) {
	driver := newRealDriver(t)
	ctx := context.Background()
	const id = "term-testa"
	t.Cleanup(func() { _ = driver.Kill(context.Background(), id) })

	if sessions, err := driver.Inventory(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("fresh inventory = %v, %v; want empty", sessions, err)
	}

	t.Setenv("SHELL", "/bin/sh")
	if err := driver.Create(ctx, id, terminals.CreateSpec{Directory: "/", Command: "sleep 60"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sessions, err := driver.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []terminals.Session{{Name: id, Reachable: true}}
	if diff := cmp.Diff(want, sessions); diff != "" {
		t.Fatalf("inventory after create (-want +got):\n%s", diff)
	}
	// The monitor's report appears (a beat after reachability — the
	// daemon settles before its root task finishes starting), is not yet
	// exit evidence, and comes to carry the foreground program observed
	// on the PTY zmx gave the monitor: `sh -c "sleep 60"` reads sleep
	// (ATC-317).
	startDeadline := time.Now().Add(3 * time.Second)
	for {
		rep, err := report.Read(driver.reportDir, id)
		if err != nil {
			t.Fatal(err)
		}
		if rep != nil {
			if rep.Exited() {
				t.Fatalf("rep = %+v, want un-exited while the command runs", rep)
			}
			if rep.Process == "sleep" {
				break
			}
		}
		if time.Now().After(startDeadline) {
			t.Fatalf("monitor never reported process sleep (last report %+v)", rep)
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Creating the same name again must refuse, never silently attach.
	if err := driver.Create(ctx, id, terminals.CreateSpec{Directory: "/"}); err == nil {
		t.Fatal("second Create with the same name succeeded")
	}

	if err := driver.Kill(ctx, id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if sessions, err := driver.Inventory(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("inventory after kill = %v, %v; want empty", sessions, err)
	}
	// Killing an absent session is success: the goal state holds.
	if err := driver.Kill(ctx, id); err != nil {
		t.Errorf("Kill(absent) = %v, want nil", err)
	}
	// zmx kill delivers HUP; the monitor forwards it and records the death
	// before the follow-up SIGKILL lands.
	deadline := time.Now().Add(2 * time.Second)
	for {
		rep, err := report.Read(driver.reportDir, id)
		if err != nil {
			t.Fatal(err)
		}
		if rep.Exited() {
			break
		}
		if time.Now().After(deadline) {
			t.Log("no exit evidence after kill (monitor outraced by SIGKILL); acceptable, evidence-free death is the missing state")
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}
