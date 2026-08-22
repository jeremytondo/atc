package supervisor

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"
)

// RunChild is the small command wrapper launched as the zmx session's root
// task. zmx owns its PTY and lifetime; this wrapper only leaves structured
// evidence of normal exits, launch failures, and signals for a later host.
func RunChild(markerPath, sessionID string, command []string) int {
	now := time.Now().UTC()
	marker := ExitMarker{Version: 1, SessionID: sessionID, StartedAt: now}
	if len(command) == 0 {
		marker.Error = "empty child command"
		exited := time.Now().UTC()
		marker.ExitedAt = &exited
		code := 127
		marker.ExitCode = &code
		_ = WriteExitMarker(markerPath, marker)
		return code
	}

	child := exec.Command(command[0], command[1:]...)
	child.Stdin = os.Stdin
	child.Stdout = os.Stdout
	child.Stderr = os.Stderr
	child.Env = os.Environ()
	if err := child.Start(); err != nil {
		marker.Error = fmt.Sprintf("start child: %v", err)
		exited := time.Now().UTC()
		marker.ExitedAt = &exited
		code := 127
		marker.ExitCode = &code
		_ = WriteExitMarker(markerPath, marker)
		fmt.Fprintln(os.Stderr, marker.Error)
		return code
	}
	marker.PID = child.Process.Pid
	if err := WriteExitMarker(markerPath, marker); err != nil {
		fmt.Fprintln(os.Stderr, "write start marker:", err)
	}

	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	waited := make(chan error, 1)
	go func() { waited <- child.Wait() }()

	var waitErr error
	for waitErr == nil {
		select {
		case waitErr = <-waited:
			if waitErr == nil {
				waitErr = errChildComplete
			}
		case received := <-signals:
			_ = child.Process.Signal(received)
		}
	}
	if errors.Is(waitErr, errChildComplete) {
		waitErr = nil
	}

	exited := time.Now().UTC()
	marker.ExitedAt = &exited
	code := 0
	if waitErr != nil {
		code = 1
		var exitError *exec.ExitError
		if errors.As(waitErr, &exitError) {
			code = exitError.ExitCode()
			if status, ok := exitError.Sys().(syscall.WaitStatus); ok && status.Signaled() {
				marker.Signal = status.Signal().String()
				code = 128 + int(status.Signal())
			}
		} else {
			marker.Error = waitErr.Error()
		}
	}
	marker.ExitCode = &code
	if err := WriteExitMarker(markerPath, marker); err != nil {
		fmt.Fprintln(os.Stderr, "write exit marker:", err)
	}
	return code
}

var errChildComplete = errors.New("child completed")
