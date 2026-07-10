package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actionstore "github.com/jeremytondo/atelier-code/internal/action"
	"github.com/jeremytondo/atelier-code/internal/diagnostics"
	"github.com/jeremytondo/atelier-code/internal/project"
	"github.com/jeremytondo/atelier-code/internal/session"
	"github.com/jeremytondo/atelier-code/internal/store"
	"github.com/jeremytondo/atelier-code/internal/zmx"
)

// fakeMux is a faked session.Multiplexer for driving the API end to end.
type fakeMux struct {
	sessions    []zmx.Session
	startErr    error
	attachPTY   zmx.PTY
	attachErr   error
	attachCalls int
	attachRows  uint16
	attachCols  uint16
	lastStart   struct {
		name, dir string
		argv      []string
	}
	lastSend struct {
		name    string
		payload []byte
	}
	lastTerminate string
}

func (f *fakeMux) Start(_ context.Context, name, dir string, argv []string) error {
	f.lastStart.name, f.lastStart.dir = name, dir
	f.lastStart.argv = append([]string(nil), argv...)
	if f.startErr != nil {
		return f.startErr
	}
	f.sessions = append(f.sessions, zmx.Session{Name: name, StartDir: dir, Cmd: strings.Join(argv, " ")})
	return nil
}

func (f *fakeMux) Send(_ context.Context, name string, payload []byte) error {
	f.lastSend.name, f.lastSend.payload = name, payload
	return nil
}

func (f *fakeMux) Attach(_ context.Context, _ string, rows, cols uint16) (zmx.PTY, error) {
	f.attachCalls++
	f.attachRows = rows
	f.attachCols = cols
	return f.attachPTY, f.attachErr
}

func (f *fakeMux) List(context.Context) ([]zmx.Session, error) {
	return f.sessions, nil
}

func (f *fakeMux) Terminate(_ context.Context, name string) error {
	f.lastTerminate = name
	for i, s := range f.sessions {
		if s.Name == name {
			f.sessions = append(f.sessions[:i], f.sessions[i+1:]...)
			break
		}
	}
	return nil
}

func newHandler(t *testing.T, mux *fakeMux) (http.Handler, *store.Store) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/atc.db")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	actions := session.ActionRegistry{
		"claude": {Command: "claude"},
		"codex": {
			Command: "codex",
			Prompt:  &session.PromptSpec{},
			Params: map[string]session.ParamSpec{
				"model": {Type: "enum", Values: []string{"gpt-5"}, Flag: "--model"},
			},
		},
	}
	environments := session.EnvironmentRegistry{
		"host-login-shell": {Kind: session.EnvironmentKindHostLoginShell},
	}
	actionStore := actionstore.NewStore(filepath.Join(t.TempDir(), "actions.json"), actions)
	projects := project.NewService(st, nil)
	sessions := session.NewService(st, mux, actionStore, environments, projects, nil)
	return Routes(diagnostics.DefaultDiagnostics(), sessions, projects, actionStore, nil), st
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	h.ServeHTTP(rec, req)
	return rec
}

func TestStartSessionReturnsFullSession(t *testing.T) {
	mux := &fakeMux{}
	h, _ := newHandler(t, mux)
	workDir := t.TempDir()
	rec := do(t, h, http.MethodPost, "/sessions/start",
		`{"action":"codex","params":{"model":"gpt-5"},"workingDir":"`+workDir+`","prompt":"review this","name":"Review"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp struct {
		ID          string         `json:"id"`
		Name        string         `json:"name"`
		Action      string         `json:"action"`
		Environment string         `json:"environment"`
		Params      map[string]any `json:"params"`
		WorkingDir  string         `json:"workingDir"`
		Prompt      string         `json:"prompt"`
		Status      string         `json:"status"`
		Attachable  bool           `json:"attachable"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.ID, "ses_") || resp.Name != "Review" || resp.Action != "codex" || resp.Environment != "host-login-shell" ||
		resp.WorkingDir != workDir || resp.Prompt != "review this" || resp.Status != "running" || !resp.Attachable {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Params["model"] != "gpt-5" {
		t.Fatalf("params = %#v", resp.Params)
	}
	if mux.lastStart.name != zmx.NameForID(resp.ID) || mux.lastStart.dir != workDir {
		t.Fatalf("start = %+v", mux.lastStart)
	}
	if got := strings.Join(mux.lastStart.argv, " "); !strings.Contains(got, "-l -i -c codex --model gpt-5 'review this'") {
		t.Fatalf("argv = %#v", mux.lastStart.argv)
	}
	if strings.Contains(rec.Body.String(), "atc-") {
		t.Fatalf("response leaked zmx name: %s", rec.Body)
	}
}

func TestStartSessionValidationErrors(t *testing.T) {
	workDir := t.TempDir()
	notADir := filepath.Join(workDir, "file.txt")
	if err := os.WriteFile(notADir, []byte("x"), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
	tests := []struct {
		name string
		body string
		code string
	}{
		{name: "missing workingDir", body: `{"action":"claude"}`, code: "invalid_request"},
		{name: "blank workingDir", body: `{"action":"claude","workingDir":"   "}`, code: "invalid_request"},
		{name: "missing action", body: `{"workingDir":"` + workDir + `"}`, code: "invalid_request"},
		{name: "unknown action", body: `{"action":"ghost","workingDir":"` + workDir + `"}`, code: "unknown_action"},
		{name: "unknown environment", body: `{"action":"codex","environment":"ghost","workingDir":"` + workDir + `"}`, code: "unknown_environment"},
		{name: "invalid params", body: `{"action":"codex","workingDir":"` + workDir + `","params":{"model":"gpt-4"}}`, code: "invalid_params"},
		{name: "unsupported prompt", body: `{"action":"claude","workingDir":"` + workDir + `","prompt":"do it"}`, code: "invalid_params"},
		{name: "relative workingDir", body: `{"action":"claude","workingDir":"relative/path"}`, code: "invalid_working_dir"},
		{name: "missing directory", body: `{"action":"claude","workingDir":"` + filepath.Join(workDir, "missing") + `"}`, code: "invalid_working_dir"},
		{name: "workingDir is a file", body: `{"action":"claude","workingDir":"` + notADir + `"}`, code: "invalid_working_dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := &fakeMux{}
			h, st := newHandler(t, mux)
			rec := do(t, h, http.MethodPost, "/sessions/start", tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
			}
			assertErrorCode(t, rec, tt.code)
			if mux.lastStart.name != "" {
				t.Fatalf("start called: %+v", mux.lastStart)
			}
			records, err := st.List(context.Background(), store.ListFilter{IncludeArchived: true})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(records) != 0 {
				t.Fatalf("records = %+v, want none", records)
			}
		})
	}
}

func TestStartSessionLaunchFailureReturnsSessionID(t *testing.T) {
	mux := &fakeMux{startErr: errors.New("zmx failed")}
	h, st := newHandler(t, mux)
	rec := do(t, h, http.MethodPost, "/sessions/start", `{"action":"claude","workingDir":"`+t.TempDir()+`"}`)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body)
	}
	var resp struct {
		Error     string `json:"error"`
		Message   string `json:"message"`
		SessionID string `json:"sessionId"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Error != "launch_failed" || resp.Message != "action failed to launch" || resp.SessionID == "" {
		t.Fatalf("error response = %+v", resp)
	}
	stored, err := st.Get(context.Background(), resp.SessionID)
	if err != nil {
		t.Fatalf("Get failed record: %v", err)
	}
	if stored.Status != store.StatusFailed || stored.FailureCode != "launch_failed" {
		t.Fatalf("stored = %+v", stored)
	}
}

func TestListSessionsOmitsPromptAndParamsAndFiltersStatus(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_old", "Old")
	seedFailed(t, st, "ses_failed")
	seedRunning(t, st, "ses_live", "Live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	rec := do(t, h, http.MethodGet, "/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var raw map[string][]map[string]any
	if err := json.NewDecoder(rec.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(raw["sessions"]) != 3 {
		t.Fatalf("sessions = %#v", raw["sessions"])
	}
	for _, item := range raw["sessions"] {
		if _, ok := item["params"]; ok {
			t.Fatalf("list item leaked params: %#v", item)
		}
		if _, ok := item["prompt"]; ok {
			t.Fatalf("list item leaked prompt: %#v", item)
		}
	}

	rec = do(t, h, http.MethodGet, "/sessions?status=failed", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var failed SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&failed); err != nil {
		t.Fatalf("decode failed list: %v", err)
	}
	if len(failed.Sessions) != 1 || failed.Sessions[0].ID != "ses_failed" {
		t.Fatalf("failed list = %+v", failed.Sessions)
	}

	rec = do(t, h, http.MethodGet, "/sessions?status=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d, want 400", rec.Code)
	}
	assertErrorCode(t, rec, "invalid_request")
}

func TestReadSessionIncludesPromptAndParams(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_detail", "Detail")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_detail")}}

	rec := do(t, h, http.MethodGet, "/sessions/ses_detail", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ID != "ses_detail" || resp.Name != "Detail" || resp.Action != "codex" || resp.Environment != "host-login-shell" || resp.Prompt != "hello" || resp.Params["model"] != "gpt-5" || !resp.Attachable {
		t.Fatalf("detail = %+v", resp)
	}
}

func TestSendTextAndKeyUseSessionID(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_live", "Live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	rec := do(t, h, http.MethodPost, "/sessions/ses_live/send-text", `{"text":"hello"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("send status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if mux.lastSend.name != zmx.NameForID("ses_live") || string(mux.lastSend.payload) != "hello" {
		t.Fatalf("send = %+v", mux.lastSend)
	}

	rec = do(t, h, http.MethodPost, "/sessions/ses_live/send-key", `{"key":"enter"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("key status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if len(mux.lastSend.payload) != 1 || mux.lastSend.payload[0] != 0x0D {
		t.Fatalf("payload = %v, want enter byte", mux.lastSend.payload)
	}
}

func TestInputErrorsMapToCodes(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_dead", "Dead")

	rec := do(t, h, http.MethodPost, "/sessions/ses_missing/send-text", `{"text":"hello"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "session_not_found")

	rec = do(t, h, http.MethodPost, "/sessions/ses_dead/send-text", `{"text":"hello"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("dead status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "session_not_live")

	rec = do(t, h, http.MethodPost, "/sessions/ses_dead/send-key", `{"key":"f1"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("key status = %d, want 400 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "invalid_request")
}

func TestTerminateAndArchive(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_live", "Live")
	seedRunning(t, st, "ses_dead", "Dead")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	rec := do(t, h, http.MethodPost, "/sessions/ses_live/archive", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("archive live status = %d, want 409", rec.Code)
	}
	assertErrorCode(t, rec, "session_live")

	rec = do(t, h, http.MethodPost, "/sessions/ses_live/terminate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("terminate status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if mux.lastTerminate != zmx.NameForID("ses_live") {
		t.Fatalf("terminate name = %q", mux.lastTerminate)
	}

	rec = do(t, h, http.MethodPost, "/sessions/ses_live/send-text", `{"text":"hello"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("post-terminate send status = %d, want 409", rec.Code)
	}

	rec = do(t, h, http.MethodPost, "/sessions/ses_dead/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive dead status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/sessions", "")
	if strings.Contains(rec.Body.String(), "ses_dead") {
		t.Fatalf("default list includes archived session: %s", rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/sessions?includeArchived=true", "")
	if !strings.Contains(rec.Body.String(), "ses_dead") {
		t.Fatalf("includeArchived list omitted archived session: %s", rec.Body)
	}
}

func TestOldRoutesAreRemoved(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	for _, tt := range []struct {
		method string
		path   string
		body   string
	}{
		{http.MethodPost, "/sessions/send", `{"name":"atc:item:DEV-1","text":"hi"}`},
		{http.MethodPost, "/sessions/key", `{"name":"atc:item:DEV-1","key":"enter"}`},
		{http.MethodGet, "/sessions/attach?name=atc:item:DEV-1", ""},
		{http.MethodGet, "/agents", ""},
	} {
		rec := do(t, h, tt.method, tt.path, tt.body)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", tt.method, tt.path, rec.Code)
		}
	}
}

func TestStartSessionInvalidJSONIs400(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	rec := do(t, h, http.MethodPost, "/sessions/start", `{not json`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	assertErrorCode(t, rec, "invalid_request")
}

func seedRunning(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	if _, err := st.CreateStarting(context.Background(), store.CreateSessionInput{
		ID:          id,
		Name:        name,
		Action:      "codex",
		Environment: "host-login-shell",
		Params:      json.RawMessage(`{"model":"gpt-5"}`),
		WorkingDir:  "/work",
		Prompt:      "hello",
	}); err != nil {
		t.Fatalf("CreateStarting(%s): %v", id, err)
	}
	if _, err := st.MarkRunning(context.Background(), id); err != nil {
		t.Fatalf("MarkRunning(%s): %v", id, err)
	}
}

func seedFailed(t *testing.T, st *store.Store, id string) {
	t.Helper()
	if _, err := st.CreateStarting(context.Background(), store.CreateSessionInput{
		ID:          id,
		Action:      "codex",
		Environment: "host-login-shell",
		WorkingDir:  "/work",
	}); err != nil {
		t.Fatalf("CreateStarting(%s): %v", id, err)
	}
	if _, err := st.MarkFailed(context.Background(), id, "action failed to launch", "launch_failed"); err != nil {
		t.Fatalf("MarkFailed(%s): %v", id, err)
	}
}

func liveSession(id string) zmx.Session {
	return zmx.Session{Name: zmx.NameForID(id)}
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var resp struct {
		Error string `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if resp.Error != want {
		t.Fatalf("error = %q, want %q", resp.Error, want)
	}
}
