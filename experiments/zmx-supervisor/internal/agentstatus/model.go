// Package agentstatus normalizes provider-specific TUI evidence without
// exposing provider protocols or screen heuristics to the supervisor. Exactly
// one source is authoritative for an observation: structured lifecycle
// evidence first, then the terminal screen, then process state.
package agentstatus

import (
	"encoding/json"
	"time"
)

type State string

const (
	StateWorking           State = "working"
	StateIdle              State = "idle"
	StateWaitingInput      State = "waiting_for_input"
	StateWaitingPermission State = "waiting_for_permission"
	StateCompleted         State = "completed"
	StateFailed            State = "failed"
	StateUnknown           State = "unknown"
	StateUnavailable       State = "unavailable"
)

type Source string

const (
	SourceStructured Source = "structured"
	SourceScreen     Source = "screen"
	SourceProcess    Source = "process"
)

type Evidence struct {
	Source Source          `json:"source"`
	Rule   string          `json:"rule"`
	Detail string          `json:"detail"`
	Raw    json.RawMessage `json:"raw"`
}

type Observation struct {
	Provider   string    `json:"provider"`
	State      State     `json:"state"`
	ObservedAt time.Time `json:"observedAt"`
	Evidence   Evidence  `json:"evidence"`
}

type ProcessState string

const (
	ProcessRunning     ProcessState = "running"
	ProcessExited      ProcessState = "exited"
	ProcessUnavailable ProcessState = "unavailable"
)

type ProcessEvidence struct {
	State    ProcessState
	ExitCode *int
	Detail   string
}
