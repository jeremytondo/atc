package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// CreateThread posts the create body as-is and decodes the thread; a
// refusal is the server's typed problem.
func TestCreateThread(t *testing.T) {
	type call struct {
		Method, Path, Body string
	}
	var got call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		got = call{Method: r.Method, Path: r.URL.Path, Body: strings.TrimSpace(string(body))}
		if strings.Contains(got.Body, `"prompt":"bad"`) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusBadGateway)
			_ = json.NewEncoder(w).Encode(Problem{Title: "Bad Gateway", Status: http.StatusBadGateway, Code: CodeThreadCreationFailed, Detail: "T3 Code rejected the command: no"})
			return
		}
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(Thread{ID: "thrd-x7k2f", IntegrationID: "t3code", Status: ThreadWorking})
	}))
	defer srv.Close()
	client := NewClient(srv.URL, "tok", "v1", nil, nil)

	thread, err := client.CreateThread(context.Background(), ThreadCreateParams{
		IntegrationID: "t3code", Agent: "codex", ProjectID: "proj-aaaaa", Prompt: "hi", Model: "m",
		Options: []ThreadOption{{ID: "reasoningEffort", Value: "high"}},
	})
	if err != nil || thread.ID != "thrd-x7k2f" || thread.Status != ThreadWorking {
		t.Fatalf("CreateThread = %+v, %v", thread, err)
	}
	want := call{Method: http.MethodPost, Path: "/v1/threads",
		Body: `{"integrationId":"t3code","agent":"codex","projectId":"proj-aaaaa","prompt":"hi","model":"m","options":[{"id":"reasoningEffort","value":"high"}]}`}
	if got != want {
		t.Errorf("request = %+v; want %+v", got, want)
	}
	// Options are omitted, not sent empty.
	if _, err := client.CreateThread(context.Background(), ThreadCreateParams{IntegrationID: "t3code", Agent: "codex", ProjectID: "p", Prompt: "hi", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got.Body, "options") {
		t.Errorf("body without options = %s", got.Body)
	}

	_, err = client.CreateThread(context.Background(), ThreadCreateParams{IntegrationID: "t3code", Agent: "codex", ProjectID: "p", Prompt: "bad", Model: "m"})
	var problem *Problem
	if !errors.As(err, &problem) || problem.Status != http.StatusBadGateway || problem.Code != CodeThreadCreationFailed || !strings.Contains(err.Error(), "T3 Code rejected the command: no") {
		t.Errorf("refusal = %v; want the typed 502 problem", err)
	}
}
