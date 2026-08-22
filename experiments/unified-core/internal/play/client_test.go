package play

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status, Status: http.StatusText(status),
		Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body)),
	}
}

func TestClientUsesCanonicalHTTPWorkflow(t *testing.T) {
	t.Helper()
	type observed struct {
		method string
		path   string
		body   map[string]string
	}
	var calls []observed
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		call := observed{method: request.Method, path: request.URL.RequestURI()}
		if request.Body != nil && request.ContentLength != 0 {
			if err := json.NewDecoder(request.Body).Decode(&call.body); err != nil {
				t.Errorf("decode request: %v", err)
			}
		}
		calls = append(calls, call)
		switch request.URL.Path {
		case "/v1/threads":
			if request.Method == http.MethodPost {
				return jsonResponse(http.StatusCreated, `{"id":"thr-new","kind":"chat","agent":"codex","cwd":"/tmp"}`), nil
			}
			return jsonResponse(http.StatusOK, `{"threads":[{"id":"thr-one","kind":"chat","agent":"claude","cwd":"/tmp"}]}`), nil
		case "/v1/terminals":
			return jsonResponse(http.StatusOK, `{"terminals":[{"id":"term-one","threadId":"thr-tui","lifecycle":"live","reachable":true}]}`), nil
		case "/v1/events":
			return jsonResponse(http.StatusOK, `{"events":[{"sequence":8,"threadId":"thr-one","resource":"turn","type":"turn.started"}]}`), nil
		case "/v1/threads/thr-one/requests":
			return jsonResponse(http.StatusOK, `{"requests":[{"id":"req-one","threadId":"thr-one","kind":"approval","prompt":"Proceed?","options":[{"id":"allow","label":"Allow once"}]}]}`), nil
		case "/v1/threads/thr-one/prompts":
			return jsonResponse(http.StatusAccepted, `{"id":"turn-one","state":"running"}`), nil
		case "/v1/threads/thr-one/requests/req-one/answer":
			return jsonResponse(http.StatusNoContent, ""), nil
		case "/v1/threads/thr-one/turns/turn-one/interrupt":
			return jsonResponse(http.StatusAccepted, `{"scope":"foreground_turn"}`), nil
		case "/v1/threads/thr-tui/terminal":
			return jsonResponse(http.StatusCreated, `{"id":"term-one","threadId":"thr-tui","lifecycle":"live","reachable":true}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{"error":{"code":"not_found","message":"not found"}}`), nil
		}
	})}

	client, err := NewClient("http://atc.test", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	threads, err := client.Threads(ctx)
	if err != nil || len(threads) != 1 || threads[0].ID != "thr-one" {
		t.Fatalf("threads = %#v, %v", threads, err)
	}
	terminals, err := client.Terminals(ctx)
	if err != nil || len(terminals) != 1 || !terminals[0].Reachable {
		t.Fatalf("terminals = %#v, %v", terminals, err)
	}
	events, err := client.Events(ctx, 7)
	if err != nil || len(events) != 1 || events[0].Sequence != 8 {
		t.Fatalf("events = %#v, %v", events, err)
	}
	requests, err := client.Requests(ctx, "thr-one")
	if err != nil || len(requests) != 1 || requests[0].Options[0].Label != "Allow once" {
		t.Fatalf("requests = %#v, %v", requests, err)
	}
	created, err := client.CreateThread(ctx, "chat", "codex", "/tmp")
	if err != nil || created.ID != "thr-new" {
		t.Fatalf("create = %#v, %v", created, err)
	}
	turn, err := client.Prompt(ctx, "thr-one", "hello")
	if err != nil || turn.ID != "turn-one" {
		t.Fatalf("prompt = %#v, %v", turn, err)
	}
	if err := client.Answer(ctx, "thr-one", "req-one", "allow", ""); err != nil {
		t.Fatal(err)
	}
	if err := client.Interrupt(ctx, "thr-one", "turn-one"); err != nil {
		t.Fatal(err)
	}
	terminal, err := client.OpenTerminal(ctx, "thr-tui")
	if err != nil || terminal.ID != "term-one" {
		t.Fatalf("open terminal = %#v, %v", terminal, err)
	}

	want := []observed{
		{method: "GET", path: "/v1/threads"},
		{method: "GET", path: "/v1/terminals"},
		{method: "GET", path: "/v1/events?after=7"},
		{method: "GET", path: "/v1/threads/thr-one/requests"},
		{method: "POST", path: "/v1/threads", body: map[string]string{"kind": "chat", "agent": "codex", "cwd": "/tmp"}},
		{method: "POST", path: "/v1/threads/thr-one/prompts", body: map[string]string{"text": "hello"}},
		{method: "POST", path: "/v1/threads/thr-one/requests/req-one/answer", body: map[string]string{"optionId": "allow"}},
		{method: "POST", path: "/v1/threads/thr-one/turns/turn-one/interrupt", body: map[string]string{}},
		{method: "POST", path: "/v1/threads/thr-tui/terminal", body: map[string]string{}},
	}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls =\n%#v\nwant =\n%#v", calls, want)
	}
}

func TestClientReturnsCanonicalAPIError(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(http.StatusConflict, `{"error":{"code":"turn_in_progress","message":"already running"}}`), nil
	})}
	client, err := NewClient("http://atc.test", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Prompt(context.Background(), "thr", "hello")
	apiError, ok := err.(*APIError)
	if !ok || apiError.Code != "turn_in_progress" || apiError.Status != http.StatusConflict {
		t.Fatalf("error = %#v", err)
	}
}

func TestClientRejectsNonHTTPBase(t *testing.T) {
	if _, err := NewClient("file:///tmp/socket", nil); err == nil {
		t.Fatal("accepted non-HTTP base URL")
	}
}
