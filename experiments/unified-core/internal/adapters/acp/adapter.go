package acp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
)

type Command struct {
	Path string
	Args []string
	Env  []string
}

type Config struct {
	Commands map[domain.Agent]Command
	Stderr   io.Writer
}

type Adapter struct {
	commands map[domain.Agent]Command
	stderr   io.Writer
}

func New(config Config) *Adapter {
	commands := config.Commands
	if commands == nil {
		commands = map[domain.Agent]Command{
			domain.AgentCodex:  {Path: "npx", Args: []string{"-y", "@agentclientprotocol/codex-acp@1.6.2"}},
			domain.AgentClaude: {Path: "npx", Args: []string{"-y", "@agentclientprotocol/claude-agent-acp@0.70.0"}},
		}
	}
	stderr := config.Stderr
	if stderr == nil {
		stderr = io.Discard
	}
	return &Adapter{commands: commands, stderr: stderr}
}

func (a *Adapter) Open(ctx context.Context, open ports.ChatOpen) (ports.ChatSession, string, error) {
	command, ok := a.commands[open.Agent]
	if !ok {
		return nil, "", fmt.Errorf("no ACP command configured for %s", open.Agent)
	}
	session := &Session{
		threadID: open.ThreadID, events: open.Events, pending: make(map[string]pendingPermission),
		activeTools: make(map[string]bool), wait: make(chan error, 1),
	}
	cmd := exec.Command(command.Path, command.Args...)
	cmd.Dir = open.CWD
	cmd.Env = scrubEnvironment(command.Env)
	cmd.Stderr = a.stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, "", err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	if err := cmd.Start(); err != nil {
		return nil, "", fmt.Errorf("start ACP adapter: %w", err)
	}
	session.command = cmd
	session.connection = newConnection(stdout, stdin, session)
	session.connection.start()
	go func() { session.wait <- cmd.Wait() }()

	var initialized struct {
		ProtocolVersion   int             `json:"protocolVersion"`
		AgentCapabilities json.RawMessage `json:"agentCapabilities"`
	}
	initialize := map[string]any{
		"protocolVersion":    protocolVersion,
		"clientCapabilities": map[string]any{},
		"clientInfo":         map[string]string{"name": "atc-unified-core", "title": "ATC unified core experiment", "version": "0.1.0"},
	}
	if err := session.connection.call(ctx, "initialize", initialize, &initialized); err != nil {
		_ = session.stop(context.Background())
		return nil, "", err
	}
	if initialized.ProtocolVersion != protocolVersion {
		_ = session.stop(context.Background())
		return nil, "", fmt.Errorf("ACP v%d negotiated; v1 required", initialized.ProtocolVersion)
	}
	identity := open.ProviderSession
	if identity == "" {
		var created struct {
			SessionID string `json:"sessionId"`
		}
		request := map[string]any{"cwd": open.CWD, "mcpServers": []any{}}
		if err := session.connection.call(ctx, "session/new", request, &created); err != nil {
			_ = session.stop(context.Background())
			return nil, "", err
		}
		if created.SessionID == "" {
			_ = session.stop(context.Background())
			return nil, "", errors.New("session/new returned no identity")
		}
		identity = created.SessionID
	} else {
		request := map[string]any{"sessionId": identity, "cwd": open.CWD, "mcpServers": []any{}}
		// Exact load is deliberately distinct from create and has no fallback.
		if err := session.connection.call(ctx, "session/load", request, nil); err != nil {
			_ = session.stop(context.Background())
			return nil, "", fmt.Errorf("load exact ACP session: %w", err)
		}
	}
	session.sessionID = identity
	return session, identity, nil
}

type pendingPermission struct {
	id      json.RawMessage
	options map[string]string
}

type Session struct {
	mu sync.Mutex

	threadID    string
	sessionID   string
	events      ports.ChatEvents
	connection  *connection
	command     *exec.Cmd
	wait        chan error
	pending     map[string]pendingPermission
	activeTools map[string]bool
	activeTurn  string
	closed      bool
}

func (s *Session) Prompt(ctx context.Context, turnID, text string) (domain.TurnOutcome, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return domain.TurnFailed, errors.New("ACP session is closed")
	}
	s.activeTurn = turnID
	s.mu.Unlock()
	pending, err := s.connection.begin("session/prompt", map[string]any{
		"sessionId": s.sessionID,
		"prompt":    []map[string]string{{"type": "text", "text": text}},
	})
	if err != nil {
		s.clearActiveTurn(turnID)
		return domain.TurnFailed, err
	}
	var response struct {
		StopReason string `json:"stopReason"`
	}
	if err := pending.await(ctx, &response); err != nil {
		s.clearActiveTurn(turnID)
		return domain.TurnFailed, err
	}
	s.clearActiveTurn(turnID)
	switch response.StopReason {
	case "cancelled":
		return domain.TurnInterrupted, nil
	case "end_turn", "max_tokens", "refusal", "":
		return domain.TurnCompleted, nil
	default:
		return domain.TurnCompleted, nil
	}
}

func (s *Session) Interrupt(_ context.Context, _ string) error {
	return s.connection.notify("session/cancel", map[string]string{"sessionId": s.sessionID})
}

func (s *Session) Answer(_ context.Context, providerRef, answer string) error {
	s.mu.Lock()
	pending, ok := s.pending[providerRef]
	s.mu.Unlock()
	if !ok {
		return errors.New("ACP request is no longer pending")
	}
	optionID, ok := pending.options[answer]
	if !ok {
		return fmt.Errorf("unknown request option %q", answer)
	}
	s.mu.Lock()
	if _, stillPending := s.pending[providerRef]; !stillPending {
		s.mu.Unlock()
		return errors.New("ACP request is already being answered")
	}
	delete(s.pending, providerRef)
	s.mu.Unlock()
	if err := s.connection.respond(pending.id, map[string]any{
		"outcome": map[string]string{"outcome": "selected", "optionId": optionID},
	}); err != nil {
		s.mu.Lock()
		s.pending[providerRef] = pending
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *Session) Close(ctx context.Context) error {
	if s.sessionID != "" {
		_ = s.connection.call(ctx, "session/close", map[string]string{"sessionId": s.sessionID}, &struct{}{})
	}
	return s.stop(ctx)
}

func (s *Session) stop(ctx context.Context) error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	s.mu.Unlock()
	closeErr := s.connection.close()
	select {
	case waitErr := <-s.wait:
		return errors.Join(closeErr, waitErr)
	case <-ctx.Done():
		if s.command != nil && s.command.Process != nil {
			_ = s.command.Process.Kill()
		}
		select {
		case <-s.wait:
		case <-time.After(time.Second):
		}
		return ctx.Err()
	}
}

func (s *Session) request(request incomingRequest) {
	if request.Method != "session/request_permission" {
		_ = s.connection.respondError(request.ID, -32601, "method not supported by ATC unified-core experiment")
		return
	}
	var permission struct {
		SessionID string `json:"sessionId"`
		ToolCall  struct {
			ToolCallID string `json:"toolCallId"`
			Title      string `json:"title"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	if json.Unmarshal(request.Params, &permission) != nil || permission.SessionID != s.sessionID {
		_ = s.connection.respondError(request.ID, -32602, "invalid permission request")
		return
	}
	providerRef := idKey(request.ID)
	publicOptions := make([]domain.RequestOption, 0, len(permission.Options))
	optionMap := make(map[string]string, len(permission.Options))
	for index, option := range permission.Options {
		id := fmt.Sprintf("option_%d", index+1)
		publicOptions = append(publicOptions, domain.RequestOption{ID: id, Label: option.Name})
		optionMap[id] = option.OptionID
	}
	s.mu.Lock()
	s.pending[providerRef] = pendingPermission{id: cloneRaw(request.ID), options: optionMap}
	s.mu.Unlock()
	kind := domain.RequestApproval
	if strings.Contains(strings.ToLower(permission.ToolCall.Title), "question") {
		kind = domain.RequestQuestion
	}
	s.mu.Lock()
	turnID := s.activeTurn
	s.mu.Unlock()
	s.events.Request(s.threadID, turnID, providerRef, kind, permission.ToolCall.Title, publicOptions)
}

func (s *Session) notification(method string, params json.RawMessage) {
	if method != "session/update" {
		return
	}
	var update struct {
		SessionID string `json:"sessionId"`
		Update    struct {
			Kind       string          `json:"sessionUpdate"`
			ToolCallID string          `json:"toolCallId"`
			Status     string          `json:"status"`
			Content    json.RawMessage `json:"content"`
		} `json:"update"`
	}
	if json.Unmarshal(params, &update) != nil || update.SessionID != s.sessionID {
		return
	}
	switch update.Update.Kind {
	case "agent_message_chunk":
		var content struct {
			Type string `json:"type"`
			Text string `json:"text"`
		}
		if json.Unmarshal(update.Update.Content, &content) == nil && content.Type == "text" {
			s.mu.Lock()
			turnID := s.activeTurn
			s.mu.Unlock()
			s.events.AssistantText(s.threadID, turnID, content.Text)
		}
	case "tool_call", "tool_call_update":
		s.trackTool(update.Update.ToolCallID, update.Update.Status)
	}
}

func (s *Session) trackTool(id, status string) {
	if id == "" {
		return
	}
	s.mu.Lock()
	switch status {
	case "completed", "failed", "cancelled":
		delete(s.activeTools, id)
	default:
		s.activeTools[id] = true
	}
	active := len(s.activeTools) > 0
	s.mu.Unlock()
	activity := domain.ActivityIdle
	if active {
		activity = domain.ActivityWorking
	}
	s.events.BackgroundActivity(s.threadID, activity)
}

func (s *Session) raw(direction string, raw json.RawMessage) {
	s.events.Raw(s.threadID, "acp."+direction, raw)
}

func (s *Session) disconnected(err error) {
	s.mu.Lock()
	closed := s.closed
	s.mu.Unlock()
	if !closed {
		s.events.Raw(s.threadID, "acp.disconnected", mustJSON(map[string]string{"error": err.Error()}))
		s.events.BackgroundActivity(s.threadID, domain.ActivityUnknown)
	}
}

func (s *Session) clearActiveTurn(turnID string) {
	s.mu.Lock()
	if s.activeTurn == turnID {
		s.activeTurn = ""
	}
	s.mu.Unlock()
}

func scrubEnvironment(overlay []string) []string {
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
	for _, entry := range overlay {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			values[key] = value
		}
	}
	environment := make([]string, 0, len(values))
	for key, value := range values {
		environment = append(environment, key+"="+value)
	}
	sort.Strings(environment)
	return environment
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
