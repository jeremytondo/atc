package t3code

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// The shell projection is T3's lightweight read model of projects and
// threads (no transcripts). Only the fields ATC uses are decoded; unknown
// fields are ignored, and a required field that is missing is a schema
// failure for the whole payload — the Integration reports it rather than
// guess.

// schemaError marks a payload ATC cannot read: permanent for that
// payload, reported as unavailable with the detail.
type schemaError struct{ err error }

func (e *schemaError) Error() string { return "T3 Code shell schema: " + e.err.Error() }
func (e *schemaError) Unwrap() error { return e.err }

func schemaErrorf(format string, args ...any) error {
	return &schemaError{err: fmt.Errorf(format, args...)}
}

// projectShell is what ATC reads of a T3 project: its workspace root is
// the origin of the threads under it. T3's title is not read — ATC
// creates no project from it.
type projectShell struct {
	ID            string `json:"id"`
	WorkspaceRoot string `json:"workspaceRoot"`
}

type threadShell struct {
	ID                  string          `json:"id"`
	ProjectID           string          `json:"projectId"`
	Title               string          `json:"title"`
	ModelSelection      *modelSelection `json:"modelSelection"`
	WorktreePath        nullableString  `json:"worktreePath"`
	Session             *sessionShell   `json:"session"`
	HasPendingApprovals *bool           `json:"hasPendingApprovals"`
	HasPendingUserInput *bool           `json:"hasPendingUserInput"`
	BackgroundLiveness  *string         `json:"backgroundLiveness"`
	// LatestTurn is T3's own latest-turn projection; null before any
	// turn.
	LatestTurn *latestTurnShell `json:"latestTurn"`
	// SettledOverride is T3's own settlement: "settled" threads leave its
	// active list while staying in the shell projection.
	SettledOverride *string `json:"settledOverride"`
}

// settled reports whether T3 has settled the thread — manually or by its
// own policy. ATC never infers settlement from idle status or age.
func (t threadShell) settled() bool {
	return t.SettledOverride != nil && *t.SettledOverride == "settled"
}

type modelSelection struct {
	Model string `json:"model"`
}

type sessionShell struct {
	Status       string  `json:"status"`
	ProviderName *string `json:"providerName"`
	LastError    *string `json:"lastError"`
}

// latestTurnShell is T3's latest turn: its id is the private provider
// turn id, and startedAt is null until the provider picks the turn up,
// so requestedAt stands in for when it began.
type latestTurnShell struct {
	TurnID      string  `json:"turnId"`
	State       string  `json:"state"`
	RequestedAt string  `json:"requestedAt"`
	StartedAt   *string `json:"startedAt"`
	CompletedAt *string `json:"completedAt"`
}

// turnObservation maps T3's latest turn to the thread vocabulary:
// running, completed, and interrupted directly; error is a failed turn
// carrying the session's error text; anything unrecognized is unknown.
// Timestamps were validated with the payload.
func turnObservation(turn *latestTurnShell, sessionError string) *threads.TurnObservation {
	if turn == nil {
		return nil
	}
	o := &threads.TurnObservation{ProviderID: turn.TurnID, StartedAt: isoTime(turn.RequestedAt)}
	if turn.StartedAt != nil {
		o.StartedAt = isoTime(*turn.StartedAt)
	}
	if turn.CompletedAt != nil {
		o.CompletedAt = isoTime(*turn.CompletedAt)
	}
	switch turn.State {
	case "running":
		o.State = api.TurnRunning
	case "completed":
		o.State = api.TurnCompleted
	case "interrupted":
		o.State = api.TurnInterrupted
	case "error":
		o.State = api.TurnFailed
		o.Error = sessionError
	default:
		o.State = api.TurnUnknown
	}
	return o
}

func isoTime(value string) time.Time {
	t, _ := time.Parse(time.RFC3339Nano, value)
	return t
}

func validISO(value *string) bool {
	if value == nil {
		return true
	}
	_, err := time.Parse(time.RFC3339Nano, *value)
	return err == nil
}

// nullableString distinguishes a required JSON null from an omitted
// field: T3 sends null for a thread running from its project's workspace
// root, and omission would be a schema change.
type nullableString struct {
	Value string
	Set   bool
}

func (n *nullableString) UnmarshalJSON(data []byte) error {
	n.Set = true
	if string(data) == "null" {
		n.Value = ""
		return nil
	}
	return json.Unmarshal(data, &n.Value)
}

type shellSnapshot struct {
	Sequence *uint64         `json:"snapshotSequence"`
	Projects *[]projectShell `json:"projects"`
	Threads  *[]threadShell  `json:"threads"`
}

// shellEvent is one stream event; the fields present depend on kind.
type shellEvent struct {
	Kind      string         `json:"kind"`
	Sequence  *uint64        `json:"sequence"`
	Snapshot  *shellSnapshot `json:"snapshot"`
	Project   *projectShell  `json:"project"`
	ProjectID string         `json:"projectId"`
	Thread    *threadShell   `json:"thread"`
	ThreadID  string         `json:"threadId"`
}

func decodeEvent(data json.RawMessage) (shellEvent, error) {
	var event shellEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return shellEvent{}, schemaErrorf("stream item: %w", err)
	}
	if event.Kind == "" {
		return shellEvent{}, schemaErrorf("stream item omitted kind")
	}
	switch event.Kind {
	case "snapshot":
		if event.Snapshot == nil {
			return shellEvent{}, schemaErrorf("snapshot item omitted snapshot")
		}
		if err := validateSnapshot(*event.Snapshot); err != nil {
			return shellEvent{}, err
		}
	case "synchronized":
	case "project-upserted", "project-removed", "thread-upserted", "thread-removed":
		if event.Sequence == nil {
			return shellEvent{}, schemaErrorf("%s omitted sequence", event.Kind)
		}
		switch event.Kind {
		case "project-upserted":
			if event.Project == nil {
				return shellEvent{}, schemaErrorf("project-upserted omitted project")
			}
			if err := validateProject(*event.Project); err != nil {
				return shellEvent{}, err
			}
		case "project-removed":
			if event.ProjectID == "" {
				return shellEvent{}, schemaErrorf("project-removed omitted projectId")
			}
		case "thread-upserted":
			if event.Thread == nil {
				return shellEvent{}, schemaErrorf("thread-upserted omitted thread")
			}
			if err := validateThread(*event.Thread); err != nil {
				return shellEvent{}, err
			}
		case "thread-removed":
			if event.ThreadID == "" {
				return shellEvent{}, schemaErrorf("thread-removed omitted threadId")
			}
		}
	default:
		return shellEvent{}, schemaErrorf("unknown stream item %q", event.Kind)
	}
	return event, nil
}

func validateSnapshot(snapshot shellSnapshot) error {
	if snapshot.Sequence == nil || snapshot.Projects == nil || snapshot.Threads == nil {
		return schemaErrorf("snapshot omitted snapshotSequence, projects, or threads")
	}
	for _, project := range *snapshot.Projects {
		if err := validateProject(project); err != nil {
			return err
		}
	}
	for _, thread := range *snapshot.Threads {
		if err := validateThread(thread); err != nil {
			return err
		}
	}
	return nil
}

func validateProject(project projectShell) error {
	if project.ID == "" || project.WorkspaceRoot == "" {
		return schemaErrorf("project %q omitted id or workspaceRoot", project.ID)
	}
	return nil
}

func validateThread(thread threadShell) error {
	switch {
	case thread.ID == "" || thread.ProjectID == "" || thread.Title == "":
		return schemaErrorf("thread %q omitted id, projectId, or title", thread.ID)
	case thread.ModelSelection == nil || thread.ModelSelection.Model == "":
		return schemaErrorf("thread %s omitted modelSelection.model", thread.ID)
	case !thread.WorktreePath.Set:
		return schemaErrorf("thread %s omitted worktreePath", thread.ID)
	case thread.HasPendingApprovals == nil || thread.HasPendingUserInput == nil:
		return schemaErrorf("thread %s omitted its pending-action flags", thread.ID)
	case thread.Session != nil && thread.Session.Status == "":
		return schemaErrorf("thread %s session omitted status", thread.ID)
	case thread.LatestTurn != nil && (thread.LatestTurn.TurnID == "" || thread.LatestTurn.State == "" || thread.LatestTurn.RequestedAt == ""):
		return schemaErrorf("thread %s latestTurn omitted turnId, state, or requestedAt", thread.ID)
	case thread.LatestTurn != nil && (!validISO(&thread.LatestTurn.RequestedAt) || !validISO(thread.LatestTurn.StartedAt) || !validISO(thread.LatestTurn.CompletedAt)):
		return schemaErrorf("thread %s latestTurn carries an unreadable timestamp", thread.ID)
	}
	return nil
}

// projectStatus normalizes each of T3's status facets into the thread
// vocabulary and lets the threads domain rank them:
//
//	hasPendingApprovals                       → waiting_for_permission
//	hasPendingUserInput                       → waiting_for_input
//	session starting / running                → working
//	session error                             → error
//	session idle / ready / interrupted /
//	  stopped, or no session                  → idle
//	background liveness working / monitoring  → working
//	anything unrecognized                     → unknown
//
// A value ATC does not recognize is honestly unknown, never a guessed
// resting state.
func projectStatus(thread threadShell) api.ThreadStatus {
	evidence := make([]api.ThreadStatus, 0, 4)
	if *thread.HasPendingApprovals {
		evidence = append(evidence, api.ThreadWaitingForPermission)
	}
	if *thread.HasPendingUserInput {
		evidence = append(evidence, api.ThreadWaitingForInput)
	}
	session := api.ThreadIdle
	if thread.Session != nil {
		switch thread.Session.Status {
		case "starting", "running":
			session = api.ThreadWorking
		case "error":
			session = api.ThreadError
		case "idle", "ready", "interrupted", "stopped":
		default:
			session = api.ThreadUnknown
		}
	}
	evidence = append(evidence, session)
	if thread.BackgroundLiveness != nil {
		switch *thread.BackgroundLiveness {
		case "working", "monitoring":
			evidence = append(evidence, api.ThreadWorking)
		case "":
		default:
			evidence = append(evidence, api.ThreadUnknown)
		}
	}
	return threads.Rank(evidence...)
}

// errUnknownProject reports a thread naming a project the projection has
// not announced — a T3 invariant broken, treated as a schema failure.
var errUnknownProject = errors.New("thread names an unknown project")
