package main

import (
	"strings"
	"testing"
)

// The test server's catalog is the shipped one with claude's binary
// available, codex's missing, and T3 Code not running (see
// startTestServer).

func TestAgentListCLI(t *testing.T) {
	startTestServer(t)
	stdout, _, err := runCLI(t, "agent", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "AVAILABLE", "ADAPTERS", "claude", "Claude Code", "claude, t3code", "codex", "unavailable"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
}

func TestAgentGetCLI(t *testing.T) {
	startTestServer(t)
	stdout, _, err := runCLI(t, "agent", "get", "codex")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"id", "codex", "unavailable", "codex, t3code"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output missing %q:\n%s", want, stdout)
		}
	}

	if _, _, err := runCLI(t, "agent", "get", "nonexistent"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get unknown = %v, want a 404 problem", err)
	}
}

// The adapter reads explain availability: a missing binary names its
// install command right in the list, and T3 Code reports its connection.
func TestAgentAdapterCLI(t *testing.T) {
	startTestServer(t)
	stdout, _, err := runCLI(t, "agent", "adapter", "list")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"ID", "DETAIL", "claude", "available", "codex", "install: npm install -g @openai/codex", "t3code", "T3 Code", "unavailable: T3 Code is not running"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("adapter list output missing %q:\n%s", want, stdout)
		}
	}

	stdout, _, err = runCLI(t, "agent", "adapter", "get", "t3code")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"t3code", "agents", "claude, codex, cursor, grok, opencode", "connection", "unavailable", "since", "detail"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("adapter get output missing %q:\n%s", want, stdout)
		}
	}
	if _, _, err := runCLI(t, "agent", "adapter", "get", "nonexistent"); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("adapter get unknown = %v, want a 404 problem", err)
	}
}

// Launching moved to `atc thread new` (ATC-282); the catalog reads remain.
func TestAgentLaunchIsGone(t *testing.T) {
	startTestServer(t)
	if _, _, err := runCLI(t, "agent", "launch", "claude"); err == nil || !strings.Contains(err.Error(), `unknown command "launch"`) {
		t.Errorf("agent launch = %v, want an unknown-command error", err)
	}
}
