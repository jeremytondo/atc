package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations/claude"
	"github.com/jeremytondo/atc/internal/integrations/hookauth"
)

// hookSecret digs this launch's secret out of its header file — the same
// file the settings' curl command reads. The command carries only the
// settings path; the header file sits beside it. The composed command is
// private to the driver — it never rides the wire — so it is read from
// the fake driver's record.
func hookSecret(t *testing.T, f *fixture, terminal api.Terminal) string {
	t.Helper()
	command := f.driverCommand(terminal.ID)
	match := regexp.MustCompile(`--settings '([^']+)\.json'`).FindStringSubmatch(command)
	if match == nil {
		t.Fatalf("launch command has no settings file: %q", command)
	}
	content, err := os.ReadFile(match[1] + ".header")
	if err != nil {
		t.Fatal(err)
	}
	secret, ok := strings.CutPrefix(string(content), hookauth.SecretHeader+": ")
	if !ok {
		t.Fatalf("header file %q lacks the header prefix", content)
	}
	return secret
}

// postHook delivers one payload to the internal route — deliberately with
// no bearer token: the per-launch secret is the route's whole
// authentication.
func (f *fixture) postHook(t *testing.T, secret, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, claude.HooksPath, strings.NewReader(body))
	if secret != "" {
		req.Header.Set(hookauth.SecretHeader, secret)
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

// The full evidence path over the wire: launch composes hook wiring, the
// first prompt births the thread (a SessionStart alone does not), status
// evidence moves it, and the terminal exposes the projection — while the
// route stays outside bearer auth and the public contract.
func TestClaudeHooksDriveThreads(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{AppID: "claude/tui"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("launch: got %d; body %s", rec.Code, rec.Body)
	}
	terminal := decodeTerminal(t, rec)
	secret := hookSecret(t, f, terminal)

	// The internal route ignores the bearer token entirely: no token
	// needed with a valid secret, and the bearer token is no substitute
	// for one.
	if rec := f.postHook(t, "", `{"session_id":"s1","hook_event_name":"SessionStart"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("hook without secret: got %d, want 404", rec.Code)
	}
	if rec := f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup","cwd":"/somewhere"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("hook delivery: got %d", rec.Code)
	}

	// No thread yet: the TUI is at its prompt with nothing said (ATC-282).
	var list api.ThreadList
	rec = f.request(t, http.MethodGet, "/v1/threads", "")
	decodeInto(t, rec, &list)
	if len(list.Threads) != 0 {
		t.Fatalf("threads after SessionStart = %+v; want none until the first prompt", list.Threads)
	}

	// The first prompt mints the thread — linked, working, titled — and
	// the terminal projects it.
	f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"fix the build","cwd":"/somewhere"}`)
	rec = f.request(t, http.MethodGet, "/v1/threads", "")
	decodeInto(t, rec, &list)
	if len(list.Threads) != 1 {
		t.Fatalf("threads = %+v", list.Threads)
	}
	thread := list.Threads[0]
	if thread.IntegrationID != "claude" || thread.AppID != "claude/tui" || thread.AgentID != "claude" || thread.TerminalID != terminal.ID || thread.Status != api.ThreadWorking ||
		thread.Title != "fix the build" || thread.Cwd != "/somewhere" {
		t.Errorf("thread = %+v", thread)
	}
	if got := decodeTerminal(t, f.request(t, http.MethodGet, "/v1/terminals/"+terminal.ID, "")); got.ActiveThreadID != thread.ID {
		t.Errorf("activeThreadId = %q, want %q", got.ActiveThreadID, thread.ID)
	}

	// /clear mid-flight: the old thread persists inactive — its mid-turn
	// working was never verified to finish, so it coerces to unknown, not
	// idle — and the terminal shows a conversation with no thread yet
	// until its first prompt mints one and takes the projection.
	f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"SessionEnd","reason":"clear"}`)
	f.postHook(t, secret, `{"session_id":"s2","hook_event_name":"SessionStart","source":"clear"}`)
	if got := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, "")); got.Status != api.ThreadUnknown {
		t.Errorf("old thread after clear = %s, want unknown (mid-turn liveness unverifiable)", got.Status)
	}
	if active := decodeTerminal(t, f.request(t, http.MethodGet, "/v1/terminals/"+terminal.ID, "")).ActiveThreadID; active != "" {
		t.Errorf("activeThreadId after clear = %q, want none", active)
	}
	f.postHook(t, secret, `{"session_id":"s2","hook_event_name":"UserPromptSubmit","prompt":"next"}`)
	rec = f.request(t, http.MethodGet, "/v1/threads", "")
	decodeInto(t, rec, &list)
	if len(list.Threads) != 2 {
		t.Fatalf("threads after clear = %+v", list.Threads)
	}
	active := decodeTerminal(t, f.request(t, http.MethodGet, "/v1/terminals/"+terminal.ID, "")).ActiveThreadID
	if active == thread.ID || active == "" {
		t.Errorf("activeThreadId after clear = %q", active)
	}

	// The public contract never mentions the internal route.
	rec = f.request(t, http.MethodGet, "/openapi.json", "")
	if strings.Contains(rec.Body.String(), "internal/claude") {
		t.Error("openapi document leaks the internal hook route")
	}
}

// Deleting the terminal revokes its hook launch over the wire: once the
// DELETE returns, the launch's secret no longer validates and its
// per-launch files are gone.
func TestTerminalDeleteRevokesHookSecret(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{AppID: "claude/tui"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("launch: got %d; body %s", rec.Code, rec.Body)
	}
	terminal := decodeTerminal(t, rec)
	secret := hookSecret(t, f, terminal)
	if rec := f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("hook before delete: got %d", rec.Code)
	}

	if rec := f.request(t, http.MethodDelete, "/v1/terminals/"+terminal.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"Stop"}`); rec.Code != http.StatusNotFound {
		t.Errorf("hook after delete: got %d, want 404", rec.Code)
	}
	settings := regexp.MustCompile(`--settings '([^']+)'`).FindStringSubmatch(f.driverCommand(terminal.ID))[1]
	for _, path := range []string{settings, strings.TrimSuffix(settings, ".json") + ".header"} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("per-launch file %s survived the delete", path)
		}
	}
}

// The internal mount must not weaken the public surface: everything else
// still demands the bearer token.
func TestInternalMountKeepsAuthIntact(t *testing.T) {
	f := newFixture(t)
	req := httptest.NewRequest(http.MethodGet, "/v1/threads", nil)
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("tokenless /v1/threads with internal mount: got %d, want 401", rec.Code)
	}
	// GET on the hook path is not a mounted pattern and must not leak
	// route existence to tokenless callers.
	req = httptest.NewRequest(http.MethodGet, claude.HooksPath, nil)
	rec = httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("GET on hook path: got %d, want 401", rec.Code)
	}
}

func decodeInto(t *testing.T, rec *httptest.ResponseRecorder, out any) {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d; body %s", rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
}
