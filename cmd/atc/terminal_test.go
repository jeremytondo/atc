package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jeremytondo/atc/internal/cli"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/server"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/terminals"
)

// cliAdapter is the fake session backend behind the CLI tests' server.
type cliAdapter struct {
	mu       sync.Mutex
	sessions map[string]bool
}

func (a *cliAdapter) Inventory(context.Context) ([]terminals.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sessions := make([]terminals.Session, 0, len(a.sessions))
	for name, reachable := range a.sessions {
		sessions = append(sessions, terminals.Session{Name: name, Reachable: reachable})
	}
	return sessions, nil
}

func (a *cliAdapter) Create(_ context.Context, id string, _ terminals.CreateSpec) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[id] = true
	return nil
}

func (a *cliAdapter) Kill(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, id)
	return nil
}

const cliTestToken = "atc_cli-test-token"

// startTestServer runs the real chassis over a fake backend and points the
// CLI at it through ATC_SERVER and ATC_TOKEN — the same paste-once setup a
// remote client uses.
func startTestServer(t *testing.T) *cliAdapter {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	adapter := &cliAdapter{sessions: map[string]bool{}}
	hub := events.NewHub(events.DefaultBacklog)
	service := terminals.NewService(terminals.Options{
		Repository: db.Terminals(),
		Adapter:    adapter,
		Projects:   db.Projects(),
		MarkerDir:  t.TempDir(),
		Hub:        hub,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	projectService := projects.NewService(projects.Options{
		Repository: db.Projects(),
		Terminals:  db.Terminals(),
		Hub:        hub,
	})
	handler := server.NewHandler(server.Options{
		Verify:    func(authorization string) bool { return authorization == "Bearer "+cliTestToken },
		Version:   "v0.0.0-test",
		Terminals: service,
		Projects:  projectService,
		Events:    hub,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("ATC_SERVER", srv.URL)
	t.Setenv("ATC_TOKEN", cliTestToken)
	return adapter
}

func runCLI(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	return runCLIInput(t, "", args...)
}

// runCLIInput drives the CLI with stdin content — the create-offer prompt.
func runCLIInput(t *testing.T, input string, args ...string) (string, string, error) {
	t.Helper()
	var stdout, stderr strings.Builder
	err := run(context.Background(), args, strings.NewReader(input), &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

var projectIDFormat = regexp.MustCompile(`proj-[a-z2-9]{5}`)

// createProjectCLI registers a project rooted at dir and returns its id.
func createProjectCLI(t *testing.T, dir string) string {
	t.Helper()
	stdout, _, err := runCLI(t, "project", "create", dir)
	if err != nil {
		t.Fatalf("project create: %v", err)
	}
	id := projectIDFormat.FindString(stdout)
	if id == "" {
		t.Fatalf("project create output has no id:\n%s", stdout)
	}
	return id
}

func forceTTY(t *testing.T) {
	t.Helper()
	prevStdio, prevStdin := cli.StdioIsTerminal, cli.StdinIsTTY
	cli.StdioIsTerminal = func() bool { return true }
	cli.StdinIsTTY = func() bool { return true }
	t.Cleanup(func() { cli.StdioIsTerminal, cli.StdinIsTTY = prevStdio, prevStdin })
}

func installFakeZmx(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "zmx"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)
}

func TestTerminalCLILifecycle(t *testing.T) {
	adapter := startTestServer(t)
	// --project skips cwd resolution entirely; the cwd (this repo) owns no
	// project on the test server.
	projectID := createProjectCLI(t, t.TempDir())

	stdout, _, err := runCLI(t, "terminal", "create", "--app", "hx", "--project", projectID)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if id == "" {
		t.Fatalf("create output has no terminal id:\n%s", stdout)
	}
	if !strings.Contains(stdout, "running") {
		t.Errorf("create output missing status:\n%s", stdout)
	}

	stdout, _, err = runCLI(t, "terminal", "list")
	if err != nil {
		t.Fatal(err)
	}
	// IDs beside names, statuses, and projects — the list contract.
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "hx") ||
		!strings.Contains(stdout, "running") || !strings.Contains(stdout, projectID) {
		t.Errorf("list output:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "terminal", "update", id, "--name", "build watcher"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "build watcher") {
		t.Errorf("update output:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "terminal", "get", id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "build watcher") {
		t.Errorf("get output:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "terminal", "delete", id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "deleted "+id) {
		t.Errorf("delete output:\n%s", stdout)
	}
	adapter.mu.Lock()
	remaining := len(adapter.sessions)
	adapter.mu.Unlock()
	if remaining != 0 {
		t.Errorf("session survived delete")
	}
	if stdout, _, err = runCLI(t, "terminal", "list"); err != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("list after delete = %q, %v", stdout, err)
	}
}

func TestTerminalGetUnknownIsError(t *testing.T) {
	startTestServer(t)
	if _, _, err := runCLI(t, "terminal", "get", "term-zzzzz"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get unknown = %v, want a 404 problem", err)
	}
}

func TestTerminalCreateAttachWithoutTTYCreatesNothing(t *testing.T) {
	startTestServer(t)
	_, _, err := runCLI(t, "terminal", "create", "--attach")
	if err == nil || !strings.Contains(err.Error(), "stdin and stdout must be TTYs") {
		t.Errorf("create attach without TTY = %v, want a TTY refusal", err)
	}
	stdout, _, listErr := runCLI(t, "terminal", "list")
	if listErr != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("list after TTY refusal = %q, %v", stdout, listErr)
	}
}

func TestTerminalCreateAttachRemoteCreatesNothing(t *testing.T) {
	forceTTY(t)
	t.Setenv("ATC_SERVER", "http://100.64.0.5:7331")
	t.Setenv("ATC_TOKEN", cliTestToken)

	_, _, err := runCLI(t, "terminal", "create", "--attach")
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Errorf("remote create attach = %v, want the local-only refusal", err)
	}
}

func TestTerminalCreateAttachMissingZmxCreatesNothing(t *testing.T) {
	forceTTY(t)
	startTestServer(t)
	t.Setenv("PATH", "")

	_, _, err := runCLI(t, "terminal", "create", "--attach")
	if err == nil || err.Error() != "zmx executable not found on PATH; install zmx to attach" {
		t.Errorf("create attach without zmx = %v", err)
	}
	stdout, _, listErr := runCLI(t, "terminal", "list")
	if listErr != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("list after zmx refusal = %q, %v", stdout, listErr)
	}
}

func TestTerminalCreateAttachMissingSocketKeepsTerminal(t *testing.T) {
	forceTTY(t)
	installFakeZmx(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	startTestServer(t)

	projectID := createProjectCLI(t, t.TempDir())
	stdout, _, err := runCLI(t, "terminal", "create", "--attach", "--project", projectID)
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if id == "" || !strings.Contains(stdout, "running") {
		t.Fatalf("create attach output does not show the created terminal:\n%s", stdout)
	}
	if err == nil || !strings.Contains(err.Error(), "has no session socket") {
		t.Fatalf("create attach without socket = %v, want the missing-socket refusal", err)
	}
	if !strings.Contains(err.Error(), "the terminal was created; retry with: atc terminal attach "+id) {
		t.Errorf("create attach error has no retry instruction: %v", err)
	}
	stdout, _, getErr := runCLI(t, "terminal", "get", id)
	if getErr != nil || !strings.Contains(stdout, id) {
		t.Errorf("get created terminal = %q, %v", stdout, getErr)
	}
}

func TestTerminalAttachWithoutTTY(t *testing.T) {
	t.Setenv("ATC_SERVER", "http://100.64.0.5:7331")
	t.Setenv("ATC_TOKEN", cliTestToken)
	_, _, err := runCLI(t, "terminal", "attach", "term-zzzzz")
	if err == nil || !strings.Contains(err.Error(), "stdin and stdout must be TTYs") {
		t.Errorf("attach without TTY = %v, want a TTY refusal", err)
	}
}

// Attach is local-only: against a non-loopback server it refuses before
// touching anything.
func TestAttachRemoteFailsClearly(t *testing.T) {
	forceTTY(t)
	t.Setenv("ATC_SERVER", "http://100.64.0.5:7331")
	t.Setenv("ATC_TOKEN", cliTestToken)
	_, _, err := runCLI(t, "terminal", "attach", "term-zzzzz")
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Errorf("remote attach = %v, want the local-only refusal", err)
	}
}

// Attach never resurrects: anything but running refuses before zmx runs.
func TestAttachRefusesNonRunning(t *testing.T) {
	forceTTY(t)
	// Defense in depth: if a regression ever let attach proceed, an empty
	// PATH fails the zmx lookup and an isolated state dir keeps even that
	// failure away from the developer's real sessions.
	t.Setenv("PATH", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	adapter := startTestServer(t)
	projectID := createProjectCLI(t, t.TempDir())
	stdout, _, err := runCLI(t, "terminal", "create", "--project", projectID)
	if err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	// The session vanishes without evidence; the next mutation's reconcile
	// (create of an unrelated terminal) settles it to missing.
	adapter.mu.Lock()
	delete(adapter.sessions, id)
	adapter.mu.Unlock()
	if _, _, err := runCLI(t, "terminal", "create", "--name", "other", "--project", projectID); err != nil {
		t.Fatal(err)
	}

	_, _, err = runCLI(t, "terminal", "attach", id)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Errorf("attach to missing terminal = %v, want a not-running refusal", err)
	}
}

func TestAPICommand(t *testing.T) {
	startTestServer(t)
	stdout, _, err := runCLI(t, "api", "/v1/health")
	if err != nil {
		t.Fatal(err)
	}
	var health struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal([]byte(stdout), &health); err != nil || health.Status != "ok" {
		t.Errorf("api /v1/health output = %q (%v)", stdout, err)
	}

	// POST with a body creates a terminal through the raw gateway.
	projectID := createProjectCLI(t, t.TempDir())
	stdout, _, err = runCLI(t, "api", "-d", `{"app":"hx","projectId":"`+projectID+`"}`, "/v1/terminals")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, `"id":"term-`) {
		t.Errorf("api create output = %q", stdout)
	}

	// An HTTP error status is a non-zero exit with the body on stdout.
	stdout, _, err = runCLI(t, "api", "/v1/terminals/term-zzzzz")
	if err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("api 404 = %v", err)
	}
	if !strings.Contains(stdout, "Not Found") {
		t.Errorf("api 404 body = %q, want the problem document on stdout", stdout)
	}
}
