package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/jeremytondo/atc/internal/paths"
)

func TestSessionsStartPostsWorkspaceShape(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/sessions/start" {
			t.Fatalf("request = %s %s, want POST /api/sessions/start", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["actionId"] != "act_codex" || req["workspaceId"] != "wsp_123" || req["name"] != "Review" {
			t.Fatalf("request = %#v, want workspace start shape", req)
		}
		if _, ok := req["workingDir"]; ok {
			t.Fatalf("request = %#v, want no workingDir", req)
		}
		for _, removed := range []string{"action", "environment", "params", "prompt"} {
			if _, ok := req[removed]; ok {
				t.Fatalf("request = %#v, contains removed field %q", req, removed)
			}
		}
		_, _ = w.Write([]byte(`{"id":"ses_123","actionId":"act_codex","actionName":"Codex","isAgent":true,"workingDir":"/repo","status":"live","createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z","workspace":{"id":"wsp_123","name":"Review"}}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"start", "--workspace", "wsp_123", "--action", "act_codex", "--name", "Review"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "ses_123\tlive\n" {
		t.Fatalf("output = %q, want id and status", got)
	}
}

func TestSessionsStartWithoutActionOmitsActionAndParams(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["workspaceId"] != "wsp_123" {
			t.Fatalf("workspaceId = %#v, want wsp_123", req["workspaceId"])
		}
		if _, ok := req["actionId"]; ok {
			t.Fatalf("request = %#v, want no actionId", req)
		}
		_, _ = w.Write([]byte(`{"id":"ses_123","isAgent":false,"workingDir":"/repo","status":"live","createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z","workspace":{"id":"wsp_123","name":"Shell"}}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"start", "--workspace", "wsp_123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "ses_123\tlive\n" {
		t.Fatalf("output = %q, want id and status", got)
	}
}

func TestSessionsStartRequiresWorkspace(t *testing.T) {
	cmd := sessionsCommand(testRuntimeLookup(t))
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"start", "--action", "codex"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("Execute succeeded without --workspace")
	}
}

func TestSessionsListJSONUsesQuery(t *testing.T) {
	lookup := testRuntimeLookup(t)
	body := `{"sessions":[{"id":"ses_123","actionId":"act_codex","actionName":"Codex","isAgent":true,"workingDir":"/repo","status":"live","createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z"}]}`
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/sessions" {
			t.Fatalf("request = %s %s, want GET /api/sessions", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("status"); got != "live" {
			t.Fatalf("status query = %q, want live", got)
		}
		_, _ = w.Write([]byte(body))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--status", "live", "-o", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != body+"\n" {
		t.Fatalf("output = %q, want raw JSON response", got)
	}
}

func TestSessionsListTextShowsCopiedActionIdentity(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"sessions":[{"id":"ses_agent","actionId":"act_codex","actionName":"Codex","isAgent":true,"workingDir":"/repo","status":"live","createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z"},{"id":"ses_shell","isAgent":false,"workingDir":"/repo","status":"ended","createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z"}]}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute: %v", err)
	}
	got := out.String()
	if !strings.Contains(got, "ses_agent\tlive\tCodex\ttrue") || !strings.Contains(got, "ses_shell\tended\t(interactive shell)\tfalse") {
		t.Fatalf("output = %q", got)
	}
}

func TestSessionsShowTextIncludesFullDetail(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/sessions/ses_show" {
			t.Fatalf("request = %s %s, want GET /api/sessions/ses_show", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"ses_show","name":"Review","actionId":"act_codex","actionName":"Codex","isAgent":true,"workingDir":"/repo","status":"live","createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z"}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"show", "ses_show"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"id\tses_show\n",
		"actionId\tact_codex\n",
		"actionName\tCodex\n",
		"isAgent\ttrue\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want to contain %q", got, want)
		}
	}
}

func TestSessionsRenamePatchesName(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch || r.URL.Path != "/api/sessions/ses_123" {
			t.Fatalf("request = %s %s, want PATCH /api/sessions/ses_123", r.Method, r.URL.Path)
		}
		var req map[string]any
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["name"] != "Renamed" {
			t.Fatalf("name = %#v, want Renamed", req["name"])
		}
		_, _ = w.Write([]byte(`{"id":"ses_123","name":"Renamed","isAgent":false,"workingDir":"/repo","status":"ended","createdAt":"2026-07-09T12:30:00Z","updatedAt":"2026-07-09T14:00:00Z"}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"rename", "ses_123", "Renamed"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "ses_123\tRenamed\n" {
		t.Fatalf("output = %q", got)
	}
}

func TestSessionActionsPostResourceRoutes(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		path     string
		wantBody string
	}{
		{
			name:     "send text",
			args:     []string{"send-text", "ses_123", "hello"},
			path:     "/api/sessions/ses_123/send-text",
			wantBody: `{"text":"hello"}`,
		},
		{
			name:     "send key",
			args:     []string{"send-key", "ses_123", "enter"},
			path:     "/api/sessions/ses_123/send-key",
			wantBody: `{"key":"enter"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lookup := testRuntimeLookup(t)
			serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != tt.path {
					t.Fatalf("request = %s %s, want POST %s", r.Method, r.URL.Path, tt.path)
				}
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Fatalf("read body: %v", err)
				}
				if got := string(body); got != tt.wantBody {
					t.Fatalf("body = %q, want %q", got, tt.wantBody)
				}
				_, _ = w.Write([]byte(`{}`))
			})

			cmd := sessionsCommand(lookup)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetArgs(tt.args)

			if err := cmd.Execute(); err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if got := out.String(); got != "ses_123\n" {
				t.Fatalf("output = %q, want affected id", got)
			}
		})
	}
}

func TestRemovedSessionCommandsAndFlagsAreRejected(t *testing.T) {
	for _, args := range [][]string{{"terminate", "ses_123"}, {"archive", "ses_123"}, {"unarchive", "ses_123"}, {"list", "--include-archived"}, {"list", "--status", "starting"}, {"start", "--workspace", "wsp_123", "--env", "host"}, {"start", "--workspace", "wsp_123", "--param", "x=y"}, {"start", "--workspace", "wsp_123", "--prompt", "hello"}} {
		cmd := sessionsCommand(testRuntimeLookup(t))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Fatalf("Execute(%v) succeeded", args)
		}
	}
}

func TestSessionsListScopesToWorkspace(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/workspaces/wsp_123/sessions" {
			t.Fatalf("request = %s %s, want GET /api/workspaces/wsp_123/sessions", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"sessions":[]}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--workspace", "wsp_123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
}

func TestSessionsDeleteUsesDeleteMethodAndPrintsFilesStatement(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/sessions/ses_123" {
			t.Fatalf("request = %s %s, want DELETE /api/sessions/ses_123", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"delete", "ses_123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "ses_123") || !strings.Contains(got, filesNotTouched) {
		t.Fatalf("output = %q, want deleted id and files statement", got)
	}
}

func TestAPIClientDialAttachOverUnixSocketSetsAuthorization(t *testing.T) {
	socketPath := testSocketPath(t, "atc.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet || r.URL.Path != "/api/sessions/ses_ws/attach" {
				t.Fatalf("request = %s %s, want GET attach", r.Method, r.URL.Path)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer secret" {
				t.Fatalf("Authorization = %q, want bearer token", got)
			}
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Fatalf("accept websocket: %v", err)
			}
			defer conn.Close(websocket.StatusNormalClosure, "")
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()

	conn, err := newAPIClientWithToken(socketPath, "secret").dialAttach(context.Background(), "ses_ws")
	if err != nil {
		t.Fatalf("dialAttach returned error: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
}

func serveUnixAPI(t *testing.T, lookup envLookup, handler http.HandlerFunc) {
	t.Helper()

	socketPath := paths.PathsForEnv(paths.EnvLookup(lookup), os.Getuid()).SocketPath
	if err := os.MkdirAll(filepath.Dir(socketPath), 0o700); err != nil {
		t.Fatalf("create socket directory: %v", err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}

	server := &http.Server{Handler: handler}
	go func() {
		_ = server.Serve(listener)
	}()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})
}
