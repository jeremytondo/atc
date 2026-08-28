// Package exitmarker reads and writes the wrapper's exit-evidence files:
// one <terminal-id>.json per session under the paths.ExitMarkerDir
// location. The wrapper (atc __child) writes a marker at start and again
// at exit; the terminals reconciler reads them. A marker counts as exit
// evidence only once ExitedAt is set — the start-time write is not an
// exit.
package exitmarker

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Version guards against a future marker shape being misread by an old
// reader; readers reject other versions.
const Version = 1

// Marker is the recorded evidence. Code follows the wait-status
// convention: the program's exit code, 128+signum for a signal death, 127
// for a launch failure. It is set exactly when ExitedAt is set.
type Marker struct {
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
}

// Exited reports whether the marker is actual exit evidence.
func (m *Marker) Exited() bool {
	return m != nil && m.ExitedAt != nil
}

// Path is the marker file location for a terminal.
func Path(dir, terminalID string) string {
	return filepath.Join(dir, terminalID+".json")
}

// Write atomically replaces the marker at path: complete-before-visible,
// so no reader and no crash can observe a partial marker.
func Write(path string, marker Marker) error {
	marker.Version = Version
	encoded, err := json.Marshal(marker)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(dir, ".marker-*.tmp")
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

// Read returns the marker for terminalID, or nil when none exists. A
// marker that is unreadable, malformed, from another version, or for a
// different terminal is an error — never adopted as evidence.
func Read(dir, terminalID string) (*Marker, error) {
	data, err := os.ReadFile(Path(dir, terminalID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var marker Marker
	if err := json.Unmarshal(data, &marker); err != nil {
		return nil, fmt.Errorf("exit marker for %s: %w", terminalID, err)
	}
	if marker.Version != Version || marker.TerminalID != terminalID {
		return nil, fmt.Errorf("exit marker for %s: invalid version or terminal id", terminalID)
	}
	return &marker, nil
}

// Remove deletes the marker; a missing file is success (the goal state
// holds).
func Remove(dir, terminalID string) error {
	err := os.Remove(Path(dir, terminalID))
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}
