// Package domain defines the provider-neutral resources exposed by the
// prototype. Provider identities are deliberately absent: persistence keeps
// them in private records and diagnostics retains the raw evidence.
package domain

import (
	"encoding/json"
	"time"
)

type ThreadKind string

const (
	ThreadChat ThreadKind = "chat"
	ThreadTUI  ThreadKind = "tui"
)

type Agent string

const (
	AgentClaude Agent = "claude"
	AgentCodex  Agent = "codex"
)

type Activity string

const (
	ActivityIdle       Activity = "idle"
	ActivityWorking    Activity = "working"
	ActivityNeedsInput Activity = "needs_input"
	ActivityUnknown    Activity = "unknown"
)

type TurnState string

const (
	TurnRunning TurnState = "running"
	TurnEnded   TurnState = "ended"
)

type TurnOutcome string

const (
	TurnCompleted   TurnOutcome = "completed"
	TurnInterrupted TurnOutcome = "interrupted"
	TurnFailed      TurnOutcome = "failed"
)

type RequestKind string

const (
	RequestQuestion RequestKind = "question"
	RequestApproval RequestKind = "approval"
)

type TerminalLifecycle string

const (
	TerminalLive  TerminalLifecycle = "live"
	TerminalEnded TerminalLifecycle = "ended"
)

type Thread struct {
	ID           string     `json:"id"`
	Kind         ThreadKind `json:"kind"`
	Agent        Agent      `json:"agent"`
	CWD          string     `json:"cwd"`
	Activity     Activity   `json:"activity"`
	TerminalID   string     `json:"terminalId,omitempty"`
	ActiveTurn   *Turn      `json:"activeTurn,omitempty"`
	LastTurn     *Turn      `json:"lastTurn,omitempty"`
	Background   Activity   `json:"backgroundActivity"`
	PendingCount int        `json:"pendingRequestCount"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}

type Turn struct {
	ID        string      `json:"id"`
	State     TurnState   `json:"state"`
	Outcome   TurnOutcome `json:"outcome,omitempty"`
	StartedAt time.Time   `json:"startedAt"`
	EndedAt   *time.Time  `json:"endedAt,omitempty"`
	Error     string      `json:"error,omitempty"`
}

type PendingRequest struct {
	ID        string          `json:"id"`
	ThreadID  string          `json:"threadId"`
	TurnID    string          `json:"turnId,omitempty"`
	Kind      RequestKind     `json:"kind"`
	Prompt    string          `json:"prompt"`
	Options   []RequestOption `json:"options,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type RequestOption struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

type Terminal struct {
	ID        string            `json:"id"`
	ThreadID  string            `json:"threadId"`
	Lifecycle TerminalLifecycle `json:"lifecycle"`
	Reachable bool              `json:"reachable"`
	Reason    string            `json:"reason,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	EndedAt   *time.Time        `json:"endedAt,omitempty"`
}

type Event struct {
	Sequence  uint64          `json:"sequence"`
	ThreadID  string          `json:"threadId,omitempty"`
	TurnID    string          `json:"turnId,omitempty"`
	Resource  string          `json:"resource"`
	Type      string          `json:"type"`
	Activity  Activity        `json:"activity,omitempty"`
	Turn      *Turn           `json:"turn,omitempty"`
	Request   *PendingRequest `json:"request,omitempty"`
	Terminal  *Terminal       `json:"terminal,omitempty"`
	Text      string          `json:"text,omitempty"`
	CreatedAt time.Time       `json:"createdAt"`
}

type Diagnostic struct {
	Sequence   uint64          `json:"sequence"`
	ThreadID   string          `json:"threadId,omitempty"`
	Layer      string          `json:"layer"`
	Kind       string          `json:"kind"`
	Raw        json.RawMessage `json:"raw,omitempty"`
	Normalized *Event          `json:"normalized,omitempty"`
	CreatedAt  time.Time       `json:"createdAt"`
}

// CombineActivity keeps foreground, pending-input, and agent-owned background
// work independent, then computes the public projection by strict precedence.
func CombineActivity(foreground, background Activity, pending bool) Activity {
	if pending {
		return ActivityNeedsInput
	}
	for _, candidate := range []Activity{foreground, background} {
		if candidate == ActivityWorking {
			return ActivityWorking
		}
	}
	for _, candidate := range []Activity{foreground, background} {
		if candidate == ActivityUnknown {
			return ActivityUnknown
		}
	}
	return ActivityIdle
}

type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string { return e.Message }

func NewError(code, message string) *Error { return &Error{Code: code, Message: message} }
