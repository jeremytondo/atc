// Package acp contains the complete draft ACP v2 dependency surface for the
// experiment. Protocol churn should require edits here, not in the normalized
// harness package.
package acp

import "encoding/json"

const ProtocolVersion = 2

type Implementation struct {
	Name    string `json:"name"`
	Title   string `json:"title,omitempty"`
	Version string `json:"version"`
}

type InitializeRequest struct {
	ProtocolVersion int            `json:"protocolVersion"`
	Capabilities    map[string]any `json:"capabilities"`
	Info            Implementation `json:"info"`
}

type InitializeResponse struct {
	ProtocolVersion int             `json:"protocolVersion"`
	Capabilities    json.RawMessage `json:"capabilities"`
	Info            Implementation  `json:"info"`
	AuthMethods     json.RawMessage `json:"authMethods,omitempty"`
}

type NewSessionRequest struct {
	CWD        string `json:"cwd"`
	MCPServers []any  `json:"mcpServers,omitempty"`
}

type NewSessionResponse struct {
	SessionID     string          `json:"sessionId"`
	ConfigOptions json.RawMessage `json:"configOptions,omitempty"`
}

type ResumeSessionRequest struct {
	SessionID  string      `json:"sessionId"`
	CWD        string      `json:"cwd"`
	MCPServers []any       `json:"mcpServers,omitempty"`
	ReplayFrom *ReplayFrom `json:"replayFrom,omitempty"`
}

type ReplayFrom struct {
	Type string `json:"type"`
}

type ResumeSessionResponse struct {
	ConfigOptions json.RawMessage `json:"configOptions,omitempty"`
}

type PromptRequest struct {
	SessionID string         `json:"sessionId"`
	Prompt    []ContentBlock `json:"prompt"`
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
	State      string          `json:"state,omitempty"`
	StopReason string          `json:"stopReason,omitempty"`
	MessageID  string          `json:"messageId,omitempty"`
	ToolCallID string          `json:"toolCallId,omitempty"`
	Status     string          `json:"status,omitempty"`
	Title      string          `json:"title,omitempty"`
	Content    json.RawMessage `json:"content,omitempty"`
}

type PermissionRequest struct {
	SessionID   string             `json:"sessionId"`
	Title       string             `json:"title"`
	Description string             `json:"description,omitempty"`
	Subject     json.RawMessage    `json:"subject,omitempty"`
	Options     []PermissionOption `json:"options"`
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
