package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	actionstore "github.com/jeremytondo/atc/internal/action"
	"github.com/jeremytondo/atc/internal/diagnostics"
	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/session"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/workspace"
	"github.com/jeremytondo/atc/internal/zmx"
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
	terminateErr  error
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
	if f.terminateErr != nil {
		return f.terminateErr
	}
	for i, s := range f.sessions {
		if s.Name == name {
			f.sessions = append(f.sessions[:i], f.sessions[i+1:]...)
			break
		}
	}
	return nil
}

// testWorkspaceID is the workspace every handler test hangs sessions off;
// newHandler seeds its project/workspace rows.
const testWorkspaceID = "wsp_test"

type handlerEnv struct {
	handler http.Handler
	store   *store.Store
	workDir string
}

func newHandlerEnv(t *testing.T, mux *fakeMux) handlerEnv {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/atc.db")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	workDir := t.TempDir()
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, store.CreateProjectInput{ID: "prj_test", Name: "Test", WorkingDir: workDir}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, store.CreateWorkspaceInput{ID: testWorkspaceID, ProjectID: "prj_test", Name: "Test workspace"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
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
	actionStore := actionstore.NewStore(t.TempDir()+"/actions.json", actions, st)
	projects := project.NewService(st, nil)
	workspaces := workspace.NewService(st, nil)
	sessions := session.NewService(st, mux, actionStore, environments, workspaces, nil)
	handler := Routes(diagnostics.DefaultDiagnostics(), sessions, projects, workspaces, actionStore, nil)
	return handlerEnv{handler: handler, store: st, workDir: workDir}
}

func newHandler(t *testing.T, mux *fakeMux) (http.Handler, *store.Store) {
	t.Helper()
	env := newHandlerEnv(t, mux)
	return env.handler, env.store
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
	env := newHandlerEnv(t, mux)
	rec := do(t, env.handler, http.MethodPost, "/sessions/start",
		`{"action":"codex","params":{"model":"gpt-5"},"workspaceId":"`+testWorkspaceID+`","prompt":"review this","name":"Review"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !strings.HasPrefix(resp.ID, "ses_") || resp.Name != "Review" || resp.Action != "codex" || resp.Environment != "host-login-shell" ||
		resp.WorkingDir != env.workDir || resp.Prompt != "review this" || resp.Status != "running" || !resp.Attachable {
		t.Fatalf("response = %+v", resp)
	}
	if resp.Params["model"] != "gpt-5" {
		t.Fatalf("params = %#v", resp.Params)
	}
	if resp.Workspace == nil || resp.Workspace.ID != testWorkspaceID || resp.Workspace.Name != "Test workspace" {
		t.Fatalf("workspace ref = %+v", resp.Workspace)
	}
	if resp.Project == nil || resp.Project.ID != "prj_test" || resp.Project.Name != "Test" {
		t.Fatalf("project ref = %+v", resp.Project)
	}
	if mux.lastStart.name != zmx.NameForID(resp.ID) || mux.lastStart.dir != env.workDir {
		t.Fatalf("start = %+v", mux.lastStart)
	}
	if got := strings.Join(mux.lastStart.argv, " "); !strings.Contains(got, "-l -i -c codex --model gpt-5 'review this'") {
		t.Fatalf("argv = %#v", mux.lastStart.argv)
	}
	if strings.Contains(rec.Body.String(), "atc-") {
		t.Fatalf("response leaked zmx name: %s", rec.Body)
	}
}

func TestStartSessionWithoutActionLaunchesInteractiveShell(t *testing.T) {
	mux := &fakeMux{}
	env := newHandlerEnv(t, mux)
	rec := do(t, env.handler, http.MethodPost, "/sessions/start", `{"workspaceId":"`+testWorkspaceID+`"}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Action != "" || resp.Status != "running" {
		t.Fatalf("response = %+v, want running interactive shell", resp)
	}
	// The wire form of an interactive shell session omits action entirely.
	if strings.Contains(rec.Body.String(), `"action"`) {
		t.Fatalf("interactive shell response carries action: %s", rec.Body)
	}
	argv := strings.Join(mux.lastStart.argv, " ")
	if strings.Contains(argv, "-c") {
		t.Fatalf("interactive shell argv carries a command payload: %#v", mux.lastStart.argv)
	}
}

func TestStartSessionValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{name: "missing workspaceId", body: `{"action":"claude"}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "blank workspaceId", body: `{"action":"claude","workspaceId":"   "}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown workspace", body: `{"action":"claude","workspaceId":"wsp_ghost"}`, status: http.StatusBadRequest, code: "workspace_not_found"},
		{name: "unknown action", body: `{"action":"ghost","workspaceId":"` + testWorkspaceID + `"}`, status: http.StatusBadRequest, code: "unknown_action"},
		{name: "unknown environment", body: `{"action":"codex","environment":"ghost","workspaceId":"` + testWorkspaceID + `"}`, status: http.StatusBadRequest, code: "unknown_environment"},
		{name: "invalid params", body: `{"action":"codex","workspaceId":"` + testWorkspaceID + `","params":{"model":"gpt-4"}}`, status: http.StatusBadRequest, code: "invalid_params"},
		{name: "unsupported prompt", body: `{"action":"claude","workspaceId":"` + testWorkspaceID + `","prompt":"do it"}`, status: http.StatusBadRequest, code: "invalid_params"},
		{name: "params without action", body: `{"workspaceId":"` + testWorkspaceID + `","params":{"model":"gpt-5"}}`, status: http.StatusBadRequest, code: "invalid_params"},
		{name: "prompt without action", body: `{"workspaceId":"` + testWorkspaceID + `","prompt":"do it"}`, status: http.StatusBadRequest, code: "invalid_params"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mux := &fakeMux{}
			env := newHandlerEnv(t, mux)
			rec := do(t, env.handler, http.MethodPost, "/sessions/start", tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			assertErrorCode(t, rec, tt.code)
			if mux.lastStart.name != "" {
				t.Fatalf("start called: %+v", mux.lastStart)
			}
			records, err := env.store.List(context.Background(), store.ListFilter{IncludeArchived: true})
			if err != nil {
				t.Fatalf("List: %v", err)
			}
			if len(records) != 0 {
				t.Fatalf("records = %+v, want none", records)
			}
		})
	}
}

func TestStartSessionRejectsArchivedWorkspace(t *testing.T) {
	mux := &fakeMux{}
	env := newHandlerEnv(t, mux)
	if _, err := env.store.ArchiveWorkspace(context.Background(), testWorkspaceID); err != nil {
		t.Fatalf("ArchiveWorkspace: %v", err)
	}
	rec := do(t, env.handler, http.MethodPost, "/sessions/start", `{"action":"claude","workspaceId":"`+testWorkspaceID+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "workspace_archived")
}

func TestStartSessionLaunchFailureReturnsSessionID(t *testing.T) {
	mux := &fakeMux{startErr: errors.New("zmx failed")}
	h, st := newHandler(t, mux)
	rec := do(t, h, http.MethodPost, "/sessions/start", `{"action":"claude","workspaceId":"`+testWorkspaceID+`"}`)

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

func TestReadSessionIncludesPromptParamsAndWorkspace(t *testing.T) {
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
	if resp.Workspace == nil || resp.Workspace.ID != testWorkspaceID {
		t.Fatalf("workspace ref = %+v", resp.Workspace)
	}
	if resp.Project == nil || resp.Project.ID != "prj_test" {
		t.Fatalf("project ref = %+v", resp.Project)
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

func TestUnarchiveSessionRoute(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_done", "Done")
	if _, err := st.MarkTerminated(context.Background(), "ses_done"); err != nil {
		t.Fatalf("MarkTerminated: %v", err)
	}
	if _, err := st.MarkArchived(context.Background(), "ses_done"); err != nil {
		t.Fatalf("MarkArchived: %v", err)
	}

	rec := do(t, h, http.MethodPost, "/sessions/ses_done/unarchive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.ArchivedAt != nil {
		t.Fatalf("archivedAt = %v, want cleared", resp.ArchivedAt)
	}

	rec = do(t, h, http.MethodPost, "/sessions/ses_missing/unarchive", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "session_not_found")
}

func TestDeleteSessionRoute(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_live", "Live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	// Deleting an active session terminates it first, then removes metadata.
	rec := do(t, h, http.MethodDelete, "/sessions/ses_live", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if mux.lastTerminate != zmx.NameForID("ses_live") {
		t.Fatalf("terminate name = %q", mux.lastTerminate)
	}
	if _, err := st.Get(context.Background(), "ses_live"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("Get after delete err = %v, want not found", err)
	}

	rec = do(t, h, http.MethodDelete, "/sessions/ses_live", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "session_not_found")
}

func TestDeleteSessionStopFailureKeepsMetadata(t *testing.T) {
	mux := &fakeMux{terminateErr: errors.New("zmx kill failed")}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_live", "Live")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_live")}}

	rec := do(t, h, http.MethodDelete, "/sessions/ses_live", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body)
	}
	if _, err := st.Get(context.Background(), "ses_live"); err != nil {
		t.Fatalf("metadata lost after failed delete: %v", err)
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
		WorkspaceID: testWorkspaceID,
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
		WorkspaceID: testWorkspaceID,
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
