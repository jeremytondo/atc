// Package child records atomic start and exit evidence around a terminal
// workload. zmx remains the sole durable supervisor; this wrapper only records.
package child

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
)

type Marker struct {
	Version    int        `json:"version"`
	TerminalID string     `json:"terminalId"`
	PID        int        `json:"pid"`
	StartedAt  time.Time  `json:"startedAt"`
	ExitedAt   *time.Time `json:"exitedAt,omitempty"`
	ExitCode   *int       `json:"exitCode,omitempty"`
	Signal     string     `json:"signal,omitempty"`
	Error      string     `json:"error,omitempty"`
}

func Run(ctx context.Context, markerPath, terminalID string, command []string) error {
	if markerPath == "" || terminalID == "" || len(command) == 0 {
		return errors.New("marker, terminal id, and command are required")
	}
	started := time.Now().UTC()
	cmd := exec.CommandContext(ctx, command[0], command[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := cmd.Start(); err != nil {
		marker := Marker{TerminalID: terminalID, StartedAt: started, Error: err.Error()}
		now := time.Now().UTC()
		marker.ExitedAt = &now
		_ = WriteMarker(markerPath, marker)
		return err
	}
	marker := Marker{TerminalID: terminalID, PID: cmd.Process.Pid, StartedAt: started}
	if err := WriteMarker(markerPath, marker); err != nil {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return err
	}
	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	contextDone := ctx.Done()
	var waitErr error
	waiting := true
	for waiting {
		select {
		case waitErr = <-waited:
			waiting = false
		case received := <-signals:
			_ = cmd.Process.Signal(received)
		case <-contextDone:
			_ = cmd.Process.Signal(syscall.SIGTERM)
			contextDone = nil
		}
	}
	exited := time.Now().UTC()
	marker.ExitedAt = &exited
	if cmd.ProcessState != nil {
		code := cmd.ProcessState.ExitCode()
		marker.ExitCode = &code
		if status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus); ok && status.Signaled() {
			marker.Signal = status.Signal().String()
		}
	}
	if waitErr != nil {
		marker.Error = waitErr.Error()
	}
	if err := WriteMarker(markerPath, marker); err != nil {
		return err
	}
	return waitErr
}

func WriteMarker(path string, marker Marker) error {
	marker.Version = 1
	encoded, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".exit-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(encoded); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace exit marker: %w", err)
	}
	return nil
}
