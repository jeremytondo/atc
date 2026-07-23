package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
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
	listErr       error
	argv          []string
	sessions      []zmx.Session
	lastTerminate string
	attachCalls   int
	attachRows    uint16
	attachCols    uint16
	lastSend      struct {
		name    string
		payload []byte
	}
}

func (f *fakeMux) Start(_ context.Context, name, _ string, argv []string) error {
	f.argv = append([]string(nil), argv...)
	if f.startErr != nil {
		return f.startErr
	}
	f.sessions = append(f.sessions, zmx.Session{Name: name})
	return nil
}
func (f *fakeMux) Send(_ context.Context, name string, payload []byte) error {
	f.lastSend.name = name
	f.lastSend.payload = append([]byte(nil), payload...)
	return nil
}
func (f *fakeMux) Attach(_ context.Context, _ string, rows, cols uint16) (zmx.PTY, error) {
	f.attachCalls++
	f.attachRows = rows
	f.attachCols = cols
	return f.attachPTY, f.attachErr
}
func (f *fakeMux) List(context.Context) ([]zmx.Session, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
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
	workDir  string
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
		workDir:  workDir,
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

func seedEnded(t *testing.T, st *store.Store, id, name string) {
	t.Helper()
	seedRunning(t, st, id, name)
	if _, err := st.MarkEnded(context.Background(), id); err != nil {
		t.Fatalf("MarkEnded(%s): %v", id, err)
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

func TestStartSessionNormalizesName(t *testing.T) {
	mux := &fakeMux{}
	env := newHandlerEnv(t, mux)
	rec := do(t, env.handler, http.MethodPost, "/sessions/start",
		`{"workspaceId":"`+testWorkspaceID+`","name":"  Review  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Name != "Review" {
		t.Fatalf("name = %q, want trimmed", resp.Name)
	}

	rec = do(t, env.handler, http.MethodPost, "/sessions/start",
		`{"workspaceId":"`+testWorkspaceID+`","name":"   "}`)
	var blank SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&blank); err != nil {
		t.Fatalf("decode blank name: %v", err)
	}
	if rec.Code != http.StatusOK || blank.Name != "" {
		t.Fatalf("blank name response = %d %s", rec.Code, rec.Body)
	}
}

func TestPatchSessionRenamesAndDecodesStrictly(t *testing.T) {
	mux := &fakeMux{}
	env := newHandlerEnv(t, mux)
	started := do(t, env.handler, http.MethodPost, "/sessions/start",
		`{"workspaceId":"`+testWorkspaceID+`","name":"Before"}`)
	var original SessionDetail
	if err := json.NewDecoder(started.Body).Decode(&original); err != nil {
		t.Fatalf("decode start: %v", err)
	}

	rec := do(t, env.handler, http.MethodPatch, "/sessions/"+original.ID, `{"name":"  After  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var renamed SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&renamed); err != nil {
		t.Fatalf("decode rename: %v", err)
	}
	if renamed.ID != original.ID || renamed.Name != "After" || renamed.Workspace == nil || renamed.Project == nil {
		t.Fatalf("renamed = %+v", renamed)
	}
	if renamed.Status != original.Status || renamed.WorkingDir != original.WorkingDir || len(mux.sessions) != 1 || mux.sessions[0].Name != zmx.NameForID(original.ID) {
		t.Fatalf("rename changed session identity/config: before=%+v after=%+v", original, renamed)
	}

	for _, tc := range []struct {
		name, id, body, code string
		status               int
	}{
		{name: "blank", id: original.ID, body: `{"name":"   "}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown field", id: original.ID, body: `{"name":"X","status":"ended"}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "trailing data", id: original.ID, body: `{"name":"X"}{"name":"Y"}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "missing", id: "ses_missing", body: `{"name":"X"}`, status: http.StatusNotFound, code: "session_not_found"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, env.handler, http.MethodPatch, "/sessions/"+tc.id, tc.body)
			if rec.Code != tc.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tc.status, rec.Body)
			}
			var failure errorResponse
			if err := json.NewDecoder(rec.Body).Decode(&failure); err != nil || failure.Error != tc.code {
				t.Fatalf("error = %+v decode=%v", failure, err)
			}
		})
	}

	seedEnded(t, env.store, "ses_ended", "Ended")
	rec = do(t, env.handler, http.MethodPatch, "/sessions/ses_ended", `{"name":"Nope"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("ended rename status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "session_ended")
}

func TestStartSessionValidationErrors(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{name: "missing workspaceId", body: `{}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "blank workspaceId", body: `{"workspaceId":"   "}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown workspace", body: `{"workspaceId":"wsp_ghost"}`, status: http.StatusBadRequest, code: "workspace_not_found"},
		{name: "unknown action", body: `{"actionId":"act_ghost","workspaceId":"` + testWorkspaceID + `"}`, status: http.StatusNotFound, code: "action_not_found"},
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
			if len(mux.sessions) != 0 {
				t.Fatalf("start called: %+v", mux.sessions)
			}
			records, err := env.store.ListAll(context.Background(), store.ListFilter{})
			if err != nil {
				t.Fatalf("ListAll: %v", err)
			}
			if len(records) != 0 {
				t.Fatalf("records = %+v, want none", records)
			}
		})
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
	t.Run("invalid request", func(t *testing.T) {
		env := newHandlerEnv(t, &fakeMux{})
		rec := do(t, env.handler, http.MethodPost, "/sessions/start", `{}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "invalid_request")
	})

	t.Run("workspace not found", func(t *testing.T) {
		env := newHandlerEnv(t, &fakeMux{})
		rec := do(t, env.handler, http.MethodPost, "/sessions/start", `{"workspaceId":"wsp_missing"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "workspace_not_found")
	})

	t.Run("action not found", func(t *testing.T) {
		env := newHandlerEnv(t, &fakeMux{})
		rec := do(t, env.handler, http.MethodPost, "/sessions/start",
			`{"workspaceId":"`+testWorkspaceID+`","actionId":"act_missing"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "action_not_found")
	})

	t.Run("action disabled", func(t *testing.T) {
		env := newHandlerEnv(t, &fakeMux{})
		disabled, err := env.store.CreateAction(context.Background(), store.Action{Name: "Off", Command: "off"})
		if err != nil {
			t.Fatalf("CreateAction: %v", err)
		}
		rec := do(t, env.handler, http.MethodPost, "/sessions/start",
			`{"workspaceId":"`+testWorkspaceID+`","actionId":"`+disabled.ID+`"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "action_disabled")
	})

	t.Run("invalid working dir", func(t *testing.T) {
		env := newHandlerEnv(t, &fakeMux{})
		if err := os.RemoveAll(env.workDir); err != nil {
			t.Fatalf("remove working dir: %v", err)
		}
		rec := do(t, env.handler, http.MethodPost, "/sessions/start",
			`{"workspaceId":"`+testWorkspaceID+`"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "invalid_working_dir")
	})

	t.Run("session not found", func(t *testing.T) {
		env := newHandlerEnv(t, &fakeMux{})
		rec := do(t, env.handler, http.MethodPost, "/sessions/ses_missing/send-text", `{"text":"hello"}`)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "session_not_found")
	})

	t.Run("session ended", func(t *testing.T) {
		env := newHandlerEnv(t, &fakeMux{})
		seedEnded(t, env.store, "ses_dead", "Dead")
		rec := do(t, env.handler, http.MethodPost, "/sessions/ses_dead/send-text", `{"text":"hello"}`)
		if rec.Code != http.StatusConflict {
			t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body)
		}
		var endedError errorResponse
		if err := json.NewDecoder(rec.Body).Decode(&endedError); err != nil {
			t.Fatalf("decode ended error: %v", err)
		}
		if endedError.Error != "session_ended" || endedError.SessionID != "ses_dead" {
			t.Fatalf("ended error = %+v", endedError)
		}
	})

	t.Run("zmx unavailable", func(t *testing.T) {
		mux := &fakeMux{listErr: errors.New("offline")}
		env := newHandlerEnv(t, mux)
		seedRunning(t, env.store, "ses_live", "Live")
		rec := do(t, env.handler, http.MethodPost, "/sessions/ses_live/send-text", `{"text":"hello"}`)
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "zmx_unavailable")
	})

	t.Run("unknown key", func(t *testing.T) {
		env := newHandlerEnv(t, &fakeMux{})
		rec := do(t, env.handler, http.MethodPost, "/sessions/ses_missing/send-key", `{"key":"f1"}`)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "invalid_request")
	})
}

func TestProvisionalRecordsAreNotPublic(t *testing.T) {
	h, st := newHandler(t, &fakeMux{})
	if _, err := st.CreateStarting(context.Background(), store.CreateSessionInput{
		ID: "ses_starting", ActionID: "act_test", ActionName: "Codex", IsAgent: true,
		WorkingDir: "/work", WorkspaceID: testWorkspaceID,
	}); err != nil {
		t.Fatalf("CreateStarting: %v", err)
	}
	for _, request := range []struct {
		method string
		path   string
	}{{http.MethodGet, "/sessions/ses_starting"}, {http.MethodDelete, "/sessions/ses_starting"}} {
		rec := do(t, h, request.method, request.path, "")
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s %s status = %d, want 404", request.method, request.path, rec.Code)
		}
		assertErrorCode(t, rec, "session_not_found")
	}
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

func TestDeleteSessionTerminateFailureKeepsMetadata(t *testing.T) {
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

func TestDeleteSessionInventoryFailureIs503AndKeepsMetadata(t *testing.T) {
	mux := &fakeMux{listErr: errors.New("offline")}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_live", "Live")

	rec := do(t, h, http.MethodDelete, "/sessions/ses_live", "")
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "zmx_unavailable")
	if got, err := st.Get(context.Background(), "ses_live"); err != nil || got.Status != store.StatusLive {
		t.Fatalf("record = %+v err=%v", got, err)
	}
}

func TestOldRoutesAreRemoved(t *testing.T) {
	env := newHandlerEnv(t, &fakeMux{})
	for _, tt := range []struct {
		method string
		path   string
		body   string
		status int
	}{
		{http.MethodPut, "/actions/" + env.actionID, `{"name":"Agent"}`, http.StatusNotFound},
		{http.MethodPut, "/actions/" + env.actionID + "/enabled", `{"enabled":false}`, http.StatusNotFound},
		{http.MethodGet, "/environments", "", http.StatusNotFound},
		{http.MethodGet, "/actions/Agent", "", http.StatusNotFound},
		{http.MethodDelete, "/actions/Agent", "", http.StatusNotFound},
		{http.MethodPost, "/sessions/send", `{"name":"atc:item:DEV-1","text":"hi"}`, http.StatusNotFound},
		{http.MethodPost, "/sessions/key", `{"name":"atc:item:DEV-1","key":"enter"}`, http.StatusNotFound},
		{http.MethodGet, "/sessions/attach?name=atc:item:DEV-1", "", http.StatusNotFound},
		{http.MethodGet, "/agents", "", http.StatusNotFound},
		{http.MethodPost, "/sessions/ses_123/terminate", "", http.StatusNotFound},
		{http.MethodPost, "/sessions/ses_123/archive", "", http.StatusNotFound},
		{http.MethodPost, "/sessions/ses_123/unarchive", "", http.StatusNotFound},
		{http.MethodPost, "/projects/prj_123/archive", "", http.StatusNotFound},
		{http.MethodPost, "/projects/prj_123/unarchive", "", http.StatusNotFound},
		{http.MethodPost, "/workspaces/wsp_123/archive", "", http.StatusNotFound},
		{http.MethodPost, "/workspaces/wsp_123/unarchive", "", http.StatusNotFound},
	} {
		rec := do(t, env.handler, tt.method, tt.path, tt.body)
		if rec.Code != tt.status {
			t.Fatalf("%s %s status = %d, want %d", tt.method, tt.path, rec.Code, tt.status)
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
