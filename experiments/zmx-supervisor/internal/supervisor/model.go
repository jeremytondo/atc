// Package supervisor owns ATC-like metadata and reconciliation policy while
// depending only on the small terminal boundary. Persisted exit markers are
// the evidence that distinguishes an exited child from a vanished session;
// absence from zmx inventory alone is never interpreted as a clean exit.
package supervisor

import (
	"time"

	"github.com/elevenideas/atc/experiments/zmx-supervisor/internal/agentstatus"
)

type State string

const (
	StateRunning      State = "running"
	StateExited       State = "exited"
	StateMissing      State = "missing"
	StateDisconnected State = "disconnected"
	StateStale        State = "stale"
)

type Record struct {
	ID              string     `json:"id"`
	Name            string     `json:"name"`
	ZmxName         string     `json:"zmxName"`
	Kind            string     `json:"kind"`
	Command         []string   `json:"command"`
	CWD             string     `json:"cwd"`
	State           State      `json:"state"`
	DaemonPID       int        `json:"daemonPid,omitempty"`
	CreatedAt       time.Time  `json:"createdAt"`
	LastSeenAt      *time.Time `json:"lastSeenAt,omitempty"`
	MissingSince    *time.Time `json:"missingSince,omitempty"`
	StopRequestedAt *time.Time `json:"stopRequestedAt,omitempty"`
}

type ExitMarker struct {
	Version   int        `json:"version"`
	SessionID string     `json:"sessionId"`
	PID       int        `json:"pid"`
	StartedAt time.Time  `json:"startedAt"`
	ExitedAt  *time.Time `json:"exitedAt,omitempty"`
	ExitCode  *int       `json:"exitCode,omitempty"`
	Signal    string     `json:"signal,omitempty"`
	Error     string     `json:"error,omitempty"`
}

type Snapshot struct {
	ID               string                   `json:"id,omitempty"`
	Name             string                   `json:"name"`
	ZmxName          string                   `json:"zmxName"`
	Kind             string                   `json:"kind,omitempty"`
	State            State                    `json:"state"`
	Reachable        bool                     `json:"reachable"`
	DaemonPID        int                      `json:"daemonPid,omitempty"`
	Orphan           bool                     `json:"orphan,omitempty"`
	Exit             *ExitMarker              `json:"exit,omitempty"`
	Reason           string                   `json:"reason,omitempty"`
	AgentStatus      *agentstatus.Observation `json:"agentStatus,omitempty"`
	AgentStatusError string                   `json:"agentStatusError,omitempty"`
}

type CreateRequest struct {
	Name    string
	Kind    string
	Command []string
	CWD     string
}

type CleanupResult struct {
	KilledOrphans []string `json:"killedOrphans"`
	Forgotten     []string `json:"forgotten"`
}
