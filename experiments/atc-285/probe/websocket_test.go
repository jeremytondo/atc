package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

func TestWatchWebSocketReconnectsAfterLastSequence(t *testing.T) {
	fixtures := loadSnapshots(t)
	threadRunning := fixtureThread(t, fixtures[1])
	threadWaiting := fixtureThread(t, fixtures[2])

	var connections atomic.Int32
	synchronized := make(chan struct{})
	serverErrors := make(chan error, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/auth/websocket-ticket":
			if got := r.Header.Get("Authorization"); got != "Bearer read-token" {
				serverErrors <- fmt.Errorf("ticket authorization = %q", got)
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]string{"ticket": "one-use-ticket"})
		case "/ws":
			connection, err := websocket.Accept(w, r, nil)
			if err != nil {
				serverErrors <- err
				return
			}
			defer func() { _ = connection.CloseNow() }()
			index := connections.Add(1)
			ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
			defer cancel()
			request, err := readRPCEnvelope(ctx, connection)
			if err != nil {
				serverErrors <- err
				return
			}
			payload, _ := request["payload"].(map[string]any)
			if request["_tag"] != "Request" || request["tag"] != shellSubscriptionMethod {
				serverErrors <- fmt.Errorf("unexpected request: %#v", request)
				return
			}
			if index == 1 {
				if _, exists := payload["afterSequence"]; exists {
					serverErrors <- fmt.Errorf("initial request unexpectedly resumed: %#v", payload)
					return
				}
				if err := writeRPCChunk(ctx, connection, request, []any{
					map[string]any{"kind": "snapshot", "snapshot": fixtures[0]},
				}); err != nil {
					serverErrors <- err
					return
				}
				if err := expectAck(ctx, connection); err != nil {
					serverErrors <- err
					return
				}
				if err := writeRPCChunk(ctx, connection, request, []any{
					map[string]any{"kind": "thread-upserted", "sequence": 2, "thread": threadRunning},
				}); err != nil {
					serverErrors <- err
					return
				}
				if err := expectAck(ctx, connection); err != nil {
					serverErrors <- err
					return
				}
				_ = connection.CloseNow()
				serverErrors <- nil
				return
			}
			if index != 2 || payload["afterSequence"] != float64(2) {
				serverErrors <- fmt.Errorf("reconnect payload = %#v", payload)
				return
			}
			if err := writeRPCChunk(ctx, connection, request, []any{
				map[string]any{"kind": "thread-upserted", "sequence": 3, "thread": threadWaiting},
				map[string]any{"kind": "synchronized"},
			}); err != nil {
				serverErrors <- err
				return
			}
			if err := expectAck(ctx, connection); err != nil {
				serverErrors <- err
				return
			}
			close(synchronized)
			serverErrors <- nil
			<-ctx.Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	client, err := newClient(server.URL, "read-token", server.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var output bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- watchWebSocket(ctx, client, "/work/atc", &output) }()

	select {
	case <-synchronized:
		cancel()
	case <-time.After(5 * time.Second):
		cancel()
		t.Fatal("timed out waiting for reconnected subscription")
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-serverErrors; err != nil {
			t.Fatal(err)
		}
	}

	var got []change
	decoder := json.NewDecoder(&output)
	for decoder.More() {
		var item change
		if err := decoder.Decode(&item); err != nil {
			t.Fatal(err)
		}
		got = append(got, item)
	}
	want := []change{
		{Kind: "created", Thread: projectedThread{
			ID: "t3-thread-1", ProjectID: "project-1", Project: "atc",
			WorkspaceRoot: "/work/atc", CWD: "/work/atc", Title: "New T3 thread",
			Status: api.ThreadWorking, NativeStatus: "running",
			UpdatedAt: time.Date(2026, 9, 1, 20, 0, 1, 0, time.UTC),
		}},
		{Kind: "status_changed", Thread: projectedThread{
			ID: "t3-thread-1", ProjectID: "project-1", Project: "atc",
			WorkspaceRoot: "/work/atc", CWD: "/work/atc", Title: "New T3 thread",
			Status: api.ThreadWaitingForPermission, NativeStatus: "running",
			UpdatedAt: time.Date(2026, 9, 1, 20, 0, 2, 0, time.UTC),
		}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Fatalf("changes mismatch (-want +got):\n%s", diff)
	}
}

func TestProjectUsesWorktreeAsThreadCWD(t *testing.T) {
	sequence := uint64(1)
	projects := []projectShell{{ID: "project-1", Title: "atc", WorkspaceRoot: "/work/atc"}}
	threads := []threadShell{{
		ID: "thread-1", ProjectID: "project-1", Title: "worktree thread",
		WorktreePath:        nullablePath{Value: "/work/atc-feature", Set: true},
		HasPendingApprovals: boolPointer(false), HasPendingUserInput: boolPointer(false),
		UpdatedAt: time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC),
	}}
	got := project(shellSnapshot{
		Sequence: &sequence, Projects: &projects, Threads: &threads,
		UpdatedAt: time.Date(2026, 9, 1, 20, 0, 0, 0, time.UTC),
	}, "")
	if diff := cmp.Diff("/work/atc-feature", got[0].CWD); diff != "" {
		t.Fatalf("cwd mismatch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff("/work/atc", got[0].WorkspaceRoot); diff != "" {
		t.Fatalf("workspace root mismatch (-want +got):\n%s", diff)
	}
}

func fixtureThread(t *testing.T, snapshot json.RawMessage) json.RawMessage {
	t.Helper()
	var decoded struct {
		Threads []json.RawMessage `json:"threads"`
	}
	if err := json.Unmarshal(snapshot, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded.Threads[0]
}

func readRPCEnvelope(ctx context.Context, connection *websocket.Conn) (map[string]any, error) {
	_, encoded, err := connection.Read(ctx)
	if err != nil {
		return nil, err
	}
	var envelope map[string]any
	if err := json.Unmarshal(encoded, &envelope); err != nil {
		return nil, err
	}
	return envelope, nil
}

func writeRPCChunk(
	ctx context.Context,
	connection *websocket.Conn,
	request map[string]any,
	values []any,
) error {
	return connection.Write(ctx, websocket.MessageText, mustJSON(map[string]any{
		"_tag": "Chunk", "requestId": request["id"], "values": values,
	}))
}

func expectAck(ctx context.Context, connection *websocket.Conn) error {
	envelope, err := readRPCEnvelope(ctx, connection)
	if err != nil {
		return err
	}
	if envelope["_tag"] != "Ack" || envelope["requestId"] != "1" {
		return fmt.Errorf("unexpected ack: %#v", envelope)
	}
	return nil
}

func boolPointer(value bool) *bool { return &value }

func mustJSON(value any) []byte {
	encoded, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return encoded
}
