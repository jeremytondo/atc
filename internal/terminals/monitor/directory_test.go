//go:build linux || darwin

package monitor

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/creack/pty"
	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/terminals/report"
)

func TestReadDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "a directory")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	want, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := readDirectory(os.Getpid()); got != want {
		t.Errorf("directory = %q, want %q", got, want)
	}
	if got := readDirectory(1 << 30); got != "" {
		t.Errorf("absent process directory = %q", got)
	}
	o := newObserver(-1, os.Getpid(), want)
	o.last.process = "shell"
	got, changed := o.poll()
	if diff := cmp.Diff(observation{process: "shell", directory: want}, got, cmp.AllowUnexported(observation{})); diff != "" || changed {
		t.Errorf("failed observation changed=%v (-want +got):\n%s", changed, diff)
	}
}

// A real job-control shell proves cd is observed with the same PID/name,
// nested shells and foreground programs supply their own cwd, background
// jobs do not, and returning to the outer shell restores its directory.
func TestObservesCurrentDirectory(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(dir, "other worktree")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	path := report.Path(dir, "term-aaaaa")
	cmd := exec.Command(self)
	cmd.Env = append(os.Environ(), "MONITOR_TEST_REPORT="+path, "MONITOR_TEST_POLL=20ms",
		"SHELL=/bin/sh", "HOME="+t.TempDir(), "PS1=$ ", "ENV=", "TERM=dumb")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	go func() { _, _ = io.Copy(io.Discard, ptmx) }()
	waitFor := func(process, directory string) *report.Report {
		t.Helper()
		deadline := time.Now().Add(10 * time.Second)
		for {
			rep, err := report.Read(dir, "term-aaaaa")
			if err == nil && rep != nil && rep.Process == process && rep.Directory == directory {
				return rep
			}
			if time.Now().After(deadline) {
				t.Fatalf("waiting for %s in %q; last report %+v (%v)", process, directory, rep, err)
			}
			time.Sleep(10 * time.Millisecond)
		}
	}
	send := func(command string) {
		t.Helper()
		if _, err := ptmx.WriteString(command + "\n"); err != nil {
			t.Fatal(err)
		}
	}
	initial := waitFor("sh", "/")
	send("cd '" + dir + "'")
	outer := waitFor("sh", dir)
	if outer.PID != initial.PID {
		t.Fatal("cd replaced the shell")
	}
	send("sh -i")
	send("cd '" + worktree + "'")
	waitFor("sh", worktree)
	send("exit")
	waitFor("sh", dir)
	send("sh -c 'cd \"" + worktree + "\"; exec sleep 30'")
	waitFor("sleep", worktree)
	if _, err := ptmx.Write([]byte{3}); err != nil {
		t.Fatal(err)
	}
	waitFor("sh", dir)
	// A background shell stays in another cwd while the foreground cd
	// must still win. Wait for and reap that job before leaving the shell.
	send("sh -c 'cd /; sleep 1' &")
	send("cd '" + worktree + "'")
	waitFor("sh", worktree)
	send("wait; exit")
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
	rep := waitFor("sh", worktree)
	if !rep.Exited() || rep.Code == nil || *rep.Code != 0 {
		t.Fatalf("final report = %+v", rep)
	}
}
