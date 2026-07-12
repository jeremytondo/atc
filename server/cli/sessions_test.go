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
		if req["action"] != "codex" || req["environment"] != "host-login-shell" || req["workspaceId"] != "wsp_123" || req["prompt"] != "review" || req["name"] != "Review" {
			t.Fatalf("request = %#v, want workspace start shape", req)
		}
		if _, ok := req["workingDir"]; ok {
			t.Fatalf("request = %#v, want no workingDir", req)
		}
		params, ok := req["params"].(map[string]any)
		if !ok || params["model"] != "gpt-5-codex" {
			t.Fatalf("params = %#v, want model param", req["params"])
		}
		_, _ = w.Write([]byte(`{"id":"ses_123","action":"codex","environment":"host-login-shell","params":{"model":"gpt-5-codex"},"workingDir":"/repo","prompt":"review","status":"running","attachable":true,"createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z","workspace":{"id":"wsp_123","name":"Review"}}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"start", "--workspace", "wsp_123", "--action", "codex", "--env", "host-login-shell", "--param", "model=gpt-5-codex", "--prompt", "review", "--name", "Review"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "ses_123\trunning\n" {
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
		// An interactive-shell start must not carry action or params keys.
		if _, ok := req["action"]; ok {
			t.Fatalf("request = %#v, want no action", req)
		}
		if _, ok := req["params"]; ok {
			t.Fatalf("request = %#v, want no params", req)
		}
		_, _ = w.Write([]byte(`{"id":"ses_123","environment":"host-login-shell","params":{},"workingDir":"/repo","status":"running","attachable":true,"createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z","workspace":{"id":"wsp_123","name":"Shell"}}`))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"start", "--workspace", "wsp_123"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "ses_123\trunning\n" {
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
	body := `{"sessions":[{"id":"ses_123","action":"codex","environment":"host-login-shell","workingDir":"/repo","status":"running","attachable":true,"createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z"}]}`
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/sessions" {
			t.Fatalf("request = %s %s, want GET /api/sessions", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("status"); got != "running" {
			t.Fatalf("status query = %q, want running", got)
		}
		if got := r.URL.Query().Get("includeArchived"); got != "true" {
			t.Fatalf("includeArchived query = %q, want true", got)
		}
		_, _ = w.Write([]byte(body))
	})

	cmd := sessionsCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"list", "--status", "running", "--include-archived", "-o", "json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != body+"\n" {
		t.Fatalf("output = %q, want raw JSON response", got)
	}
}

func TestSessionsShowTextIncludesFullDetail(t *testing.T) {
	lookup := testRuntimeLookup(t)
	serveUnixAPI(t, lookup, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/sessions/ses_show" {
			t.Fatalf("request = %s %s, want GET /api/sessions/ses_show", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"id":"ses_show","name":"Review","action":"codex","environment":"host-login-shell","params":{"model":"gpt-5-codex"},"workingDir":"/repo","prompt":"review","status":"running","attachable":true,"createdAt":"2026-06-25T15:04:05Z","updatedAt":"2026-06-25T15:04:06Z"}`))
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
		"action\tcodex\n",
		"environment\thost-login-shell\n",
		"prompt\treview\n",
		"params\t{\"model\":\"gpt-5-codex\"}\n",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want to contain %q", got, want)
		}
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
		{
			name:     "terminate",
			args:     []string{"terminate", "ses_123"},
			path:     "/api/sessions/ses_123/terminate",
			wantBody: `{}`,
		},
		{
			name:     "archive",
			args:     []string{"archive", "ses_123"},
			path:     "/api/sessions/ses_123/archive",
			wantBody: `{}`,
		},
		{
			name:     "unarchive",
			args:     []string{"unarchive", "ses_123"},
			path:     "/api/sessions/ses_123/unarchive",
			wantBody: `{}`,
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
