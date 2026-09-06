package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

// The client side of /v1/events (ATC-307): a reader of the SSE feed for
// a client that waits on a resource — the CLI waiting for a turn's
// reply. It reads events and heartbeats; a stream that goes quiet for
// longer than the server's heartbeat cadence allows has lost its
// connection, and the reader ends so the caller can reconnect with the
// last event id and refetch. Reconnecting and refetching are the
// caller's: the feed says what changed, never the state.

// DefaultStallTimeout is how long a stream may stay silent — no event,
// no heartbeat — before the reader treats the connection as lost. The
// server heartbeats every 15 seconds.
const DefaultStallTimeout = 60 * time.Second

// ErrStreamStalled reports a stream silent past the stall timeout.
var ErrStreamStalled = errors.New("event stream stalled")

// Event is one server-sent event: its name (the change vocabulary of
// api.Event*), its id (the sequence number, for Last-Event-ID), and its
// payload.
type Event struct {
	Name string
	ID   string
	Data json.RawMessage
	// heartbeat marks a comment line: read to keep the stream alive,
	// never returned.
	heartbeat bool
}

// Change decodes the event's ChangeEvent payload; a resync event has
// none of its fields but decodes without error.
func (e Event) Change() (ChangeEvent, error) {
	var change ChangeEvent
	err := json.Unmarshal(e.Data, &change)
	return change, err
}

// EventStream is one open connection to the feed. Next blocks for the
// next event; Close ends the connection. Construct with Client.Events.
type EventStream struct {
	body   io.ReadCloser
	events chan Event
	errs   chan error
	stall  time.Duration
	// closed ends the reader once the caller is gone, so an event it
	// could not hand over never keeps it alive.
	closed    chan struct{}
	closeOnce sync.Once
	// LastEventID is the id of the last event delivered — what a
	// reconnect presents.
	LastEventID string
}

// Events opens the feed, presenting lastEventID for catch-up when
// non-empty; the server has answered the request when this returns, and
// its events follow through Next. stallTimeout zero means
// DefaultStallTimeout.
func (c *Client) Events(ctx context.Context, lastEventID string, stallTimeout time.Duration) (*EventStream, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/events", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set(ClientVersionHeader, c.version)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GET /v1/events: %w", err)
	}
	if c.onServerVersion != nil {
		if serverVersion := resp.Header.Get(ServerVersionHeader); serverVersion != "" {
			c.onServerVersion(serverVersion)
		}
	}
	if resp.StatusCode != http.StatusOK {
		problem := problemFrom(resp, resp.Header.Get(ServerVersionHeader))
		_ = resp.Body.Close()
		return nil, problem
	}
	if stallTimeout <= 0 {
		stallTimeout = DefaultStallTimeout
	}
	stream := &EventStream{
		body: resp.Body, events: make(chan Event, 16), errs: make(chan error, 1), closed: make(chan struct{}),
		stall: stallTimeout, LastEventID: lastEventID,
	}
	go stream.read()
	return stream, nil
}

// Next returns the next event, or the error that ended the stream: the
// context's, ErrStreamStalled, or the connection's. Heartbeats keep the
// stream alive without reaching the caller.
func (s *EventStream) Next(ctx context.Context) (Event, error) {
	timer := time.NewTimer(s.stall)
	defer timer.Stop()
	for {
		select {
		case event := <-s.events:
			if event.heartbeat {
				timer.Reset(s.stall)
				continue
			}
			if event.ID != "" {
				s.LastEventID = event.ID
			}
			return event, nil
		case err := <-s.errs:
			return Event{}, err
		case <-timer.C:
			_ = s.Close()
			return Event{}, ErrStreamStalled
		case <-ctx.Done():
			_ = s.Close()
			return Event{}, ctx.Err()
		}
	}
}

// Close ends the connection and the reader.
func (s *EventStream) Close() error {
	s.closeOnce.Do(func() { close(s.closed) })
	return s.body.Close()
}

// read parses the SSE wire format: fields per line, an empty line
// dispatches; comment lines are heartbeats. It ends with the connection.
func (s *EventStream) read() {
	reader := bufio.NewReader(s.body)
	var event Event
	var data []string
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			s.errs <- fmt.Errorf("event stream: %w", err)
			return
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if len(data) > 0 || event.Name != "" {
				event.Data = json.RawMessage(strings.Join(data, "\n"))
				if !s.deliver(event) {
					return
				}
			}
			event, data = Event{}, nil
		case strings.HasPrefix(line, ":"):
			if !s.deliver(Event{heartbeat: true}) {
				return
			}
		default:
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			switch field {
			case "event":
				event.Name = value
			case "id":
				event.ID = value
			case "data":
				data = append(data, value)
			}
		}
	}
}

// deliver hands an event to Next, reporting false once the stream is
// closed.
func (s *EventStream) deliver(event Event) bool {
	select {
	case s.events <- event:
		return true
	case <-s.closed:
		return false
	}
}
