package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The message and decision calls post their bodies as-is and decode the
// resource; refusals are the server's typed problems.
func TestSendMessageAndDecide(t *testing.T) {
	type call struct {
		Method, Path, Body string
	}
	var got call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = call{Method: r.Method, Path: r.URL.Path, Body: strings.TrimSpace(string(body))}
		switch {
		case strings.HasSuffix(r.URL.Path, "/messages"):
			w.WriteHeader(http.StatusAccepted)
			_ = json.NewEncoder(w).Encode(ThreadMessage{ID: "msg-aaaaaaaaaa", ThreadID: "thrd-x7k2f", TurnID: "turn-bbbbbbbbbb", Delivery: MessageAccepted})
		default:
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(Problem{Title: "Conflict", Status: http.StatusConflict, Code: CodeApprovalResolved, Detail: "resolved"})
		}
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "tok", "v1", nil, nil)

	message, err := client.SendThreadMessage(context.Background(), "thrd-x7k2f", ThreadMessageParams{Text: "hi", Key: "k1"})
	if err != nil || message.ID != "msg-aaaaaaaaaa" || message.TurnID != "turn-bbbbbbbbbb" || message.Delivery != MessageAccepted {
		t.Fatalf("SendThreadMessage = %+v, %v", message, err)
	}
	if want := (call{Method: http.MethodPost, Path: "/v1/threads/thrd-x7k2f/messages", Body: `{"text":"hi","key":"k1"}`}); got != want {
		t.Errorf("request = %+v; want %+v", got, want)
	}
	if _, err := client.SendThreadMessage(context.Background(), "thrd-x7k2f", ThreadMessageParams{Text: "hi"}); err != nil || strings.Contains(got.Body, "key") {
		t.Errorf("body without a key = %s, %v", got.Body, err)
	}

	_, err = client.DecideThreadApproval(context.Background(), "thrd-x7k2f", "aprv-cccccccccc", ApprovalDecisionParams{Decision: DecisionApprove})
	var problem *Problem
	if !errors.As(err, &problem) || problem.Status != http.StatusConflict || problem.Code != CodeApprovalResolved {
		t.Errorf("refusal = %v; want the typed 409 problem", err)
	}
	if want := (call{Method: http.MethodPost, Path: "/v1/threads/thrd-x7k2f/approvals/aprv-cccccccccc/decide", Body: `{"decision":"approve"}`}); got != want {
		t.Errorf("request = %+v; want %+v", got, want)
	}
}

// The event stream reader: the feed's headers, events with their ids and
// multi-line data, heartbeats that keep the stream alive without
// reaching the caller, the stall that ends a silent stream, and the
// typed problem for a refused connection.
func TestEvents(t *testing.T) {
	var lastEventID string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer tok" {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(Problem{Title: "Unauthorized", Status: http.StatusUnauthorized, Code: CodeUnauthorized})
			return
		}
		lastEventID = r.Header.Get("Last-Event-ID")
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		_, _ = fmt.Fprint(w, ": connected\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "event: thread.updated\nid: 7\ndata: {\"seq\":7,\ndata:\"resource\":\"thread\",\"id\":\"thrd-aaaaa\"}\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, ": heartbeat\n\n")
		flusher.Flush()
		_, _ = fmt.Fprint(w, "event: resync\nid: 9\ndata: {\"seq\":9}\n\n")
		flusher.Flush()
		// Then silence, until the client gives up.
		<-r.Context().Done()
	}))
	defer srv.Close()

	client := NewClient(srv.URL, "tok", "v1", nil, nil)
	stream, err := client.Events(context.Background(), "5", 300*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	if lastEventID != "5" {
		t.Errorf("Last-Event-ID presented = %q", lastEventID)
	}
	next := func() Event {
		t.Helper()
		event, err := stream.Next(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		return event
	}
	event := next()
	change, err := event.Change()
	if err != nil || event.Name != "thread.updated" || event.ID != "7" || change.Seq != 7 || change.Resource != "thread" || change.ID != "thrd-aaaaa" {
		t.Errorf("first event = %+v (%+v, %v)", event, change, err)
	}
	if event = next(); event.Name != "resync" || event.ID != "9" || stream.LastEventID != "9" {
		t.Errorf("second event = %+v; last id %q", event, stream.LastEventID)
	}
	if _, err := stream.Next(context.Background()); !errors.Is(err, ErrStreamStalled) {
		t.Errorf("silent stream = %v; want ErrStreamStalled", err)
	}

	_, err = NewClient(srv.URL, "", "v1", nil, nil).Events(context.Background(), "", 0)
	var problem *Problem
	if !errors.As(err, &problem) || problem.Status != http.StatusUnauthorized {
		t.Errorf("refused stream = %v; want the typed 401", err)
	}
	// A cancelled context ends the wait.
	stream, err = client.Events(context.Background(), "", 0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = stream.Close() }()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := stream.Next(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("Next on a cancelled context = %v", err)
	}
}
