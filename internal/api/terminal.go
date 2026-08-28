package api

import "time"

// TerminalStatus is the server-owned, honestly-derived state of a terminal
// (ATC-251). There is no stale state and no timer-based promotion.
type TerminalStatus string

const (
	// TerminalRunning: present in a complete zmx inventory and reachable.
	TerminalRunning TerminalStatus = "running"
	// TerminalExited: absent with recorded exit evidence.
	TerminalExited TerminalStatus = "exited"
	// TerminalUnreachable: present but unresponsive, or liveness cannot
	// currently be verified (inventory failure, create still settling).
	TerminalUnreachable TerminalStatus = "unreachable"
	// TerminalMissing: absent without exit evidence — vanished, cause
	// unknown. Deliberately permanent until the user deletes the record.
	TerminalMissing TerminalStatus = "missing"
)

// Terminal is the /v1/terminals resource. The id doubles as the zmx
// session name; records stay listed after exit until explicitly deleted.
type Terminal struct {
	ID        string         `json:"id" doc:"Server-minted identifier; also the zmx session name."`
	Name      string         `json:"name" doc:"Display name; the only mutable field."`
	Directory string         `json:"directory" doc:"Working directory the session started in. Immutable."`
	App       string         `json:"app,omitempty" doc:"Command launched in the session; empty means a plain shell. Immutable."`
	Status    TerminalStatus `json:"status" enum:"running,exited,unreachable,missing" doc:"Server-owned session state."`
	ExitCode  *int           `json:"exitCode,omitempty" doc:"Recorded exit evidence: exit code, 128+signal for signal deaths, 127 for launch failure. Omitted while running and for ATC-initiated stops."`
	CreatedAt time.Time      `json:"createdAt"`
	UpdatedAt time.Time      `json:"updatedAt"`
}

// TerminalCreateParams is the POST /v1/terminals request body. Every field
// is optional.
type TerminalCreateParams struct {
	Name      string `json:"name,omitempty" doc:"Display name; defaults from app, else \"Shell\"."`
	Directory string `json:"directory,omitempty" doc:"Working directory; defaults to the server user's home."`
	App       string `json:"app,omitempty" doc:"Free-form command run through the user's shell; empty starts a plain interactive shell."`
}

// TerminalUpdateParams is the PATCH /v1/terminals/{id} request body. Name
// is the only mutable field; unknown or immutable fields are rejected.
type TerminalUpdateParams struct {
	Name string `json:"name" minLength:"1" doc:"New display name."`
}

// TerminalList is the GET /v1/terminals response body.
type TerminalList struct {
	Terminals []Terminal `json:"terminals"`
}
