package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremytondo/atelier-code/internal/diagnostics"
	"github.com/jeremytondo/atelier-code/internal/zmx"
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

	created := createTestProject(t, h, "Atelier Code", workDir)
	if !strings.HasPrefix(created.ID, "prj_") || created.Name != "Atelier Code" || created.WorkingDir != workDir {
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
		{name: "missing workingDir", body: `{"name":"Atelier Code"}`, code: "invalid_working_dir"},
		{name: "relative workingDir", body: `{"name":"Atelier Code","workingDir":"relative/path"}`, code: "invalid_working_dir"},
		{name: "missing directory", body: `{"name":"Atelier Code","workingDir":"` + filepath.Join(workDir, "missing") + `"}`, code: "invalid_working_dir"},
		{name: "workingDir is a file", body: `{"name":"Atelier Code","workingDir":"` + notADir + `"}`, code: "invalid_working_dir"},
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
	var active ProjectListResponse
	if err := json.NewDecoder(rec.Body).Decode(&active); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(active.Projects) != 1 || active.Projects[0].ID != second.ID {
		t.Fatalf("default list = %+v, want only active project", active.Projects)
	}

	rec = do(t, h, http.MethodGet, "/projects?includeArchived=true", "")
	var all ProjectListResponse
	if err := json.NewDecoder(rec.Body).Decode(&all); err != nil {
		t.Fatalf("decode full list: %v", err)
	}
	// Newest-created-first.
	if len(all.Projects) != 2 || all.Projects[0].ID != second.ID || all.Projects[1].ID != first.ID {
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
	created := createTestProject(t, h, "Atelier Code", t.TempDir())

	rec := do(t, h, http.MethodGet, "/projects/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var got Project
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.ID != created.ID || got.Name != "Atelier Code" {
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
	created := createTestProject(t, h, "Atelier Code", t.TempDir())

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
	created := createTestProject(t, h, "Atelier Code", t.TempDir())

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

func TestArchiveProjectWithActiveSessionConflicts(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	created := createTestProject(t, h, "Atelier Code", t.TempDir())

	rec := do(t, h, http.MethodPost, "/sessions/start", `{"action":"claude","projectId":"`+created.ID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var started SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&started); err != nil {
		t.Fatalf("decode started: %v", err)
	}

	rec = do(t, h, http.MethodPost, "/projects/"+created.ID+"/archive", "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("archive with running session status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "project_has_active_sessions")

	// The rejected archive left the project active.
	rec = do(t, h, http.MethodGet, "/projects/"+created.ID, "")
	var got Project
	if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
		t.Fatalf("decode project: %v", err)
	}
	if got.ArchivedAt != nil {
		t.Fatalf("project archived despite conflict: %+v", got)
	}

	// Once the session terminates, the archive goes through.
	rec = do(t, h, http.MethodPost, "/sessions/"+started.ID+"/terminate", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("terminate status = %d (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/projects/"+created.ID+"/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive after terminate status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
}

func TestStartSessionWithProject(t *testing.T) {
	mux := &fakeMux{}
	h, _ := newHandler(t, mux)
	projectDir := t.TempDir()
	created := createTestProject(t, h, "Atelier Code", projectDir)

	rec := do(t, h, http.MethodPost, "/sessions/start", `{"action":"claude","projectId":"`+created.ID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp SessionDetail
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.WorkingDir != projectDir {
		t.Fatalf("workingDir = %q, want inherited project dir", resp.WorkingDir)
	}
	if resp.Project == nil || resp.Project.ID != created.ID || resp.Project.Name != "Atelier Code" || resp.Project.WorkingDir != projectDir {
		t.Fatalf("project = %+v", resp.Project)
	}
	if mux.lastStart.dir != projectDir {
		t.Fatalf("launch dir = %q, want project dir", mux.lastStart.dir)
	}

	// The nested project appears on list and detail reads too.
	rec = do(t, h, http.MethodGet, "/sessions", "")
	var list SessionListResponse
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Sessions) != 1 || list.Sessions[0].Project == nil || list.Sessions[0].Project.ID != created.ID {
		t.Fatalf("list = %+v", list.Sessions)
	}
}

func TestStartSessionProjectErrors(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	workDir := t.TempDir()
	active := createTestProject(t, h, "Active", workDir)

	archived := createTestProject(t, h, "Archived", workDir)
	rec := do(t, h, http.MethodPost, "/projects/"+archived.ID+"/archive", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("archive status = %d (%s)", rec.Code, rec.Body)
	}

	vanishedDir := filepath.Join(t.TempDir(), "repo")
	if err := os.Mkdir(vanishedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	vanished := createTestProject(t, h, "Vanished", vanishedDir)
	if err := os.Remove(vanishedDir); err != nil {
		t.Fatalf("remove dir: %v", err)
	}

	tests := []struct {
		name   string
		body   string
		status int
		code   string
	}{
		{name: "workingDir and projectId conflict", body: `{"action":"claude","workingDir":"` + workDir + `","projectId":"` + active.ID + `"}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "neither workingDir nor projectId", body: `{"action":"claude"}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "unknown project", body: `{"action":"claude","projectId":"prj_missing"}`, status: http.StatusBadRequest, code: "project_not_found"},
		{name: "archived project", body: `{"action":"claude","projectId":"` + archived.ID + `"}`, status: http.StatusConflict, code: "project_archived"},
		{name: "vanished project directory", body: `{"action":"claude","projectId":"` + vanished.ID + `"}`, status: http.StatusBadRequest, code: "invalid_working_dir"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, http.MethodPost, "/sessions/start", tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			assertErrorCode(t, rec, tt.code)
		})
	}
}

func TestListProjectSessions(t *testing.T) {
	mux := &fakeMux{}
	h, st := newHandler(t, mux)
	created := createTestProject(t, h, "Atelier Code", t.TempDir())

	rec := do(t, h, http.MethodPost, "/sessions/start", `{"action":"claude","projectId":"`+created.ID+`"}`)
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
	h := Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/projects", nil)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	assertErrorCode(t, rec, "internal_error")
}
