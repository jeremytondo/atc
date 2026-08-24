package acp

import (
	"context"
	"encoding/json"
	"fmt"
)

type Client struct {
	connection *Connection
}

func NewClient(connection *Connection) *Client {
	return &Client{connection: connection}
}

func (c *Client) Initialize(ctx context.Context) (InitializeResponse, error) {
	request := InitializeRequest{
		ProtocolVersion:    ProtocolVersion,
		ClientCapabilities: map[string]any{},
		ClientInfo: Implementation{
			Name:    "atc-acp-host",
			Title:   "ATC ACP experiment",
			Version: "0.1.0",
		},
	}
	var response InitializeResponse
	if err := c.connection.Call(ctx, "initialize", request, &response); err != nil {
		return InitializeResponse{}, err
	}
	if response.ProtocolVersion != ProtocolVersion {
		return response, fmt.Errorf("agent negotiated ACP v%d; this experiment requires ACP v%d", response.ProtocolVersion, ProtocolVersion)
	}
	if err := json.Unmarshal(response.AgentCapabilities, &response.Capabilities); err != nil {
		return response, fmt.Errorf("decode agent capabilities: %w", err)
	}
	return response, nil
}

func (c *Client) NewSession(ctx context.Context, cwd string) (NewSessionResponse, error) {
	var response NewSessionResponse
	err := c.connection.Call(ctx, "session/new", NewSessionRequest{CWD: cwd, MCPServers: []any{}}, &response)
	if err == nil && response.SessionID == "" {
		err = fmt.Errorf("session/new returned an empty sessionId")
	}
	return response, err
}

func (c *Client) LoadSession(ctx context.Context, sessionID, cwd string) error {
	request := ExistingSessionRequest{SessionID: sessionID, CWD: cwd, MCPServers: []any{}}
	return c.connection.Call(ctx, "session/load", request, nil)
}

func (c *Client) ResumeSession(ctx context.Context, sessionID, cwd string) (ResumeSessionResponse, error) {
	request := ExistingSessionRequest{SessionID: sessionID, CWD: cwd, MCPServers: []any{}}
	var response ResumeSessionResponse
	err := c.connection.Call(ctx, "session/resume", request, &response)
	return response, err
}

func (c *Client) BeginPrompt(sessionID, prompt string) (*PendingCall, error) {
	return c.connection.BeginCall("session/prompt", PromptRequest{
		SessionID: sessionID,
		Prompt:    []ContentBlock{{Type: "text", Text: prompt}},
	})
}

func (c *Client) AwaitPrompt(ctx context.Context, pending *PendingCall) (PromptResponse, error) {
	var response PromptResponse
	err := pending.Await(ctx, &response)
	return response, err
}

func (c *Client) Cancel(sessionID string) error {
	return c.connection.Notify("session/cancel", SessionParams{SessionID: sessionID})
}

func (c *Client) CloseSession(ctx context.Context, sessionID string) error {
	return c.connection.Call(ctx, "session/close", SessionParams{SessionID: sessionID}, &struct{}{})
}

func (c *Client) RespondPermission(id json.RawMessage, optionID string) error {
	return c.connection.Respond(id, PermissionResponse{Outcome: PermissionOutcome{Outcome: "selected", OptionID: optionID}})
}

func (c *Client) CancelPermission(id json.RawMessage) error {
	return c.connection.Respond(id, PermissionResponse{Outcome: PermissionOutcome{Outcome: "cancelled"}})
}

func (c *Client) RejectUnknownRequest(request IncomingRequest) error {
	return c.connection.RespondError(request.ID, -32601, "method not supported by ATC ACP experiment: "+request.Method)
}

func (c *Client) RespondError(id json.RawMessage, code int, message string) error {
	return c.connection.RespondError(id, code, message)
}
