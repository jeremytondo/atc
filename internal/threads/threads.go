// Package threads is the Threads domain (ATC-255): the resource behind
// /v1/threads. A thread is one exact provider conversation, observed into
// existence inside an ATC-launched agent TUI — there is no create verb.
// Records persist in the ATC-262 store as the durable index of resumable
// conversations; the private identity mapping (agent, provider
// conversation id) → thread never leaves the server.
//
// The package owns policy: capture and reattach, the six-status model and
// its inactive coercion, archive/delete with their active refusals, and
// the activeThreadId projection onto terminals. Provider observation —
// Claude hooks, the shared Codex app-server — lives with the agents side
// and feeds this service only neutral observations; no provider
// vocabulary enters here. Statuses come from evidence, never guesses:
// unknown means no evidence, and a thread that stops being observed keeps
// idle but coerces the unverifiable live states back to unknown.
package threads

import (
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
// signals (Claude SessionStart, Codex thread adoption). Only session
// observations move a terminal's active thread — delayed status evidence
// never selects a stale conversation.
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
