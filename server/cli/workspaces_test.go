package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestWorkspacesCreatePostsProjectAndName(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/workspaces" {
			t.Fatalf("request = %s %s, want POST /api/workspaces", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["projectId"] != "prj_123" || req["name"] != "Login bug" {
			t.Fatalf("request = %#v, want projectId and name", req)
		}
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"wsp_123","projectId":"prj_123","name":"Login bug","createdAt":"2026-07-09T12:30:00Z","updatedAt":"2026-07-09T12:30:00Z"}`))
	})

	cmd := workspacesCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"create", "--project", "prj_123", "--name", "Login bug"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "wsp_123\tLogin bug\n" {
		t.Fatalf("output = %q, want id and name", got)
	}
}

func TestWorkspacesCreateRequiresFlags(t *testing.T) {
	cmd := workspacesCommand(testRuntimeLookup(t))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"create", "--name", "X"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute succeeded without --project")
	}
}

func TestWorkspacesListUsesFilters(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/workspaces" {
			t.Fatalf("request = %s %s, want GET /api/workspaces", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("projectId"); got != "prj_123" {
			t.Fatalf("projectId query = %q, want prj_123", got)
		}
		if got := r.URL.Query().Get("includeArchived"); got != "true" {
			t.Fatalf("includeArchived query = %q, want true", got)
		}
		_, _ = w.Write([]byte(`{"workspaces":[{"id":"wsp_123","projectId":"prj_123","name":"Login bug","createdAt":"2026-07-09T12:30:00Z","updatedAt":"2026-07-09T12:30:00Z","archivedAt":"2026-07-10T09:00:00Z"}]}`))
	})

	cmd := workspacesCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--project", "prj_123", "--include-archived"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "wsp_123\tLogin bug\tprj_123\t2026-07-10T09:00:00Z\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestWorkspacesShowTextIncludesFullRecord(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/workspaces/wsp_123" {
			t.Fatalf("request = %s %s, want GET /api/workspaces/wsp_123", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"wsp_123","projectId":"prj_123","name":"Login bug","createdAt":"2026-07-09T12:30:00Z","updatedAt":"2026-07-09T12:30:00Z"}`))
	})

	cmd := workspacesCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "wsp_123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{"id\twsp_123\n", "name\tLogin bug\n", "projectId\tprj_123\n"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want to contain %q", got, want)
		}
	}
}

func TestWorkspacesRenamePatchesName(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/workspaces/wsp_123" {
			t.Fatalf("request = %s %s, want PATCH /api/workspaces/wsp_123", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["name"] != "Renamed" {
			t.Fatalf("request = %#v, want name", req)
		}
		_, _ = w.Write([]byte(`{"id":"wsp_123","projectId":"prj_123","name":"Renamed","createdAt":"2026-07-09T12:30:00Z","updatedAt":"2026-07-09T14:00:00Z"}`))
	})

	cmd := workspacesCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"rename", "wsp_123", "Renamed"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "wsp_123\tRenamed\n" {
		t.Fatalf("output = %q, want id and new name", got)
	}
}

func TestWorkspacesArchiveUnarchivePostResourceRoutes(t *testing.T) {
	for _, action := range []string{"archive", "unarchive"} {
		t.Run(action, func(t *testing.T) {
			lookup := testRuntimeLookup(t)
			serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
				wantPath := "/api/workspaces/wsp_123/" + action
				if r.Method != http.MethodPost || r.URL.Path != wantPath {
					t.Fatalf("request = %s %s, want POST %s", r.Method, r.URL.Path, wantPath)
				}
				_, _ = w.Write([]byte(`{}`))
			})

			cmd := workspacesCommand(lookup)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{action, "wsp_123"})

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if got := out.String(); got != "wsp_123\n" {
				t.Fatalf("output = %q, want affected id", got)
			}
		})
	}
}

func TestWorkspacesDeleteUsesDeleteMethodAndPrintsFilesStatement(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/workspaces/wsp_123" {
			t.Fatalf("request = %s %s, want DELETE /api/workspaces/wsp_123", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	})

	cmd := workspacesCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"delete", "wsp_123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "wsp_123") || !strings.Contains(got, filesNotTouched) {
		t.Fatalf("output = %q, want deleted id and files statement", got)
	}
}
