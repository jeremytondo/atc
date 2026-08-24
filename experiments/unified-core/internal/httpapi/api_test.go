package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/elevenideas/atc/experiments/unified-core/internal/core"
	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
	"github.com/elevenideas/atc/experiments/unified-core/internal/store"
)

type apiChat struct{}

func (apiChat) Open(_ context.Context, open ports.ChatOpen) (ports.ChatSession, string, error) {
	return apiChatSession{}, "private-session-must-not-leak", nil
}

type apiChatSession struct{}

func (apiChatSession) Prompt(context.Context, string, string) (domain.TurnOutcome, error) {
	return domain.TurnCompleted, nil
}
func (apiChatSession) Interrupt(context.Context, string) error      { return nil }
func (apiChatSession) Answer(context.Context, string, string) error { return nil }
func (apiChatSession) Close(context.Context) error                  { return nil }

type apiTerminal struct {
	entries map[string]ports.TerminalEntry
}

func (a *apiTerminal) Open(_ context.Context, open ports.TerminalOpen) error {
	a.entries[open.TerminalID] = ports.TerminalEntry{Name: open.TerminalID, Reachable: true, DaemonPID: 42}
	return nil
}
func (a *apiTerminal) Inventory(context.Context) ([]ports.TerminalEntry, error) {
	result := make([]ports.TerminalEntry, 0, len(a.entries))
	for _, entry := range a.entries {
		result = append(result, entry)
	}
	return result, nil
}
func (a *apiTerminal) Terminate(_ context.Context, name string) error {
	delete(a.entries, name)
	return nil
}

func TestCanonicalContractHasNoProtocolLeakage(t *testing.T) {
	service, err := core.New(core.Config{
		Repository: store.NewMemory(), Chat: apiChat{},
		Terminal: &apiTerminal{entries: make(map[string]ports.TerminalEntry)}, StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(service, false)
	created := request(t, handler, http.MethodPost, "/v1/threads", `{"kind":"chat","agent":"codex","cwd":"`+t.TempDir()+`"}`, "127.0.0.1:1234")
	if created.Code != http.StatusCreated {
		t.Fatalf("create = %d: %s", created.Code, created.Body.String())
	}
	var thread domain.Thread
	if err := json.Unmarshal(created.Body.Bytes(), &thread); err != nil {
		t.Fatal(err)
	}
	prompt := request(t, handler, http.MethodPost, "/v1/threads/"+thread.ID+"/prompts", `{"text":"hello"}`, "127.0.0.1:1234")
	if prompt.Code != http.StatusAccepted {
		t.Fatalf("prompt = %d: %s", prompt.Code, prompt.Body.String())
	}
	responses := []string{
		created.Body.String(),
		request(t, handler, http.MethodGet, "/v1/threads/"+thread.ID, "", "127.0.0.1:1234").Body.String(),
		request(t, handler, http.MethodGet, "/v1/events", "", "127.0.0.1:1234").Body.String(),
	}
	for _, response := range responses {
		lower := strings.ToLower(response)
		for _, forbidden := range []string{"providersession", "sessionid", "zmx", "acp", "jsonrpc", "hook_event", "daemonpid"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("public response leaked %q: %s", forbidden, response)
			}
		}
	}
	debug := request(t, handler, http.MethodGet, "/debug/timeline", "", "127.0.0.1:1234")
	if debug.Code != http.StatusNotFound {
		t.Fatalf("debug endpoint without flag = %d", debug.Code)
	}
}

func TestWrongKindErrorAndLocalOnlyCreation(t *testing.T) {
	service, err := core.New(core.Config{
		Repository: store.NewMemory(), Chat: apiChat{},
		Terminal: &apiTerminal{entries: make(map[string]ports.TerminalEntry)}, StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(service, false)
	remote := request(t, handler, http.MethodPost, "/v1/threads", `{"kind":"chat","agent":"codex","cwd":"/tmp"}`, "198.51.100.2:1234")
	if remote.Code != http.StatusForbidden || !strings.Contains(remote.Body.String(), `"code":"local_only"`) {
		t.Fatalf("remote create = %d: %s", remote.Code, remote.Body.String())
	}
	created := request(t, handler, http.MethodPost, "/v1/threads", `{"kind":"tui","agent":"claude","cwd":"`+t.TempDir()+`"}`, "[::1]:1234")
	var thread domain.Thread
	if err := json.Unmarshal(created.Body.Bytes(), &thread); err != nil {
		t.Fatal(err)
	}
	wrong := request(t, handler, http.MethodPost, "/v1/threads/"+thread.ID+"/prompts", `{"text":"no"}`, "127.0.0.1:1")
	if wrong.Code != http.StatusConflict || !strings.Contains(wrong.Body.String(), `"code":"wrong_thread_kind"`) {
		t.Fatalf("wrong kind = %d: %s", wrong.Code, wrong.Body.String())
	}
	opened := request(t, handler, http.MethodPost, "/v1/threads/"+thread.ID+"/terminal", `{}`, "127.0.0.1:1")
	var terminal domain.Terminal
	if err := json.Unmarshal(opened.Body.Bytes(), &terminal); err != nil {
		t.Fatal(err)
	}
	proxied := request(t, handler, http.MethodPost, "/v1/terminals/"+terminal.ID+"/attach", "", "127.0.0.1:1")
	if proxied.Code != http.StatusNotFound {
		t.Fatalf("terminal proxy route still exists: %d", proxied.Code)
	}
}

func TestEventCursorReconnect(t *testing.T) {
	service, err := core.New(core.Config{
		Repository: store.NewMemory(), Chat: apiChat{},
		Terminal: &apiTerminal{entries: make(map[string]ports.TerminalEntry)}, StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := New(service, false)
	first := request(t, handler, http.MethodPost, "/v1/threads", `{"kind":"chat","agent":"codex","cwd":"`+t.TempDir()+`"}`, "127.0.0.1:1")
	if first.Code != http.StatusCreated {
		t.Fatal(first.Body.String())
	}
	initial := service.EventsAfter(0, "")
	cursor := initial[len(initial)-1].Sequence
	second := request(t, handler, http.MethodPost, "/v1/threads", `{"kind":"tui","agent":"claude","cwd":"`+t.TempDir()+`"}`, "127.0.0.1:1")
	if second.Code != http.StatusCreated {
		t.Fatal(second.Body.String())
	}
	reconnected := request(t, handler, http.MethodGet, "/v1/events?after="+strconvFormat(cursor), "", "127.0.0.1:1")
	var body struct {
		Events []domain.Event `json:"events"`
	}
	if err := json.Unmarshal(reconnected.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Events) != 1 || body.Events[0].Sequence <= cursor {
		t.Fatalf("reconnected events = %#v", body.Events)
	}
}

func request(t *testing.T, handler http.Handler, method, path, body, remote string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewBufferString(body))
	req.RemoteAddr = remote
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, req)
	return response
}

func strconvFormat(value uint64) string {
	const digits = "0123456789"
	if value == 0 {
		return "0"
	}
	buffer := make([]byte, 0, 20)
	for value > 0 {
		buffer = append(buffer, digits[value%10])
		value /= 10
	}
	for left, right := 0, len(buffer)-1; left < right; left, right = left+1, right-1 {
		buffer[left], buffer[right] = buffer[right], buffer[left]
	}
	return string(buffer)
}
