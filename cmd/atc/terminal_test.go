package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jeremytondo/atc/internal/events"
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
		MarkerDir:  t.TempDir(),
		Hub:        hub,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		HomeDir:    "/home/tester",
	})
	handler := server.NewHandler(server.Options{
		Verify:    func(authorization string) bool { return authorization == "Bearer "+cliTestToken },
		Version:   "v0.0.0-test",
		Terminals: service,
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
	var stdout, stderr strings.Builder
	err := run(context.Background(), args, &stdout, &stderr)
	return stdout.String(), stderr.String(), err
}

func TestTerminalCLILifecycle(t *testing.T) {
	adapter := startTestServer(t)

	stdout, _, err := runCLI(t, "terminal", "create", "--app", "hx", "--dir", "/proj")
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
	// IDs beside names and statuses — the list contract.
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "hx") || !strings.Contains(stdout, "running") {
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

// Attach is local-only: against a non-loopback server it refuses before
// touching anything.
func TestAttachRemoteFailsClearly(t *testing.T) {
	t.Setenv("ATC_SERVER", "http://100.64.0.5:7331")
	t.Setenv("ATC_TOKEN", cliTestToken)
	_, _, err := runCLI(t, "terminal", "attach", "term-zzzzz")
	if err == nil || !strings.Contains(err.Error(), "local-only") {
		t.Errorf("remote attach = %v, want the local-only refusal", err)
	}
}

// Attach never resurrects: anything but running refuses before zmx runs.
func TestAttachRefusesNonRunning(t *testing.T) {
	// Defense in depth: if a regression ever let attach proceed, an empty
	// PATH fails the zmx lookup and an isolated state dir keeps even that
	// failure away from the developer's real sessions.
	t.Setenv("PATH", "")
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	adapter := startTestServer(t)
	stdout, _, err := runCLI(t, "terminal", "create")
	if err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	// The session vanishes without evidence; the next mutation's reconcile
	// (create of an unrelated terminal) settles it to missing.
	adapter.mu.Lock()
	delete(adapter.sessions, id)
	adapter.mu.Unlock()
	if _, _, err := runCLI(t, "terminal", "create", "--name", "other"); err != nil {
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
	stdout, _, err = runCLI(t, "api", "-d", `{"app":"hx"}`, "/v1/terminals")
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
