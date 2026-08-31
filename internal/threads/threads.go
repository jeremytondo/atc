// Package threads is the Threads domain (ATC-255): the resource behind
// /v1/threads. A thread is one exact provider conversation, observed into
// existence inside an ATC-launched agent TUI at its first prompt — there
// is no create verb. Open (ATC-282) is the one intent the domain acts on:
// it resolves a thread to the terminal holding it, resuming the
// conversation in a new terminal when nothing does.
// Records persist in the ATC-262 store as the durable index of resumable
// conversations; the private identity mapping (agent, provider
// conversation id) → thread never leaves the server.
//
// The package owns policy: capture and reattach, the six-status model and
// its inactive coercion, archive/delete with their active refusals (and
// the unarchive a reattach implies — active means unarchived), and
// the activeThreadId projection onto terminals. Provider observation —
// the Claude hooks, the Codex app-server observer — lives with the
// agents side and feeds this service only neutral observations; no
// provider vocabulary enters here. Statuses come from evidence, never guesses:
// unknown means no evidence, and a thread that stops being observed keeps
// idle but coerces the unverifiable live states back to unknown.
package threads

import (
	"context"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
)

// SweepInterval is the cadence of the background sweep that notices
// terminals leaving (exited, missing, deleted) and coerces their threads'
// live statuses.
const SweepInterval = 2 * time.Second

const idPrefix = "thrd-"

// randomID mints one candidate ID; the caller collision-checks it against
// the database and re-rolls.
func randomID() string {
	return ids.New(idPrefix)
}

// Metadata is the observed best-effort thread metadata. Empty fields mean
// "not observed this time" and never clear a previously observed value.
type Metadata struct {
	Title          string
	Model          string
	Effort         string
	Cwd            string
	PermissionMode string
}

// SessionObservation reports that a terminal has a provider conversation
// open: the identity transition providers derive from their authoritative
// signals (Claude's SessionStart hook, Codex's launch binding). Only
// session observations move a terminal's active thread — delayed status
// evidence never selects a stale conversation.
type SessionObservation struct {
	// Agent is the catalog id; with ProviderID it forms the private
	// identity key.
	Agent string
	// ProviderID is the provider's own conversation id. It never appears
	// in the public API.
	ProviderID string
	// TerminalID is the observing terminal; ProjectID is copied onto the
	// record at first observation only (immutable afterwards).
	TerminalID string
	ProjectID  string
	// At is when the evidence arrived; zero means now.
	At time.Time
	// Status is the initial status evidence riding on the transition;
	// empty means unknown.
	Status   api.ThreadStatus
	Metadata Metadata
}

// StatusObservation reports fresh evidence about what a known
// conversation's agent is doing. Evidence for an unmapped identity is
// dropped: without a session observation there is no terminal context to
// create an honest record from.
type StatusObservation struct {
	Agent      string
	ProviderID string
	// At is when the evidence arrived; zero means now.
	At     time.Time
	Status api.ThreadStatus
	// LastError sets the thread's lastError detail: nil leaves it alone,
	// a pointer to "" clears it, anything else records it (a failed turn
	// leaves the thread idle with the detail here).
	LastError *string
	Metadata  Metadata
}

// ResumeRequest asks the agents side to launch a terminal running the
// provider's exact resume of a dormant conversation (ATC-282). It carries
// the private identity out of this domain to the adapter that composes
// the command — never onto the wire.
type ResumeRequest struct {
	// Agent is the catalog id; ProviderID the provider's own conversation
	// id, together the private identity key.
	Agent      string
	ProviderID string
	// ProjectID is the thread's project, which the terminal joins.
	ProjectID string
	// Directory is the conversation's last recorded working directory,
	// empty when never observed; the resumer falls back to the project's
	// directory when it no longer exists.
	Directory string
}

// Resumer launches the resume terminal for an open decision
// (agents.Service.Resume in production). Open invokes it for at most one
// in-flight resume per thread.
type Resumer func(ctx context.Context, req ResumeRequest) (api.Terminal, error)
