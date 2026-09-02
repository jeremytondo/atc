package main

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/threads"
)

// observeThreadCLI plants an observed conversation behind the test server
// and returns its thread id — the seam providers use in production; the
// wire has no create verb.
func observeThreadCLI(t *testing.T, service *threads.Service, terminalID, directory, providerID string) string {
	t.Helper()
	id, err := service.ObserveSession(context.Background(), threads.SessionObservation{
		IntegrationID:    "claude",
		AppID:            "claude/tui",
		AgentID:          "claude",
		ProviderID:       providerID,
		TerminalID:       terminalID,
		InitialDirectory: directory,
		Status:           api.ThreadIdle,
		Metadata:         threads.Metadata{Title: "fix the build"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// createTerminalCLI creates a terminal over the wire and returns its id.
func createTerminalCLI(t *testing.T, projectID string) string {
	t.Helper()
	stdout, _, err := runCLI(t, "terminal", "create", "--command", "claude", "--project", projectID)
	if err != nil {
		t.Fatalf("terminal create: %v", err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if id == "" {
		t.Fatalf("terminal create output has no id:\n%s", stdout)
	}
	return id
}

func TestThreadCLILifecycle(t *testing.T) {
	_, threadService := startTestServerWithThreads(t)
	projectDir := t.TempDir()
	projectID := createProjectCLI(t, projectDir)
	terminalID := createTerminalCLI(t, projectID)
	id := observeThreadCLI(t, threadService, terminalID, projectDir, "sess-1")

	stdout, _, err := runCLI(t, "thread", "list", "--project", projectID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "idle") ||
		!strings.Contains(stdout, "claude") || !strings.Contains(stdout, "fix the build") {
		t.Errorf("list output:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "thread", "get", id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, terminalID) ||
		!strings.Contains(stdout, projectID) || !strings.Contains(stdout, "fix the build") {
		t.Errorf("get output:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "thread", "update", id, "--title", "my title"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "my title") {
		t.Errorf("update output:\n%s", stdout)
	}

	// The conversation is still open in the terminal: archive and delete
	// are refused with a conflict naming it.
	if _, _, err := runCLI(t, "thread", "archive", id); err == nil || !strings.Contains(err.Error(), terminalID) {
		t.Errorf("archive active = %v; want a conflict naming %s", err, terminalID)
	}
	if _, _, err := runCLI(t, "thread", "delete", id); err == nil || !strings.Contains(err.Error(), terminalID) {
		t.Errorf("delete active = %v; want a conflict naming %s", err, terminalID)
	}

	// Switch the terminal to another conversation; the first thread is now
	// inactive and archivable.
	observeThreadCLI(t, threadService, terminalID, projectDir, "sess-2")
	if stdout, _, err = runCLI(t, "thread", "archive", id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "archived") {
		t.Errorf("archive output:\n%s", stdout)
	}

	// Hidden by default, shown with --archived, restored by unarchive.
	if stdout, _, err = runCLI(t, "thread", "list", "--project", projectID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, id) {
		t.Errorf("archived thread still listed:\n%s", stdout)
	}
	if stdout, _, err = runCLI(t, "thread", "list", "--project", projectID, "--archived"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, id) {
		t.Errorf("--archived list misses the thread:\n%s", stdout)
	}
	if stdout, _, err = runCLI(t, "thread", "unarchive", id); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "archived") {
		t.Errorf("unarchive output still marked archived:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "thread", "delete", id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "deleted "+id) {
		t.Errorf("delete output:\n%s", stdout)
	}
	if stdout, _, err = runCLI(t, "thread", "list", "--project", projectID); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, id) {
		t.Errorf("deleted thread still listed:\n%s", stdout)
	}
}

func TestThreadListUnfiltered(t *testing.T) {
	_, threadService := startTestServerWithThreads(t)
	projectDir := t.TempDir()
	projectID := createProjectCLI(t, projectDir)
	terminalID := createTerminalCLI(t, projectID)
	id := observeThreadCLI(t, threadService, terminalID, projectDir, "sess-1")

	// Like terminal list: unfiltered means everything, no project
	// resolution.
	stdout, _, err := runCLI(t, "thread", "list")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(stdout, id) {
		t.Errorf("list output:\n%s", stdout)
	}
}

func TestThreadGetUnknownIsError(t *testing.T) {
	startTestServer(t)
	if _, _, err := runCLI(t, "thread", "get", "thrd-zzzzz"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get unknown = %v, want a 404 problem", err)
	}
}

// An App launch is `terminal create --app` (ATC-294): the terminal shows
// the app and the app's name, never the composed command, and no thread
// exists before the first prompt. `thread new` is gone.
func TestTerminalCreateAppCLI(t *testing.T) {
	_, threadService := startTestServerWithThreads(t)
	projectDir := t.TempDir()
	projectID := createProjectCLI(t, projectDir)

	stdout, _, err := runCLI(t, "terminal", "create", "--app", "claude/tui", "--project", projectID, "--detach")
	if err != nil {
		t.Fatalf("create --app: %v", err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if id == "" || !strings.Contains(stdout, "running") {
		t.Fatalf("create output has no running terminal:\n%s", stdout)
	}
	if !regexp.MustCompile(`(?m)^app\s+claude/tui$`).MatchString(stdout) || !strings.Contains(stdout, "Claude Code") {
		t.Errorf("create output missing the app or default name:\n%s", stdout)
	}
	if strings.Contains(stdout, "thrd-") || strings.Contains(stdout, "--settings") {
		t.Errorf("create printed a thread id or the private command:\n%s", stdout)
	}
	if _, _, err := runCLI(t, "thread", "new", "claude", "--project", projectID); err == nil {
		t.Error("thread new still exists")
	}
	if _, _, err := runCLI(t, "terminal", "create", "--app", "claude/tui", "--command", "hx", "--project", projectID); err == nil || !strings.Contains(err.Error(), "none of the others can be") {
		t.Errorf("--app with --command = %v, want cobra's mutual-exclusion refusal", err)
	}

	// The terminal is a normal terminal from here, and --terminal leads
	// from it to its thread once the conversation has one.
	stdout, _, err = runCLI(t, "terminal", "list")
	if err != nil || !strings.Contains(stdout, id) {
		t.Errorf("terminal list = %q, %v", stdout, err)
	}
	if stdout, _, err = runCLI(t, "thread", "list", "--terminal", id); err != nil || !strings.Contains(stdout, "no threads") {
		t.Errorf("thread list --terminal before the first prompt = %q, %v", stdout, err)
	}
	threadID := observeThreadCLI(t, threadService, id, projectDir, "sess-1")
	if stdout, _, err = runCLI(t, "thread", "list", "--terminal", id); err != nil || !strings.Contains(stdout, threadID) {
		t.Errorf("thread list --terminal = %q, %v", stdout, err)
	}
	if stdout, _, err = runCLI(t, "thread", "list", "--terminal", "term-zzzzz"); err != nil || !strings.Contains(stdout, "no threads") {
		t.Errorf("thread list --terminal other = %q, %v", stdout, err)
	}
}

// Without a TTY, an App launch still creates and prints the terminal,
// noting on stderr why it did not attach; a missing executable, a
// handoff App, and an unknown App are refused before anything exists.
func TestTerminalCreateAppWithoutTTYAndRefusals(t *testing.T) {
	startTestServer(t)
	projectDir := t.TempDir()
	projectID := createProjectCLI(t, projectDir)

	stdout, stderr, err := runCLI(t, "terminal", "create", "--app", "claude/tui", "--project", projectID)
	if err != nil {
		t.Fatalf("create --app without TTY = %v", err)
	}
	if !regexp.MustCompile(`term-[a-z2-9]{5}`).MatchString(stdout) {
		t.Errorf("create output has no terminal id:\n%s", stdout)
	}
	if !strings.Contains(stderr, "not attaching") || !strings.Contains(stderr, "stdin and stdout must be TTYs") {
		t.Errorf("stderr = %q; want the skipped-attach note", stderr)
	}

	_, _, err = runCLI(t, "terminal", "create", "--app", "codex/tui", "--project", projectID)
	if err == nil || !strings.Contains(err.Error(), `"codex"`) ||
		!strings.Contains(err.Error(), "npm install -g @openai/codex") {
		t.Errorf("create unavailable app = %v, want the command and its install hint", err)
	}
	if _, _, err = runCLI(t, "terminal", "create", "--app", "t3code/web", "--project", projectID); err == nil || !strings.Contains(err.Error(), "does not run in a terminal") {
		t.Errorf("create handoff app = %v, want the not-terminal refusal", err)
	}
	if _, _, err = runCLI(t, "terminal", "create", "--app", "nonexistent/tui", "--project", projectID); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("create unknown app = %v, want a 404 problem", err)
	}
	stdout, _, listErr := runCLI(t, "terminal", "list")
	if listErr != nil || strings.Count(stdout, "term-") != 1 {
		t.Errorf("a refused create left a terminal: %q, %v", stdout, listErr)
	}
}

// Open lands on the terminal showing the thread, and resumes a dormant
// one in a new terminal linked to it.
func TestThreadOpenCLI(t *testing.T) {
	driver, threadService := startTestServerWithThreads(t)
	projectDir := t.TempDir()
	projectID := createProjectCLI(t, projectDir)
	stdout, _, err := runCLI(t, "terminal", "create", "--app", "claude/tui", "--project", projectID, "--detach")
	if err != nil {
		t.Fatal(err)
	}
	terminalID := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	id := observeThreadCLI(t, threadService, terminalID, projectDir, "sess-1")

	stdout, stderr, err := runCLI(t, "thread", "open", id, "--detach")
	if err != nil {
		t.Fatalf("open active: %v", err)
	}
	if !regexp.MustCompile(`(?m)^id\s+`+terminalID+`$`).MatchString(stdout) || strings.Contains(stderr, "attach") {
		t.Errorf("open active output = %q, stderr %q; want %s and no attach note", stdout, stderr, terminalID)
	}

	// The TUI exits; the thread is dormant. Open resumes it in a fresh
	// terminal running the exact resume (privately: the output shows the
	// app, never the command), and the thread now points there.
	driver.mu.Lock()
	delete(driver.sessions, terminalID)
	driver.mu.Unlock()
	if _, _, err := runCLI(t, "terminal", "delete", terminalID); err != nil {
		t.Fatal(err)
	}
	stdout, stderr, err = runCLI(t, "thread", "open", id)
	if err != nil {
		t.Fatalf("open dormant: %v", err)
	}
	resumed := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if resumed == "" || resumed == terminalID || !strings.Contains(stdout, "running") ||
		!regexp.MustCompile(`(?m)^app\s+claude/tui$`).MatchString(stdout) || strings.Contains(stdout, "--resume") {
		t.Errorf("open dormant output:\n%s", stdout)
	}
	driver.mu.Lock()
	command := driver.commands[resumed]
	driver.mu.Unlock()
	if !strings.HasSuffix(command, "--resume 'sess-1'") {
		t.Errorf("resumed session command = %q", command)
	}
	if !strings.Contains(stderr, "not attaching") {
		t.Errorf("stderr = %q; want the skipped-attach note", stderr)
	}
	if stdout, _, err = runCLI(t, "thread", "get", id); err != nil || !strings.Contains(stdout, resumed) {
		t.Errorf("thread after open = %q, %v; want linked to %s", stdout, err, resumed)
	}

	if _, _, err := runCLI(t, "thread", "open", "thrd-zzzzz"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("open unknown = %v, want a 404 problem", err)
	}
}

// --project assigns any project, --remove-project clears; both at once,
// or an empty --project, is refused before any request.
func TestThreadUpdateProjectCLI(t *testing.T) {
	_, threadService := startTestServerWithThreads(t)
	projectDir, err := paths.CanonicalDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectID := createProjectCLI(t, projectDir)
	otherID := createProjectCLI(t, t.TempDir())
	terminalID := createTerminalCLI(t, projectID)
	id := observeThreadCLI(t, threadService, terminalID, projectDir, "sess-1")

	stdout, _, err := runCLI(t, "thread", "get", id)
	if err != nil || !regexp.MustCompile(`(?m)^project\s+`+projectID+`$`).MatchString(stdout) ||
		!regexp.MustCompile(`(?m)^initial directory\s+`+regexp.QuoteMeta(projectDir)+`$`).MatchString(stdout) {
		t.Fatalf("get = %q, %v; want classified into %s with its origin", stdout, err, projectID)
	}
	if stdout, _, err = runCLI(t, "thread", "update", id, "--project", otherID); err != nil || !regexp.MustCompile(`(?m)^project\s+`+otherID+`$`).MatchString(stdout) {
		t.Errorf("assign = %q, %v", stdout, err)
	}
	if stdout, _, err = runCLI(t, "thread", "update", id, "--remove-project"); err != nil || strings.Contains(stdout, "\nproject") {
		t.Errorf("remove = %q, %v; want no project line", stdout, err)
	}
	if stdout, _, err = runCLI(t, "thread", "list", "--project", projectID); err != nil || strings.Contains(stdout, id) {
		t.Errorf("list after remove = %q, %v; want the thread gone from the project", stdout, err)
	}
	if _, _, err := runCLI(t, "thread", "update", id, "--project", otherID, "--remove-project"); err == nil {
		t.Error("--project with --remove-project was accepted")
	}
	if _, _, err := runCLI(t, "thread", "update", id, "--project", ""); err == nil || !strings.Contains(err.Error(), "--remove-project") {
		t.Errorf("empty --project = %v; want a refusal pointing at --remove-project", err)
	}
	if _, _, err := runCLI(t, "thread", "update", id); err == nil {
		t.Error("update with no flags was accepted")
	}
}
