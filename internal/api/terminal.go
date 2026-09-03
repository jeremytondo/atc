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
// A terminal belongs to exactly one space (ATC-296) and never to a
// project.
type Terminal struct {
	ID        string `json:"id" doc:"Server-minted identifier; also the zmx session name."`
	Name      string `json:"name" doc:"Display name; mutable."`
	SpaceID   string `json:"spaceId" doc:"Space the terminal belongs to; mutable — moving a terminal changes nothing but this."`
	Directory string `json:"directory" doc:"Working directory the session started in: the one supplied at creation, else the space's directory at that moment — never a thread's own directory. Immutable."`
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

// TerminalCreateParams is the POST /v1/terminals request body — the one
// launch surface (ATC-297): a plain shell, a command, an App, or a
// thread resumed through the App that started it. command, appId, and
// threadId are mutually exclusive. Placement is optional in every mode:
// the space defaults to the Default space, the directory to the space's,
// the name to the directory's basename.
type TerminalCreateParams struct {
	SpaceID   string `json:"spaceId,omitempty" doc:"Space the terminal belongs to; defaults to the Default space."`
	Directory string `json:"directory,omitempty" doc:"Working directory the session starts in; defaults to the space's directory. Must exist on the server's machine."`
	Name      string `json:"name,omitempty" doc:"Display name; defaults to the basename of the resolved directory."`
	Command   string `json:"command,omitempty" doc:"Free-form command run through the user's shell; empty starts a plain interactive shell. Mutually exclusive with appId and threadId."`
	AppID     string `json:"appId,omitempty" doc:"Integration-qualified App id (integration/app) to launch; the Integration privately composes the command and the id is recorded on the terminal. Mutually exclusive with command and threadId."`
	ThreadID  string `json:"threadId,omitempty" doc:"Thread to resume through the App that started it. A terminal already running (or unreachable) for the thread is reused and returned with 200; otherwise a new terminal runs the exact resume and is returned with 201. Placement options are ignored on reuse. Mutually exclusive with command and appId."`
}

// TerminalUpdateParams is the PATCH /v1/terminals/{id} request body, a
// JSON Merge Patch: an omitted field is unchanged; neither field accepts
// null.
type TerminalUpdateParams struct {
	Name    Optional[string] `json:"name,omitzero" minLength:"1" nullable:"false" doc:"New display name."`
	SpaceID Optional[string] `json:"spaceId,omitzero" minLength:"1" nullable:"false" doc:"Space to move the terminal to. The session, directory, app, and thread are untouched."`
}

// TerminalList is the GET /v1/terminals response body.
type TerminalList struct {
	Terminals []Terminal `json:"terminals"`
}
