package play

import (
	"errors"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestSyncKeepsCanonicalThreadsWhenTerminalInventoryFails(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/v1/threads":
			return jsonResponse(http.StatusOK, `{"threads":[{"id":"thr-one","kind":"chat","agent":"claude","activity":"idle","backgroundActivity":"idle"}]}`), nil
		case "/v1/terminals":
			return jsonResponse(http.StatusServiceUnavailable, `{"error":{"code":"terminal_inventory_unavailable","message":"zmx offline"}}`), nil
		case "/v1/events":
			return jsonResponse(http.StatusOK, `{"events":[{"sequence":3,"threadId":"thr-one","type":"thread.created"}]}`), nil
		case "/v1/threads/thr-one/requests":
			return jsonResponse(http.StatusOK, `{"requests":[]}`), nil
		default:
			return jsonResponse(http.StatusNotFound, `{}`), nil
		}
	})}
	client, err := NewClient("http://atc.test", httpClient)
	if err != nil {
		t.Fatal(err)
	}
	m := newModel(t.Context(), client, defaultBase)
	message := m.syncCmd()().(syncMsg)
	if message.err != nil || message.warning == nil || len(message.threads) != 1 || len(message.events) != 1 {
		t.Fatalf("sync = %#v", message)
	}
	updated, _ := m.Update(message)
	m = updated.(model)
	if !m.connected || m.cursor != 3 || len(m.threads) != 1 || !strings.Contains(m.status, "terminal state unavailable") {
		t.Fatalf("partial state = %#v", m)
	}
}

func TestReconnectPreservesStateAndCatchesUpCursor(t *testing.T) {
	m := newModel(t.Context(), nil, defaultBase)
	connected, _ := m.Update(syncMsg{
		threads: []Thread{{ID: "thr-one", Kind: "chat", Activity: "working", BackgroundActivity: "idle"}},
		events:  []Event{{Sequence: 4, ThreadID: "thr-one", Type: "turn.started"}},
	})
	m = connected.(model)
	if !m.connected || m.cursor != 4 {
		t.Fatalf("initial state = connected %v cursor %d", m.connected, m.cursor)
	}

	disconnected, _ := m.Update(syncMsg{err: errors.New("connection refused")})
	m = disconnected.(model)
	if m.connected || len(m.threads) != 1 || m.cursor != 4 {
		t.Fatalf("offline state discarded snapshot: %#v", m)
	}

	reconnected, _ := m.Update(syncMsg{
		threads: []Thread{{ID: "thr-one", Kind: "chat", Activity: "idle", BackgroundActivity: "idle"}},
		events: []Event{
			{Sequence: 4, ThreadID: "thr-one", Type: "turn.started"},
			{Sequence: 5, ThreadID: "thr-one", Type: "turn.ended"},
		},
	})
	m = reconnected.(model)
	if !m.connected || m.cursor != 5 || len(m.events) != 2 {
		t.Fatalf("reconnected state = connected %v cursor %d events %#v", m.connected, m.cursor, m.events)
	}
	if !strings.Contains(m.status, "caught up 1 event(s)") {
		t.Fatalf("status = %q", m.status)
	}
}

func TestLifecycleViewKeepsIndependentDimensionsLegible(t *testing.T) {
	thread := Thread{
		ID: "thr-1234567890", Kind: "tui", Agent: "codex", Activity: "needs_input",
		ActiveTurn: &Turn{ID: "turn-one", State: "running"}, BackgroundActivity: "working",
		PendingRequestCount: 2,
	}
	terminal := Terminal{ID: "term-one", ThreadID: thread.ID, Lifecycle: "live", Reachable: false}
	view := threadLifecycle(thread, terminal)
	for _, expected := range []string{"needs_input", "foreground:running", "background:working", "pending:2", "terminal:live/unreachable"} {
		if !strings.Contains(view, expected) {
			t.Fatalf("lifecycle %q is missing %q", view, expected)
		}
	}
}

func TestTUISessionShowsDirectZmxAttachCommand(t *testing.T) {
	m := newModel(t.Context(), nil, defaultBase)
	terminal := Terminal{ID: "term_0123456789abcdef01234567", ThreadID: "thr-one", Lifecycle: "live", Reachable: true}
	updated, _ := m.Update(terminalOpenedMsg{terminal: terminal})
	m = updated.(model)
	if !strings.Contains(m.status, "make attach TERMINAL="+terminal.ID) {
		t.Fatalf("status = %q", m.status)
	}

	m.threads = []Thread{{ID: "thr-one", Kind: "tui", Agent: "claude", CWD: "/tmp"}}
	m.terminals["thr-one"] = terminal
	if detail := m.threadDetail(100, 20); !strings.Contains(detail, "attach elsewhere: make attach TERMINAL="+terminal.ID) {
		t.Fatalf("detail = %q", detail)
	}
}

func TestConversationCoalescesStreamedACPResponse(t *testing.T) {
	request := PendingRequest{Prompt: "Allow the edit?"}
	lines := conversationLines([]Event{
		{Sequence: 1, TurnID: "turn-one", Type: "user.message", Text: "Explain this repository"},
		{Sequence: 2, TurnID: "turn-one", Type: "assistant.delta", Text: "It has "},
		{Sequence: 3, ThreadID: "thr-one", Type: "thread.activity", Activity: "working"},
		{Sequence: 4, TurnID: "turn-one", Type: "assistant.delta", Text: "two packages."},
		{Sequence: 5, TurnID: "turn-one", Type: "request.opened", Request: &request},
	}, 80)
	joined := strings.Join(lines, "\n")
	for _, expected := range []string{"You: Explain this repository", "Agent: It has two packages.", "Request: Allow the edit?"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("conversation %q is missing %q", joined, expected)
		}
	}
}

func TestAnswerModeUsesLabeledOpaqueOptionID(t *testing.T) {
	m := newModel(t.Context(), nil, defaultBase)
	m.mode = modeAnswer
	m.requests = []PendingRequest{{
		ID: "req-one", ThreadID: "thr-one", Kind: "approval", Prompt: "May I?",
		Options: []RequestOption{{ID: "opaque-deny", Label: "Deny once"}, {ID: "opaque-allow", Label: "Allow once"}},
	}}
	updated, command := m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m = updated.(model)
	if m.option != 1 || command != nil {
		t.Fatalf("option = %d command = %v", m.option, command)
	}
	if !strings.Contains(m.answerView(), "Allow once") || strings.Contains(m.answerView(), "opaque-allow") {
		t.Fatalf("answer view leaked IDs or omitted labels: %s", m.answerView())
	}
}

func TestPlayPackageCannotImportServerInternals(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate test source")
	}
	directory := filepath.Dir(file)
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") {
			continue
		}
		parsed, err := parser.ParseFile(token.NewFileSet(), filepath.Join(directory, entry.Name()), nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, spec := range parsed.Imports {
			path := strings.Trim(spec.Path.Value, `"`)
			for _, forbidden := range []string{"/internal/core", "/internal/adapters", "/internal/store", "/internal/ports", "/internal/provider", "/internal/status", "/internal/domain"} {
				if strings.Contains(path, forbidden) {
					t.Fatalf("%s imports forbidden server package %q", entry.Name(), path)
				}
			}
		}
	}
}
