package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/zmx"
)

// createTestWorkspace drives POST /workspaces and returns the created record.
func createTestWorkspace(t *testing.T, h http.Handler, projectID, name string) Workspace {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/workspaces", `{"projectId":"`+projectID+`","name":"`+name+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create workspace status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	var created Workspace
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode created workspace: %v", err)
	}
	return created
}

func TestCreateWorkspaceReturnsFullRecord(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	project := createTestProject(t, h, "atc", t.TempDir())

	created := createTestWorkspace(t, h, project.ID, "Login bug")
	if !strings.HasPrefix(created.ID, "wsp_") || created.Name != "Login bug" || created.ProjectID != project.ID {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("created timestamps = %+v", created)
	}
}

func TestCreateWorkspaceValidationErrors(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	project := createTestProject(t, h, "atc", t.TempDir())

	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{name: "invalid JSON", body: `{not json`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "missing name", body: `{"projectId":"` + project.ID + `"}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "blank name", body: `{"projectId":"` + project.ID + `","name":"   "}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "missing projectId", body: `{"name":"X"}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown project", body: `{"projectId":"prj_missing","name":"X"}`, status: http.StatusNotFound, code: "project_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/workspaces", tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			assertErrorCode(t, rec, tt.code)
		})
	}
}

func TestListWorkspacesFilters(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	first := createTestProject(t, h, "First", t.TempDir())
	second := createTestProject(t, h, "Second", t.TempDir())
	one := createTestWorkspace(t, h, first.ID, "One")
	two := createTestWorkspace(t, h, second.ID, "Two")
	three := createTestWorkspace(t, h, first.ID, "Three")
	// The seeded test workspace (wsp_test) also exists; scope by project to
	// keep assertions exact.
	rec := do(t, h, http.MethodGet, "/workspaces?projectId="+first.ID, "")
	var scoped WorkspaceListResponse
	if err := json.NewDecoder(rec.Body).Decode(&scoped); err != nil {
		t.Fatalf("decode scoped: %v", err)
	}
	if len(scoped.Workspaces) != 2 || scoped.Workspaces[0].ID != three.ID || scoped.Workspaces[1].ID != one.ID {
		t.Fatalf("scoped list = %+v, want newest-first", scoped.Workspaces)
	}

	// Without projectId the list spans every project.
	rec = do(t, h, http.MethodGet, "/workspaces", "")
	var global WorkspaceListResponse
	if err := json.NewDecoder(rec.Body).Decode(&global); err != nil {
		t.Fatalf("decode global: %v", err)
	}
	ids := map[string]bool{}
	for _, ws := range global.Workspaces {
		ids[ws.ID] = true
	}
	if !ids[one.ID] || !ids[two.ID] || !ids[testWorkspaceID] {
		t.Fatalf("global list = %+v, want workspaces across projects", global.Workspaces)
	}
}

func TestGetPatchWorkspace(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	project := createTestProject(t, h, "atc", t.TempDir())
	created := createTestWorkspace(t, h, project.ID, "Original")

	rec := do(t, h, http.MethodGet, "/workspaces/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got Workspace
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != created.ID || got.Name != "Original" {
		t.Fatalf("got = %+v", got)
	}

	rec = do(t, h, http.MethodPatch, "/workspaces/"+created.ID, `{"name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var renamed Workspace
	if err := json.NewDecoder(rec.Body).Decode(&renamed); err != nil {
		t.Fatalf("decode renamed: %v", err)
	}
	if renamed.Name != "Renamed" {
		t.Fatalf("renamed = %+v", renamed)
	}

	// Unknown fields are rejected — projectId especially, which is fixed.
	for _, body := range []string{
		`{"name":"X","projectId":"prj_other"}`,
		`{"projectId":"prj_other"}`,
		`{"name":"X","unknown":true}`,
	} {
		rec = do(t, h, http.MethodPatch, "/workspaces/"+created.ID, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("patch %s status = %d, want 400 (%s)", body, rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "invalid_request")
	}

	rec = do(t, h, http.MethodGet, "/workspaces/wsp_missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "workspace_not_found")
}

func TestDeleteWorkspaceStopsSessionsAndRemovesMetadata(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_active", "Active")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_active")}}

	rec := do(t, h, http.MethodDelete, "/workspaces/"+testWorkspaceID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if mux.lastTerminate != zmx.NameForID("ses_active") {
		t.Fatalf("terminate = %q, want Live Session ended", mux.lastTerminate)
	}
	if _, err := st.GetWorkspace(context.Background(), testWorkspaceID); !errors.Is(err, store.ErrWorkspaceNotFound) {
		t.Fatalf("workspace survived delete: %v", err)
	}
	if _, err := st.Get(context.Background(), "ses_active"); !errors.Is(err, store.ErrSessionNotFound) {
		t.Fatalf("session metadata survived delete: %v", err)
	}

	rec = do(t, h, http.MethodDelete, "/workspaces/"+testWorkspaceID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("second delete status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "workspace_not_found")
}

func TestDeleteWorkspaceEndFailureReturns502AndKeepsMetadata(t *testing.T) {
	mux := &fakeMux{terminateErr: errors.New("zmx kill failed")}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_stuck", "Stuck")
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_stuck")}}

	rec := do(t, h, http.MethodDelete, "/workspaces/"+testWorkspaceID, "")
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("delete status = %d, want 502 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "session_end_failed")
	if _, err := st.GetWorkspace(context.Background(), testWorkspaceID); err != nil {
		t.Fatalf("workspace lost after aborted delete: %v", err)
	}
	if _, err := st.Get(context.Background(), "ses_stuck"); err != nil {
		t.Fatalf("session lost after aborted delete: %v", err)
	}
}

func TestListWorkspaceSessions(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	project := createTestProject(t, h, "atc", t.TempDir())
	other := createTestWorkspace(t, h, project.ID, "Other")
	seedRunning(t, st, "ses_scoped", "Scoped")
	if _, err := st.CreateStarting(context.Background(), store.CreateSessionInput{
		ID: "ses_other", ActionID: "act_codex", ActionName: "Codex", WorkingDir: "/work", WorkspaceID: other.ID,
	}); err != nil {
		t.Fatalf("CreateStarting other: %v", err)
	}
	mux.sessions = []zmx.Session{{Name: zmx.NameForID("ses_scoped")}}

	rec := do(t, h, http.MethodGet, "/workspaces/"+testWorkspaceID+"/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var list SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].ID != "ses_scoped" {
		t.Fatalf("workspace sessions = %+v, want only scoped session", list.Sessions)
	}
	if list.Sessions[0].Workspace == nil || list.Sessions[0].Workspace.ID != testWorkspaceID {
		t.Fatalf("workspace ref = %+v", list.Sessions[0].Workspace)
	}

	// Ended sessions remain in the default workspace list.
	if _, err := st.MarkEnded(context.Background(), "ses_scoped"); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}
	rec = do(t, h, http.MethodGet, "/workspaces/"+testWorkspaceID+"/sessions", "")
	var full SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&full); err != nil {
		t.Fatalf("decode full: %v", err)
	}
	if len(full.Sessions) != 1 || full.Sessions[0].ID != "ses_scoped" || full.Sessions[0].Status != "ended" {
		t.Fatalf("full workspace sessions = %+v", full.Sessions)
	}

	rec = do(t, h, http.MethodGet, "/workspaces/wsp_missing/sessions", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing workspace status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "workspace_not_found")
}
