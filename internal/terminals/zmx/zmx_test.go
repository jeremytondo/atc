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
	"github.com/jeremytondo/atc/internal/terminals/exitmarker"
	"github.com/jeremytondo/atc/internal/terminals/wrapper"
)

// TestMain doubles as the wrapper executable for the real-zmx integration
// tests: re-exec'd as `<test-binary> __child --marker … --id … --dir …
// [--command …]`, it runs the real wrapper exactly the way cmd/atc does.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "__child" {
		flags := flag.NewFlagSet("__child", flag.ExitOnError)
		marker := flags.String("marker", "", "")
		id := flags.String("id", "", "")
		dir := flags.String("dir", "", "")
		command := flags.String("command", "", "")
		_ = flags.Parse(os.Args[2:])
		os.Exit(wrapper.Run(wrapper.Options{
			MarkerPath: *marker, TerminalID: *id, Directory: *dir, Command: *command,
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
	_, err := New(Options{SocketDir: deep, MarkerDir: t.TempDir(), WrapperExecutable: "/bin/true"})
	if err == nil || !strings.Contains(err.Error(), "move your state dir") {
		t.Errorf("New(deep dir) = %v, want the socket-path guard error", err)
	}
}

func TestNewTightensPermissiveSocketDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sockets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Options{SocketDir: dir, MarkerDir: t.TempDir(), WrapperExecutable: "/bin/true"}); err != nil {
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

// Integration against a real zmx in a private, throwaway socket directory
// (never the developer's real sessions — repo doctrine). /tmp keeps the
// socket-path budget; TempDir on macOS does not.
func newRealAdapter(t *testing.T) *Adapter {
	t.Helper()
	if _, err := New(Options{SocketDir: t.TempDir(), MarkerDir: t.TempDir(), WrapperExecutable: "/bin/true"}); err != nil {
		t.Skipf("adapter unavailable: %v", err)
	}
	adapter, err := New(Options{
		SocketDir:         mkShortTempDir(t),
		MarkerDir:         t.TempDir(),
		WrapperExecutable: testBinary(t),
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.zmx(); err != nil {
		t.Skip("zmx not installed; skipping real-zmx integration test")
	}
	return adapter
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
	adapter := newRealAdapter(t)
	ctx := context.Background()
	const id = "term-testa"
	t.Cleanup(func() { _ = adapter.Kill(context.Background(), id) })

	if sessions, err := adapter.Inventory(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("fresh inventory = %v, %v; want empty", sessions, err)
	}

	t.Setenv("SHELL", "/bin/sh")
	if err := adapter.Create(ctx, id, terminals.CreateSpec{Directory: "/", Command: "sleep 60"}); err != nil {
		t.Fatalf("Create: %v", err)
	}
	sessions, err := adapter.Inventory(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := []terminals.Session{{Name: id, Reachable: true}}
	if diff := cmp.Diff(want, sessions); diff != "" {
		t.Fatalf("inventory after create (-want +got):\n%s", diff)
	}
	// The wrapper's start marker appears (a beat after reachability — the
	// daemon settles before its root task finishes starting) and is not
	// yet exit evidence.
	startDeadline := time.Now().Add(3 * time.Second)
	for {
		marker, err := exitmarker.Read(adapter.markerDir, id)
		if err != nil {
			t.Fatal(err)
		}
		if marker != nil {
			if marker.Exited() {
				t.Fatalf("marker = %+v, want un-exited while the command runs", marker)
			}
			break
		}
		if time.Now().After(startDeadline) {
			t.Fatal("wrapper never wrote its start marker")
		}
		time.Sleep(25 * time.Millisecond)
	}

	// Creating the same name again must refuse, never silently attach.
	if err := adapter.Create(ctx, id, terminals.CreateSpec{Directory: "/"}); err == nil {
		t.Fatal("second Create with the same name succeeded")
	}

	if err := adapter.Kill(ctx, id); err != nil {
		t.Fatalf("Kill: %v", err)
	}
	if sessions, err := adapter.Inventory(ctx); err != nil || len(sessions) != 0 {
		t.Fatalf("inventory after kill = %v, %v; want empty", sessions, err)
	}
	// Killing an absent session is success: the goal state holds.
	if err := adapter.Kill(ctx, id); err != nil {
		t.Errorf("Kill(absent) = %v, want nil", err)
	}
	// zmx kill delivers HUP; the wrapper forwards it and records the death
	// before the follow-up SIGKILL lands.
	deadline := time.Now().Add(2 * time.Second)
	for {
		marker, err := exitmarker.Read(adapter.markerDir, id)
		if err != nil {
			t.Fatal(err)
		}
		if marker.Exited() {
			break
		}
		if time.Now().After(deadline) {
			t.Log("no exit marker after kill (wrapper outraced by SIGKILL); acceptable, evidence-free death is the missing state")
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
}
