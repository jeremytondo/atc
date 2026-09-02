package api

// The /v1/events SSE feed (ATC-247 §2): small numbered events saying what
// changed, never the state itself — clients refetch the changed resource.
// SSE event names are the ATC-wide change vocabulary; each name carries a
// ChangeEvent payload. Reconnection is standard Last-Event-ID catch-up
// against an in-memory backlog; a cursor that has fallen off the backlog
// gets a "resync" event and refetches snapshots once. Comment lines are
// heartbeats.
const (
	EventTerminalCreated = "terminal.created"
	EventTerminalUpdated = "terminal.updated"
	EventTerminalDeleted = "terminal.deleted"
	EventProjectCreated  = "project.created"
	EventProjectUpdated  = "project.updated"
	EventProjectDeleted  = "project.deleted"
	EventThreadCreated   = "thread.created"
	EventThreadUpdated   = "thread.updated"
	EventThreadDeleted   = "thread.deleted"
	// EventAgentAdapterUpdated fires when an observing adapter's connection
	// state changes (ATC-285) — on transitions only, never per reconnect
	// attempt.
	EventAgentAdapterUpdated = "agent_adapter.updated"
	EventResync              = "resync"
)

// ChangeEvent is the payload of every change event: what changed, by kind
// and ID. Status changes arrive as updated; deleted is distinct because
// there is nothing left to refetch.
type ChangeEvent struct {
	Seq      uint64 `json:"seq" doc:"Monotonic position in this server run's stream; also the SSE event id."`
	Resource string `json:"resource" doc:"Resource kind, e.g. \"terminal\"."`
	ID       string `json:"id" doc:"Identifier of the changed resource."`
}

// TerminalCreatedEvent is the terminal.created payload.
type TerminalCreatedEvent struct{ ChangeEvent }

// TerminalUpdatedEvent is the terminal.updated payload.
type TerminalUpdatedEvent struct{ ChangeEvent }

// TerminalDeletedEvent is the terminal.deleted payload.
type TerminalDeletedEvent struct{ ChangeEvent }

// ProjectCreatedEvent is the project.created payload.
type ProjectCreatedEvent struct{ ChangeEvent }

// ProjectUpdatedEvent is the project.updated payload.
type ProjectUpdatedEvent struct{ ChangeEvent }

// ProjectDeletedEvent is the project.deleted payload.
type ProjectDeletedEvent struct{ ChangeEvent }

// ThreadCreatedEvent is the thread.created payload.
type ThreadCreatedEvent struct{ ChangeEvent }

// ThreadUpdatedEvent is the thread.updated payload. It fires only on
// meaningful change (status, terminal linkage, title, archived,
// metadata); a lastEvidenceAt refresh alone updates silently.
type ThreadUpdatedEvent struct{ ChangeEvent }

// ThreadDeletedEvent is the thread.deleted payload.
type ThreadDeletedEvent struct{ ChangeEvent }

// AgentAdapterUpdatedEvent is the agent_adapter.updated payload; the id is
// the adapter's.
type AgentAdapterUpdatedEvent struct{ ChangeEvent }

// ResyncEvent tells a reconnecting client its cursor has fallen off the
// backlog: refetch snapshots once, then resume from the live stream.
type ResyncEvent struct {
	Seq uint64 `json:"seq" doc:"Current head of the stream at resync time."`
}
