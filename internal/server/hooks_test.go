package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/agents/claude"
	"github.com/jeremytondo/atc/internal/agents/hookauth"
	"github.com/jeremytondo/atc/internal/api"
)

// hookSecret digs this launch's secret out of its header file — the same
// file the settings' curl command reads. The command carries only the
// settings path; the header file sits beside it.
func hookSecret(t *testing.T, terminal api.Terminal) string {
	t.Helper()
	match := regexp.MustCompile(`--settings '([^']+)\.json'`).FindStringSubmatch(terminal.Command)
	if match == nil {
		t.Fatalf("launch command has no settings file: %q", terminal.Command)
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

// The full evidence path over the wire: launch composes hook wiring, a
// SessionStart births the thread, status evidence moves it, and the
// terminal exposes the projection — while the route stays outside bearer
// auth and the public contract.
func TestClaudeHooksDriveThreads(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodPost, "/v1/agents/claude/launch", `{"projectId":"`+f.projectID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("launch: got %d; body %s", rec.Code, rec.Body)
	}
	terminal := decodeTerminal(t, rec)
	secret := hookSecret(t, terminal)

	// The internal route ignores the bearer token entirely: no token
	// needed with a valid secret, and the bearer token is no substitute
	// for one.
	if rec := f.postHook(t, "", `{"session_id":"s1","hook_event_name":"SessionStart"}`); rec.Code != http.StatusNotFound {
		t.Fatalf("hook without secret: got %d, want 404", rec.Code)
	}
	if rec := f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup","cwd":"/somewhere"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("hook delivery: got %d", rec.Code)
	}

	// The thread exists, linked and idle, and the terminal projects it.
	var list api.ThreadList
	rec = f.request(t, http.MethodGet, "/v1/threads", "")
	decodeInto(t, rec, &list)
	if len(list.Threads) != 1 {
		t.Fatalf("threads = %+v", list.Threads)
	}
	thread := list.Threads[0]
	if thread.Agent != "claude" || thread.TerminalID != terminal.ID || thread.Status != api.ThreadIdle || thread.Cwd != "/somewhere" {
		t.Errorf("thread = %+v", thread)
	}
	if got := decodeTerminal(t, f.request(t, http.MethodGet, "/v1/terminals/"+terminal.ID, "")); got.ActiveThreadID != thread.ID {
		t.Errorf("activeThreadId = %q, want %q", got.ActiveThreadID, thread.ID)
	}

	// A turn: prompt evidence marks it working and titles it.
	f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"fix the build"}`)
	rec = f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, "")
	got := decodeThread(t, rec)
	if got.Status != api.ThreadWorking || got.Title != "fix the build" {
		t.Errorf("after prompt: %+v", got)
	}

	// /clear mid-flight: the old thread persists inactive (working
	// coerces), the new one takes the projection.
	f.postHook(t, secret, `{"session_id":"s1","hook_event_name":"SessionEnd","reason":"clear"}`)
	f.postHook(t, secret, `{"session_id":"s2","hook_event_name":"SessionStart","source":"clear"}`)
	rec = f.request(t, http.MethodGet, "/v1/threads", "")
	decodeInto(t, rec, &list)
	if len(list.Threads) != 2 {
		t.Fatalf("threads after clear = %+v", list.Threads)
	}
	if got := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, "")); got.Status != api.ThreadIdle {
		t.Errorf("old thread after clear = %s, want idle (SessionEnd evidence)", got.Status)
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
