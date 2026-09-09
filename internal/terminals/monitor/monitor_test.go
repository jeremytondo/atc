package monitor

// The monitor is process supervision; these tests run real processes. The
// shell is /bin/sh where behavior matters and the re-exec'd test binary
// where the exact argv must be observed (a #! script cannot see argv[0]).

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/terminals/monitor/report"
)

func TestMain(m *testing.M) {
	// Helper mode: when re-exec'd as the "shell", record argv and exit.
	if record := os.Getenv("MONITOR_TEST_RECORD"); record != "" {
		_ = os.WriteFile(record, []byte(strings.Join(os.Args, "\n")), 0o600)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runMonitor(t *testing.T, command string, directory string) (int, *report.Report) {
	t.Helper()
	dir := t.TempDir()
	path := report.Path(dir, "term-aaaaa")
	code := Run(Options{ReportPath: path, TerminalID: "term-aaaaa", Directory: directory, Command: command})
	rep, err := report.Read(dir, "term-aaaaa")
	if err != nil {
		t.Fatal(err)
	}
	return code, rep
}

func TestCommandExitCodeRecorded(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	code, rep := runMonitor(t, "exit 3", t.TempDir())
	if code != 3 {
		t.Errorf("Run = %d, want 3", code)
	}
	if !rep.Exited() || rep.Code == nil || *rep.Code != 3 {
		t.Errorf("report = %+v, want exited with code 3", rep)
	}
}

func TestSignalDeathRecordedAs128PlusSignum(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	code, rep := runMonitor(t, "kill -KILL $$", t.TempDir())
	if code != 137 {
		t.Errorf("Run = %d, want 137 (128+SIGKILL)", code)
	}
	if !rep.Exited() || rep.Code == nil || *rep.Code != 137 {
		t.Errorf("report = %+v, want exited with code 137", rep)
	}
	if rep.Signal == "" {
		t.Error("report.Signal empty for a signal death")
	}
}

func TestLaunchFailureRecordedAs127(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	code, rep := runMonitor(t, "true", filepath.Join(t.TempDir(), "does-not-exist"))
	if code != LaunchFailureCode {
		t.Errorf("Run = %d, want %d", code, LaunchFailureCode)
	}
	if !rep.Exited() || rep.Code == nil || *rep.Code != LaunchFailureCode {
		t.Errorf("report = %+v, want exited with launch-failure code", rep)
	}
	if rep.Error == "" {
		t.Error("report.Error empty for a launch failure")
	}
}

// HUP/INT/TERM must reach the workload through the monitor — zmx kill
// signals the monitor's process group, and a shell that moved to its own
// group only hears about it by forwarding.
func TestForwardsSignalsToChild(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	ready := filepath.Join(t.TempDir(), "ready")
	// The backgrounded sleep must not inherit the test's output pipes, or
	// an orphan holds go test's stdout open for the full 30 seconds.
	command := "trap 'exit 42' HUP; : > " + ready + "; sleep 30 >/dev/null 2>&1 & wait"

	dir := t.TempDir()
	path := report.Path(dir, "term-aaaaa")
	done := make(chan int, 1)
	go func() {
		done <- Run(Options{ReportPath: path, TerminalID: "term-aaaaa", Directory: "/", Command: command})
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("child shell never signalled readiness")
		}
		time.Sleep(10 * time.Millisecond)
	}
	// Signal our own process: the monitor's handler owns HUP while
	// registered and forwards it to the child.
	if err := syscall.Kill(os.Getpid(), syscall.SIGHUP); err != nil {
		t.Fatal(err)
	}
	select {
	case code := <-done:
		if code != 42 {
			t.Errorf("Run = %d, want the trap's 42", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("monitor did not return after forwarded HUP")
	}
}

// The exact shell argv is the fidelity contract: a plain terminal gets
// the traditional login convention (argv[0] = "-shell", nothing else); a
// command gets `$SHELL -i -l -c "<command>"`.
func TestShellInvocation(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		command string
		want    func() []string
	}{
		"plain shell": {"", func() []string {
			return []string{"-" + filepath.Base(self)}
		}},
		"command": {"hx .", func() []string {
			return []string{self, "-i", "-l", "-c", "hx ."}
		}},
	} {
		record := filepath.Join(t.TempDir(), "argv")
		t.Setenv("SHELL", self)
		t.Setenv("MONITOR_TEST_RECORD", record)
		code, rep := runMonitor(t, tc.command, "/")
		if code != 0 || !rep.Exited() {
			t.Fatalf("%s: Run = %d, rep %+v", name, code, rep)
		}
		data, err := os.ReadFile(record)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		got := strings.Split(string(data), "\n")
		want := tc.want()
		if len(got) != len(want) {
			t.Fatalf("%s: argv = %q, want %q", name, got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("%s: argv[%d] = %q, want %q", name, i, got[i], want[i])
			}
		}
	}
}
