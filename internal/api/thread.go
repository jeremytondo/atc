package api

import "time"

// ThreadStatus is the server-owned state of an agent conversation
// (ATC-255), derived only from structured provider evidence — never
// guessed, never screen-parsed. Unknown means no evidence.
type ThreadStatus string

const (
	// ThreadUnknown: no current evidence. Live states coerce here when the
	// thread stops being observed (TUI closed, conversation switched away,
	// server restart).
	ThreadUnknown ThreadStatus = "unknown"
	// ThreadIdle: the agent finished its last turn. Idle persists while
	// the thread is inactive — a resumable conversation at rest.
	ThreadIdle ThreadStatus = "idle"
	// ThreadWorking: the agent is doing work right now. Claude fires no
	// hook on user interrupt, so working can overstay by up to roughly a
	// minute until the next prompt or idle notification (accepted
	// limitation).
	ThreadWorking ThreadStatus = "working"
	// ThreadWaitingForInput: the agent asked the user a question and is
	// blocked on the answer.
	ThreadWaitingForInput ThreadStatus = "waiting_for_input"
	// ThreadWaitingForPermission: the agent is blocked on a permission
	// approval.
	ThreadWaitingForPermission ThreadStatus = "waiting_for_permission"
	// ThreadError: the thread itself is faulted. A merely failed turn is
	// idle with the detail in lastError.
	ThreadError ThreadStatus = "error"
)

// Thread is the /v1/threads resource: one exact provider conversation,
// observed inside an ATC-launched App or mirrored from an external
// program its Integration observes (ATC-285). Threads are observed into
// existence — there is no create verb — and persist as the durable index
// of conversations until deleted. The provider's own conversation id
// never appears here; the ATC thread id is the public identity.
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
	Status         ThreadStatus `json:"status" enum:"unknown,idle,working,waiting_for_input,waiting_for_permission,error" doc:"What the agent is doing right now, derived from provider evidence; unknown means no evidence."`
	LastError      string       `json:"lastError,omitempty" doc:"Detail of the most recent failed turn; the thread itself stays idle."`
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

// ThreadList is the GET /v1/threads response body.
type ThreadList struct {
	Threads []Thread `json:"threads"`
}
