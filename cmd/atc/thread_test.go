package main

import (
	"context"
	"regexp"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// observeThreadCLI plants an observed conversation behind the test server
// and returns its thread id — the seam providers use in production; the
// wire has no create verb.
func observeThreadCLI(t *testing.T, service *threads.Service, terminalID, projectID, providerID string) string {
	t.Helper()
	id, err := service.ObserveSession(context.Background(), threads.SessionObservation{
		Agent:      "claude",
		ProviderID: providerID,
		TerminalID: terminalID,
		ProjectID:  projectID,
		Status:     api.ThreadIdle,
		Metadata:   threads.Metadata{Title: "fix the build"},
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
	projectID := createProjectCLI(t, t.TempDir())
	terminalID := createTerminalCLI(t, projectID)
	id := observeThreadCLI(t, threadService, terminalID, projectID, "sess-1")

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
	observeThreadCLI(t, threadService, terminalID, projectID, "sess-2")
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
	projectID := createProjectCLI(t, t.TempDir())
	terminalID := createTerminalCLI(t, projectID)
	id := observeThreadCLI(t, threadService, terminalID, projectID, "sess-1")

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
