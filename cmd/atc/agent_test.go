package main

import (
	"regexp"
	"strings"
	"testing"
)

// The test server's catalog is the shipped one with claude's binary
// available and codex's missing (see startTestServer).

func TestAgentListCLI(t *testing.T) {
	startTestServer(t)
	stdout, _, err := runCLI(t, "agent", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "claude") || !strings.Contains(stdout, "Claude Code") ||
		!strings.Contains(stdout, "tui (available)") {
		t.Errorf("list output missing claude's availability:\n%s", stdout)
	}
	// The acceptance bar: an unavailable capability names its install
	// command right in the list.
	if !strings.Contains(stdout, "codex") || !strings.Contains(stdout, "tui (unavailable; install: npm install -g @openai/codex)") {
		t.Errorf("list output missing codex's unavailability and install hint:\n%s", stdout)
	}
}

func TestAgentGetCLI(t *testing.T) {
	startTestServer(t)
	stdout, _, err := runCLI(t, "agent", "get", "codex")
	if err != nil {
		t.Fatal(err)
	}
	// Entry details include the install hint whether or not it is needed.
	if !strings.Contains(stdout, "codex") || !strings.Contains(stdout, "unavailable") ||
		!strings.Contains(stdout, "npm install -g @openai/codex") {
		t.Errorf("get output:\n%s", stdout)
	}

	if _, _, err := runCLI(t, "agent", "get", "nonexistent"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get unknown = %v, want a 404 problem", err)
	}
}

func TestAgentLaunchCLI(t *testing.T) {
	startTestServer(t)
	projectID := createProjectCLI(t, t.TempDir())

	stdout, _, err := runCLI(t, "agent", "launch", "claude", "--project", projectID)
	if err != nil {
		t.Fatalf("launch: %v", err)
	}
	id := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if id == "" || !strings.Contains(stdout, "running") {
		t.Fatalf("launch output has no running terminal:\n%s", stdout)
	}
	if !regexp.MustCompile(`(?m)^agent\s+claude$`).MatchString(stdout) || !strings.Contains(stdout, "Claude Code") {
		t.Errorf("launch output missing the agent label or default name:\n%s", stdout)
	}

	// The terminal is a normal terminal from here: listed, labeled, deletable.
	stdout, _, err = runCLI(t, "terminal", "list")
	if err != nil || !strings.Contains(stdout, id) {
		t.Errorf("terminal list = %q, %v", stdout, err)
	}
}

func TestAgentLaunchUnavailableCLI(t *testing.T) {
	startTestServer(t)
	projectID := createProjectCLI(t, t.TempDir())

	_, _, err := runCLI(t, "agent", "launch", "codex", "--project", projectID)
	if err == nil || !strings.Contains(err.Error(), `"codex"`) ||
		!strings.Contains(err.Error(), "npm install -g @openai/codex") {
		t.Errorf("launch unavailable = %v, want the command and its install hint", err)
	}
	stdout, _, listErr := runCLI(t, "terminal", "list")
	if listErr != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("a refused launch left a terminal: %q, %v", stdout, listErr)
	}
}

// Launch --attach shares terminal create's preflights: a non-TTY refusal
// happens before anything is created.
func TestAgentLaunchAttachWithoutTTYCreatesNothing(t *testing.T) {
	startTestServer(t)
	_, _, err := runCLI(t, "agent", "launch", "claude", "--attach")
	if err == nil || !strings.Contains(err.Error(), "stdin and stdout must be TTYs") {
		t.Errorf("launch attach without TTY = %v, want a TTY refusal", err)
	}
	stdout, _, listErr := runCLI(t, "terminal", "list")
	if listErr != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("list after TTY refusal = %q, %v", stdout, listErr)
	}
}
