// Package t3 implements the deliberately small portion of T3 Code's external
// HTTP authentication and Effect RPC wire protocol needed by the ATC-236
// orchestration spike. T3 contract values remain raw JSON above the transport;
// this package does not reproduce T3's Effect schemas.
package t3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"
)

const requestedScopes = "orchestration:read orchestration:operate terminal:operate review:write relay:read"

type Auth struct {
	BaseURL     string
	AccessToken string
	HTTPClient  *http.Client
}

type tokenResponse struct {
	AccessToken string `json:"access_token"`
}

type ticketResponse struct {
	Ticket string `json:"ticket"`
}

func ExchangeBootstrapCredential(
	ctx context.Context,
	httpClient *http.Client,
	baseURL string,
	bootstrapCredential string,
) (Auth, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	form := url.Values{
		"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
		"subject_token":        {bootstrapCredential},
		"subject_token_type":   {"urn:t3:params:oauth:token-type:environment-bootstrap"},
		"requested_token_type": {"urn:ietf:params:oauth:token-type:access_token"},
		"scope":                {requestedScopes},
		"client_label":         {"ATC-236 Go adapter"},
		"client_device_type":   {"desktop"},
		"client_os":            {"linux"},
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		baseURL+"/oauth/token",
		strings.NewReader(form.Encode()),
	)
	if err != nil {
		return Auth{}, fmt.Errorf("build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	var response tokenResponse
	if err := doJSON(httpClient, req, &response); err != nil {
		return Auth{}, fmt.Errorf("exchange bootstrap credential: %w", err)
	}
	if strings.TrimSpace(response.AccessToken) == "" {
		return Auth{}, errors.New("exchange bootstrap credential: response omitted access_token")
	}
	return Auth{BaseURL: baseURL, AccessToken: response.AccessToken, HTTPClient: httpClient}, nil
}

func (a Auth) WebSocketURL(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		a.BaseURL+"/api/auth/websocket-ticket",
		bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		return "", fmt.Errorf("build websocket ticket request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+a.AccessToken)
	req.Header.Set("Content-Type", "application/json")

	var response ticketResponse
	if err := doJSON(a.HTTPClient, req, &response); err != nil {
		return "", fmt.Errorf("issue websocket ticket: %w", err)
	}
	if strings.TrimSpace(response.Ticket) == "" {
		return "", errors.New("issue websocket ticket: response omitted ticket")
	}

	parsed, err := url.Parse(a.BaseURL)
	if err != nil {
		return "", fmt.Errorf("parse base URL: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	default:
		return "", fmt.Errorf("unsupported base URL scheme %q", parsed.Scheme)
	}
	parsed.Path = "/ws"
	parsed.RawQuery = url.Values{"wsTicket": {response.Ticket}}.Encode()
	return parsed.String(), nil
}

func doJSON(httpClient *http.Client, req *http.Request, output any) error {
	response, err := httpClient.Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", response.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

type Client struct {
	conn      *websocket.Conn
	closed    chan struct{}
	closeOnce sync.Once
	writeMu   sync.Mutex
	mu        sync.Mutex
	pending   map[string]*request
	nextID    atomic.Uint64
	readErr   error
}

type request struct {
	stream bool
	result chan response
}

type response struct {
	values []json.RawMessage
	result json.RawMessage
	err    error
	done   bool
}

type rpcEnvelope struct {
	Tag       string          `json:"_tag"`
	RequestID json.RawMessage `json:"requestId"`
	Values    json.RawMessage `json:"values"`
	Exit      *rpcExit        `json:"exit"`
	Defect    json.RawMessage `json:"defect"`
}

type rpcExit struct {
	Tag   string          `json:"_tag"`
	Value json.RawMessage `json:"value"`
	Cause json.RawMessage `json:"cause"`
}

type RPCError struct {
	Cause json.RawMessage
}

func (e *RPCError) Error() string {
	return fmt.Sprintf("T3 RPC failure: %s", e.Cause)
}

func Dial(ctx context.Context, socketURL string) (*Client, error) {
	conn, response, err := websocket.Dial(ctx, socketURL, nil)
	if err != nil {
		if response != nil {
			return nil, fmt.Errorf("dial websocket (HTTP %d): %w", response.StatusCode, err)
		}
		return nil, fmt.Errorf("dial websocket: %w", err)
	}
	conn.SetReadLimit(64 << 20)
	client := &Client{
		conn:    conn,
		closed:  make(chan struct{}),
		pending: make(map[string]*request),
	}
	go client.readLoop()
	return client, nil
}

func (c *Client) Close() error {
	c.closeWithError(errors.New("T3 RPC client closed"))
	return c.conn.Close(websocket.StatusNormalClosure, "client closed")
}

func (c *Client) Call(ctx context.Context, method string, payload any, output any) error {
	requestID, responses, err := c.start(ctx, method, payload, false)
	if err != nil {
		return err
	}
	defer c.remove(requestID)

	select {
	case <-ctx.Done():
		c.interrupt(requestID)
		return ctx.Err()
	case response := <-responses:
		if response.err != nil {
			return response.err
		}
		if output == nil || len(response.result) == 0 || bytes.Equal(response.result, []byte("null")) {
			return nil
		}
		if err := json.Unmarshal(response.result, output); err != nil {
			return fmt.Errorf("decode %s response: %w", method, err)
		}
		return nil
	}
}

type Subscription struct {
	Items  <-chan json.RawMessage
	Done   <-chan error
	cancel func()
}

func (s *Subscription) Close() {
	s.cancel()
}

func (c *Client) Subscribe(ctx context.Context, method string, payload any) (*Subscription, error) {
	requestID, responses, err := c.start(ctx, method, payload, true)
	if err != nil {
		return nil, err
	}
	items := make(chan json.RawMessage, 64)
	done := make(chan error, 1)
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			c.interrupt(requestID)
			c.remove(requestID)
		})
	}

	go func() {
		defer close(items)
		defer close(done)
		defer cancel()
		for {
			select {
			case <-ctx.Done():
				done <- ctx.Err()
				return
			case response, ok := <-responses:
				if !ok {
					done <- errors.New("T3 RPC subscription closed without terminal response")
					return
				}
				if response.err != nil {
					done <- response.err
					return
				}
				for _, item := range response.values {
					select {
					case items <- item:
					case <-ctx.Done():
						done <- ctx.Err()
						return
					}
				}
				if response.done {
					done <- nil
					return
				}
			}
		}
	}()

	return &Subscription{Items: items, Done: done, cancel: cancel}, nil
}

func (c *Client) start(
	ctx context.Context,
	method string,
	payload any,
	stream bool,
) (string, <-chan response, error) {
	id := strconv.FormatUint(c.nextID.Add(1), 10)
	responses := make(chan response, 16)
	c.mu.Lock()
	if c.readErr != nil {
		err := c.readErr
		c.mu.Unlock()
		return "", nil, err
	}
	c.pending[id] = &request{stream: stream, result: responses}
	c.mu.Unlock()

	message := map[string]any{
		"_tag":    "Request",
		"id":      id,
		"tag":     method,
		"payload": payload,
		"headers": []any{},
	}
	if err := c.write(ctx, message); err != nil {
		c.remove(id)
		return "", nil, err
	}
	return id, responses, nil
}

func (c *Client) readLoop() {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			c.closeWithError(fmt.Errorf("read websocket: %w", err))
			return
		}
		var envelopes []rpcEnvelope
		if len(data) > 0 && data[0] == '[' {
			if err := json.Unmarshal(data, &envelopes); err != nil {
				c.closeWithError(fmt.Errorf("decode RPC envelope batch: %w", err))
				return
			}
		} else {
			var envelope rpcEnvelope
			if err := json.Unmarshal(data, &envelope); err != nil {
				c.closeWithError(fmt.Errorf("decode RPC envelope: %w", err))
				return
			}
			envelopes = []rpcEnvelope{envelope}
		}
		for _, envelope := range envelopes {
			if err := c.handleEnvelope(envelope); err != nil {
				c.closeWithError(err)
				return
			}
		}
	}
}

func (c *Client) handleEnvelope(envelope rpcEnvelope) error {
	if envelope.Tag == "Pong" {
		return nil
	}
	if envelope.Tag == "Defect" {
		return fmt.Errorf("T3 RPC protocol defect: %s", envelope.Defect)
	}
	if envelope.Tag != "Chunk" && envelope.Tag != "Exit" {
		return fmt.Errorf("unknown T3 RPC envelope tag %q", envelope.Tag)
	}
	id, err := decodeID(envelope.RequestID)
	if err != nil {
		return nil
	}

	c.mu.Lock()
	pending := c.pending[id]
	if pending != nil && envelope.Tag == "Exit" {
		delete(c.pending, id)
	}
	c.mu.Unlock()
	if pending == nil {
		return nil
	}

	if envelope.Tag == "Chunk" {
		var values []json.RawMessage
		if err := json.Unmarshal(envelope.Values, &values); err != nil {
			pending.result <- response{err: fmt.Errorf("decode stream chunk: %w", err), done: true}
			close(pending.result)
			return nil
		}
		pending.result <- response{values: values}
		c.ack(id)
		return nil
	}
	if envelope.Exit == nil {
		pending.result <- response{err: errors.New("T3 RPC exit omitted exit value"), done: true}
		close(pending.result)
		return nil
	}
	if envelope.Exit.Tag == "Failure" {
		pending.result <- response{err: &RPCError{Cause: envelope.Exit.Cause}, done: true}
		close(pending.result)
		return nil
	}
	if envelope.Exit.Tag != "Success" {
		pending.result <- response{err: fmt.Errorf("unknown T3 RPC exit tag %q", envelope.Exit.Tag), done: true}
		close(pending.result)
		return nil
	}
	pending.result <- response{result: envelope.Exit.Value, done: true}
	close(pending.result)
	return nil
}

func decodeID(raw json.RawMessage) (string, error) {
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", errors.New("missing response id")
	}
	var stringID string
	if err := json.Unmarshal(raw, &stringID); err == nil {
		return stringID, nil
	}
	var numberID json.Number
	if err := json.Unmarshal(raw, &numberID); err == nil {
		return numberID.String(), nil
	}
	return "", fmt.Errorf("invalid response id %s", raw)
}

func (c *Client) ack(requestID string) {
	_ = c.write(context.Background(), map[string]any{
		"_tag":      "Ack",
		"requestId": requestID,
	})
}

func (c *Client) interrupt(requestID string) {
	_ = c.write(context.Background(), map[string]any{
		"_tag":      "Interrupt",
		"requestId": requestID,
	})
}

func (c *Client) write(ctx context.Context, message any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	data, err := json.Marshal(message)
	if err != nil {
		return fmt.Errorf("encode RPC message: %w", err)
	}
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write websocket: %w", err)
	}
	return nil
}

func (c *Client) remove(requestID string) {
	c.mu.Lock()
	delete(c.pending, requestID)
	c.mu.Unlock()
}

func (c *Client) closeWithError(err error) {
	c.closeOnce.Do(func() {
		c.mu.Lock()
		c.readErr = err
		pending := c.pending
		c.pending = make(map[string]*request)
		c.mu.Unlock()
		for _, request := range pending {
			request.result <- response{err: err, done: true}
			close(request.result)
		}
		close(c.closed)
	})
}
