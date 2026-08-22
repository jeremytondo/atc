package agentstatus

import (
	"encoding/json"
	"time"
)

type claudeEvent struct {
	HookEvent      string `json:"hook_event_name"`
	Notification   string `json:"notification_type"`
	ToolName       string `json:"tool_name"`
	PermissionMode string `json:"permission_mode"`
	SessionID      string `json:"session_id"`
	PromptID       string `json:"prompt_id"`
}

type reducedObservation struct {
	observation Observation
	promptID    string
}

func reduceStructured(kind string, signals []storedSignal) (Observation, bool) {
	if kind != "claude" {
		return Observation{}, false
	}
	var current reducedObservation
	found := false
	for _, signal := range signals {
		if signal.Provider != kind {
			continue
		}
		var event claudeEvent
		if json.Unmarshal(signal.Payload, &event) != nil {
			continue
		}
		state, recognized := reduceClaudeState(event, current)
		if !recognized {
			continue
		}
		current = reducedObservation{
			observation: claudeObservation(kind, signal, event, state),
			promptID:    event.PromptID,
		}
		found = true
	}
	return current.observation, found
}

func reduceClaudeState(event claudeEvent, current reducedObservation) (State, bool) {
	switch event.HookEvent {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		return StateWorking, true
	case "PermissionRequest":
		if event.ToolName == "AskUserQuestion" {
			return StateWaitingInput, true
		}
		return StateWaitingPermission, true
	case "Notification":
		return reduceClaudeNotification(event, current)
	case "Stop", "StopFailure":
		return StateIdle, true
	case "SessionEnd":
		return StateCompleted, true
	default:
		return "", false
	}
}

func reduceClaudeNotification(event claudeEvent, current reducedObservation) (State, bool) {
	samePrompt := event.PromptID != "" && event.PromptID == current.promptID
	pending := current.observation.State == StateWaitingInput ||
		current.observation.State == StateWaitingPermission
	switch event.Notification {
	case "permission_prompt":
		if samePrompt && pending {
			return "", false
		}
		return StateWaitingPermission, true
	case "elicitation_dialog", "agent_needs_input":
		return StateWaitingInput, true
	case "idle_prompt":
		if samePrompt && pending {
			return "", false
		}
		return StateIdle, true
	default:
		return "", false
	}
}

func claudeObservation(kind string, signal storedSignal, event claudeEvent, state State) Observation {
	rule := event.HookEvent
	if event.Notification != "" {
		rule += "." + event.Notification
	}
	detail := event.HookEvent
	if event.Notification != "" {
		detail += " notification=" + event.Notification
	}
	if event.ToolName != "" {
		detail += " tool=" + event.ToolName
	}
	if event.PermissionMode != "" {
		detail += " permission_mode=" + event.PermissionMode
	}
	if event.SessionID != "" {
		detail += " session_id=" + event.SessionID
	}
	return Observation{
		Provider: kind, State: state, ObservedAt: signal.ReceivedAt,
		Evidence: Evidence{Source: SourceStructured, Rule: rule, Detail: detail, Raw: signal.Payload},
	}
}

func processObservation(kind string, process ProcessEvidence, observedAt time.Time) Observation {
	state := StateUnavailable
	rule := string(process.State)
	switch process.State {
	case ProcessRunning:
		state = StateUnknown
	case ProcessExited:
		state = StateCompleted
		if process.ExitCode != nil && *process.ExitCode != 0 {
			state = StateFailed
		}
	}
	raw, _ := json.Marshal(map[string]any{"state": process.State, "exitCode": process.ExitCode})
	return Observation{
		Provider: kind, State: state, ObservedAt: observedAt,
		Evidence: Evidence{Source: SourceProcess, Rule: rule, Detail: process.Detail, Raw: raw},
	}
}

func isSupportedProvider(kind string) bool {
	return kind == "claude" || kind == "codex"
}
