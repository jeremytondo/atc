package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jeremytondo/atc/internal/application"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/integrations/claude"
	"github.com/jeremytondo/atc/internal/integrations/codex"
	"github.com/jeremytondo/atc/internal/integrations/t3code"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
	"github.com/jeremytondo/atc/internal/integrations/zmx"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/server"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
)

// cliDriver is the fake session backend behind the CLI tests' server.
// commands records what each session was created with — the private App
// command the CLI must never print.
type cliDriver struct {
	mu       sync.Mutex
	sessions map[string]bool
	commands map[string]string
}

func (a *cliDriver) Inventory(context.Context) ([]terminals.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	sessions := make([]terminals.Session, 0, len(a.sessions))
	for name, reachable := range a.sessions {
		sessions = append(sessions, terminals.Session{Name: name, Reachable: reachable})
	}
	return sessions, nil
}

func (a *cliDriver) Create(_ context.Context, id string, spec terminals.CreateSpec) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sessions[id] = true
	if a.commands == nil {
		a.commands = map[string]string{}
	}
	a.commands[id] = spec.Command
	return nil
}

func (a *cliDriver) Kill(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.sessions, id)
	return nil
}

const cliTestToken = "atc_cli-test-token"

// startTestServer runs the real chassis over a fake backend and points the
// CLI at it through ATC_SERVER and ATC_TOKEN — the same paste-once setup a
// remote client uses.
func startTestServer(t *testing.T) *cliDriver {
	t.Helper()
	return startTestServerFull(t).driver
}

// startTestServerWithThreads additionally exposes the threads service so
// thread tests can plant observed conversations — the seam providers use
// in production.
func startTestServerWithThreads(t *testing.T) (*cliDriver, *threads.Service) {
	t.Helper()
	ts := startTestServerFull(t)
	return ts.driver, ts.threads
}

// testServer is the chassis behind the CLI under test: the fake terminal
// driver, the threads service, and the T3 Code Integration over a fake
// T3 environment that connectT3 brings up on demand.
type testServer struct {
	driver   *cliDriver
	threads  *threads.Service
	t3       *t3code.Service
	t3Server *t3codetest.Server
	t3Home   string
}

// connectT3 brings the T3 Code Integration up against the fake
// environment, which knows one project rooted at root, and keeps it
// running until the test ends.
func (ts *testServer) connectT3(t *testing.T, root string) {
	t.Helper()
	t3codetest.Connect(t, ts.t3Server, ts.t3Home, root, ts.t3.Run, ts.t3.Connection)
}

func startTestServerFull(t *testing.T) *testServer {
	t.Helper()
	// The server is local, so an unplaced create opens in this process's
	// cwd: a scratch directory, never the repository the tests run from.
	t.Chdir(t.TempDir())
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	driver := &cliDriver{sessions: map[string]bool{}}
	hub := events.NewHub(events.DefaultBacklog)
	service := terminals.NewService(terminals.Options{
		Repository: db.Terminals(),
		Driver:     driver,
		Spaces:     db.Spaces(),
		HomeDir:    t.TempDir(),
		MarkerDir:  t.TempDir(),
		Hub:        hub,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err := service.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	projectService := projects.NewService(projects.Options{
		Repository: db.Projects(),
		Hub:        hub,
	})
	threadService := threads.NewService(threads.Options{
		Repository: db.Threads(),
		Terminals:  service,
		Projects:   db.Projects(),
		Hub:        hub,
	})
	if err := threadService.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	claudeHooks, err := claude.NewHooks(claude.HooksOptions{
		Dir:       t.TempDir(),
		BaseURL:   "http://127.0.0.1:0",
		Threads:   threadService,
		Terminals: service,
	})
	if err != nil {
		t.Fatal(err)
	}
	codexObserver := codex.NewObserver(codex.ObserverOptions{
		CodexHome: t.TempDir(),
		Threads:   threadService,
		Terminals: service,
	})
	// The T3 Code service over a private home and a fake environment:
	// registered for its catalog entry and links, and run only by tests
	// that connect it.
	t3Server := t3codetest.NewServer(t)
	t3Home := t.TempDir()
	t3Service := t3code.New(t3code.Options{
		Home:         t3Home,
		SessionPath:  filepath.Join(t.TempDir(), "t3code-session.json"),
		Threads:      threadService,
		Hub:          hub,
		RunCLI:       t3codetest.NewCLI(t3Server).Run,
		ProcessAlive: func(int) bool { return true },
	})
	threadService.SetLinker(t3code.ID, t3Service.Links)
	catalog, err := integrations.NewService(integrations.Options{
		Integrations: []integrations.Integration{
			claude.Integration(claudeHooks), codex.Integration(codexObserver), t3code.Integration(t3Service), zmx.Integration(),
		},
		// The probe never consults this machine's PATH: claude and zmx
		// "exist", codex does not.
		LookPath: func(name string) (string, error) {
			if name == "claude" || name == "zmx" {
				return "/bin/" + name, nil
			}
			return "", errors.New("executable file not found in $PATH")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := server.NewHandler(server.Options{
		Coordinator:  application.New(application.Options{Terminals: service, Threads: threadService, Projects: projectService, Integrations: catalog}),
		Verify:       func(authorization string) bool { return authorization == "Bearer "+cliTestToken },
		Version:      "v0.0.0-test",
		Terminals:    service,
		Projects:     projectService,
		Integrations: catalog,
		Threads:      threadService,
		Events:       hub,
	})
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	t.Setenv("ATC_SERVER", srv.URL)
	t.Setenv("ATC_TOKEN", cliTestToken)
	return &testServer{driver: driver, threads: threadService, t3: t3Service, t3Server: t3Server, t3Home: t3Home}
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
	prevStdio, prevStdin := stdioIsTerminal, stdinIsTTY
	stdioIsTerminal = func() bool { return true }
	stdinIsTTY = func() bool { return true }
	t.Cleanup(func() { stdioIsTerminal, stdinIsTTY = prevStdio, prevStdin })
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
	driver := startTestServer(t)
	// The server is local, so the terminal opens in this process's cwd —
	// a temp directory here, so the test never touches the repo.
	dir := canonical(t, t.TempDir())
	t.Chdir(dir)

	stdout, _, err := runCLI(t, "terminal", "create", "--command", "hx", "--name", "hx")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if id == "" {
		t.Fatalf("create output has no terminal id:\n%s", stdout)
	}
	if !strings.Contains(stdout, "running") || !regexp.MustCompile(`(?m)^directory\s+`+regexp.QuoteMeta(dir)+`$`).MatchString(stdout) {
		t.Errorf("create output missing status or the cwd as directory:\n%s", stdout)
	}

	stdout, _, err = runCLI(t, "terminal", "list")
	if err != nil {
		t.Fatal(err)
	}
	// IDs beside names, statuses, and spaces — the list contract.
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "hx") ||
		!strings.Contains(stdout, "running") || !strings.Contains(stdout, "spce-") {
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
	driver.mu.Lock()
	remaining := len(driver.sessions)
	driver.mu.Unlock()
	if remaining != 0 {
		t.Errorf("session survived delete")
	}
	if stdout, _, err = runCLI(t, "terminal", "list"); err != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("list after delete = %q, %v", stdout, err)
	}
}

func TestTerminalListShowsApp(t *testing.T) {
	startTestServer(t)

	plainOut, _, err := runCLI(t, "terminal", "create", "--command", "hx", "--name", "plain", "--detach")
	if err != nil {
		t.Fatal(err)
	}
	plainID := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(plainOut)
	if plainID == "" {
		t.Fatalf("plain create output has no terminal id:\n%s", plainOut)
	}
	appOut, _, err := runCLI(t, "terminal", "create", "--app", "claude/tui", "--name", "agent", "--detach")
	if err != nil {
		t.Fatal(err)
	}
	appID := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(appOut)
	if appID == "" {
		t.Fatalf("app create output has no terminal id:\n%s", appOut)
	}

	stdout, _, err := runCLI(t, "terminal", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !containsRow(stdout, "ID\tSTATUS\tNAME\tAPP\tSPACE\tDIRECTORY") {
		t.Fatalf("list output missing APP header:\n%s", stdout)
	}
	if got := tableColumn(t, stdout, plainID, "APP", "SPACE"); got != "" {
		t.Errorf("plain terminal APP = %q, want blank\n%s", got, stdout)
	}
	if got := tableColumn(t, stdout, appID, "APP", "SPACE"); got != "claude/tui" {
		t.Errorf("app terminal APP = %q, want claude/tui\n%s", got, stdout)
	}
}

func tableColumn(t *testing.T, table, rowID, column, nextColumn string) string {
	t.Helper()
	lines := strings.Split(table, "\n")
	if len(lines) == 0 {
		t.Fatal("empty table")
	}
	start := strings.Index(lines[0], column)
	end := strings.Index(lines[0], nextColumn)
	if start < 0 || end <= start {
		t.Fatalf("table header has no %s column before %s:\n%s", column, nextColumn, table)
	}
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, rowID) {
			if len(line) < end {
				return ""
			}
			return strings.TrimSpace(line[start:end])
		}
	}
	t.Fatalf("table has no row %s:\n%s", rowID, table)
	return ""
}

func TestTerminalCreateHelpIsScannable(t *testing.T) {
	help, _, err := runCLI(t, "terminal", "create", "--help")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(strings.Fields(help), " ")
	for _, want := range []string{
		"Session mode (choose one):",
		"(none) start a plain interactive shell",
		"--command CMD run CMD through your login shell",
		"--app ID launch an app listed by `atc integration list`",
		"--thread ID resume a conversation in its original app",
		"App conversations become threads at their first prompt",
		"Unavailable apps are rejected before creating a terminal",
		"a local server uses your current directory; a remote server uses the space's directory",
		"attaches by default",
		"Use --detach to leave the session running in the background",
		"reattach with `atc terminal attach <id>`",
		"working directory (default: --space directory; otherwise local cwd or remote Default space)",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("terminal create help missing %q:\n%s", want, help)
		}
	}
	for _, section := range []string{"\n\nSession mode", "\n\nApp conversations", "\n\nUse --space", "\n\nThe command attaches"} {
		if !strings.Contains(help, section) {
			t.Errorf("terminal create help missing section break before %q:\n%s", strings.TrimSpace(section), help)
		}
	}
}

func TestTerminalGetUnknownIsError(t *testing.T) {
	startTestServer(t)
	if _, _, err := runCLI(t, "terminal", "get", "term-zzzzz"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get unknown = %v, want a 404 problem", err)
	}
}

// Attach is the default, but never a precondition where it is impossible:
// without a TTY the terminal is still created and printed, and stderr
// says why nothing was attached.
func TestTerminalCreateWithoutTTYStillCreates(t *testing.T) {
	startTestServer(t)
	stdout, stderr, err := runCLI(t, "terminal", "create")
	if err != nil {
		t.Fatalf("create without TTY = %v", err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if id == "" || !strings.Contains(stdout, "running") {
		t.Fatalf("create output has no running terminal:\n%s", stdout)
	}
	if !strings.Contains(stderr, "not attaching") || !strings.Contains(stderr, "stdin and stdout must be TTYs") {
		t.Errorf("stderr = %q; want the skipped-attach note", stderr)
	}
	// --detach is silent about attaching: nothing was asked for.
	if _, stderr, err := runCLI(t, "terminal", "create", "--detach"); err != nil || strings.Contains(stderr, "attach") {
		t.Errorf("create --detach = %q, %v", stderr, err)
	}
}

// A tooling preflight — no zmx on this machine — fails before anything is
// created; --detach sidesteps it.
func TestTerminalCreateMissingZmxCreatesNothing(t *testing.T) {
	forceTTY(t)
	startTestServer(t)
	t.Setenv("PATH", "")

	_, _, err := runCLI(t, "terminal", "create")
	if err == nil || err.Error() != "zmx executable not found on PATH; install zmx to attach" {
		t.Errorf("create without zmx = %v", err)
	}
	stdout, _, listErr := runCLI(t, "terminal", "list")
	if listErr != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("list after zmx refusal = %q, %v", stdout, listErr)
	}
	if _, _, err := runCLI(t, "terminal", "create", "--detach"); err != nil {
		t.Errorf("create --detach without zmx = %v", err)
	}
}

func TestTerminalCreateAttachMissingSocketKeepsTerminal(t *testing.T) {
	forceTTY(t)
	installFakeZmx(t)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	startTestServer(t)

	stdout, _, err := runCLI(t, "terminal", "create")
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if id == "" || !strings.Contains(stdout, "running") {
		t.Fatalf("create attach output does not show the created terminal:\n%s", stdout)
	}
	if err == nil || !strings.Contains(err.Error(), "has no session socket") {
		t.Fatalf("create attach without socket = %v, want the missing-socket refusal", err)
	}
	if !strings.Contains(err.Error(), "retry with: atc terminal attach "+id) {
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

	driver := startTestServer(t)
	stdout, _, err := runCLI(t, "terminal", "create", "--detach")
	if err != nil {
		t.Fatal(err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	// The session vanishes without evidence; the next mutation's reconcile
	// (create of an unrelated terminal) settles it to missing.
	driver.mu.Lock()
	delete(driver.sessions, id)
	driver.mu.Unlock()
	if _, _, err := runCLI(t, "terminal", "create", "--name", "other", "--detach"); err != nil {
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
	stdout, _, err = runCLI(t, "api", "-d", `{"command":"hx"}`, "/v1/terminals")
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
