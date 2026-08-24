// Package codexproxy relays one native Codex TUI to the prototype's shared
// app-server. It never originates conversation methods. Observing the TUI's
// own thread/start response gives an exact root identity; only that root and
// descendants learned from subAgentActivity are forwarded as status evidence.
package codexproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

type Config struct {
	Remote    string
	StatusURL string
	CWD       string
	Command   []string
}

func Run(ctx context.Context, config Config) error {
	if config.Remote == "" || config.StatusURL == "" || config.CWD == "" || len(config.Command) == 0 {
		return errors.New("remote, status URL, cwd, and Codex command are required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	defer listener.Close()
	relay := newRelay(config.Remote, config.StatusURL)
	mux := http.NewServeMux()
	mux.HandleFunc("/", relay.serve)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.Serve(listener) }()

	remote := "ws://" + listener.Addr().String()
	arguments := append([]string{"--cd", config.CWD, "--remote", remote}, config.Command[1:]...)
	command := exec.CommandContext(ctx, config.Command[0], arguments...)
	command.Dir = config.CWD
	command.Env = os.Environ()
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := command.Start(); err != nil {
		_ = server.Close()
		return fmt.Errorf("start Codex TUI: %w", err)
	}
	tuiDone := make(chan error, 1)
	go func() { tuiDone <- command.Wait() }()
	select {
	case err := <-tuiDone:
		_ = server.Shutdown(context.Background())
		return err
	case err := <-serverDone:
		_ = command.Process.Kill()
		<-tuiDone
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("Codex relay stopped: %w", err)
	case <-ctx.Done():
		_ = command.Process.Kill()
		<-tuiDone
		_ = server.Shutdown(context.Background())
		return ctx.Err()
	}
}

type relay struct {
	remote   string
	tracker  *tracker
	evidence chan []byte
	ctx      context.Context
}

func newRelay(remote, statusURL string) *relay {
	ctx := context.Background()
	relay := &relay{remote: remote, tracker: newTracker(), evidence: make(chan []byte, 4096), ctx: ctx}
	go postEvidence(ctx, statusURL, relay.evidence)
	return relay
}

func (r *relay) serve(response http.ResponseWriter, request *http.Request) {
	client, err := websocket.Accept(response, request, &websocket.AcceptOptions{CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		return
	}
	defer client.CloseNow()
	server, _, err := websocket.Dial(request.Context(), r.remote, nil)
	if err != nil {
		_ = client.Close(websocket.StatusTryAgainLater, "shared app-server unavailable")
		return
	}
	defer server.CloseNow()
	server.SetReadLimit(16 << 20)
	client.SetReadLimit(16 << 20)

	result := make(chan error, 2)
	go func() { result <- r.forwardClient(request.Context(), client, server) }()
	go func() { result <- r.forwardServer(request.Context(), server, client) }()
	<-result
}

func (r *relay) forwardClient(ctx context.Context, source, target *websocket.Conn) error {
	for {
		kind, payload, err := source.Read(ctx)
		if err != nil {
			return err
		}
		r.tracker.client(payload)
		if err := target.Write(ctx, kind, payload); err != nil {
			return err
		}
	}
}

func (r *relay) forwardServer(ctx context.Context, source, target *websocket.Conn) error {
	for {
		kind, payload, err := source.Read(ctx)
		if err != nil {
			return err
		}
		if evidence, ok := r.tracker.server(payload); ok {
			select {
			case r.evidence <- evidence:
			default:
				// The TUI transport must not stall behind diagnostics. A full
				// queue is surfaced by the next successful status snapshot.
			}
		}
		if err := target.Write(ctx, kind, payload); err != nil {
			return err
		}
	}
}

type tracker struct {
	mu       sync.Mutex
	root     string
	requests map[string]string
	parents  map[string]string
}

func newTracker() *tracker {
	return &tracker{requests: make(map[string]string), parents: make(map[string]string)}
}

func (t *tracker) client(payload []byte) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
	}
	if json.Unmarshal(payload, &message) != nil || len(message.ID) == 0 {
		return
	}
	if message.Method != "thread/start" && message.Method != "thread/resume" && message.Method != "thread/fork" {
		return
	}
	t.mu.Lock()
	t.requests[idKey(message.ID)] = message.Method
	t.mu.Unlock()
}

func (t *tracker) server(payload []byte) ([]byte, bool) {
	var message struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result struct {
			Thread struct {
				ID string `json:"id"`
			} `json:"thread"`
			ThreadID string `json:"threadId"`
		} `json:"result"`
		Params struct {
			ThreadID string `json:"threadId"`
			Thread   struct {
				ID string `json:"id"`
			} `json:"thread"`
			Item struct {
				Type          string `json:"type"`
				AgentThreadID string `json:"agentThreadId"`
			} `json:"item"`
		} `json:"params"`
	}
	if json.Unmarshal(payload, &message) != nil {
		return nil, false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(message.ID) > 0 {
		if transition, ok := t.requests[idKey(message.ID)]; ok {
			delete(t.requests, idKey(message.ID))
			root := message.Result.Thread.ID
			if root == "" {
				root = message.Result.ThreadID
			}
			if root != "" {
				t.root = root
				t.parents = make(map[string]string)
				synthetic := map[string]any{
					"method": "thread/started", "atcExactRoot": root,
					"atcThreadTransition": strings.TrimPrefix(transition, "thread/"),
					"params":              map[string]any{"thread": map[string]any{"id": root, "status": map[string]string{"type": "unknown"}}},
				}
				encoded, _ := json.Marshal(synthetic)
				return encoded, true
			}
		}
		return nil, false
	}
	if t.root == "" || message.Method == "" {
		return nil, false
	}
	relevant := false
	switch message.Method {
	case "thread/started":
		relevant = message.Params.Thread.ID == t.root
	case "thread/status/changed":
		id := message.Params.ThreadID
		relevant = id == t.root || t.descends(id)
	case "item/started", "item/completed":
		parent := message.Params.ThreadID
		if parent == t.root || t.descends(parent) {
			if message.Params.Item.Type == "subAgentActivity" && message.Params.Item.AgentThreadID != "" {
				t.parents[message.Params.Item.AgentThreadID] = parent
			}
			relevant = true
		}
	}
	if !relevant {
		return nil, false
	}
	var wrapped map[string]any
	if json.Unmarshal(payload, &wrapped) != nil {
		return nil, false
	}
	wrapped["atcExactRoot"] = t.root
	encoded, _ := json.Marshal(wrapped)
	return encoded, true
}

func (t *tracker) descends(id string) bool {
	seen := make(map[string]bool)
	for id != "" && !seen[id] {
		seen[id] = true
		parent, ok := t.parents[id]
		if !ok {
			return false
		}
		if parent == t.root {
			return true
		}
		id = parent
	}
	return false
}

func postEvidence(ctx context.Context, endpoint string, evidence <-chan []byte) {
	client := &http.Client{Timeout: 5 * time.Second}
	for payload := range evidence {
		for {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
			if err == nil {
				request.Header.Set("Content-Type", "application/json")
				response, requestErr := client.Do(request)
				if requestErr == nil {
					_, _ = io.Copy(io.Discard, response.Body)
					_ = response.Body.Close()
					if response.StatusCode < 500 {
						break
					}
				}
			}
			timer := time.NewTimer(500 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	}
}

func idKey(id json.RawMessage) string {
	if len(id) > 0 && id[0] == '"' {
		var text string
		if json.Unmarshal(id, &text) == nil {
			return "s:" + text
		}
	}
	return "n:" + strings.TrimSpace(string(id))
}
