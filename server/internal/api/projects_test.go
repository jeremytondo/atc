package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/diagnostics"
	"github.com/jeremytondo/atc/internal/zmx"
)

// createTestProject drives POST /projects and returns the created record.
func createTestProject(t *testing.T, h http.Handler, name, dir string) Project {
	t.Helper()
	rec := do(t, h, http.MethodPost, "/projects", `{"name":"`+name+`","workingDir":"`+dir+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	var created Project
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode created project: %v", err)
	}
	return created
}

func TestCreateProjectReturnsFullRecord(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	workDir := t.TempDir()

	created := createTestProject(t, h, "atc", workDir)
	if !strings.HasPrefix(created.ID, "prj_") || created.Name != "atc" || created.WorkingDir != workDir {
		t.Fatalf("created = %+v", created)
	}
	if created.CreatedAt == "" || created.UpdatedAt == "" {
		t.Fatalf("created timestamps = %+v", created)
	}
}

func TestCreateProjectValidationErrors(t *testing.T) {
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
		{name: "invalid JSON", body: `{not json`, code: "invalid_request"},
		{name: "missing name", body: `{"workingDir":"` + workDir + `"}`, code: "invalid_request"},
		{name: "blank name", body: `{"name":"   ","workingDir":"` + workDir + `"}`, code: "invalid_request"},
		{name: "missing workingDir", body: `{"name":"atc"}`, code: "invalid_working_dir"},
		{name: "relative workingDir", body: `{"name":"atc","workingDir":"relative/path"}`, code: "invalid_working_dir"},
		{name: "missing directory", body: `{"name":"atc","workingDir":"` + filepath.Join(workDir, "missing") + `"}`, code: "invalid_working_dir"},
		{name: "workingDir is a file", body: `{"name":"atc","workingDir":"` + notADir + `"}`, code: "invalid_working_dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, _ := newHandler(t, &fakeMux{})
			rec := do(t, h, http.MethodPost, "/projects", tt.body)
			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
			}
			assertErrorCode(t, rec, tt.code)
		})
	}
}

func TestListProjectsNewestFirst(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	workDir := t.TempDir()

	first := createTestProject(t, h, "First", workDir)
	second := createTestProject(t, h, "Second", workDir)
	rec := do(t, h, http.MethodGet, "/projects", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	// The handler seeds prj_test, so lists include it alongside this test's
	// projects, newest-created-first.
	var projects ProjectListResponse
	if err := json.NewDecoder(rec.Body).Decode(&projects); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(projects.Projects) != 3 || projects.Projects[0].ID != second.ID || projects.Projects[1].ID != first.ID || projects.Projects[2].ID != "prj_test" {
		t.Fatalf("projects = %+v, want newest-first", projects.Projects)
	}
}

func TestGetProjectAndNotFound(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	created := createTestProject(t, h, "atc", t.TempDir())

	rec := do(t, h, http.MethodGet, "/projects/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got Project
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != created.ID || got.Name != "atc" {
		t.Fatalf("got = %+v", got)
	}

	rec = do(t, h, http.MethodGet, "/projects/prj_missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "project_not_found")
}

func TestPatchProjectRenamesWithStrictDecode(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	created := createTestProject(t, h, "atc", t.TempDir())

	rec := do(t, h, http.MethodPatch, "/projects/"+created.ID, `{"name":"Renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var renamed Project
	if err := json.NewDecoder(rec.Body).Decode(&renamed); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if renamed.Name != "Renamed" || renamed.WorkingDir != created.WorkingDir {
		t.Fatalf("renamed = %+v", renamed)
	}

	// Unknown fields are rejected — workingDir especially, which is fixed.
	for _, body := range []string{
		`{"name":"X","workingDir":"/elsewhere"}`,
		`{"workingDir":"/elsewhere"}`,
		`{"name":"X","unknown":true}`,
	} {
		rec = do(t, h, http.MethodPatch, "/projects/"+created.ID, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("patch %s status = %d, want 400 (%s)", body, rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "invalid_request")
	}

	rec = do(t, h, http.MethodPatch, "/projects/"+created.ID, `{"name":"   "}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("blank rename status = %d, want 400", rec.Code)
	}
	assertErrorCode(t, rec, "invalid_request")

	rec = do(t, h, http.MethodPatch, "/projects/prj_missing", `{"name":"X"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing patch status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "project_not_found")
}

func TestDeleteProjectRequiresZeroWorkspaces(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	created := createTestProject(t, h, "atc", t.TempDir())
	ws := createTestWorkspace(t, h, created.ID, "Only workspace")

	rec := do(t, h, http.MethodDelete, "/projects/"+created.ID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete with workspaces status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "project_has_workspaces")

	rec = do(t, h, http.MethodDelete, "/workspaces/"+ws.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace delete status = %d (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodDelete, "/projects/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/projects/"+created.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get after delete status = %d, want 404", rec.Code)
	}

	rec = do(t, h, http.MethodDelete, "/projects/prj_missing", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete missing status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "project_not_found")
}

func TestListProjectSessions(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	created := createTestProject(t, h, "atc", t.TempDir())
	ws := createTestWorkspace(t, h, created.ID, "Feature work")

	rec := do(t, h, http.MethodPost, "/sessions/start", `{"actionId":"act_vpj2tlg9viqd8ms52ptuvao5c4","workspaceId":"`+ws.ID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d (%s)", rec.Code, rec.Body)
	}
	var scoped SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&scoped); err != nil {
		t.Fatalf("decode scoped: %v", err)
	}
	seedRunning(t, st, "ses_unscoped", "Elsewhere")
	mux.sessions = append(mux.sessions, zmx.Session{Name: zmx.NameForID("ses_unscoped")})

	rec = do(t, h, http.MethodGet, "/projects/"+created.ID+"/sessions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var list SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].ID != scoped.ID {
		t.Fatalf("project sessions = %+v, want only scoped session", list.Sessions)
	}

	// Ended sessions remain in default scoped lists and can be filtered.
	if _, err := st.MarkEnded(context.Background(), scoped.ID); err != nil {
		t.Fatalf("MarkEnded: %v", err)
	}
	rec = do(t, h, http.MethodGet, "/projects/"+created.ID+"/sessions", "")
	var all SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&all); err != nil {
		t.Fatalf("decode all: %v", err)
	}
	if len(all.Sessions) != 1 || all.Sessions[0].Status != "ended" {
		t.Fatalf("default project sessions = %+v", all.Sessions)
	}
	rec = do(t, h, http.MethodGet, "/projects/"+created.ID+"/sessions?status=ended", "")
	var full SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&full); err != nil {
		t.Fatalf("decode full: %v", err)
	}
	if len(full.Sessions) != 1 || full.Sessions[0].ID != scoped.ID {
		t.Fatalf("filtered project sessions = %+v", full.Sessions)
	}

	rec = do(t, h, http.MethodGet, "/projects/prj_missing/sessions", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing project status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "project_not_found")
}

func TestProjectRoutesWithoutServiceReturnInternalError(t *testing.T) {
	h := Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	assertErrorCode(t, rec, "internal_error")
}
