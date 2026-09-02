package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const shellSubscriptionMethod = "orchestration.subscribeShell"

type httpStatusError struct {
	status int
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.status)
}

type permanentWebSocketError struct {
	err error
}

func (e *permanentWebSocketError) Error() string { return e.err.Error() }
func (e *permanentWebSocketError) Unwrap() error { return e.err }

type ticketResponse struct {
	Ticket string `json:"ticket"`
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
	Cause json.RawMessage `json:"cause"`
}

type shellState struct {
	initialized bool
	sequence    uint64
	projects    map[string]projectShell
	threads     map[string]threadShell
}

func newShellState() *shellState {
	return &shellState{
		projects: make(map[string]projectShell),
		threads:  make(map[string]threadShell),
	}
}

func watchWebSocket(ctx context.Context, c *client, projectRoot string, output io.Writer) error {
	state := newShellState()
	backoff := 100 * time.Millisecond
	for {
		err := c.subscribeShell(ctx, state.afterSequence(), func(item json.RawMessage) error {
			changes, err := state.apply(item, projectRoot)
			if err != nil {
				return &permanentWebSocketError{err: err}
			}
			if err := writeChanges(output, changes); err != nil {
				return &permanentWebSocketError{err: err}
			}
			backoff = 100 * time.Millisecond
			return nil
		})
		if ctx.Err() != nil {
			return nil
		}
		var permanent *permanentWebSocketError
		if errors.As(err, &permanent) || isAuthorizationError(err) {
			return err
		}

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
		backoff = min(backoff*2, 2*time.Second)
	}
}

func (s *shellState) afterSequence() *uint64 {
	if !s.initialized {
		return nil
	}
	sequence := s.sequence
	return &sequence
}

func (c *client) subscribeShell(
	ctx context.Context,
	afterSequence *uint64,
	handle func(json.RawMessage) error,
) error {
	socketURL, err := c.webSocketURL(ctx)
	if err != nil {
		return err
	}
	connection, response, err := websocket.Dial(ctx, socketURL, &websocket.DialOptions{
		HTTPClient: c.http,
	})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
			return fmt.Errorf("dial T3 Code websocket: %w: %w", &httpStatusError{status: response.StatusCode}, err)
		}
		return fmt.Errorf("dial T3 Code websocket: %w", err)
	}
	defer func() { _ = connection.CloseNow() }()
	connection.SetReadLimit(64 << 20)

	payload := map[string]any{"requestCompletionMarker": true}
	if afterSequence != nil {
		payload["afterSequence"] = *afterSequence
	}
	requestID := "1"
	if err := writeRPC(ctx, connection, map[string]any{
		"_tag":    "Request",
		"id":      requestID,
		"tag":     shellSubscriptionMethod,
		"payload": payload,
		"headers": []any{},
	}); err != nil {
		return err
	}

	for {
		_, data, err := connection.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read T3 Code websocket: %w", err)
		}
		envelopes, err := decodeEnvelopes(data)
		if err != nil {
			return &permanentWebSocketError{err: err}
		}
		for _, envelope := range envelopes {
			switch envelope.Tag {
			case "Pong":
				continue
			case "Defect":
				return &permanentWebSocketError{err: fmt.Errorf("T3 Effect RPC defect: %s", envelope.Defect)}
			case "Chunk", "Exit":
			default:
				return &permanentWebSocketError{err: fmt.Errorf("unknown T3 Effect RPC envelope %q", envelope.Tag)}
			}
			id, err := decodeRPCID(envelope.RequestID)
			if err != nil || id != requestID {
				return &permanentWebSocketError{err: fmt.Errorf("invalid T3 Effect RPC request id: %w", err)}
			}
			if envelope.Tag == "Exit" {
				if envelope.Exit == nil {
					return &permanentWebSocketError{err: errors.New("T3 Effect RPC exit omitted its value")}
				}
				if envelope.Exit.Tag == "Failure" {
					return &permanentWebSocketError{err: fmt.Errorf("T3 shell subscription failed: %s", envelope.Exit.Cause)}
				}
				return errors.New("T3 shell subscription ended")
			}

			var values []json.RawMessage
			if err := json.Unmarshal(envelope.Values, &values); err != nil {
				return &permanentWebSocketError{err: fmt.Errorf("decode T3 Effect RPC chunk: %w", err)}
			}
			for _, item := range values {
				if err := handle(item); err != nil {
					return err
				}
			}
			if err := writeRPC(ctx, connection, map[string]any{
				"_tag":      "Ack",
				"requestId": requestID,
			}); err != nil {
				return err
			}
		}
	}
}

func (c *client) webSocketURL(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.origin+"/api/auth/websocket-ticket",
		bytes.NewReader([]byte("{}")),
	)
	if err != nil {
		return "", fmt.Errorf("build T3 Code websocket-ticket request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Content-Type", "application/json")
	var ticket ticketResponse
	if err := doJSON(c.http, req, &ticket); err != nil {
		return "", fmt.Errorf("issue T3 Code websocket ticket: %w", err)
	}
	if strings.TrimSpace(ticket.Ticket) == "" {
		return "", &permanentWebSocketError{err: errors.New("T3 Code websocket-ticket response omitted ticket")}
	}
	parsed, err := url.Parse(c.origin)
	if err != nil {
		return "", &permanentWebSocketError{err: fmt.Errorf("parse T3 Code origin: %w", err)}
	}
	if parsed.Scheme == "http" {
		parsed.Scheme = "ws"
	} else {
		parsed.Scheme = "wss"
	}
	parsed.Path = "/ws"
	parsed.RawQuery = url.Values{"wsTicket": {ticket.Ticket}}.Encode()
	return parsed.String(), nil
}

func doJSON(httpClient *http.Client, request *http.Request, output any) error {
	response, err := httpClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &httpStatusError{status: response.StatusCode}
	}
	if err := json.Unmarshal(body, output); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func isAuthorizationError(err error) bool {
	var status *httpStatusError
	return errors.As(err, &status) &&
		(status.status == http.StatusUnauthorized || status.status == http.StatusForbidden)
}

func writeRPC(ctx context.Context, connection *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode T3 Effect RPC envelope: %w", err)
	}
	if err := connection.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write T3 Code websocket: %w", err)
	}
	return nil
}

func decodeEnvelopes(data []byte) ([]rpcEnvelope, error) {
	if len(data) > 0 && data[0] == '[' {
		var envelopes []rpcEnvelope
		if err := json.Unmarshal(data, &envelopes); err != nil {
			return nil, fmt.Errorf("decode T3 Effect RPC envelope batch: %w", err)
		}
		return envelopes, nil
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode T3 Effect RPC envelope: %w", err)
	}
	return []rpcEnvelope{envelope}, nil
}

func decodeRPCID(raw json.RawMessage) (string, error) {
	var id string
	if err := json.Unmarshal(raw, &id); err != nil || id == "" {
		return "", errors.New("missing or invalid request id")
	}
	return id, nil
}

func (s *shellState) apply(encoded json.RawMessage, projectRoot string) ([]change, error) {
	var header struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal(encoded, &header); err != nil || header.Kind == "" {
		return nil, errors.New("T3 shell stream item omitted kind")
	}
	if header.Kind == "snapshot" {
		var item struct {
			Snapshot *shellSnapshot `json:"snapshot"`
		}
		if err := json.Unmarshal(encoded, &item); err != nil || item.Snapshot == nil {
			return nil, errors.New("T3 shell snapshot item omitted snapshot")
		}
		if err := validateSnapshot(*item.Snapshot); err != nil {
			return nil, fmt.Errorf("decode T3 shell snapshot: %w", err)
		}
		before := s.projected(projectRoot)
		first := !s.initialized
		s.replace(*item.Snapshot)
		after := s.projected(projectRoot)
		if first {
			return presentChanges(after), nil
		}
		return diff(before, after), nil
	}
	if header.Kind == "synchronized" {
		if !s.initialized {
			return nil, errors.New("T3 shell synchronized before initial snapshot")
		}
		return nil, nil
	}
	if !s.initialized {
		return nil, fmt.Errorf("T3 shell event %q arrived before initial snapshot", header.Kind)
	}

	var event struct {
		Sequence  *uint64       `json:"sequence"`
		Project   *projectShell `json:"project"`
		ProjectID string        `json:"projectId"`
		Thread    *threadShell  `json:"thread"`
		ThreadID  string        `json:"threadId"`
	}
	if err := json.Unmarshal(encoded, &event); err != nil || event.Sequence == nil {
		return nil, fmt.Errorf("decode T3 shell event %q", header.Kind)
	}
	if *event.Sequence <= s.sequence {
		return nil, nil
	}
	before := s.projected(projectRoot)
	switch header.Kind {
	case "project-upserted":
		if event.Project == nil {
			return nil, errors.New("T3 project-upserted omitted project")
		}
		if err := validateProject(*event.Project); err != nil {
			return nil, err
		}
		s.projects[event.Project.ID] = *event.Project
	case "project-removed":
		if event.ProjectID == "" {
			return nil, errors.New("T3 project-removed omitted projectId")
		}
		for _, thread := range s.threads {
			if thread.ProjectID == event.ProjectID {
				return nil, fmt.Errorf("T3 removed project %s while thread %s still references it", event.ProjectID, thread.ID)
			}
		}
		delete(s.projects, event.ProjectID)
	case "thread-upserted":
		if event.Thread == nil {
			return nil, errors.New("T3 thread-upserted omitted thread")
		}
		if err := validateThread(*event.Thread, s.projects); err != nil {
			return nil, err
		}
		s.threads[event.Thread.ID] = *event.Thread
	case "thread-removed":
		if event.ThreadID == "" {
			return nil, errors.New("T3 thread-removed omitted threadId")
		}
		delete(s.threads, event.ThreadID)
	default:
		return nil, fmt.Errorf("unknown T3 shell stream item %q", header.Kind)
	}
	s.sequence = *event.Sequence
	return diff(before, s.projected(projectRoot)), nil
}

func (s *shellState) replace(snapshot shellSnapshot) {
	s.projects = make(map[string]projectShell, len(*snapshot.Projects))
	for _, project := range *snapshot.Projects {
		s.projects[project.ID] = project
	}
	s.threads = make(map[string]threadShell, len(*snapshot.Threads))
	for _, thread := range *snapshot.Threads {
		s.threads[thread.ID] = thread
	}
	s.sequence = *snapshot.Sequence
	s.initialized = true
}

func (s *shellState) projected(projectRoot string) []projectedThread {
	projects := make([]projectShell, 0, len(s.projects))
	for _, project := range s.projects {
		projects = append(projects, project)
	}
	threads := make([]threadShell, 0, len(s.threads))
	for _, thread := range s.threads {
		threads = append(threads, thread)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	sort.Slice(threads, func(i, j int) bool { return threads[i].ID < threads[j].ID })
	sequence := s.sequence
	snapshot := shellSnapshot{Sequence: &sequence, Projects: &projects, Threads: &threads, UpdatedAt: time.Now()}
	return project(snapshot, projectRoot)
}
