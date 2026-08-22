package agentstatus

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSourcePrecedenceAndTransitionEvidence(t *testing.T) {
	stateDir := t.TempDir()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	service, err := New(stateDir, "/tmp/atc-zmx", func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	process := ProcessEvidence{State: ProcessRunning, Detail: "zmx process is running"}
	screen := func(context.Context) ([]byte, error) {
		return []byte("Claude Code\nDo you want to proceed?\n❯"), nil
	}

	fromScreen, err := service.Observe(context.Background(), "claude", "session", screen, process)
	if err != nil {
		t.Fatal(err)
	}
	if fromScreen.State != StateIdle || fromScreen.Evidence.Source != SourceScreen {
		t.Fatalf("screen observation = %#v", fromScreen)
	}

	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "UserPromptSubmit", "session_id": "provider-id", "permission_mode": "default",
	})
	fromStructured, err := service.Observe(context.Background(), "claude", "session", screen, process)
	if err != nil {
		t.Fatal(err)
	}
	if fromStructured.State != StateWorking || fromStructured.Evidence.Source != SourceStructured {
		t.Fatalf("structured observation = %#v", fromStructured)
	}

	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "PermissionRequest", "tool_name": "AskUserQuestion",
	})
	waiting, err := service.Observe(context.Background(), "claude", "session", screen, process)
	if err != nil {
		t.Fatal(err)
	}
	if waiting.State != StateWaitingInput || waiting.Evidence.Rule != "PermissionRequest" {
		t.Fatalf("waiting observation = %#v", waiting)
	}

	exitCode := 9
	failed, err := service.Observe(context.Background(), "claude", "session", screen, ProcessEvidence{
		State: ProcessExited, ExitCode: &exitCode, Detail: "child exited with code 9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.State != StateFailed || failed.Evidence.Source != SourceProcess {
		t.Fatalf("failed observation = %#v", failed)
	}

	transitions, err := service.Transitions("session")
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 4 {
		t.Fatalf("transition count = %d, want 4: %#v", len(transitions), transitions)
	}
	if _, err := service.Observe(context.Background(), "claude", "session", screen, ProcessEvidence{
		State: ProcessExited, ExitCode: &exitCode, Detail: "child exited with code 9",
	}); err != nil {
		t.Fatal(err)
	}
	transitions, err = service.Transitions("session")
	if err != nil {
		t.Fatal(err)
	}
	if len(transitions) != 4 {
		t.Fatalf("unchanged observation added a transition: %#v", transitions)
	}
}

func TestScreenFallbackUsesNewestDeclarativeMatch(t *testing.T) {
	observedAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	observation, ok := screenObservation("codex", []byte("› old prompt\n\x1b[32mWorking...\x1b[0m esc to interrupt"), observedAt)
	if !ok {
		t.Fatal("screen produced no observation")
	}
	if observation.State != StateWorking || observation.Evidence.Rule != "working_indicator" {
		t.Fatalf("screen observation = %#v", observation)
	}
	if !strings.Contains(string(observation.Evidence.Raw), "esc to interrupt") {
		t.Fatalf("screen evidence = %s", observation.Evidence.Raw)
	}
}

func TestScreenFallbackRecognizesClaudeDecisionDialogs(t *testing.T) {
	observedAt := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	screen := []byte("Quick safety check: Is this a project you trust?\n❯ 1. Yes, I trust this folder\nEnter to confirm · Esc to cancel")
	observation, ok := screenObservation("claude", screen, observedAt)
	if !ok {
		t.Fatal("screen produced no observation")
	}
	if observation.State != StateWaitingPermission || observation.Evidence.Rule != "permission_prompt" {
		t.Fatalf("screen observation = %#v", observation)
	}
}

func TestClaudeStructuredMappings(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    State
	}{
		{name: "working", payload: `{"hook_event_name":"PreToolUse","tool_name":"Bash"}`, want: StateWorking},
		{name: "permission", payload: `{"hook_event_name":"PermissionRequest","tool_name":"Bash"}`, want: StateWaitingPermission},
		{name: "input", payload: `{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion"}`, want: StateWaitingInput},
		{name: "idle", payload: `{"hook_event_name":"Notification","notification_type":"idle_prompt"}`, want: StateIdle},
		{name: "complete", payload: `{"hook_event_name":"SessionEnd"}`, want: StateCompleted},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, ok := structuredObservation("claude", storedSignal{
				Provider: "claude", ReceivedAt: time.Now(), Payload: json.RawMessage(test.payload),
			})
			if !ok || observation.State != test.want {
				t.Fatalf("structured observation = %#v, ok=%t", observation, ok)
			}
		})
	}
}

func TestPrepareClaudeAddsPrivateHookSettings(t *testing.T) {
	stateDir := t.TempDir()
	service, err := New(stateDir, "/path with spaces/atc-zmx", nil)
	if err != nil {
		t.Fatal(err)
	}
	command, err := service.Prepare("claude", "session-id", []string{"claude", "--model", "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if len(command) != 5 || command[0] != "claude" || command[1] != "--settings" {
		t.Fatalf("prepared command = %#v", command)
	}
	contents, err := os.ReadFile(command[2])
	if err != nil {
		t.Fatal(err)
	}
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Type    string `json:"type"`
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	if err := json.Unmarshal(contents, &settings); err != nil {
		t.Fatal(err)
	}
	hook := settings.Hooks["PermissionRequest"][0].Hooks[0]
	if hook.Type != "command" || !strings.Contains(hook.Command, "'/path with spaces/atc-zmx'") ||
		!strings.Contains(hook.Command, "--id 'session-id'") {
		t.Fatalf("hook = %#v", hook)
	}
	if filepath.Dir(command[2]) != filepath.Join(stateDir, "agent-status", "session-id") {
		t.Fatalf("settings path = %s", command[2])
	}
}

func TestRecordHookRejectsUnsafeSessionID(t *testing.T) {
	err := RecordHook(t.TempDir(), "../escape", "claude", strings.NewReader(`{"hook_event_name":"Stop"}`))
	if err == nil {
		t.Fatal("RecordHook accepted a path-traversal session id")
	}
}

func recordTestHook(t *testing.T, stateDir, sessionID string, payload map[string]any) {
	t.Helper()
	contents, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordHook(stateDir, sessionID, "claude", strings.NewReader(string(contents))); err != nil {
		t.Fatal(err)
	}
}
