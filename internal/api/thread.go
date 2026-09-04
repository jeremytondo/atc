package api

import "time"

// ThreadStatus is what an agent conversation is doing right now
// (ATC-255, ATC-301), derived only from structured provider evidence —
// never guessed, never screen-parsed. Unknown means no evidence. It says
// nothing about how the last turn ended; that is the thread's latestTurn.
type ThreadStatus string

const (
	// ThreadUnknown: no current evidence. Live states coerce here when the
	// thread stops being observed (TUI closed, conversation switched away,
	// server restart).
	ThreadUnknown ThreadStatus = "unknown"
	// ThreadIdle: no foreground turn running, no background work reported,
	// nothing waiting on the user. Idle persists while the thread is
	// inactive — a resumable conversation at rest — and is what a thread
	// whose last turn failed reads once it is otherwise at rest.
	ThreadIdle ThreadStatus = "idle"
	// ThreadWorking: foreground or background work is active, a starting
	// session included. Claude fires no hook on user interrupt, so working
	// can overstay by up to roughly a minute until the next prompt or idle
	// notification (accepted limitation).
	ThreadWorking ThreadStatus = "working"
	// ThreadWaitingForInput: the agent asked the user a question and is
	// blocked on the answer.
	ThreadWaitingForInput ThreadStatus = "waiting_for_input"
	// ThreadWaitingForPermission: the agent is blocked on a permission
	// approval.
	ThreadWaitingForPermission ThreadStatus = "waiting_for_permission"
	// ThreadError: the provider session itself is faulted and cannot take
	// a prompt, with the provider's explanation in statusDetail. A failed
	// turn never produces it.
	ThreadError ThreadStatus = "error"
)

// TurnState is how a thread's latest turn stands: still running, or the
// way it ended. A completed turn is not a completed task.
type TurnState string

const (
	// TurnUnknown: ATC saw the turn but not how it ended — observation
	// stopped, the thread went idle without an end signal, or a new turn
	// replaced it.
	TurnUnknown TurnState = "unknown"
	// TurnRunning: the turn is in progress.
	TurnRunning TurnState = "running"
	// TurnCompleted: the agent ended the turn on its own terms.
	TurnCompleted TurnState = "completed"
	// TurnFailed: anything but a cancellation cut the turn short — a
	// provider error, a refusal, a limit, the session faulting mid-turn.
	TurnFailed TurnState = "failed"
	// TurnInterrupted: a person or host cancelled it, and the provider
	// said so.
	TurnInterrupted TurnState = "interrupted"
)

// ThreadTurn is the most recent execution ATC observed or created on a
// thread. Its id is ATC-minted — the provider's own turn id never appears
// — and is what a caller that submitted a prompt waits on: the thread's
// latestTurn carries that id until a later turn replaces it.
type ThreadTurn struct {
	ID          string     `json:"id" doc:"Server-minted turn identifier (turn-…); the id a submission returned, until a later turn replaces it."`
	State       TurnState  `json:"state" enum:"unknown,running,completed,failed,interrupted" doc:"Whether the turn is running or how it ended; unknown means ATC saw the turn but not its end."`
	StartedAt   time.Time  `json:"startedAt" doc:"When the turn began, as best ATC knows."`
	CompletedAt *time.Time `json:"completedAt,omitempty" doc:"When the turn ended; omitted while running and when the end went unobserved."`
	Error       string     `json:"error,omitempty" doc:"Failure detail from the provider; present only for a failed turn that supplied one."`
	Response    string     `json:"response,omitempty" doc:"The provider's final assistant message for the turn, as produced (Markdown in practice); omitted until known, and always while running."`
}

// Ended reports a turn that is over — completed, failed, or interrupted —
// as opposed to running or unknown.
func (s TurnState) Ended() bool {
	switch s {
	case TurnCompleted, TurnFailed, TurnInterrupted:
		return true
	}
	return false
}

// Thread is the /v1/threads resource: one exact provider conversation,
// observed inside an ATC-launched App, mirrored from an external program
// its Integration observes (ATC-285), or started through ATC in an
// Integration that can create one (ATC-289). Threads persist as the
// durable index of conversations until deleted. The provider's own
// conversation id never appears here; the ATC thread id is the public
// identity.
type Thread struct {
	ID            string `json:"id" doc:"Server-minted identifier; the public identity of the conversation."`
	IntegrationID string `json:"integrationId" doc:"Integration that produced the thread; the namespace of its private provider identity. Immutable."`
	AppID         string `json:"appId,omitempty" doc:"Integration-qualified App (integration/app) the conversation was started in, when reliably known at creation; omitted permanently otherwise. Immutable."`
	AgentID       string `json:"agentId,omitempty" doc:"Integration-scoped agent id the conversation runs under, as the Integration reports it; may be empty and may change."`
	// ProjectID is classified from InitialDirectory when the thread is
	// first observed (the most specific project containing it), backfilled
	// when a project appears or moves, and editable; never overwritten by
	// classification once set.
	ProjectID        string `json:"projectId,omitempty" doc:"Project the thread belongs to: the most specific project containing its initial directory at first observation, backfilled when projects are created or moved, or set explicitly. Omitted when none. Cleared when the project is deleted."`
	InitialDirectory string `json:"initialDirectory,omitempty" doc:"Canonical directory the conversation reliably originated in, as the Integration reported it at first observation; omitted when no usable local directory was reported. Immutable — later resumes never change it."`
	TerminalID       string `json:"terminalId,omitempty" doc:"Terminal currently or most recently observed hosting the conversation; omitted once that terminal is deleted, and always for threads an external program owns. Runtime evidence, not ownership: whether the conversation is open right now is the terminal's activeThreadId."`
	Title            string `json:"title,omitempty" doc:"Display title: user-editable, with an observed default. Once set through ATC, observation never overwrites it."`
	Model            string `json:"model,omitempty" doc:"Observed model, best-effort."`
	Effort           string `json:"effort,omitempty" doc:"Observed reasoning effort, best-effort."`
	Cwd              string `json:"cwd,omitempty" doc:"Provider-reported current working directory, best-effort and mutable; a resumed conversation can run from a different directory than it originated in."`
	// PermissionMode is provider-native and read-only; ATC imposes no
	// normalized vocabulary on it.
	PermissionMode string       `json:"permissionMode,omitempty" doc:"Provider-native permission mode string, read-only."`
	Status         ThreadStatus `json:"status" enum:"unknown,idle,working,waiting_for_input,waiting_for_permission,error" doc:"What the agent is doing right now, derived from provider evidence; unknown means no evidence. Says nothing about how the last turn ended — see latestTurn."`
	StatusDetail   string       `json:"statusDetail,omitempty" doc:"The provider's own explanation of a faulted session; present only while status is error."`
	LatestTurn     *ThreadTurn  `json:"latestTurn,omitempty" doc:"The most recent turn ATC observed or created on the thread; omitted until there is one."`
	LastEvidenceAt *time.Time   `json:"lastEvidenceAt,omitempty" doc:"When the most recent provider evidence for this thread arrived."`
	Links          *ThreadLinks `json:"links,omitempty" doc:"Where the conversation opens in the program that owns it; present only for threads an external program owns."`
	Archived       bool         `json:"archived" doc:"Reversible soft-hide; archived threads are excluded from lists unless requested. Observing the conversation open again (resumed inside the TUI, or reported again by its program) unarchives it."`
	ArchivedAt     *time.Time   `json:"archivedAt,omitempty" doc:"When the thread was archived; server-managed."`
	CreatedAt      time.Time    `json:"createdAt"`
	UpdatedAt      time.Time    `json:"updatedAt"`
}

// ThreadLinks are the deep links into the external program that owns a
// thread — its handoff Apps — derived from the Integration's live
// connection at read time.
type ThreadLinks struct {
	Web string `json:"web" doc:"URL that opens the conversation in the program's web UI."`
	App string `json:"app" doc:"URL that opens the conversation in the program's desktop app."`
}

// ThreadUpdateParams is the PATCH /v1/threads/{id} request body, a JSON
// Merge Patch: omitted fields are unchanged. Title, archived, and
// projectId are the mutable fields; archive/unarchive is a PATCH of
// archived, with no custom action routes.
type ThreadUpdateParams struct {
	Title     Optional[string] `json:"title,omitzero" minLength:"1" nullable:"false" doc:"New display title. Observation never overwrites a title set here."`
	Archived  Optional[bool]   `json:"archived,omitzero" nullable:"false" doc:"true archives the thread, false unarchives it. Archiving an active thread — one a terminal has open, or one its external program still reports — is refused."`
	ProjectID Optional[string] `json:"projectId,omitzero" minLength:"1" doc:"Project to assign, any project regardless of directory; null clears the association, leaving the thread unassigned until a project is created or moved to contain its initial directory."`
}

// ThreadCreateParams is the POST /v1/threads request body (ATC-289): start
// a new conversation with its first prompt in the named Integration's
// program. Model and options are opaque to ATC — copied to the
// Integration untouched, judged only by the program that runs them.
type ThreadCreateParams struct {
	IntegrationID string         `json:"integrationId" doc:"Integration that runs the work; it must support thread creation (t3code)."`
	Agent         string         `json:"agent" doc:"Integration-scoped agent id, one the Integration lists."`
	ProjectID     string         `json:"projectId" doc:"Project the conversation runs in; its directory must be registered in the Integration's program."`
	Prompt        string         `json:"prompt" doc:"The first user message. Must be non-empty after trimming."`
	Model         string         `json:"model" doc:"Model identifier, passed to the Integration untouched and never validated by ATC."`
	Options       []ThreadOption `json:"options,omitempty" doc:"Provider option pairs passed to the Integration untouched (Codex's reasoningEffort, for example)."`
}

// ThreadOption is one opaque provider knob on a thread create.
type ThreadOption struct {
	ID    string `json:"id" doc:"Option id as the provider names it."`
	Value string `json:"value" doc:"Option value, passed through as a string."`
}

// ThreadList is the GET /v1/threads response body.
type ThreadList struct {
	Threads []Thread `json:"threads"`
}
