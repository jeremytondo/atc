// Package store is the prototype persistence seam. The JSON format is
// intentionally disposable; atomic replacement is the only storage decision
// being validated here.
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
)

const stateVersion = 1

type ThreadRecord struct {
	Thread          domain.Thread   `json:"thread"`
	ProviderSession string          `json:"providerSession,omitempty"`
	ProviderRoot    string          `json:"providerRoot,omitempty"`
	Foreground      domain.Activity `json:"foreground"`
	Requests        []RequestRecord `json:"requests,omitempty"`
}

type RequestRecord struct {
	Request     domain.PendingRequest `json:"request"`
	ProviderRef string                `json:"providerRef"`
}

type TerminalState string

const (
	TerminalRunning      TerminalState = "running"
	TerminalExited       TerminalState = "exited"
	TerminalMissing      TerminalState = "missing"
	TerminalDisconnected TerminalState = "disconnected"
	TerminalStale        TerminalState = "stale"
)

type TerminalRecord struct {
	Terminal        domain.Terminal `json:"terminal"`
	LegacyName      string          `json:"privateName,omitempty"`
	State           TerminalState   `json:"state"`
	DaemonPID       int             `json:"daemonPid,omitempty"`
	LastSeenAt      *time.Time      `json:"lastSeenAt,omitempty"`
	MissingSince    *time.Time      `json:"missingSince,omitempty"`
	StopRequestedAt *time.Time      `json:"stopRequestedAt,omitempty"`
	ExitPath        string          `json:"exitPath"`
}

type State struct {
	Version     int                 `json:"version"`
	NextEvent   uint64              `json:"nextEvent"`
	NextDiag    uint64              `json:"nextDiagnostic"`
	Threads     []ThreadRecord      `json:"threads"`
	Terminals   []TerminalRecord    `json:"terminals"`
	Events      []domain.Event      `json:"events"`
	Diagnostics []domain.Diagnostic `json:"diagnostics"`
}

type Repository interface {
	Load() (State, error)
	Save(State) error
}

type File struct {
	path    string
	logPath string
	mu      sync.Mutex
}

func NewFile(dir string) (*File, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("resolve state directory: %w", err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return nil, fmt.Errorf("create state directory: %w", err)
	}
	if err := os.Chmod(abs, 0o700); err != nil {
		return nil, fmt.Errorf("protect state directory: %w", err)
	}
	return &File{path: filepath.Join(abs, "state.json"), logPath: filepath.Join(abs, "timeline.jsonl")}, nil
}

func (f *File) Load() (State, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	contents, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return State{Version: stateVersion, NextEvent: 1, NextDiag: 1}, nil
	}
	if err != nil {
		return State{}, fmt.Errorf("read state: %w", err)
	}
	var state State
	if err := json.Unmarshal(contents, &state); err != nil {
		return State{}, fmt.Errorf("decode state: %w", err)
	}
	if state.Version != stateVersion {
		return State{}, fmt.Errorf("unsupported state version %d", state.Version)
	}
	normalize(&state)
	return state, nil
}

func (f *File) Save(state State) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	state.Version = stateVersion
	normalize(&state)
	contents, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode state: %w", err)
	}
	if err := replaceFile(f.path, contents); err != nil {
		return fmt.Errorf("replace state: %w", err)
	}
	return appendNewDiagnostics(f.logPath, state.Diagnostics)
}

func appendNewDiagnostics(path string, records []domain.Diagnostic) error {
	// The state file is authoritative. Rebuilding the human-review timeline on
	// every save avoids a second commit protocol between state and JSONL.
	temporary, err := os.CreateTemp(filepath.Dir(path), ".timeline-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	encoder := json.NewEncoder(temporary)
	for _, record := range records {
		if err := encoder.Encode(record); err != nil {
			temporary.Close()
			return err
		}
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func replaceFile(path string, contents []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".state-*.tmp")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(contents); err != nil {
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
	return os.Rename(temporaryPath, path)
}

func normalize(state *State) {
	if state.NextEvent == 0 {
		state.NextEvent = 1
	}
	if state.NextDiag == 0 {
		state.NextDiag = 1
	}
	if state.Threads == nil {
		state.Threads = []ThreadRecord{}
	}
	if state.Terminals == nil {
		state.Terminals = []TerminalRecord{}
	}
	if state.Events == nil {
		state.Events = []domain.Event{}
	}
	if state.Diagnostics == nil {
		state.Diagnostics = []domain.Diagnostic{}
	}
}

type Memory struct {
	mu    sync.Mutex
	State State
}

func NewMemory() *Memory {
	return &Memory{State: State{Version: stateVersion, NextEvent: 1, NextDiag: 1}}
}

func (m *Memory) Load() (State, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return clone(m.State)
}

func (m *Memory) Save(state State) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	copy, err := clone(state)
	if err != nil {
		return err
	}
	m.State = copy
	return nil
}

func clone(state State) (State, error) {
	encoded, err := json.Marshal(state)
	if err != nil {
		return State{}, err
	}
	var copy State
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return State{}, err
	}
	normalize(&copy)
	return copy, nil
}
