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
	ID        string `json:"id" doc:"Server-minted identifier; also the zmx session name."`
	Name      string `json:"name" doc:"Display name; the only mutable field."`
	ProjectID string `json:"projectId" doc:"Project the terminal belongs to. Immutable; terminals never move between projects."`
	Directory string `json:"directory" doc:"Working directory the session started in: the project's directory, or for a terminal resuming a conversation that conversation's recorded one. Immutable."`
	Command   string `json:"command,omitempty" doc:"User-supplied command launched in the session; empty means a plain shell or an App launch (an App's resolved command is Integration-private and never exposed). Immutable."`
	AppID     string `json:"appId,omitempty" doc:"Integration-qualified App id (integration/app) the terminal was launched with; omitted for plain terminals. Server-set launch intent only, immutable, no liveness meaning."`
	// ActiveThreadID is a projection from the threads domain (ATC-255),
	// not terminal state: the terminals domain never sets it.
	ActiveThreadID string         `json:"activeThreadId,omitempty" doc:"Thread whose conversation is currently open in this terminal; omitted when no conversation is observed."`
	Status         TerminalStatus `json:"status" enum:"running,exited,unreachable,missing" doc:"Server-owned session state."`
	ExitCode       *int           `json:"exitCode,omitempty" doc:"Recorded exit evidence: exit code, 128+signal for signal deaths, 127 for launch failure. Omitted while running and for ATC-initiated stops."`
	CreatedAt      time.Time      `json:"createdAt"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

// TerminalCreateParams is the POST /v1/terminals request body. The project
// is required (ATC-256) and supplies the working directory; everything
// else is optional.
type TerminalCreateParams struct {
	ProjectID string `json:"projectId" minLength:"1" doc:"Project the terminal belongs to; its directory becomes the terminal's working directory."`
	Name      string `json:"name,omitempty" doc:"Display name; defaults from command, else \"Shell\"."`
	Command   string `json:"command,omitempty" doc:"Free-form command run through the user's shell; empty starts a plain interactive shell. Mutually exclusive with appId."`
	AppID     string `json:"appId,omitempty" doc:"Integration-qualified App id (integration/app) to launch; the Integration privately composes the command and the id is recorded on the terminal. Mutually exclusive with command."`
}

// TerminalUpdateParams is the PATCH /v1/terminals/{id} request body, a
// JSON Merge Patch: an omitted field is unchanged. Name is the only
// mutable field; it does not accept null.
type TerminalUpdateParams struct {
	Name Optional[string] `json:"name,omitzero" minLength:"1" nullable:"false" doc:"New display name."`
}

// TerminalList is the GET /v1/terminals response body.
type TerminalList struct {
	Terminals []Terminal `json:"terminals"`
}
