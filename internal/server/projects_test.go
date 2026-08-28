package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

func decodeProject(t *testing.T, rec *httptest.ResponseRecorder) api.Project {
	t.Helper()
	var project api.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &project); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return project
}

func TestProjectCRUDOverTheWire(t *testing.T) {
	f := newFixture(t)
	dir := t.TempDir()

	created := f.createProject(t, dir)
	// The name defaults to the basename of the canonical directory.
	if created.Name != filepath.Base(created.Directory) || created.Directory == "" {
		t.Fatalf("created = %+v", created)
	}
	if !strings.HasPrefix(created.ID, "proj-") {
		t.Fatalf("id = %q, want the proj- prefix", created.ID)
	}

	rec := f.request(t, http.MethodGet, "/v1/projects/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d", rec.Code)
	}
	if diff := cmp.Diff(created, decodeProject(t, rec)); diff != "" {
		t.Errorf("get (-created +got):\n%s", diff)
	}

	rec = f.request(t, http.MethodGet, "/v1/projects", "")
	var list api.ProjectList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	// The fixture project plus this one, in creation order.
	if len(list.Projects) != 2 || list.Projects[1].ID != created.ID {
		t.Errorf("list = %+v", list)
	}

	rec = f.request(t, http.MethodPatch, "/v1/projects/"+created.ID, `{"name":"renamed"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d; body %s", rec.Code, rec.Body)
	}
	if got := decodeProject(t, rec); got.Name != "renamed" {
		t.Errorf("updated name = %q", got.Name)
	}

	// Immutable and unknown fields are rejected by schema.
	for name, body := range map[string]string{
		"immutable directory": `{"name":"x","directory":"/elsewhere"}`,
		"unknown field":       `{"name":"x","frobnicate":true}`,
		"missing name":        `{}`,
	} {
		rec := f.request(t, http.MethodPatch, "/v1/projects/"+created.ID, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422; body %s", name, rec.Code, rec.Body)
		}
	}

	rec = f.request(t, http.MethodDelete, "/v1/projects/"+created.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodGet, "/v1/projects/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: got %d, want 404", rec.Code)
	}
}

func TestProjectCreateValidation(t *testing.T) {
	f := newFixture(t)

	// A path that does not exist (or is a file) cannot become a project.
	rec := f.request(t, http.MethodPost, "/v1/projects", `{"directory":"/no/such/dir"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("nonexistent dir: got %d, want 422; body %s", rec.Code, rec.Body)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(api.ProjectCreateParams{Directory: file})
	if rec := f.request(t, http.MethodPost, "/v1/projects", string(body)); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("file path: got %d, want 422; body %s", rec.Code, rec.Body)
	}

	// A symlinked spelling of a claimed folder is the same canonical
	// directory: uniqueness compares canonical forms only.
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(f.projectDir, link); err != nil {
		t.Fatal(err)
	}
	body, _ = json.Marshal(api.ProjectCreateParams{Directory: link})
	rec = f.request(t, http.MethodPost, "/v1/projects", string(body))
	if rec.Code != http.StatusConflict {
		t.Errorf("symlinked duplicate: got %d, want 409; body %s", rec.Code, rec.Body)
	}

	// An explicit name wins over the basename default, and names are not
	// required to be unique.
	other := t.TempDir()
	body, _ = json.Marshal(api.ProjectCreateParams{Directory: other, Name: "custom"})
	rec = f.request(t, http.MethodPost, "/v1/projects", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("named create: got %d; body %s", rec.Code, rec.Body)
	}
	if got := decodeProject(t, rec); got.Name != "custom" {
		t.Errorf("name = %q, want custom", got.Name)
	}
}

func TestProjectDeleteRefusedWhileTerminalsRemain(t *testing.T) {
	f := newFixture(t)
	created := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals",
		f.createTerminalBody(t, api.TerminalCreateParams{})))

	rec := f.request(t, http.MethodDelete, "/v1/projects/"+f.projectID, "")
	if rec.Code != http.StatusConflict {
		t.Fatalf("delete non-empty: got %d, want 409; body %s", rec.Code, rec.Body)
	}
	// The refusal reports what remains.
	if !strings.Contains(rec.Body.String(), created.ID) {
		t.Errorf("refusal does not name the terminal: %s", rec.Body)
	}

	if rec := f.request(t, http.MethodDelete, "/v1/terminals/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete terminal: got %d", rec.Code)
	}
	if rec := f.request(t, http.MethodDelete, "/v1/projects/"+f.projectID, ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete emptied project: got %d; body %s", rec.Code, rec.Body)
	}
}

// The ATC-256 terminal-create contract: projectId is required, must name a
// known project, and the project's directory must exist at that moment.
func TestTerminalCreateProjectContract(t *testing.T) {
	f := newFixture(t)

	if rec := f.request(t, http.MethodPost, "/v1/terminals", `{}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("missing projectId: got %d, want 422; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodPost, "/v1/terminals", `{"projectId":"proj-zzzzz"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("unknown project: got %d, want 422; body %s", rec.Code, rec.Body)
	}
	// The directory parameter is gone from create.
	if rec := f.request(t, http.MethodPost, "/v1/terminals",
		`{"projectId":"`+f.projectID+`","directory":"/elsewhere"}`); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("directory param: got %d, want 422; body %s", rec.Code, rec.Body)
	}

	// The project's folder vanishing after project creation refuses the
	// create, and no terminal row is written.
	if err := os.RemoveAll(f.projectDir); err != nil {
		t.Fatal(err)
	}
	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{}))
	if rec.Code != http.StatusConflict {
		t.Errorf("vanished directory: got %d, want 409; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodGet, "/v1/terminals", ""); !strings.Contains(rec.Body.String(), `"terminals":[]`) {
		t.Errorf("refused create left a terminal: %s", rec.Body)
	}
}

func TestTerminalListProjectFilter(t *testing.T) {
	f := newFixture(t)
	other := f.createProject(t, t.TempDir())

	mine := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals",
		f.createTerminalBody(t, api.TerminalCreateParams{})))
	theirs := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals",
		f.createTerminalBody(t, api.TerminalCreateParams{ProjectID: other.ID})))

	var list api.TerminalList
	rec := f.request(t, http.MethodGet, "/v1/terminals?project="+other.ID, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Terminals) != 1 || list.Terminals[0].ID != theirs.ID {
		t.Errorf("filtered list = %+v, want only %s", list, theirs.ID)
	}
	rec = f.request(t, http.MethodGet, "/v1/terminals", "")
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Terminals) != 2 {
		t.Errorf("unfiltered list = %+v, want both %s and %s", list, mine.ID, theirs.ID)
	}
}
