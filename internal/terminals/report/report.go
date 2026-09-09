// Package report reads and writes the monitor's per-terminal report: one
// <terminal-id>.json per session under the paths.ReportDir location. The
// monitor (atc __child) writes it at start, whenever the observed
// foreground program changes, and at exit; the terminals reconciler
// reads it. A report counts as exit evidence only once ExitedAt is set —
// every earlier write is observation, not an exit.
package report

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Version guards against a future report shape being misread by an old
// reader; readers reject other versions.
const Version = 1

// Report is what the monitor knows about its terminal. Code follows the
// wait-status convention: the program's exit code, 128+signum for a
// signal death, 127 for a launch failure. It is set exactly when ExitedAt
// is set.
type Report struct {
	Version    int        `json:"version"`
	TerminalID string     `json:"terminalId"`
	PID        int        `json:"pid,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	ExitedAt   *time.Time `json:"exitedAt,omitempty"`
	Code       *int       `json:"code,omitempty"`
	// Signal is the signal name for a signal death, diagnostic only.
	Signal string `json:"signal,omitempty"`
	// Error describes a launch failure, diagnostic only.
	Error string `json:"error,omitempty"`
	// Process is the short name of the program last observed in the
	// terminal's foreground; empty until the first successful
	// observation. It survives exit: an exited terminal keeps what last
	// ran in it.
	Process string `json:"process,omitempty"`
}

// Exited reports whether the report is actual exit evidence.
func (r *Report) Exited() bool {
	return r != nil && r.ExitedAt != nil
}

// Path is the report file location for a terminal.
func Path(dir, terminalID string) string {
	return filepath.Join(dir, terminalID+".json")
}

// Write atomically replaces the report at path: complete-before-visible,
// so no reader and no crash can observe a partial report.
func Write(path string, r Report) error {
	r.Version = Version
	encoded, err := json.Marshal(r)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".report-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := writeAll(temporary, encoded); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func writeAll(f *os.File, data []byte) error {
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// Read returns the report for terminalID, or nil when none exists. A
// report that is unreadable, malformed, from another version, or for a
// different terminal is an error — never adopted as evidence.
func Read(dir, terminalID string) (*Report, error) {
	data, err := os.ReadFile(Path(dir, terminalID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var r Report
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("report for %s: %w", terminalID, err)
	}
	if r.Version != Version || r.TerminalID != terminalID {
		return nil, fmt.Errorf("report for %s: invalid version or terminal id", terminalID)
	}
	return &r, nil
}

// Remove deletes the report; a missing file is success (the goal state
// holds).
func Remove(dir, terminalID string) error {
	err := os.Remove(Path(dir, terminalID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
