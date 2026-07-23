package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/diagnostics"
	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/session"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/workspace"
	"github.com/jeremytondo/atc/internal/zmx"
)

const testWorkspaceID = "wsp_test"

type fakeMux struct {
	startErr      error
	terminateErr  error
	attachErr     error
	attachPTY     zmx.PTY
	argv          []string
	sessions      []zmx.Session
	lastTerminate string
	attachCalls   int
	attachRows    uint16
	attachCols    uint16
}

func (f *fakeMux) Start(_ context.Context, name, _ string, argv []string) error {
	f.argv = append([]string(nil), argv...)
	if f.startErr != nil {
		return f.startErr
	}
	f.sessions = append(f.sessions, zmx.Session{Name: name})
	return nil
}
func (f *fakeMux) Send(context.Context, string, []byte) error { return nil }
func (f *fakeMux) Attach(_ context.Context, _ string, rows, cols uint16) (zmx.PTY, error) {
	f.attachCalls++
	f.attachRows = rows
	f.attachCols = cols
	return f.attachPTY, f.attachErr
}
func (f *fakeMux) List(context.Context) ([]zmx.Session, error) {
	return append([]zmx.Session(nil), f.sessions...), nil
}
func (f *fakeMux) Terminate(_ context.Context, name string) error {
	f.lastTerminate = name
	if f.terminateErr != nil {
		return f.terminateErr
	}
	for i, item := range f.sessions {
		if item.Name == name {
			f.sessions = append(f.sessions[:i], f.sessions[i+1:]...)
			break
		}
	}
	return nil
}

type handlerEnv struct {
	handler  http.Handler
	store    *store.Store
	actionID string
}

func newHandlerEnv(t *testing.T, mux *fakeMux) handlerEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	workDir := t.TempDir()
	if _, err := st.CreateProject(ctx, store.CreateProjectInput{ID: "prj_test", Name: "Test", WorkingDir: workDir}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, store.CreateWorkspaceInput{ID: testWorkspaceID, ProjectID: "prj_test", Name: "Test workspace"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	action, err := st.CreateAction(ctx, store.Action{
		Name: "Agent", Description: "Test agent", Enabled: true, Command: "agent",
		Args: []string{"$HOME", "two words"}, IsAgent: true,
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	projects := project.NewService(st, nil)
	workspaces := workspace.NewService(st, nil)
	sessions := session.NewService(st, mux, workspaces, nil)
	return handlerEnv{
		handler:  Routes(diagnostics.DefaultDiagnostics(), sessions, projects, workspaces, st, nil),
		store:    st,
		actionID: action.ID,
	}
}

func newHandler(t *testing.T, mux *fakeMux) (http.Handler, *store.Store) {
	t.Helper()
	env := newHandlerEnv(t, mux)
	return env.handler, env.store
}

func seedRunning(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	created, err := st.CreateStarting(context.Background(), store.CreateSessionInput{
		ID: id, Name: name, ActionID: "act_fh9g7e6571qo53r0t647ughtfg", ActionName: "Codex",
		IsAgent: true, WorkingDir: "/work", WorkspaceID: testWorkspaceID,
	})
	if err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	if _, err := st.PromoteToLive(context.Background(), created.ID); err != nil {
		t.Fatalf("PromoteToLive: %v", err)
	}
}

func liveSession(id string) zmx.Session {
	return zmx.Session{Name: zmx.NameForID(id)}
}

func TestStartSessionByActionID(t *testing.T) {
	t.Setenv("SHELL", "/bin/zsh")
	mux := &fakeMux{}
	env := newHandlerEnv(t, mux)
	rec := do(t, env.handler, http.MethodPost, "/sessions/start",
		`{"workspaceId":"`+testWorkspaceID+`","actionId":"`+env.actionID+`","name":"Review"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got SessionResponse
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ActionID != env.actionID || got.ActionName != "Agent" || !got.IsAgent || got.Name != "Review" {
		t.Fatalf("response = %+v", got)
	}
	wantArgv := []string{"/bin/zsh", "-l", "-i", "-c", `agent '$HOME' 'two words'`}
	if !reflect.DeepEqual(mux.argv, wantArgv) {
		t.Fatalf("argv = %#v, want %#v", mux.argv, wantArgv)
	}
}

func TestStartInteractiveShellResponseOmitsActionIdentity(t *testing.T) {
	t.Setenv("SHELL", "/bin/fish")
	mux := &fakeMux{}
	env := newHandlerEnv(t, mux)
	rec := do(t, env.handler, http.MethodPost, "/sessions/start", `{"workspaceId":"`+testWorkspaceID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); containsAny(body, `"actionId"`, `"actionName"`) {
		t.Fatalf("interactive response exposed Action identity: %s", body)
	}
	var got SessionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.IsAgent || !reflect.DeepEqual(mux.argv, []string{"/bin/fish", "-l", "-i"}) {
		t.Fatalf("response=%+v argv=%#v", got, mux.argv)
	}
}

func TestStartSessionErrorsUsePinnedTaxonomy(t *testing.T) {
	mux := &fakeMux{}
	env := newHandlerEnv(t, mux)

	rec := do(t, env.handler, http.MethodPost, "/sessions/start", `{"workspaceId":"`+testWorkspaceID+`","actionId":"act_missing"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "action_not_found")

	disabled, err := env.store.CreateAction(context.Background(), store.Action{Name: "Off", Command: "off"})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}
	rec = do(t, env.handler, http.MethodPost, "/sessions/start", `{"workspaceId":"`+testWorkspaceID+`","actionId":"`+disabled.ID+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("disabled status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "action_disabled")
}

func TestStartSessionStrictlyRejectsLegacyFields(t *testing.T) {
	env := newHandlerEnv(t, &fakeMux{})
	for _, body := range []string{
		`{"workspaceId":"` + testWorkspaceID + `","action":"agent"}`,
		`{"workspaceId":"` + testWorkspaceID + `","environment":"host-login-shell"}`,
		`{"workspaceId":"` + testWorkspaceID + `","params":{}}`,
		`{"workspaceId":"` + testWorkspaceID + `","prompt":"hello"}`,
	} {
		rec := do(t, env.handler, http.MethodPost, "/sessions/start", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %s status = %d, want 400 (%s)", body, rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "invalid_request")
	}
}

func TestLaunchFailureReturns502AndDeletesProvisional(t *testing.T) {
	env := newHandlerEnv(t, &fakeMux{startErr: errors.New("executable missing")})
	rec := do(t, env.handler, http.MethodPost, "/sessions/start",
		`{"workspaceId":"`+testWorkspaceID+`","actionId":"`+env.actionID+`"}`)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "launch_failed")
	records, err := env.store.ListAll(context.Background(), store.ListFilter{})
	if err != nil {
		t.Fatalf("ListAll: %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("records after failed launch = %+v", records)
	}
}

func TestSessionListAndDetailShareShape(t *testing.T) {
	env := newHandlerEnv(t, &fakeMux{})
	start := do(t, env.handler, http.MethodPost, "/sessions/start",
		`{"workspaceId":"`+testWorkspaceID+`","actionId":"`+env.actionID+`"}`)
	var created SessionResponse
	if err := json.Unmarshal(start.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode start: %v", err)
	}

	detail := do(t, env.handler, http.MethodGet, "/sessions/"+created.ID, "")
	if detail.Code != http.StatusOK {
		t.Fatalf("detail status = %d (%s)", detail.Code, detail.Body)
	}
	var got SessionResponse
	if err := json.Unmarshal(detail.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if got.ActionID != env.actionID || got.ActionName != "Agent" || !got.IsAgent {
		t.Fatalf("detail = %+v", got)
	}

	list := do(t, env.handler, http.MethodGet, "/sessions", "")
	var listed SessionListResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Sessions) != 1 || listed.Sessions[0].ActionID != got.ActionID || listed.Sessions[0].IsAgent != got.IsAgent {
		t.Fatalf("list = %+v", listed)
	}
}

func do(t *testing.T, h http.Handler, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, nil)
	if body != "" {
		req = httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertErrorCode(t *testing.T, rec *httptest.ResponseRecorder, want string) {
	t.Helper()
	var got errorResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
	if got.Error != want {
		t.Fatalf("error code = %q, want %q (%s)", got.Error, want, rec.Body)
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}
