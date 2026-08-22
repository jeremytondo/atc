package agentstatus

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Service struct {
	dir        string
	executable string
	now        func() time.Time
}

type storedSignal struct {
	Provider   string          `json:"provider"`
	ReceivedAt time.Time       `json:"receivedAt"`
	Payload    json.RawMessage `json:"payload"`
}

func New(stateDir, executable string, now func() time.Time) (*Service, error) {
	dir := filepath.Join(stateDir, "agent-status")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create agent status directory: %w", err)
	}
	if now == nil {
		now = time.Now
	}
	return &Service{dir: dir, executable: executable, now: now}, nil
}

func (s *Service) Prepare(kind, sessionID string, command []string) ([]string, error) {
	if kind != "claude" {
		return append([]string(nil), command...), nil
	}
	if len(command) == 0 {
		return nil, errors.New("prepare claude status hooks: empty command")
	}
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	hookCommand := strings.Join([]string{
		shellQuote(s.executable), "__agent_status_hook",
		"--state-dir", shellQuote(filepath.Dir(s.dir)),
		"--id", shellQuote(sessionID),
		"--provider", "claude",
	}, " ")
	events := []string{
		"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse",
		"PermissionRequest", "Notification", "Stop", "StopFailure", "SessionEnd",
	}
	hooks := make(map[string]any, len(events))
	for _, event := range events {
		hooks[event] = []any{map[string]any{
			"hooks": []any{map[string]any{"type": "command", "command": hookCommand}},
		}}
	}
	settings := map[string]any{"hooks": hooks}
	contents, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode claude hook settings: %w", err)
	}
	settingsPath := filepath.Join(s.dir, sessionID, "claude-settings.json")
	if err := replaceFile(settingsPath, contents); err != nil {
		return nil, fmt.Errorf("write claude hook settings: %w", err)
	}
	prepared := []string{command[0], "--settings", settingsPath}
	return append(prepared, command[1:]...), nil
}

func RecordHook(stateDir, sessionID, provider string, input io.Reader) error {
	if provider != "claude" {
		return fmt.Errorf("unsupported structured status provider %q", provider)
	}
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	payload, err := io.ReadAll(io.LimitReader(input, 2<<20))
	if err != nil {
		return fmt.Errorf("read status hook: %w", err)
	}
	if !json.Valid(payload) {
		return errors.New("status hook payload is not valid JSON")
	}
	service, err := New(stateDir, "", nil)
	if err != nil {
		return err
	}
	signal := storedSignal{Provider: provider, ReceivedAt: time.Now().UTC(), Payload: payload}
	contents, err := json.Marshal(signal)
	if err != nil {
		return fmt.Errorf("encode status hook: %w", err)
	}
	random := make([]byte, 6)
	if _, err := rand.Read(random); err != nil {
		return fmt.Errorf("name status hook: %w", err)
	}
	name := fmt.Sprintf("%020d-%s.json", signal.ReceivedAt.UnixNano(), hex.EncodeToString(random))
	path := filepath.Join(service.signalDir(sessionID), name)
	if err := replaceFile(path, contents); err != nil {
		return err
	}
	if observation, ok := structuredObservation(provider, signal); ok {
		return service.recordTransition(sessionID, observation)
	}
	return nil
}

func (s *Service) Observe(ctx context.Context, kind, sessionID string, screen func(context.Context) ([]byte, error), process ProcessEvidence) (Observation, error) {
	if _, ok := providers[kind]; !ok {
		return Observation{}, fmt.Errorf("unsupported agent status provider %q", kind)
	}
	if err := validateSessionID(sessionID); err != nil {
		return Observation{}, err
	}
	if process.State == ProcessRunning {
		if signals, err := s.loadSignals(sessionID); err != nil {
			return Observation{}, err
		} else {
			for index := len(signals) - 1; index >= 0; index-- {
				if observation, ok := structuredObservation(kind, signals[index]); ok {
					return observation, s.recordTransition(sessionID, observation)
				}
			}
		}
	}
	now := s.now().UTC()
	if process.State == ProcessRunning && screen != nil {
		contents, err := screen(ctx)
		if err == nil {
			if observation, ok := screenObservation(kind, contents, now); ok {
				return observation, s.recordTransition(sessionID, observation)
			}
		}
	}
	observation := processObservation(kind, process, now)
	return observation, s.recordTransition(sessionID, observation)
}

func (s *Service) Transitions(sessionID string) ([]Observation, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, err
	}
	path := s.transitionPath(sessionID)
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return []Observation{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open agent status transitions: %w", err)
	}
	defer file.Close()
	result := make([]Observation, 0)
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2<<20)
	for scanner.Scan() {
		var observation Observation
		if err := json.Unmarshal(scanner.Bytes(), &observation); err != nil {
			return nil, fmt.Errorf("decode agent status transition: %w", err)
		}
		result = append(result, observation)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read agent status transitions: %w", err)
	}
	return result, nil
}

func (s *Service) Remove(sessionID string) error {
	if err := validateSessionID(sessionID); err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Join(s.dir, sessionID)); err != nil {
		return fmt.Errorf("remove agent status evidence: %w", err)
	}
	return nil
}

func (s *Service) loadSignals(sessionID string) ([]storedSignal, error) {
	entries, err := os.ReadDir(s.signalDir(sessionID))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read structured status signals: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	result := make([]storedSignal, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		contents, err := os.ReadFile(filepath.Join(s.signalDir(sessionID), entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read structured status signal: %w", err)
		}
		var signal storedSignal
		if err := json.Unmarshal(contents, &signal); err != nil {
			return nil, fmt.Errorf("decode structured status signal: %w", err)
		}
		result = append(result, signal)
	}
	return result, nil
}

func (s *Service) recordTransition(sessionID string, observation Observation) error {
	transitions, err := s.Transitions(sessionID)
	if err != nil {
		return err
	}
	if len(transitions) > 0 && sameObservation(transitions[len(transitions)-1], observation) {
		return nil
	}
	contents, err := json.Marshal(observation)
	if err != nil {
		return fmt.Errorf("encode agent status transition: %w", err)
	}
	path := s.transitionPath(sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open agent status transitions: %w", err)
	}
	if _, err := file.Write(append(contents, '\n')); err != nil {
		file.Close()
		return fmt.Errorf("append agent status transition: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close agent status transitions: %w", err)
	}
	return nil
}

func (s *Service) signalDir(sessionID string) string {
	return filepath.Join(s.dir, sessionID, "signals")
}

func (s *Service) transitionPath(sessionID string) string {
	return filepath.Join(s.dir, sessionID, "transitions.jsonl")
}

func sameObservation(left, right Observation) bool {
	return left.Provider == right.Provider && left.State == right.State &&
		left.Evidence.Source == right.Evidence.Source
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func validateSessionID(value string) error {
	if value == "" || value == "." || value == ".." || filepath.Base(value) != value {
		return errors.New("invalid agent status session id")
	}
	return nil
}

func replaceFile(path string, contents []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".status-*.tmp")
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
