package supervisor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/agentstatus"
	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/terminal"
)

type Config struct {
	Terminal    terminal.Terminal
	Store       *Store
	Executable  string
	StaleAfter  time.Duration
	Now         func() time.Time
	AgentStatus *agentstatus.Service
}

type Supervisor struct {
	mu sync.Mutex

	terminal    terminal.Terminal
	store       *Store
	executable  string
	staleAfter  time.Duration
	now         func() time.Time
	agentStatus *agentstatus.Service
	records     []Record
}

func New(config Config) (*Supervisor, error) {
	if config.Terminal == nil || config.Store == nil {
		return nil, errors.New("terminal and store are required")
	}
	records, err := config.Store.Load()
	if err != nil {
		return nil, err
	}
	staleAfter := config.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 30 * time.Second
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	statusService := config.AgentStatus
	if statusService == nil {
		statusService, err = agentstatus.New(config.Store.Dir(), config.Executable, now)
		if err != nil {
			return nil, err
		}
	}
	return &Supervisor{
		terminal:    config.Terminal,
		store:       config.Store,
		executable:  config.Executable,
		staleAfter:  staleAfter,
		now:         now,
		records:     records,
		agentStatus: statusService,
	}, nil
}

func (s *Supervisor) Create(ctx context.Context, request CreateRequest) (Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	name := strings.TrimSpace(request.Name)
	if name == "" {
		return Snapshot{}, errors.New("session name cannot be empty")
	}
	for _, record := range s.records {
		if record.Name == name {
			return Snapshot{}, fmt.Errorf("session %q already exists; stop and cleanup it first", name)
		}
	}
	command, kind, err := resolveCommand(request.Kind, request.Command)
	if err != nil {
		return Snapshot{}, err
	}
	cwd, err := filepath.Abs(request.CWD)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve working directory: %w", err)
	}
	id, err := randomID()
	if err != nil {
		return Snapshot{}, err
	}
	now := s.now().UTC()
	preparedCommand, err := s.agentStatus.Prepare(kind, id, command)
	if err != nil {
		return Snapshot{}, err
	}
	record := Record{
		ID:        id,
		Name:      name,
		ZmxName:   "atc-exp-" + id,
		Kind:      kind,
		Command:   command,
		CWD:       cwd,
		State:     StateMissing,
		CreatedAt: now,
	}
	s.records = append(s.records, record)
	if err := s.store.Save(s.records); err != nil {
		s.records = s.records[:len(s.records)-1]
		return Snapshot{}, err
	}
	wrapped := append([]string{s.executable, "__child", "--marker", s.store.ExitPath(id), "--id", id, "--"}, preparedCommand...)
	if err := s.terminal.Create(ctx, terminal.CreateOptions{
		Name:    record.ZmxName,
		CWD:     cwd,
		Command: wrapped,
		Env: map[string]string{
			"ATC_SESSION_ID":   id,
			"ATC_SESSION_NAME": name,
		},
	}); err != nil {
		// Keep the record. A crash can happen after zmx creates the daemon but
		// before its client observes settlement; reconciliation must decide.
		return Snapshot{}, fmt.Errorf("create %q: %w", name, err)
	}
	snapshots, err := s.reconcileLocked(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	return snapshotByName(snapshots, name)
}

func (s *Supervisor) Reconcile(ctx context.Context) ([]Snapshot, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reconcileLocked(ctx)
}

func (s *Supervisor) reconcileLocked(ctx context.Context) ([]Snapshot, error) {
	inventory, err := s.terminal.List(ctx)
	if err != nil {
		snapshots := make([]Snapshot, 0, len(s.records))
		for i := range s.records {
			s.records[i].State = StateDisconnected
			snapshot := snapshotFor(s.records[i], nil, nil, "zmx inventory unavailable")
			s.enrichAgentStatus(&snapshot, s.records[i], nil, "zmx inventory unavailable")
			snapshots = append(snapshots, snapshot)
		}
		_ = s.store.Save(s.records)
		return snapshots, err
	}
	byName := make(map[string]terminal.Session, len(inventory))
	for _, session := range inventory {
		byName[session.Name] = session
	}
	now := s.now().UTC()
	snapshots := make([]Snapshot, 0, len(s.records)+len(inventory))
	managed := make(map[string]bool, len(s.records))
	for i := range s.records {
		record := &s.records[i]
		managed[record.ZmxName] = true
		entry, present := byName[record.ZmxName]
		marker, markerErr := s.store.LoadExit(record.ID)
		if markerErr != nil {
			return nil, markerErr
		}
		reason := ""
		if present && entry.Reachable {
			record.State = StateRunning
			record.DaemonPID = entry.PID
			record.LastSeenAt = timePointer(now)
			record.MissingSince = nil
		} else if present {
			record.State = StateDisconnected
			record.DaemonPID = entry.PID
			reason = "zmx knows the session but its daemon did not answer"
		} else if marker != nil && marker.ExitedAt != nil {
			record.State = StateExited
			record.DaemonPID = 0
			if record.StopRequestedAt != nil {
				reason = "terminated deliberately"
			} else {
				reason = exitReason(marker)
			}
		} else if record.StopRequestedAt != nil {
			record.State = StateExited
			record.DaemonPID = 0
			reason = "terminated deliberately"
		} else {
			if record.MissingSince == nil {
				record.MissingSince = timePointer(now)
			}
			record.DaemonPID = 0
			if now.Sub(*record.MissingSince) >= s.staleAfter {
				record.State = StateStale
				reason = "session stayed absent without an exit marker"
			} else {
				record.State = StateMissing
				reason = "session absent without exit evidence; retry before cleanup"
			}
		}
		var session *terminal.Session
		if present {
			copy := entry
			session = &copy
		}
		snapshot := snapshotFor(*record, session, marker, reason)
		s.enrichAgentStatus(&snapshot, *record, marker, reason)
		snapshots = append(snapshots, snapshot)
	}
	for _, entry := range inventory {
		if managed[entry.Name] {
			continue
		}
		state := StateStale
		reason := "unmanaged session in the experiment's private zmx directory"
		if !entry.Reachable {
			state = StateDisconnected
			reason = "unmanaged session is temporarily unreachable"
		}
		snapshots = append(snapshots, Snapshot{
			Name: entry.Name, ZmxName: entry.Name, State: state,
			Reachable: entry.Reachable, DaemonPID: entry.PID, Orphan: true, Reason: reason,
		})
	}
	if err := s.store.Save(s.records); err != nil {
		return nil, err
	}
	sort.Slice(snapshots, func(i, j int) bool { return snapshots[i].Name < snapshots[j].Name })
	return snapshots, nil
}

func (s *Supervisor) Send(ctx context.Context, name string, input []byte) error {
	snapshot, err := s.getRunning(ctx, name)
	if err != nil {
		return err
	}
	return s.terminal.Send(ctx, snapshot.ZmxName, input)
}

func (s *Supervisor) History(ctx context.Context, name string) ([]byte, error) {
	snapshot, err := s.getRunning(ctx, name)
	if err != nil {
		return nil, err
	}
	return s.terminal.History(ctx, snapshot.ZmxName)
}

func (s *Supervisor) Attach(ctx context.Context, name string, stdin, stdout *os.File, stderr io.Writer) error {
	snapshot, err := s.getRunning(ctx, name)
	if err != nil {
		return err
	}
	return s.terminal.Attach(ctx, snapshot.ZmxName, stdin, stdout, stderr)
}

func (s *Supervisor) Stop(ctx context.Context, name string) error {
	s.mu.Lock()
	index := s.recordIndex(name)
	if index < 0 {
		s.mu.Unlock()
		return fmt.Errorf("unknown session %q", name)
	}
	now := s.now().UTC()
	s.records[index].StopRequestedAt = &now
	zmxName := s.records[index].ZmxName
	if err := s.store.Save(s.records); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	if err := s.terminal.Kill(ctx, zmxName); err != nil {
		return err
	}
	_, err := s.Reconcile(ctx)
	return err
}

func (s *Supervisor) AgentTransitions(name string) ([]agentstatus.Observation, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	index := s.recordIndex(name)
	if index < 0 {
		return nil, fmt.Errorf("unknown session %q", name)
	}
	return s.agentStatus.Transitions(s.records[index].ID)
}

func (s *Supervisor) Cleanup(ctx context.Context) (CleanupResult, error) {
	s.mu.Lock()
	snapshots, err := s.reconcileLocked(ctx)
	if err != nil {
		s.mu.Unlock()
		return CleanupResult{}, fmt.Errorf("refusing cleanup without a complete inventory: %w", err)
	}
	orphans := make([]string, 0)
	forget := make(map[string]bool)
	result := CleanupResult{KilledOrphans: []string{}, Forgotten: []string{}}
	for _, snapshot := range snapshots {
		if snapshot.Orphan {
			if snapshot.State == StateStale {
				orphans = append(orphans, snapshot.ZmxName)
			}
			continue
		}
		if snapshot.State == StateExited || snapshot.State == StateStale {
			forget[snapshot.ID] = true
			result.Forgotten = append(result.Forgotten, snapshot.Name)
		}
	}
	s.mu.Unlock()

	for _, name := range orphans {
		if err := s.terminal.Kill(ctx, name); err != nil {
			return result, err
		}
		result.KilledOrphans = append(result.KilledOrphans, name)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]Record, 0, len(s.records))
	for _, record := range s.records {
		if forget[record.ID] {
			if err := s.store.RemoveExit(record.ID); err != nil {
				return result, err
			}
			if err := s.agentStatus.Remove(record.ID); err != nil {
				return result, err
			}
			continue
		}
		kept = append(kept, record)
	}
	s.records = kept
	if err := s.store.Save(s.records); err != nil {
		return result, err
	}
	sort.Strings(result.Forgotten)
	sort.Strings(result.KilledOrphans)
	return result, nil
}

func (s *Supervisor) getRunning(ctx context.Context, name string) (Snapshot, error) {
	snapshots, err := s.Reconcile(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := snapshotByName(snapshots, name)
	if err != nil {
		return Snapshot{}, err
	}
	if snapshot.State != StateRunning {
		return Snapshot{}, fmt.Errorf("session %q is %s: %s", name, snapshot.State, snapshot.Reason)
	}
	return snapshot, nil
}

func (s *Supervisor) recordIndex(name string) int {
	for i, record := range s.records {
		if record.Name == name {
			return i
		}
	}
	return -1
}

func snapshotByName(snapshots []Snapshot, name string) (Snapshot, error) {
	for _, snapshot := range snapshots {
		if !snapshot.Orphan && snapshot.Name == name {
			return snapshot, nil
		}
	}
	return Snapshot{}, fmt.Errorf("unknown session %q", name)
}

func snapshotFor(record Record, session *terminal.Session, marker *ExitMarker, reason string) Snapshot {
	snapshot := Snapshot{
		ID: record.ID, Name: record.Name, ZmxName: record.ZmxName, Kind: record.Kind,
		State: record.State, DaemonPID: record.DaemonPID, Exit: marker, Reason: reason,
	}
	if session != nil {
		snapshot.Reachable = session.Reachable
	}
	return snapshot
}

func resolveCommand(kind string, command []string) ([]string, string, error) {
	switch kind {
	case "shell":
		if len(command) != 0 {
			return nil, "", errors.New("shell workload does not accept a command")
		}
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		return []string{shell, "-l"}, kind, nil
	case "codex", "claude":
		return append([]string{kind}, command...), kind, nil
	case "process", "custom":
		if len(command) == 0 {
			return nil, "", fmt.Errorf("%s workload requires command argv", kind)
		}
		return append([]string(nil), command...), kind, nil
	default:
		return nil, "", fmt.Errorf("unknown workload %q (use shell, process, codex, claude, or custom)", kind)
	}
}

func randomID() (string, error) {
	data := make([]byte, 8)
	if _, err := rand.Read(data); err != nil {
		return "", fmt.Errorf("generate session id: %w", err)
	}
	return hex.EncodeToString(data), nil
}

func timePointer(value time.Time) *time.Time { return &value }

func exitReason(marker *ExitMarker) string {
	if marker.Error != "" {
		return marker.Error
	}
	if marker.Signal != "" {
		return "child exited from signal " + marker.Signal
	}
	if marker.ExitCode != nil {
		return fmt.Sprintf("child exited with code %d", *marker.ExitCode)
	}
	return "child exited"
}

func processEvidence(record Record, marker *ExitMarker, reason string) agentstatus.ProcessEvidence {
	switch record.State {
	case StateRunning:
		return agentstatus.ProcessEvidence{State: agentstatus.ProcessRunning, Detail: "zmx daemon and child are running"}
	case StateExited:
		var exitCode *int
		if marker != nil && record.StopRequestedAt == nil {
			exitCode = marker.ExitCode
		}
		return agentstatus.ProcessEvidence{State: agentstatus.ProcessExited, ExitCode: exitCode, Detail: reason}
	default:
		return agentstatus.ProcessEvidence{State: agentstatus.ProcessUnavailable, Detail: reason}
	}
}

func (s *Supervisor) enrichAgentStatus(snapshot *Snapshot, record Record, marker *ExitMarker, reason string) {
	if record.Kind != "codex" && record.Kind != "claude" {
		return
	}
	observation, err := s.agentStatus.Observe(record.Kind, record.ID, processEvidence(record, marker, reason))
	if err != nil {
		snapshot.AgentStatusError = err.Error()
		return
	}
	snapshot.AgentStatus = &observation
}
