package api

import (
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
	if created.CreatedAt == "" || created.UpdatedAt == "" || created.ArchivedAt != nil {
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

func TestListProjectsHidesArchivedByDefault(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	workDir := t.TempDir()

	first := createTestProject(t, h, "First", workDir)
	second := createTestProject(t, h, "Second", workDir)
	rec := do(t, h, http.MethodPost, "/projects/"+first.ID+"/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d, want 200 (%s)", rec.Code, rec.Body)
	}

	rec = do(t, h, http.MethodGet, "/projects", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	// The handler seeds prj_test, so lists include it alongside this test's
	// projects, newest-created-first.
	var active ProjectListResponse
	if err := json.NewDecoder(rec.Body).Decode(&active); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(active.Projects) != 2 || active.Projects[0].ID != second.ID || active.Projects[1].ID != "prj_test" {
		t.Fatalf("default list = %+v, want active projects only", active.Projects)
	}

	rec = do(t, h, http.MethodGet, "/projects?includeArchived=true", "")
	var all ProjectListResponse
	if err := json.NewDecoder(rec.Body).Decode(&all); err != nil {
		t.Fatalf("decode full list: %v", err)
	}
	if len(all.Projects) != 3 || all.Projects[0].ID != second.ID || all.Projects[1].ID != first.ID {
		t.Fatalf("full list = %+v", all.Projects)
	}

	rec = do(t, h, http.MethodGet, "/projects?includeArchived=bogus", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("bad includeArchived status = %d, want 400", rec.Code)
	}
	assertErrorCode(t, rec, "invalid_request")
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

func TestArchiveAndUnarchiveProjectAreIdempotent(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	created := createTestProject(t, h, "atc", t.TempDir())

	rec := do(t, h, http.MethodPost, "/projects/"+created.ID+"/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var archived Project
	if err := json.NewDecoder(rec.Body).Decode(&archived); err != nil {
		t.Fatalf("decode archived: %v", err)
	}
	if archived.ArchivedAt == nil {
		t.Fatalf("archived = %+v, want archivedAt", archived)
	}

	rec = do(t, h, http.MethodPost, "/projects/"+created.ID+"/archive", "")
	var archivedAgain Project
	if err := json.NewDecoder(rec.Body).Decode(&archivedAgain); err != nil {
		t.Fatalf("decode archived again: %v", err)
	}
	if rec.Code != http.StatusOK || *archivedAgain.ArchivedAt != *archived.ArchivedAt {
		t.Fatalf("second archive = %d %+v", rec.Code, archivedAgain)
	}

	rec = do(t, h, http.MethodPost, "/projects/"+created.ID+"/unarchive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("unarchive status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var unarchived Project
	if err := json.NewDecoder(rec.Body).Decode(&unarchived); err != nil {
		t.Fatalf("decode unarchived: %v", err)
	}
	if unarchived.ArchivedAt != nil {
		t.Fatalf("unarchived = %+v", unarchived)
	}

	rec = do(t, h, http.MethodPost, "/projects/prj_missing/archive", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing archive status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "project_not_found")
}

func TestArchiveProjectBlockedByUnarchivedWorkspace(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	created := createTestProject(t, h, "atc", t.TempDir())
	ws := createTestWorkspace(t, h, created.ID, "Feature work")

	rec := do(t, h, http.MethodPost, "/projects/"+created.ID+"/archive", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("archive with unarchived workspace status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "project_has_unarchived_workspaces")

	// The rejected archive left the project active.
	rec = do(t, h, http.MethodGet, "/projects/"+created.ID, "")
	var got Project
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if got.ArchivedAt != nil {
		t.Fatalf("project archived despite conflict: %+v", got)
	}

	// Once every workspace is archived, the archive goes through.
	rec = do(t, h, http.MethodPost, "/workspaces/"+ws.ID+"/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("workspace archive status = %d (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/projects/"+created.ID+"/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive after workspace archive status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
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

	rec := do(t, h, http.MethodPost, "/sessions/start", `{"action":"claude","workspaceId":"`+ws.ID+`"}`)
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

	// Terminate and archive the scoped session: hidden by default, shown with
	// includeArchived, and the status filter applies.
	rec = do(t, h, http.MethodPost, "/sessions/"+scoped.ID+"/terminate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("terminate status = %d (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/sessions/"+scoped.ID+"/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/projects/"+created.ID+"/sessions", "")
	var hidden SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&hidden); err != nil {
		t.Fatalf("decode hidden: %v", err)
	}
	if len(hidden.Sessions) != 0 {
		t.Fatalf("default project sessions = %+v, want archived hidden", hidden.Sessions)
	}
	rec = do(t, h, http.MethodGet, "/projects/"+created.ID+"/sessions?includeArchived=true&status=terminated", "")
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
