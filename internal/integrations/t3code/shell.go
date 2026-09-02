package t3code

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jeremytondo/atc/internal/api"
)

// The shell projection is T3's lightweight read model of projects and
// threads (no transcripts). Only the fields ATC uses are decoded; unknown
// fields are ignored, and a required field that is missing is a schema
// failure for the whole payload — the adapter reports it rather than
// guess.

// schemaError marks a payload ATC cannot read: permanent for that
// payload, reported as unavailable with the detail.
type schemaError struct{ err error }

func (e *schemaError) Error() string { return "T3 Code shell schema: " + e.err.Error() }
func (e *schemaError) Unwrap() error { return e.err }

func schemaErrorf(format string, args ...any) error {
	return &schemaError{err: fmt.Errorf(format, args...)}
}

type projectShell struct {
	ID            string `json:"id"`
	Title         string `json:"title"`
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
	if project.ID == "" || project.Title == "" || project.WorkspaceRoot == "" {
		return schemaErrorf("project %q omitted id, title, or workspaceRoot", project.ID)
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
	}
	return nil
}

// projectStatus maps T3's evidence to the thread vocabulary, first match
// wins:
//
//	hasPendingApprovals                          → waiting_for_permission
//	hasPendingUserInput                          → waiting_for_input
//	session starting / running                   → working
//	session error                                → error
//	session at rest (or none) with live
//	  background work (working / monitoring)     → working
//	session at rest (or none), no liveness       → idle
//	anything unrecognized                        → unknown
//
// Approval and input outrank the session because they name the action
// that unblocks the thread; an error outranks stale background liveness.
// A value ATC does not recognize is honestly unknown, never a guessed
// resting state.
func projectStatus(thread threadShell) api.ThreadStatus {
	if *thread.HasPendingApprovals {
		return api.ThreadWaitingForPermission
	}
	if *thread.HasPendingUserInput {
		return api.ThreadWaitingForInput
	}
	if thread.Session != nil {
		switch thread.Session.Status {
		case "starting", "running":
			return api.ThreadWorking
		case "error":
			return api.ThreadError
		case "idle", "ready", "interrupted", "stopped":
		default:
			return api.ThreadUnknown
		}
	}
	if thread.BackgroundLiveness != nil {
		switch *thread.BackgroundLiveness {
		case "working", "monitoring":
			return api.ThreadWorking
		case "":
		default:
			return api.ThreadUnknown
		}
	}
	return api.ThreadIdle
}

// errUnknownProject reports a thread naming a project the projection has
// not announced — a T3 invariant broken, treated as a schema failure.
var errUnknownProject = errors.New("thread names an unknown project")
