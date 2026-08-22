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
		ProtocolVersion: ProtocolVersion,
		Capabilities:    map[string]any{},
		Info: Implementation{
			Name:    "atc-acp-v2-host",
			Title:   "ATC ACP v2 experiment",
			Version: "0.1.0",
		},
	}
	var response InitializeResponse
	if err := c.connection.Call(ctx, "initialize", request, &response); err != nil {
		return InitializeResponse{}, err
	}
	if response.ProtocolVersion != ProtocolVersion {
		return response, fmt.Errorf("agent negotiated ACP v%d; this experiment requires ACP v%d and will not fall back", response.ProtocolVersion, ProtocolVersion)
	}
	var capabilities map[string]json.RawMessage
	if err := json.Unmarshal(response.Capabilities, &capabilities); err != nil {
		return response, fmt.Errorf("decode agent capabilities: %w", err)
	}
	if _, ok := capabilities["session"]; !ok {
		return response, fmt.Errorf("agent did not advertise the required ACP v2 session capability")
	}
	return response, nil
}

func (c *Client) NewSession(ctx context.Context, cwd string) (NewSessionResponse, error) {
	var response NewSessionResponse
	err := c.connection.Call(ctx, "session/new", NewSessionRequest{CWD: cwd}, &response)
	if err == nil && response.SessionID == "" {
		err = fmt.Errorf("session/new returned an empty sessionId")
	}
	return response, err
}

func (c *Client) ResumeSession(ctx context.Context, sessionID, cwd string, replay bool) (ResumeSessionResponse, error) {
	request := ResumeSessionRequest{SessionID: sessionID, CWD: cwd}
	if replay {
		request.ReplayFrom = &ReplayFrom{Type: "start"}
	}
	var response ResumeSessionResponse
	err := c.connection.Call(ctx, "session/resume", request, &response)
	return response, err
}

func (c *Client) Prompt(ctx context.Context, sessionID, prompt string) error {
	return c.connection.Call(ctx, "session/prompt", PromptRequest{
		SessionID: sessionID,
		Prompt:    []ContentBlock{{Type: "text", Text: prompt}},
	}, &struct{}{})
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
	return c.connection.RespondError(request.ID, -32601, "method not supported by ATC ACP v2 experiment: "+request.Method)
}

func (c *Client) RespondError(id json.RawMessage, code int, message string) error {
	return c.connection.RespondError(id, code, message)
}
