// Package terminals is the Terminals domain (ATC-251): the resource
// behind /v1/terminals, its honest four-state status model, and the
// background reconciliation that keeps an in-memory view fresh against
// zmx. The package owns policy; the session backend stays behind the
// small Adapter interface (internal/zmx in production), and durable facts
// live in the ATC-262 store.
//
// Reads are served from the in-memory view and never touch zmx or the
// database synchronously; the database exists to rebuild the view after a
// restart. Records leave the database only through the delete verb — no
// time-based cleanup or auto-forget anywhere.
package terminals

import (
	"context"
	"time"
)

// Every cadence the domain uses, in one place (spec decision). No jitter,
// no backoff: an inventory failure leaves statuses unreachable and the
// loop keeps its cadence; the next good pass heals them.
const (
	// ReconcileInterval is the flat background reconciliation cadence.
	ReconcileInterval = 2 * time.Second
	// VerifyInterval is the inventory polling cadence for create and kill
	// verification.
	VerifyInterval = 100 * time.Millisecond
	// VerifyPasses is how many complete inventory passes create and kill
	// verification wait for (~4s at VerifyInterval). Complete passes are
	// counted rather than wall time, so a slow zmx extends the wait
	// instead of manufacturing a failure.
	VerifyPasses = 40
	// VerifyFailureCap bounds consecutive failed inventory attempts during
	// verification — with the inventory down, waiting longer proves
	// nothing and statuses go unreachable.
	VerifyFailureCap = 20
)

// Session is one entry of a complete backend inventory. Only a reachable
// entry proves a live session; only a complete inventory proves absence.
type Session struct {
	Name      string
	Reachable bool
}

// CreateSpec is what the backend needs to start a session.
type CreateSpec struct {
	// Directory the workload starts in (the wrapper chdirs; a bad
	// directory surfaces as launch-failure evidence, not a create error).
	Directory string
	// Command is the free-form command run through the user's shell; empty
	// starts a plain interactive login shell.
	Command string
}

// Adapter is the session backend seam. Implementations own every backend
// detail — commands, inventory parsing, environment traps, attach
// mechanics — and start each session with the ATC wrapper as its root
// task.
type Adapter interface {
	// Inventory returns the complete session inventory. An error means the
	// inventory is unavailable — never "empty".
	Inventory(ctx context.Context) ([]Session, error)
	// Create starts the session named id. The caller has already persisted
	// the record; an error does not prove the session was never born.
	Create(ctx context.Context, id string, spec CreateSpec) error
	// Kill terminates the session best-effort and verifies absence by
	// polling the inventory. A session that is already absent is success.
	Kill(ctx context.Context, id string) error
}
