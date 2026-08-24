// Package agentstatus normalizes provider lifecycle evidence without exposing
// provider protocols to the supervisor. Structured evidence is authoritative
// while a provider process is running; process state remains the conservative
// fallback when no structured adapter is available.
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
