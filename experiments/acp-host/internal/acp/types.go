// Package acp contains the complete ACP v1 dependency surface for the
// experiment. Protocol changes should require edits here, not in the
// normalized harness package.
package acp

import "encoding/json"

const ProtocolVersion = 1

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type InitializeRequest struct {
	ProtocolVersion    int            `json:"protocolVersion"`
	ClientCapabilities map[string]any `json:"clientCapabilities"`
	ClientInfo         Implementation `json:"clientInfo"`
}

type InitializeResponse struct {
	ProtocolVersion   int               `json:"protocolVersion"`
	AgentCapabilities json.RawMessage   `json:"agentCapabilities"`
	Capabilities      AgentCapabilities `json:"-"`
	AgentInfo         Implementation    `json:"agentInfo"`
	AuthMethods       json.RawMessage   `json:"authMethods,omitempty"`
}

type AgentCapabilities struct {
	LoadSession         bool                       `json:"loadSession,omitempty"`
	SessionCapabilities map[string]json.RawMessage `json:"sessionCapabilities,omitempty"`
}

type NewSessionRequest struct {
	CWD        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type NewSessionResponse struct {
	SessionID     string          `json:"sessionId"`
	ConfigOptions json.RawMessage `json:"configOptions,omitempty"`
}

type ExistingSessionRequest struct {
	SessionID  string `json:"sessionId"`
	CWD        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers"`
}

type ResumeSessionResponse struct {
	ConfigOptions json.RawMessage `json:"configOptions,omitempty"`
}

type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
}

type PromptResponse struct {
	StopReason string `json:"stopReason"`
}

type ContentBlock struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

type SessionParams struct {
	SessionID string `json:"sessionId"`
}

type SessionUpdateParams struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

type SessionUpdate struct {
	Kind       string          `json:"sessionUpdate"`
	MessageID  string          `json:"messageId,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Status     string          `json:"status,omitempty"`
	Title      string          `json:"title,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
}

type PermissionRequest struct {
	SessionID string             `json:"sessionId"`
	ToolCall  ToolCallUpdate     `json:"toolCall"`
	Options   []PermissionOption `json:"options"`
}

type ToolCallUpdate struct {
	ToolCallID string `json:"toolCallId"`
	Title      string `json:"title,omitempty"`
}

type PermissionOption struct {
	OptionID string `json:"optionId"`
	Name     string `json:"name"`
	Kind     string `json:"kind"`
}

type PermissionResponse struct {
	Outcome PermissionOutcome `json:"outcome"`
}

type PermissionOutcome struct {
	Outcome  string `json:"outcome"`
	OptionID string `json:"optionId,omitempty"`
}

type IncomingRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

type Notification struct {
	Method string
	Params json.RawMessage
}

type RawTraffic struct {
	Direction string
	Message   json.RawMessage
}

type Handler interface {
	HandleRequest(IncomingRequest)
	HandleNotification(Notification)
	HandleDisconnect(error)
	HandleRaw(RawTraffic)
}
