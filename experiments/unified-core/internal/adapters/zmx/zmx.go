// Package zmx owns an isolated zmx directory for the prototype. Public
// Terminal IDs are used directly as session names so a human can attach with
// zmx itself; legacy prototype names remain visible only for state migration.
package zmx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/creack/pty"
	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
	"github.com/elevenideas/atc/experiments/unified-core/internal/provider"
)

type Config struct {
	Executable        string
	WrapperExecutable string
	SocketDir         string
	LogDir            string
	HookBaseURL       string
	CodexRemote       string
	Models            map[domain.Agent]string
	Efforts           map[domain.Agent]string
	PollInterval      time.Duration
	VerifyPasses      int
}

type Adapter struct {
	executable        string
	wrapperExecutable string
	socketDir         string
	logDir            string
	hookBaseURL       string
	codexRemote       string
	models            map[domain.Agent]string
	efforts           map[domain.Agent]string
	pollInterval      time.Duration
	verifyPasses      int
}

func New(config Config) (*Adapter, error) {
	executable, err := exec.LookPath(config.Executable)
	if err != nil {
		return nil, fmt.Errorf("find zmx: %w", err)
	}
	wrapper, err := filepath.Abs(config.WrapperExecutable)
	if err != nil {
		return nil, fmt.Errorf("resolve wrapper executable: %w", err)
	}
	socketDir, err := filepath.Abs(config.SocketDir)
	if err != nil {
		return nil, err
	}
	logDir, err := filepath.Abs(config.LogDir)
	if err != nil {
		return nil, err
	}
	for _, dir := range []string{socketDir, logDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := os.Chmod(dir, 0o700); err != nil {
			return nil, err
		}
	}
	pollInterval := config.PollInterval
	if pollInterval <= 0 {
		pollInterval = 100 * time.Millisecond
	}
	verifyPasses := config.VerifyPasses
	if verifyPasses <= 0 {
		verifyPasses = 40
	}
	return &Adapter{
		executable: executable, wrapperExecutable: wrapper, socketDir: socketDir,
		logDir: logDir, hookBaseURL: strings.TrimRight(config.HookBaseURL, "/"),
		codexRemote: config.CodexRemote, models: config.Models, efforts: config.Efforts,
		pollInterval: pollInterval, verifyPasses: verifyPasses,
	}, nil
}

func (a *Adapter) Open(ctx context.Context, open ports.TerminalOpen) error {
	if !isManagedName(open.TerminalID) {
		return errors.New("refusing terminal name outside prototype namespace")
	}
	if _, err := os.Stat(open.CWD); err != nil {
		return fmt.Errorf("inspect working directory: %w", err)
	}
	existing, err := a.find(ctx, open.TerminalID)
	if err != nil {
		return err
	}
	if existing != nil {
		return fmt.Errorf("terminal %s already exists", open.TerminalID)
	}
	command, err := a.providerCommand(open)
	if err != nil {
		return err
	}
	wrapped := append([]string{a.wrapperExecutable, "__child", "--marker", open.ExitPath, "--terminal", open.TerminalID, "--"}, command...)
	create := exec.CommandContext(ctx, a.executable, append([]string{"attach", open.TerminalID}, wrapped...)...)
	create.Dir = open.CWD
	create.Env = a.environment(nil)
	ptmx, err := pty.StartWithSize(create, &pty.Winsize{Rows: 24, Cols: 100})
	if err != nil {
		return fmt.Errorf("start zmx creation client: %w", err)
	}
	drained := make(chan struct{})
	var tail limitedBuffer
	go func() {
		_, _ = io.Copy(&tail, ptmx)
		close(drained)
	}()
	reachable, pollErr := a.poll(ctx, func(entries []ports.TerminalEntry) bool {
		for _, entry := range entries {
			if entry.Name == open.TerminalID && entry.Reachable {
				return true
			}
		}
		return false
	})
	_ = ptmx.Close()
	waitErr := create.Wait()
	select {
	case <-drained:
	case <-ctx.Done():
	}
	if pollErr != nil {
		return pollErr
	}
	if reachable {
		return nil
	}
	if detail := strings.TrimSpace(tail.String()); detail != "" {
		return fmt.Errorf("terminal did not settle: %s", detail)
	}
	return fmt.Errorf("terminal did not settle: %v", waitErr)
}

func (a *Adapter) Inventory(ctx context.Context) ([]ports.TerminalEntry, error) {
	stdout, stderr, err := a.run(ctx, nil, "list")
	if err != nil {
		return nil, fmt.Errorf("zmx inventory unavailable: %w: %s", err, strings.TrimSpace(string(stderr)))
	}
	return ParseInventory(string(stdout)), nil
}

func ParseInventory(output string) []ports.TerminalEntry {
	entries := make([]ports.TerminalEntry, 0)
	for line := range strings.Lines(output) {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "→"))
		fields := make(map[string]string)
		for field := range strings.SplitSeq(line, "\t") {
			key, value, ok := strings.Cut(field, "=")
			if ok {
				fields[strings.TrimSpace(key)] = value
			}
		}
		name := fields["name"]
		if !isManagedName(name) {
			continue
		}
		pid, _ := strconv.Atoi(fields["pid"])
		_, unreachable := fields["err"]
		entries = append(entries, ports.TerminalEntry{Name: name, Reachable: !unreachable, DaemonPID: pid})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name < entries[j].Name })
	return entries
}

func (a *Adapter) Terminate(ctx context.Context, name string) error {
	if !isManagedName(name) {
		return errors.New("refusing to terminate outside prototype namespace")
	}
	existing, err := a.find(ctx, name)
	if err != nil || existing == nil {
		return err
	}
	_, _, _ = a.run(ctx, nil, "kill", name)
	gone, err := a.poll(ctx, func(entries []ports.TerminalEntry) bool {
		for _, entry := range entries {
			if entry.Name == name {
				return false
			}
		}
		return true
	})
	if err != nil {
		return err
	}
	if !gone {
		return errors.New("zmx kill returned but verified absence was not observed")
	}
	return nil
}

func (a *Adapter) providerCommand(open ports.TerminalOpen) ([]string, error) {
	if len(open.Command) > 0 {
		return append([]string(nil), open.Command...), nil
	}
	model := a.models[open.Agent]
	effort := a.efforts[open.Agent]
	if err := provider.ValidateSelection(open.Agent, model, effort); err != nil {
		return nil, err
	}
	switch open.Agent {
	case domain.AgentCodex:
		if a.codexRemote == "" {
			return nil, errors.New("Codex TUI requires the shared --codex-remote endpoint")
		}
		if a.hookBaseURL == "" {
			return nil, errors.New("Codex TUI requires the core status ingestion URL")
		}
		return []string{
			a.wrapperExecutable, "__codex_tui", "--remote", a.codexRemote,
			"--status-url", a.hookBaseURL + "/internal/hooks/codex/terminal/" + open.TerminalID,
			"--cwd", open.CWD, "--", "codex", "--model", model,
			"--config", "model_reasoning_effort=\"" + effort + "\"",
		}, nil
	case domain.AgentClaude:
		sessionID := stableUUID(open.TerminalID)
		command := []string{"claude", "--session-id", sessionID, "--model", model, "--effort", effort}
		if a.hookBaseURL != "" {
			settings, err := claudeHookSettings(a.hookBaseURL, open.TerminalID)
			if err != nil {
				return nil, err
			}
			command = append(command, "--settings", settings)
		}
		return command, nil
	default:
		return nil, fmt.Errorf("unsupported TUI agent %s", open.Agent)
	}
}

func claudeHookSettings(baseURL, terminalID string) (string, error) {
	endpoint := baseURL + "/internal/hooks/claude/terminal/" + terminalID
	hook := map[string]any{"type": "command", "command": "curl -fsS -X POST --data-binary @- " + endpoint}
	names := []string{"SessionStart", "UserPromptSubmit", "PreToolUse", "PostToolUse", "PermissionRequest", "Notification", "Stop", "StopFailure", "SessionEnd", "SubagentStart", "SubagentStop"}
	hooks := make(map[string]any, len(names))
	for _, name := range names {
		hooks[name] = []any{map[string]any{"hooks": []any{hook}}}
	}
	encoded, err := json.Marshal(map[string]any{"hooks": hooks})
	return string(encoded), err
}

func stableUUID(value string) string {
	digest := sha256.Sum256([]byte(value))
	hex := fmt.Sprintf("%x", digest[:16])
	return hex[:8] + "-" + hex[8:12] + "-4" + hex[13:16] + "-a" + hex[17:20] + "-" + hex[20:32]
}

func (a *Adapter) find(ctx context.Context, name string) (*ports.TerminalEntry, error) {
	entries, err := a.Inventory(ctx)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.Name == name {
			copy := entry
			return &copy, nil
		}
	}
	return nil, nil
}

func isManagedName(name string) bool {
	return strings.HasPrefix(name, "term_") || strings.HasPrefix(name, "atcu-") || strings.HasPrefix(name, "atc-unified-")
}

func (a *Adapter) poll(ctx context.Context, predicate func([]ports.TerminalEntry) bool) (bool, error) {
	for pass := 0; pass < a.verifyPasses; pass++ {
		entries, err := a.Inventory(ctx)
		if err != nil {
			return false, err
		}
		if predicate(entries) {
			return true, nil
		}
		if pass+1 == a.verifyPasses {
			break
		}
		timer := time.NewTimer(a.pollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return false, ctx.Err()
		case <-timer.C:
		}
	}
	return false, nil
}

func (a *Adapter) run(ctx context.Context, input io.Reader, args ...string) ([]byte, []byte, error) {
	command := exec.CommandContext(ctx, a.executable, args...)
	command.Env = a.environment(nil)
	command.Stdin = input
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	return stdout.Bytes(), stderr.Bytes(), err
}

func (a *Adapter) environment(overlay map[string]string) []string {
	values := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	for _, key := range []string{"ZMX_SESSION", "ZMX_SESSION_PREFIX", "CLAUDECODE", "CODEX_THREAD_ID"} {
		delete(values, key)
	}
	values["ZMX_DIR"] = a.socketDir
	values["ZMX_LOG_DIR"] = a.logDir
	values["TERM"] = "xterm-256color"
	for key, value := range overlay {
		values[key] = value
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		result = append(result, key+"="+value)
	}
	sort.Strings(result)
	return result
}

type limitedBuffer struct{ data []byte }

func (b *limitedBuffer) Write(data []byte) (int, error) {
	b.data = append(b.data, data...)
	if len(b.data) > 8<<10 {
		b.data = append([]byte(nil), b.data[len(b.data)-(8<<10):]...)
	}
	return len(data), nil
}

func (b *limitedBuffer) String() string { return string(b.data) }
