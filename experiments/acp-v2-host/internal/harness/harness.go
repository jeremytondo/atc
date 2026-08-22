package harness

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/acp-v2-host/internal/acp"
)

type Status string

const (
	StatusConnecting           Status = "connecting"
	StatusIdle                 Status = "idle"
	StatusWorking              Status = "working"
	StatusWaitingForPermission Status = "waiting_for_permission"
	StatusWaitingForInput      Status = "waiting_for_input"
	StatusFailed               Status = "failed"
	StatusDisconnected         Status = "disconnected"
)

type Snapshot struct {
	Status         Status `json:"status"`
	LastOutcome    string `json:"lastOutcome,omitempty"`
	LastStopReason string `json:"lastStopReason,omitempty"`
	LastError      string `json:"lastError,omitempty"`
	SessionID      string `json:"sessionId,omitempty"`
	Provider       string `json:"provider"`
	Pending        int    `json:"pendingPermissions"`
}

type Event struct {
	Kind string
	Text string
	Raw  json.RawMessage
}

type PendingPermission struct {
	ID      json.RawMessage       `json:"id"`
	Request acp.PermissionRequest `json:"request"`
}

type Config struct {
	Provider       string
	Command        acp.Command
	CWD            string
	StatePath      string
	ReplayOnResume bool
	ForceNew       bool
	ProbeOnly      bool
	RawVisible     bool
	Decision       string
	Logger         *JSONLLogger
	Stderr         io.Writer
}

type Harness struct {
	mu sync.Mutex

	config     Config
	client     *acp.Client
	process    *acp.Process
	metadata   Metadata
	snapshot   Snapshot
	pending    map[string]PendingPermission
	rawVisible bool
	stopping   bool
	events     chan Event
}

func New(config Config) *Harness {
	return &Harness{
		config: config,
		snapshot: Snapshot{
			Status:   StatusConnecting,
			Provider: config.Provider,
		},
		pending:    make(map[string]PendingPermission),
		rawVisible: config.RawVisible,
		events:     make(chan Event, 128),
	}
}

func (h *Harness) Events() <-chan Event {
	return h.events
}

func (h *Harness) Start(ctx context.Context) error {
	process, err := acp.Launch(h.config.Command, h, h.config.Stderr)
	if err != nil {
		return err
	}
	h.mu.Lock()
	h.process = process
	h.client = acp.NewClient(process.Connection())
	h.mu.Unlock()
	h.log("process", "agent_started", "", rawData(map[string]any{
		"pid":     process.PID(),
		"command": h.config.Command.Path,
		"args":    h.config.Command.Args,
	}))

	initialize, err := h.client.Initialize(ctx)
	if err != nil {
		h.fail("initialize", err)
		return err
	}
	metadata := Metadata{
		Provider:        h.config.Provider,
		Command:         h.config.Command.Path,
		Args:            append([]string(nil), h.config.Command.Args...),
		CWD:             h.config.CWD,
		ProtocolVersion: initialize.ProtocolVersion,
		AgentInfo:       initialize.Info,
		Capabilities:    cloneRaw(initialize.Capabilities),
	}
	h.mu.Lock()
	h.metadata = metadata
	h.mu.Unlock()
	h.log("normalized", "initialized", StatusConnecting, rawData(initialize))
	if h.config.ProbeOnly {
		h.transition(StatusIdle, "protocol_v2_supported", "", "")
		return nil
	}

	stored, err := LoadMetadata(h.config.StatePath)
	if err != nil {
		h.fail("load_state", err)
		return err
	}
	canResume := !h.config.ForceNew && stored.SessionID != "" && stored.Provider == h.config.Provider && stored.CWD == h.config.CWD
	if canResume {
		h.mu.Lock()
		h.metadata.SessionID = stored.SessionID
		h.snapshot.SessionID = stored.SessionID
		h.mu.Unlock()
		if _, err := h.client.ResumeSession(ctx, stored.SessionID, h.config.CWD, h.config.ReplayOnResume); err != nil {
			h.fail("resume_session", err)
			return fmt.Errorf("resume exact session %s: %w", stored.SessionID, err)
		}
		h.log("normalized", "session_resumed", StatusIdle, rawData(map[string]any{"replay": h.config.ReplayOnResume}))
	} else {
		created, err := h.client.NewSession(ctx, h.config.CWD)
		if err != nil {
			h.fail("new_session", err)
			return err
		}
		h.mu.Lock()
		h.metadata.SessionID = created.SessionID
		h.snapshot.SessionID = created.SessionID
		h.mu.Unlock()
		h.log("normalized", "session_created", StatusIdle, rawData(created))
	}
	if err := SaveMetadata(h.config.StatePath, h.Metadata()); err != nil {
		h.fail("save_state", err)
		return err
	}
	h.transition(StatusIdle, "session_ready", "", "")
	return nil
}

func (h *Harness) Prompt(ctx context.Context, prompt string) error {
	h.mu.Lock()
	if h.snapshot.Status != StatusIdle {
		status := h.snapshot.Status
		h.mu.Unlock()
		return fmt.Errorf("cannot prompt while status is %s", status)
	}
	sessionID := h.snapshot.SessionID
	h.mu.Unlock()
	if sessionID == "" {
		return errors.New("no active session")
	}
	h.mu.Lock()
	h.snapshot.LastOutcome = ""
	h.snapshot.LastStopReason = ""
	h.snapshot.LastError = ""
	h.mu.Unlock()
	h.transition(StatusWorking, "prompt_submitted", "", "")
	if err := h.client.Prompt(ctx, sessionID, prompt); err != nil {
		h.fail("prompt_rejected", err)
		return err
	}
	h.log("normalized", "prompt_accepted", StatusWorking, nil)
	return nil
}

func (h *Harness) Cancel() error {
	h.mu.Lock()
	if h.snapshot.SessionID == "" {
		h.mu.Unlock()
		return errors.New("no active session")
	}
	sessionID := h.snapshot.SessionID
	pending := make([]PendingPermission, 0, len(h.pending))
	for _, permission := range h.pending {
		pending = append(pending, permission)
	}
	h.pending = make(map[string]PendingPermission)
	h.snapshot.Pending = 0
	h.mu.Unlock()
	for _, permission := range pending {
		if err := h.client.CancelPermission(permission.ID); err != nil {
			return err
		}
	}
	if err := h.client.Cancel(sessionID); err != nil {
		return err
	}
	h.log("normalized", "cancel_requested", StatusWorking, nil)
	return nil
}

func (h *Harness) Decide(permissionID, decision string) error {
	h.mu.Lock()
	key, permission, err := h.findPermissionLocked(permissionID)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	option, err := selectPermissionOption(permission.Request.Options, decision)
	if err != nil {
		h.mu.Unlock()
		return err
	}
	delete(h.pending, key)
	h.snapshot.Pending = len(h.pending)
	h.mu.Unlock()
	if err := h.client.RespondPermission(permission.ID, option.OptionID); err != nil {
		return err
	}
	h.log("normalized", "permission_"+decision, StatusWorking, rawData(map[string]string{
		"requestId": key,
		"optionId":  option.OptionID,
	}))
	return nil
}

func (h *Harness) PendingPermissions() []PendingPermission {
	h.mu.Lock()
	defer h.mu.Unlock()
	permissions := make([]PendingPermission, 0, len(h.pending))
	for _, permission := range h.pending {
		permission.ID = cloneRaw(permission.ID)
		permissions = append(permissions, permission)
	}
	return permissions
}

func (h *Harness) Snapshot() Snapshot {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.snapshot
}

func (h *Harness) Metadata() Metadata {
	h.mu.Lock()
	defer h.mu.Unlock()
	metadata := h.metadata
	metadata.Args = append([]string(nil), metadata.Args...)
	metadata.Capabilities = cloneRaw(metadata.Capabilities)
	return metadata
}

func (h *Harness) SetRawVisible(visible bool) {
	h.mu.Lock()
	h.rawVisible = visible
	h.mu.Unlock()
}

func (h *Harness) Stop(ctx context.Context) error {
	h.mu.Lock()
	if h.stopping {
		h.mu.Unlock()
		return nil
	}
	h.stopping = true
	client := h.client
	process := h.process
	sessionID := h.snapshot.SessionID
	h.mu.Unlock()

	var closeErr error
	if client != nil && sessionID != "" && !h.config.ProbeOnly {
		if err := client.CloseSession(ctx, sessionID); err != nil {
			closeErr = fmt.Errorf("close session: %w", err)
		} else {
			h.log("normalized", "session_closed", StatusIdle, nil)
		}
	}
	if process != nil {
		if err := process.Stop(ctx); err != nil && closeErr == nil {
			closeErr = fmt.Errorf("stop agent: %w", err)
		}
	}
	h.transition(StatusDisconnected, "host_stopped", "", "")
	return closeErr
}

func (h *Harness) HandleRaw(traffic acp.RawTraffic) {
	h.log("raw", "jsonrpc", "", traffic.Message, traffic.Direction)
	h.mu.Lock()
	visible := h.rawVisible
	h.mu.Unlock()
	if visible {
		h.emit(Event{Kind: "raw", Text: traffic.Direction, Raw: cloneRaw(traffic.Message)})
	}
}

func (h *Harness) HandleRequest(request acp.IncomingRequest) {
	if request.Method != "session/request_permission" {
		if err := h.client.RejectUnknownRequest(request); err != nil {
			h.fail("reject_request", err)
		}
		return
	}
	var permission acp.PermissionRequest
	if err := json.Unmarshal(request.Params, &permission); err != nil {
		_ = h.client.RespondError(request.ID, -32602, "invalid permission request")
		h.fail("permission_decode", err)
		return
	}
	if permission.SessionID != h.Snapshot().SessionID {
		_ = h.client.RespondError(request.ID, -32602, "permission request belongs to another session")
		return
	}
	pending := PendingPermission{ID: cloneRaw(request.ID), Request: permission}
	key := string(request.ID)
	h.mu.Lock()
	h.pending[key] = pending
	h.snapshot.Pending = len(h.pending)
	decision := h.config.Decision
	h.mu.Unlock()
	h.log("normalized", "permission_requested", StatusWaitingForPermission, rawData(pending))
	h.transition(StatusWaitingForPermission, "permission_requested", "", "")
	h.emit(Event{Kind: "permission", Text: formatPermission(key, permission)})
	if decision == "allow" || decision == "deny" {
		if err := h.Decide(key, decision); err != nil {
			h.fail("automatic_permission", err)
		}
	}
}

func (h *Harness) HandleNotification(notification acp.Notification) {
	if notification.Method != "session/update" {
		h.log("normalized", "notification", h.Snapshot().Status, rawData(notification))
		return
	}
	var params acp.SessionUpdateParams
	if err := json.Unmarshal(notification.Params, &params); err != nil {
		h.fail("update_decode", err)
		return
	}
	if params.SessionID != h.Snapshot().SessionID {
		h.log("normalized", "foreign_session_update", h.Snapshot().Status, notification.Params)
		return
	}
	var update acp.SessionUpdate
	if err := json.Unmarshal(params.Update, &update); err != nil {
		h.fail("update_decode", err)
		return
	}
	h.log("normalized", update.Kind, h.Snapshot().Status, params.Update)
	h.applyUpdate(update)
}

func (h *Harness) HandleDisconnect(err error) {
	h.mu.Lock()
	stopping := h.stopping
	h.mu.Unlock()
	if stopping {
		return
	}
	if errors.Is(err, io.EOF) {
		h.transition(StatusDisconnected, "agent_disconnected", "", "agent exited")
		return
	}
	h.transition(StatusDisconnected, "agent_disconnected", "", err.Error())
}

func (h *Harness) applyUpdate(update acp.SessionUpdate) {
	switch update.Kind {
	case "state_update":
		switch update.State {
		case "running":
			h.transition(StatusWorking, "state_changed", "", "")
		case "requires_action":
			status := StatusWaitingForInput
			if h.Snapshot().Pending > 0 {
				status = StatusWaitingForPermission
			}
			h.transition(status, "state_changed", "", "")
		case "idle":
			outcome := "completed"
			if update.StopReason == "cancelled" {
				outcome = "cancelled"
			}
			h.transition(StatusIdle, "turn_finished", outcome, update.StopReason)
		default:
			h.emit(Event{Kind: "activity", Text: "unknown ACP state: " + update.State})
		}
	case "agent_message", "agent_message_chunk":
		if text := contentText(update.Content); text != "" {
			h.emit(Event{Kind: "assistant", Text: text})
		}
	case "agent_thought", "agent_thought_chunk":
		if text := contentText(update.Content); text != "" {
			h.emit(Event{Kind: "activity", Text: text})
		}
	case "tool_call", "tool_call_update":
		label := strings.TrimSpace(update.Title)
		if label == "" {
			label = update.ToolCallID
		}
		h.emit(Event{Kind: "activity", Text: fmt.Sprintf("tool %s: %s", label, update.Status)})
	default:
		h.emit(Event{Kind: "activity", Text: update.Kind})
	}
}

func (h *Harness) transition(status Status, kind, outcome, stopReason string) {
	h.mu.Lock()
	h.snapshot.Status = status
	if outcome != "" {
		h.snapshot.LastOutcome = outcome
	}
	if stopReason != "" {
		h.snapshot.LastStopReason = stopReason
	}
	snapshot := h.snapshot
	h.mu.Unlock()
	h.log("normalized", kind, status, rawData(snapshot))
	h.emit(Event{Kind: "status", Text: string(status)})
}

func (h *Harness) fail(kind string, err error) {
	h.mu.Lock()
	h.snapshot.Status = StatusFailed
	h.snapshot.LastOutcome = "failed"
	h.snapshot.LastError = err.Error()
	snapshot := h.snapshot
	h.mu.Unlock()
	h.log("normalized", kind, StatusFailed, rawData(snapshot))
	h.emit(Event{Kind: "error", Text: err.Error()})
}

func (h *Harness) emit(event Event) {
	select {
	case h.events <- event:
	default:
	}
}

func (h *Harness) log(layer, kind string, status Status, data json.RawMessage, direction ...string) {
	if h.config.Logger == nil {
		return
	}
	record := LogRecord{
		Layer:     layer,
		Kind:      kind,
		SessionID: h.Snapshot().SessionID,
		Status:    string(status),
		Data:      cloneRaw(data),
	}
	if len(direction) > 0 {
		record.Direction = direction[0]
	}
	_ = h.config.Logger.Write(record)
}

func (h *Harness) findPermissionLocked(permissionID string) (string, PendingPermission, error) {
	if permissionID != "" {
		permission, ok := h.pending[permissionID]
		if !ok {
			return "", PendingPermission{}, fmt.Errorf("no pending permission %s", permissionID)
		}
		return permissionID, permission, nil
	}
	if len(h.pending) != 1 {
		return "", PendingPermission{}, fmt.Errorf("expected one pending permission, found %d; specify its request ID", len(h.pending))
	}
	for key, permission := range h.pending {
		return key, permission, nil
	}
	panic("unreachable")
}

func selectPermissionOption(options []acp.PermissionOption, decision string) (acp.PermissionOption, error) {
	prefix := "allow_"
	if decision == "deny" {
		prefix = "reject_"
	}
	for _, option := range options {
		if strings.HasPrefix(option.Kind, prefix) {
			return option, nil
		}
	}
	return acp.PermissionOption{}, fmt.Errorf("agent offered no %s option", decision)
}

func formatPermission(id string, permission acp.PermissionRequest) string {
	var options []string
	for _, option := range permission.Options {
		options = append(options, fmt.Sprintf("%s=%s (%s)", option.OptionID, option.Name, option.Kind))
	}
	return fmt.Sprintf("permission %s: %s\n%s\noptions: %s", id, permission.Title, permission.Description, strings.Join(options, ", "))
}

func contentText(content json.RawMessage) string {
	if len(content) == 0 || string(content) == "null" {
		return ""
	}
	var block acp.ContentBlock
	if json.Unmarshal(content, &block) == nil && block.Type == "text" {
		return block.Text
	}
	var blocks []acp.ContentBlock
	if json.Unmarshal(content, &blocks) == nil {
		var builder strings.Builder
		for _, item := range blocks {
			if item.Type == "text" {
				builder.WriteString(item.Text)
			}
		}
		return builder.String()
	}
	return ""
}

func cloneRaw(value []byte) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}

func ShutdownContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), 3*time.Second)
}
