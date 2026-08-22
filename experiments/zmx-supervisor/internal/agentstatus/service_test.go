package agentstatus

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClaudeReducerPreservesSpecificPendingInput(t *testing.T) {
	stateDir := t.TempDir()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	service, err := New(stateDir, "/tmp/atc-zmx", func() time.Time { return clock })
	if err != nil {
		t.Fatal(err)
	}
	process := ProcessEvidence{State: ProcessRunning, Detail: "zmx process is running"}

	initial := observeTestStatus(t, service, "claude", "session", process)
	if initial.State != StateUnknown || initial.Evidence.Source != SourceProcess {
		t.Fatalf("initial observation = %#v", initial)
	}

	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "UserPromptSubmit", "prompt_id": "prompt-1",
		"session_id": "provider-id", "permission_mode": "plan",
	})
	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "PermissionRequest", "prompt_id": "prompt-1",
		"tool_name": "AskUserQuestion",
	})
	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "Notification", "prompt_id": "prompt-1",
		"notification_type": "permission_prompt",
	})
	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "Notification", "prompt_id": "prompt-1",
		"notification_type": "idle_prompt",
	})

	waiting := observeTestStatus(t, service, "claude", "session", process)
	if waiting.State != StateWaitingInput || waiting.Evidence.Rule != "PermissionRequest" {
		t.Fatalf("waiting observation = %#v", waiting)
	}

	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "PostToolUse", "prompt_id": "prompt-1",
		"tool_name": "AskUserQuestion",
	})
	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "Stop", "prompt_id": "prompt-1",
	})

	idle := observeTestStatus(t, service, "claude", "session", process)
	if idle.State != StateIdle || idle.Evidence.Rule != "Stop" {
		t.Fatalf("idle observation = %#v", idle)
	}
	assertTransitionStates(t, service, "session", []State{
		StateUnknown, StateWorking, StateWaitingInput, StateWorking, StateIdle,
	})
}

func TestClaudeReducerPreservesSpecificPermissionRequest(t *testing.T) {
	stateDir := t.TempDir()
	service, err := New(stateDir, "/tmp/atc-zmx", nil)
	if err != nil {
		t.Fatal(err)
	}
	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "UserPromptSubmit", "prompt_id": "prompt-1",
	})
	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "PermissionRequest", "prompt_id": "prompt-1", "tool_name": "Bash",
	})
	recordTestHook(t, stateDir, "session", map[string]any{
		"hook_event_name": "Notification", "prompt_id": "prompt-1",
		"notification_type": "permission_prompt",
	})

	observation := observeTestStatus(t, service, "claude", "session", ProcessEvidence{State: ProcessRunning})
	if observation.State != StateWaitingPermission || observation.Evidence.Rule != "PermissionRequest" {
		t.Fatalf("permission observation = %#v", observation)
	}
	assertTransitionStates(t, service, "session", []State{StateWorking, StateWaitingPermission})
}

func TestProcessExitOverridesStructuredEvidence(t *testing.T) {
	stateDir := t.TempDir()
	service, err := New(stateDir, "/tmp/atc-zmx", nil)
	if err != nil {
		t.Fatal(err)
	}
	recordTestHook(t, stateDir, "session", map[string]any{"hook_event_name": "UserPromptSubmit"})
	exitCode := 9
	failed := observeTestStatus(t, service, "claude", "session", ProcessEvidence{
		State: ProcessExited, ExitCode: &exitCode, Detail: "child exited with code 9",
	})
	if failed.State != StateFailed || failed.Evidence.Source != SourceProcess {
		t.Fatalf("failed observation = %#v", failed)
	}
	observeTestStatus(t, service, "claude", "session", ProcessEvidence{
		State: ProcessExited, ExitCode: &exitCode, Detail: "child exited with code 9",
	})
	assertTransitionStates(t, service, "session", []State{StateWorking, StateFailed})
}

func TestCodexRunningStateRemainsUnknownWithoutStructuredAdapter(t *testing.T) {
	service, err := New(t.TempDir(), "/tmp/atc-zmx", nil)
	if err != nil {
		t.Fatal(err)
	}
	running := observeTestStatus(t, service, "codex", "session", ProcessEvidence{State: ProcessRunning})
	if running.State != StateUnknown || running.Evidence.Source != SourceProcess {
		t.Fatalf("running observation = %#v", running)
	}
	exitCode := 0
	completed := observeTestStatus(t, service, "codex", "session", ProcessEvidence{
		State: ProcessExited, ExitCode: &exitCode,
	})
	if completed.State != StateCompleted || completed.Evidence.Source != SourceProcess {
		t.Fatalf("completed observation = %#v", completed)
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
			observation, ok := reduceStructured("claude", []storedSignal{{
				Provider: "claude", ReceivedAt: time.Now(), Payload: json.RawMessage(test.payload),
			}})
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

func observeTestStatus(t *testing.T, service *Service, kind, sessionID string, process ProcessEvidence) Observation {
	t.Helper()
	observation, err := service.Observe(kind, sessionID, process)
	if err != nil {
		t.Fatal(err)
	}
	return observation
}

func assertTransitionStates(t *testing.T, service *Service, sessionID string, want []State) {
	t.Helper()
	transitions, err := service.Transitions(sessionID)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]State, len(transitions))
	for index, transition := range transitions {
		got[index] = transition.State
	}
	if len(got) != len(want) {
		t.Fatalf("transition states = %#v, want %#v", got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("transition states = %#v, want %#v", got, want)
		}
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
