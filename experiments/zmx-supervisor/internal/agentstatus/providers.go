package agentstatus

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type screenRule struct {
	name     string
	state    State
	patterns []string
}

type provider struct {
	screenRules []screenRule
}

var providers = map[string]provider{
	"claude": {
		screenRules: []screenRule{
			{name: "permission_prompt", state: StateWaitingPermission, patterns: []string{"do you want to proceed?", "yes, allow", "permission required", "enter to confirm"}},
			{name: "input_prompt", state: StateWaitingInput, patterns: []string{"select an option", "enter to select", "agent needs input"}},
			{name: "working_indicator", state: StateWorking, patterns: []string{"esc to interrupt", "thinking…", "thinking...", "working…", "working..."}},
			{name: "ready_prompt", state: StateIdle, patterns: []string{"❯"}},
		},
	},
	"codex": {
		screenRules: []screenRule{
			{name: "permission_prompt", state: StateWaitingPermission, patterns: []string{"would you like to run", "press enter to confirm", "approve this command"}},
			{name: "input_prompt", state: StateWaitingInput, patterns: []string{"select an option", "enter to select", "waiting for your input"}},
			{name: "working_indicator", state: StateWorking, patterns: []string{"esc to interrupt", "working…", "working..."}},
			{name: "ready_prompt", state: StateIdle, patterns: []string{"›"}},
		},
	},
}

func structuredObservation(kind string, signal storedSignal) (Observation, bool) {
	if kind != "claude" || signal.Provider != kind {
		return Observation{}, false
	}
	var payload struct {
		HookEvent      string `json:"hook_event_name"`
		Notification   string `json:"notification_type"`
		ToolName       string `json:"tool_name"`
		PermissionMode string `json:"permission_mode"`
		SessionID      string `json:"session_id"`
	}
	if json.Unmarshal(signal.Payload, &payload) != nil {
		return Observation{}, false
	}
	state := State("")
	rule := payload.HookEvent
	switch payload.HookEvent {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		state = StateWorking
	case "PermissionRequest":
		state = StateWaitingPermission
		if payload.ToolName == "AskUserQuestion" {
			state = StateWaitingInput
		}
	case "Notification":
		switch payload.Notification {
		case "permission_prompt":
			state = StateWaitingPermission
		case "elicitation_dialog", "agent_needs_input":
			state = StateWaitingInput
		case "idle_prompt":
			state = StateIdle
		default:
			return Observation{}, false
		}
		rule += "." + payload.Notification
	case "Stop", "StopFailure":
		state = StateIdle
	case "SessionEnd":
		state = StateCompleted
	default:
		return Observation{}, false
	}
	detail := payload.HookEvent
	if payload.Notification != "" {
		detail += " notification=" + payload.Notification
	}
	if payload.ToolName != "" {
		detail += " tool=" + payload.ToolName
	}
	if payload.PermissionMode != "" {
		detail += " permission_mode=" + payload.PermissionMode
	}
	if payload.SessionID != "" {
		detail += " session_id=" + payload.SessionID
	}
	return Observation{
		Provider: kind, State: state, ObservedAt: signal.ReceivedAt,
		Evidence: Evidence{Source: SourceStructured, Rule: rule, Detail: detail, Raw: signal.Payload},
	}, true
}

func screenObservation(kind string, screen []byte, observedAt time.Time) (Observation, bool) {
	definition, ok := providers[kind]
	if !ok || len(screen) == 0 {
		return Observation{}, false
	}
	visible := cleanScreen(string(screen))
	lower := strings.ToLower(visible)
	bestIndex := -1
	var bestRule screenRule
	bestPattern := ""
	for _, rule := range definition.screenRules {
		for _, pattern := range rule.patterns {
			index := strings.LastIndex(lower, strings.ToLower(pattern))
			if index > bestIndex {
				bestIndex = index
				bestRule = rule
				bestPattern = pattern
			}
		}
	}
	if bestIndex < 0 {
		return Observation{}, false
	}
	line := matchingLine(visible, bestIndex)
	raw, _ := json.Marshal(map[string]string{"line": line, "pattern": bestPattern})
	return Observation{
		Provider: kind, State: bestRule.state, ObservedAt: observedAt,
		Evidence: Evidence{
			Source: SourceScreen, Rule: bestRule.name,
			Detail: "matched terminal pattern " + strconv.Quote(bestPattern), Raw: raw,
		},
	}, true
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

var (
	ansiCSI = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiOSC = regexp.MustCompile(`\x1b\][^\x07]*(?:\x07|\x1b\\)`)
)

func cleanScreen(value string) string {
	value = ansiOSC.ReplaceAllString(value, "")
	value = ansiCSI.ReplaceAllString(value, "")
	value = strings.ReplaceAll(value, "\r", "\n")
	return string(bytes.ToValidUTF8([]byte(value), []byte("�")))
}

func matchingLine(screen string, index int) string {
	start := strings.LastIndex(screen[:index], "\n") + 1
	endOffset := strings.Index(screen[index:], "\n")
	end := len(screen)
	if endOffset >= 0 {
		end = index + endOffset
	}
	return strings.TrimSpace(screen[start:end])
}
