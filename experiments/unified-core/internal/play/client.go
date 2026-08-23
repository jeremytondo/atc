// Package play is the HTTP-only human client for the unified-core prototype.
// It intentionally owns small canonical response DTOs instead of importing
// server internals, keeping the same boundary that an external client has.
package play

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Thread struct {
	ID                  string `json:"id"`
	Kind                string `json:"kind"`
	Agent               string `json:"agent"`
	CWD                 string `json:"cwd"`
	Activity            string `json:"activity"`
	TerminalID          string `json:"terminalId,omitempty"`
	ActiveTurn          *Turn  `json:"activeTurn,omitempty"`
	LastTurn            *Turn  `json:"lastTurn,omitempty"`
	BackgroundActivity  string `json:"backgroundActivity"`
	PendingRequestCount int    `json:"pendingRequestCount"`
	CreatedAt           string `json:"createdAt"`
	UpdatedAt           string `json:"updatedAt"`
}

type Turn struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	Outcome string `json:"outcome,omitempty"`
	Error   string `json:"error,omitempty"`
}

type PendingRequest struct {
	ID       string          `json:"id"`
	ThreadID string          `json:"threadId"`
	TurnID   string          `json:"turnId,omitempty"`
	Kind     string          `json:"kind"`
	Prompt   string          `json:"prompt"`
	Options  []RequestOption `json:"options,omitempty"`
}

type RequestOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Terminal struct {
	ID             string `json:"id"`
	ThreadID       string `json:"threadId"`
	ActiveThreadID string `json:"activeThreadId,omitempty"`
	Lifecycle      string `json:"lifecycle"`
	Reachable      bool   `json:"reachable"`
	Reason         string `json:"reason,omitempty"`
}

type Event struct {
	Sequence uint64          `json:"sequence"`
	ThreadID string          `json:"threadId,omitempty"`
	TurnID   string          `json:"turnId,omitempty"`
	Resource string          `json:"resource"`
	Type     string          `json:"type"`
	Activity string          `json:"activity,omitempty"`
	Turn     *Turn           `json:"turn,omitempty"`
	Request  *PendingRequest `json:"request,omitempty"`
	Terminal *Terminal       `json:"terminal,omitempty"`
	Text     string          `json:"text,omitempty"`
	Created  time.Time       `json:"createdAt"`
}

type APIError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Status  int    `json:"-"`
}

func (e *APIError) Error() string {
	if e.Code == "" {
		return e.Message
	}
	return e.Code + ": " + e.Message
}

type Client struct {
	baseURL *url.URL
	http    *http.Client
}

func NewClient(base string, client *http.Client) (*Client, error) {
	parsed, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, fmt.Errorf("parse server URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return nil, errors.New("server URL must use http or https")
	}
	if parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, errors.New("server URL must contain a host and no query or fragment")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{baseURL: parsed, http: client}, nil
}

func (c *Client) Threads(ctx context.Context) ([]Thread, error) {
	var result struct {
		Threads []Thread `json:"threads"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/threads", nil, &result); err != nil {
		return nil, err
	}
	return result.Threads, nil
}

func (c *Client) Terminals(ctx context.Context) ([]Terminal, error) {
	var result struct {
		Terminals []Terminal `json:"terminals"`
	}
	if err := c.doJSON(ctx, http.MethodGet, "/v1/terminals", nil, &result); err != nil {
		return nil, err
	}
	return result.Terminals, nil
}

func (c *Client) Events(ctx context.Context, after uint64) ([]Event, error) {
	var result struct {
		Events []Event `json:"events"`
	}
	path := fmt.Sprintf("/v1/events?after=%d", after)
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result.Events, nil
}

func (c *Client) Requests(ctx context.Context, threadID string) ([]PendingRequest, error) {
	var result struct {
		Requests []PendingRequest `json:"requests"`
	}
	path := "/v1/threads/" + url.PathEscape(threadID) + "/requests"
	if err := c.doJSON(ctx, http.MethodGet, path, nil, &result); err != nil {
		return nil, err
	}
	return result.Requests, nil
}

func (c *Client) CreateThread(ctx context.Context, kind, agent, cwd string) (Thread, error) {
	var thread Thread
	body := map[string]string{"kind": kind, "agent": agent, "cwd": cwd}
	err := c.doJSON(ctx, http.MethodPost, "/v1/threads", body, &thread)
	return thread, err
}

func (c *Client) Prompt(ctx context.Context, threadID, text string) (Turn, error) {
	var turn Turn
	path := "/v1/threads/" + url.PathEscape(threadID) + "/prompts"
	err := c.doJSON(ctx, http.MethodPost, path, map[string]string{"text": text}, &turn)
	return turn, err
}

func (c *Client) Answer(ctx context.Context, threadID, requestID, optionID, text string) error {
	path := "/v1/threads/" + url.PathEscape(threadID) + "/requests/" + url.PathEscape(requestID) + "/answer"
	body := map[string]string{"optionId": optionID}
	if optionID == "" {
		body = map[string]string{"text": text}
	}
	return c.doJSON(ctx, http.MethodPost, path, body, nil)
}

func (c *Client) Interrupt(ctx context.Context, threadID, turnID string) error {
	path := "/v1/threads/" + url.PathEscape(threadID) + "/turns/" + url.PathEscape(turnID) + "/interrupt"
	return c.doJSON(ctx, http.MethodPost, path, map[string]string{}, nil)
}

func (c *Client) OpenTerminal(ctx context.Context, threadID string) (Terminal, error) {
	var terminal Terminal
	path := "/v1/threads/" + url.PathEscape(threadID) + "/terminal"
	err := c.doJSON(ctx, http.MethodPost, path, map[string]string{}, &terminal)
	return terminal, err
}

func (c *Client) doJSON(ctx context.Context, method, path string, body any, target any) error {
	var input io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		input = bytes.NewReader(encoded)
	}
	requestURL := *c.baseURL
	requestURL.Path = strings.TrimRight(c.baseURL.Path, "/") + strings.SplitN(path, "?", 2)[0]
	if index := strings.IndexByte(path, '?'); index >= 0 {
		requestURL.RawQuery = path[index+1:]
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL.String(), input)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.http.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode >= 400 {
		var envelope struct {
			Error APIError `json:"error"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope); err != nil {
			return fmt.Errorf("server returned %s", response.Status)
		}
		envelope.Error.Status = response.StatusCode
		if envelope.Error.Message == "" {
			envelope.Error.Message = response.Status
		}
		return &envelope.Error
	}
	if target == nil || response.StatusCode == http.StatusNoContent {
		_, _ = io.Copy(io.Discard, response.Body)
		return nil
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 8<<20)).Decode(target); err != nil {
		return fmt.Errorf("decode %s %s: %w", method, path, err)
	}
	return nil
}
