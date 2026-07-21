package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectsCreatePostsResolvedDir(t *testing.T) {
	tests := []struct {
		name    string
		dir     func(t *testing.T) (arg, want string)
		chdir   bool
		homeDir bool
	}{
		{
			name: "absolute",
			dir: func(t *testing.T) (string, string) {
				d := t.TempDir()
				return d, d
			},
		},
		{
			name:  "relative resolves against cwd",
			chdir: true,
			dir: func(t *testing.T) (string, string) {
				return "sub/dir", ""
			},
		},
		{
			name:    "tilde expands to home",
			homeDir: true,
			dir: func(t *testing.T) (string, string) {
				return "~/repo", ""
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := testRuntimeLookup(t)
			arg, want := tt.dir(t)
			if tt.chdir {
				dir := t.TempDir()
				previous, err := os.Getwd()
				if err != nil {
					t.Fatalf("get cwd: %v", err)
				}
				if err := os.Chdir(dir); err != nil {
					t.Fatalf("chdir: %v", err)
				}
				t.Cleanup(func() { _ = os.Chdir(previous) })
				cwd, err := os.Getwd()
				if err != nil {
					t.Fatalf("get changed cwd: %v", err)
				}
				want = filepath.Join(cwd, "sub", "dir")
			}
			if tt.homeDir {
				home := t.TempDir()
				t.Setenv("HOME", home)
				want = filepath.Join(home, "repo")
			}

			serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/api/projects" {
					t.Fatalf("request = %s %s, want POST /api/projects", r.Method, r.URL.Path)
				}
				var req map[string]any
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatalf("decode request: %v", err)
				}
				if req["name"] != "atc" || req["workingDir"] != want {
					t.Fatalf("request = %#v, want name atc dir %q", req, want)
				}
				w.WriteHeader(http.StatusCreated)
				_, _ = w.Write([]byte(`{"id":"prj_123","name":"atc","workingDir":"` + want + `","createdAt":"2026-07-07T15:04:05Z","updatedAt":"2026-07-07T15:04:05Z"}`))
			})

			cmd := projectsCommand(lookup)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs([]string{"create", "--name", "atc", "--dir", arg})

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if got := out.String(); got != "prj_123\tatc\n" {
				t.Fatalf("output = %q, want id and name", got)
			}
		})
	}
}

func TestProjectsListTextAndQuery(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects" {
			t.Fatalf("request = %s %s, want GET /api/projects", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"projects":[
			{"id":"prj_new","name":"New","workingDir":"/repo/new","createdAt":"2026-07-07T15:04:05Z","updatedAt":"2026-07-07T15:04:05Z"},
			{"id":"prj_old","name":"Old","workingDir":"/repo/old","createdAt":"2026-07-01T15:04:05Z","updatedAt":"2026-07-02T15:04:05Z"}
		]}`))
	})

	cmd := projectsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := "prj_new\tNew\t/repo/new\nprj_old\tOld\t/repo/old\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestProjectsListJSONIsRawBody(t *testing.T) {
	lookup := testRuntimeLookup(t)
	body := `{"projects":[{"id":"prj_123","name":"atc","workingDir":"/repo","createdAt":"2026-07-07T15:04:05Z","updatedAt":"2026-07-07T15:04:05Z"}]}`
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	})

	cmd := projectsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "-o", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != body+"\n" {
		t.Fatalf("output = %q, want raw JSON body", got)
	}
}

func TestProjectsShowPrintsKeyValueLines(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects/prj_show" {
			t.Fatalf("request = %s %s, want GET /api/projects/prj_show", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"prj_show","name":"atc","workingDir":"/repo","createdAt":"2026-07-07T15:04:05Z","updatedAt":"2026-07-07T16:04:05Z"}`))
	})

	cmd := projectsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "prj_show"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	want := "id\tprj_show\nname\tatc\nworkingDir\t/repo\ncreatedAt\t2026-07-07T15:04:05Z\nupdatedAt\t2026-07-07T16:04:05Z\n"
	if got := out.String(); got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}
}

func TestProjectsRenameUsesPatch(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/projects/prj_123" {
			t.Fatalf("request = %s %s, want PATCH /api/projects/prj_123", r.Method, r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if got := string(body); got != `{"name":"Renamed"}` {
			t.Fatalf("body = %q, want rename body", got)
		}
		_, _ = w.Write([]byte(`{"id":"prj_123","name":"Renamed","workingDir":"/repo","createdAt":"2026-07-07T15:04:05Z","updatedAt":"2026-07-07T16:04:05Z"}`))
	})

	cmd := projectsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"rename", "prj_123", "Renamed"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "prj_123\tRenamed\n" {
		t.Fatalf("output = %q, want id and new name", got)
	}
}

func TestProjectsDeleteUsesDeleteMethodAndPrintsFilesStatement(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/projects/prj_123" {
			t.Fatalf("request = %s %s, want DELETE /api/projects/prj_123", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	})

	cmd := projectsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"delete", "prj_123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "prj_123") || !strings.Contains(got, filesNotTouched) {
		t.Fatalf("output = %q, want deleted id and files statement", got)
	}
}

func TestSessionsStartRejectsProjectWithDir(t *testing.T) {
	cmd := sessionsCommand(testRuntimeLookup(t))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"start", "--action", "codex", "--project", "prj_123", "--dir", "/repo"})

	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute accepted --project with --dir, want mutual-exclusion error")
	}
}

func TestSessionsListWithProjectUsesProjectRoute(t *testing.T) {
	lookup := testRuntimeLookup(t)
	body := `{"sessions":[]}`
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/projects/prj_123/sessions" {
			t.Fatalf("request = %s %s, want GET /api/projects/prj_123/sessions", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("status"); got != "live" {
			t.Fatalf("status query = %q, want live", got)
		}
		_, _ = w.Write([]byte(body))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--project", "prj_123", "--status", "live", "-o", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != body+"\n" {
		t.Fatalf("output = %q, want raw JSON response", got)
	}
}

func TestSessionsShowPrintsProjectFields(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"id":"ses_show","action":"codex","environment":"host-login-shell","params":{},"workingDir":"/repo","status":"live","createdAt":"2026-07-07T15:04:05Z","updatedAt":"2026-07-07T15:04:06Z","project":{"id":"prj_123","name":"atc","workingDir":"/repo"}}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "ses_show"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{"project\tprj_123\n", "projectName\tatc\n"} {
		if !bytes.Contains([]byte(got), []byte(want)) {
			t.Fatalf("output = %q, want to contain %q", got, want)
		}
	}
}
