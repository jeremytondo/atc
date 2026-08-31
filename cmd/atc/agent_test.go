package main

import (
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

// Launching moved to `atc thread new` (ATC-282); the catalog reads remain.
func TestAgentLaunchIsGone(t *testing.T) {
	startTestServer(t)
	if _, _, err := runCLI(t, "agent", "launch", "claude"); err == nil || !strings.Contains(err.Error(), `unknown command "launch"`) {
		t.Errorf("agent launch = %v, want an unknown-command error", err)
	}
}
