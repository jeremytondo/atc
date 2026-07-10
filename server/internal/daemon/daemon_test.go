package daemon

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/paths"
)

func TestInspectReportsMissingPIDFileAsNotRunning(t *testing.T) {
	status, err := Inspect(filepath.Join(t.TempDir(), "atc.pid"))
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	if status.Running || status.PID != 0 || status.Stale {
		t.Fatalf("status = %+v, want not running", status)
	}
}

func TestInspectReportsCurrentProcessAsRunning(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "atc.pid")
	if err := writePIDFile(pidPath, os.Getpid()); err != nil {
		t.Fatalf("write PID file: %v", err)
	}

	status, err := Inspect(pidPath)
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	if !status.Running || status.PID != os.Getpid() || status.Stale {
		t.Fatalf("status = %+v, want running current process", status)
	}
}

func TestInspectReportsStalePIDFile(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "atc.pid")
	stalePID := unusedPID(t)
	if err := writePIDFile(pidPath, stalePID); err != nil {
		t.Fatalf("write PID file: %v", err)
	}

	status, err := Inspect(pidPath)
	if err != nil {
		t.Fatalf("Inspect returned error: %v", err)
	}
	if status.Running || status.PID != stalePID || !status.Stale {
		t.Fatalf("status = %+v, want stale pid %d", status, stalePID)
	}
}

func TestInspectServiceReportsUntrackedSocketOwner(t *testing.T) {
	servicePaths := paths.PathsForDir(t.TempDir())
	listener, err := net.Listen("unix", servicePaths.SocketPath)
	if err != nil {
		t.Fatalf("listen on Unix socket: %v", err)
	}
	defer listener.Close()

	status, err := InspectService(servicePaths)
	if err != nil {
		t.Fatalf("InspectService returned error: %v", err)
	}
	if status.Running || status.PID != 0 || status.Stale {
		t.Fatalf("status = %+v, want no PID-file status", status)
	}
	if !status.SocketReachable || status.SocketPID != os.Getpid() {
		t.Fatalf("status = %+v, want reachable socket owned by current process", status)
	}
}

func TestInspectRejectsInvalidPIDFile(t *testing.T) {
	pidPath := filepath.Join(t.TempDir(), "atc.pid")
	if err := os.WriteFile(pidPath, []byte("not-a-pid\n"), 0o600); err != nil {
		t.Fatalf("write PID file: %v", err)
	}

	err := func() error {
		_, err := Inspect(pidPath)
		return err
	}()
	if err == nil {
		t.Fatal("Inspect returned nil error, want invalid PID file error")
	}
	if !strings.Contains(err.Error(), "invalid PID file") {
		t.Fatalf("error = %q, want invalid PID file", err)
	}
}

func TestStopFailsWhenNoDaemonIsRunning(t *testing.T) {
	servicePaths := paths.PathsForDir(t.TempDir())
	_, err := Stop(context.Background(), servicePaths)
	if err == nil {
		t.Fatal("Stop returned nil error, want not running error")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Fatalf("error = %q, want not running", err)
	}
}

func TestStopTerminatesRunningProcessAndRemovesPIDFile(t *testing.T) {
	servicePaths := paths.PathsForDir(t.TempDir())
	cmd := exec.Command("sleep", "30")
	if err := cmd.Start(); err != nil {
		t.Fatalf("start sleep process: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-waitCh
	})

	if err := writePIDFile(servicePaths.PIDPath, cmd.Process.Pid); err != nil {
		t.Fatalf("write PID file: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pid, err := Stop(ctx, servicePaths)
	if err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if pid != cmd.Process.Pid {
		t.Fatalf("pid = %d, want %d", pid, cmd.Process.Pid)
	}
	if _, err := os.Stat(servicePaths.PIDPath); !os.IsNotExist(err) {
		t.Fatalf("PID file still exists, stat err = %v", err)
	}
}

func TestStopTerminatesUntrackedSocketOwner(t *testing.T) {
	servicePaths := paths.PathsForDir(t.TempDir())
	cmd := exec.Command(os.Args[0], "-test.run=TestSocketOwnerHelper", "--", servicePaths.SocketPath)
	cmd.Env = append(os.Environ(), "ATC_SOCKET_OWNER_HELPER=1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("open helper stdout: %v", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			_ = cmd.Process.Kill()
			<-waitCh
		}
	})

	scanner := bufio.NewScanner(stdout)
	if !scanner.Scan() {
		t.Fatal("helper exited before reporting ready")
	}
	if got := scanner.Text(); got != "ready" {
		t.Fatalf("helper output = %q, want ready", got)
	}

	status, err := InspectService(servicePaths)
	if err != nil {
		t.Fatalf("InspectService returned error: %v", err)
	}
	if !status.SocketReachable || status.SocketPID != cmd.Process.Pid {
		t.Fatalf("status = %+v, want helper PID %d", status, cmd.Process.Pid)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pid, err := Stop(ctx, servicePaths)
	if err != nil {
		t.Fatalf("Stop returned error: %v", err)
	}
	if pid != cmd.Process.Pid {
		t.Fatalf("pid = %d, want %d", pid, cmd.Process.Pid)
	}

	select {
	case <-waitCh:
		stopped = true
	case <-time.After(2 * time.Second):
		t.Fatal("helper did not stop")
	}
}

func TestSocketOwnerHelper(t *testing.T) {
	if os.Getenv("ATC_SOCKET_OWNER_HELPER") != "1" {
		return
	}
	socketPath := os.Args[len(os.Args)-1]
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "listen on Unix socket: %v\n", err)
		os.Exit(2)
	}
	defer listener.Close()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	fmt.Fprintln(os.Stdout, "ready")
	select {}
}

func unusedPID(t *testing.T) int {
	t.Helper()

	for pid := 999999; pid > 1; pid-- {
		running, err := daemonPIDRunning(pid)
		if err != nil {
			t.Fatalf("check candidate stale pid %d: %v", pid, err)
		}
		if !running {
			return pid
		}
	}
	t.Fatal("could not find an unused pid")
	return 0
}
