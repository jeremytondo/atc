package codex

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/placement"
)

// startServer runs the codex on PATH as a detached app-server listening
// on the well-known socket of the given home, output to the control
// directory's log file.
func TestStartServerSpawnsDetachedAppServer(t *testing.T) {
	testStartServer(t, &placement.Host{})
}

// With a real systemd user manager, the server runs in its own scope
// (ATC-319), never in the cgroup of the process that started it.
func TestRealSupervisorCodexServerIsScoped(t *testing.T) {
	host := placement.Detect()
	if err := host.Preflight(context.Background()); !host.Scoped() || err != nil {
		if os.Getenv("ATC_SUPERVISOR_TESTS") == "require" {
			t.Fatalf("real supervisor tests required but unavailable: scoped=%v err=%v", host.Scoped(), err)
		}
		t.Skipf("real supervisor tests unavailable: scoped=%v err=%v", host.Scoped(), err)
	}
	cgroup := testStartServer(t, host)
	if !strings.Contains(cgroup, "/atc-codex-") {
		t.Errorf("spawned codex runs in cgroup %q, want an atc-codex scope", cgroup)
	}
}

// testStartServer runs a fake codex through startServer with the given
// placement and returns the cgroup the fake saw itself in.
func testStartServer(t *testing.T, host *placement.Host) string {
	t.Helper()
	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "argv")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$ATC_TEST_OUT\"\nprintf '%s\\n' \"$CODEX_HOME\" >> \"$ATC_TEST_OUT\"\n" +
		"pwd >> \"$ATC_TEST_OUT\"\ncat /proc/self/cgroup > \"$ATC_TEST_OUT.cgroup\" 2>/dev/null\necho started >&2\n"
	if err := os.WriteFile(filepath.Join(bin, "codex"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("ATC_TEST_OUT", out)
	home := t.TempDir()
	t.Setenv("HOME", home)
	codexHome := filepath.Join(home, ".codex")

	if err := startServer(codexHome, host); err != nil {
		t.Fatal(err)
	}
	var content []byte
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if content, _ = os.ReadFile(out); strings.Count(string(content), "\n") >= 5 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	// pwd reports the physical directory; on macOS the temp root is a
	// symlink into /private.
	physicalHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	want := "app-server\n--listen\nunix://\n" + codexHome + "\n" + physicalHome + "\n"
	if string(content) != want {
		t.Errorf("spawned codex saw:\n%s\nwant:\n%s", content, want)
	}
	// "started" is the fake's last write, after its cgroup file.
	deadline = time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if log, err := os.ReadFile(serverLogPath(codexHome)); err == nil && strings.Contains(string(log), "started") {
			cgroup, _ := os.ReadFile(out + ".cgroup")
			return strings.TrimSpace(string(cgroup))
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("server output never reached the control directory's log file")
	return ""
}

func TestStartServerRequiresCodexOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	if err := startServer(t.TempDir(), &placement.Host{}); err == nil {
		t.Fatal("startServer succeeded with no codex on PATH")
	}
}
