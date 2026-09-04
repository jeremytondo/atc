package t3code

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/paths"
)

// Thread creation (ATC-289): ATC starts a T3 thread by dispatching one
// thread.turn.start command whose bootstrap creates the thread — T3 commits
// the thread and its first turn together, and everything after is driven
// by its shell events like any thread started inside T3. ATC owns every
// default it sends; T3 applies no project defaults to a thread created
// this way, and ATC holds no model knowledge: model and options are
// copied in untouched.
const (
	runtimeMode     = "auto"
	interactionMode = "default"
	titleLimit      = 72
	untitled        = "New thread"
	// dispatchTimeout bounds the wait for T3 to commit a creation: T3
	// answers once the thread and turn are persisted, well within this,
	// and a T3 that never answers must not hold a caller forever.
	dispatchTimeout = 60 * time.Second
)

// PrepareThread resolves a create against the live connection: the
// Integration must be connected, and the Project's directory must be the
// canonical workspace root of a T3 project — T3 is asked to create the
// thread there, never the project. The T3 thread id and command id are
// chosen here: T3 returns neither, the thread id is how T3's later
// reports of the thread find the record ATC creates before dispatching,
// and the command id is T3's idempotency key. Nothing is sent until
// Dispatch.
func (s *Service) PrepareThread(ctx context.Context, req integrations.ThreadCreation) (integrations.PreparedThread, error) {
	s.mu.Lock()
	connection, client := s.connection, s.client
	projects := make([]projectShell, 0, len(s.shell.projects))
	for _, project := range s.shell.projects {
		projects = append(projects, project)
	}
	s.mu.Unlock()
	if connection.State != api.IntegrationConnected {
		return integrations.PreparedThread{}, fmt.Errorf("%w: T3 Code is %s: %s", integrations.ErrNotConnected, connection.State, connection.Detail)
	}
	sort.Slice(projects, func(i, j int) bool { return projects[i].ID < projects[j].ID })
	projectID := ""
	for _, project := range projects {
		if canonical, err := paths.CanonicalDir(project.WorkspaceRoot); err == nil && canonical == req.Directory {
			projectID = project.ID
			break
		}
	}
	if projectID == "" {
		return integrations.PreparedThread{}, fmt.Errorf("%w: %s is not registered in T3 Code", integrations.ErrProjectNotRegistered, req.Directory)
	}
	threadID := newUUID()
	title := threadTitle(req.Prompt)
	command := turnStartCommand(threadID, projectID, title, req, s.now())
	return integrations.PreparedThread{
		ProviderID: threadID,
		Title:      title,
		// The connection the project was resolved on is the one the
		// command goes to: a connection that came up since may be another
		// environment, where the project id means something else.
		Dispatch: func(ctx context.Context) error { return s.dispatch(ctx, client, threadID, command) },
	}, nil
}

// dispatch sends one command and waits for T3 to commit it. T3's typed
// refusal is final — T3 says whether it rolled the thread back — and
// reports as a failed creation with T3's message. The connection dropping
// or the caller giving up before the answer leaves the outcome unknown:
// when T3's shell has meanwhile reported the thread, it exists and the
// creation succeeded; otherwise it reports as failed too, and a thread T3
// did create arrives later as an observed one. No connection at all is
// the not-connected refusal.
func (s *Service) dispatch(ctx context.Context, client *rpcClient, threadID string, command map[string]any) error {
	if client == nil {
		return fmt.Errorf("%w: T3 Code's connection is not up", integrations.ErrNotConnected)
	}
	ctx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()
	err := client.call(ctx, dispatchMethod, command)
	var failure *rpcFailure
	switch {
	case err == nil:
		return nil
	case errors.As(err, &failure):
		message := failure.message
		if message == "" {
			message = failure.Error()
		}
		if failure.rolledBack {
			message += "; T3 Code rolled back the thread it had created"
		}
		return fmt.Errorf("%w: T3 Code rejected the command: %s", integrations.ErrThreadCreationFailed, message)
	case s.reported(threadID):
		s.logger.Warn("t3code: T3 Code reported the thread before answering its create; treated as created", "thread", threadID, "error", err)
		return nil
	default:
		return fmt.Errorf("%w: T3 Code did not answer: %w", integrations.ErrThreadCreationFailed, err)
	}
}

// reported says whether T3's shell projection holds the thread.
func (s *Service) reported(threadID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.shell.threads[threadID]
	return ok
}

// turnStartCommand is the thread.turn.start command with
// bootstrap.createThread: the same model selection in both places, the
// ATC-owned defaults, execution at the project root (no branch, no
// worktree, no worktree preparation, no setup script), and the provider
// instance named after the agent — T3 names each provider's default
// instance after its kind, and the agent ids here are those kinds.
func turnStartCommand(threadID, projectID, title string, req integrations.ThreadCreation, now time.Time) map[string]any {
	selection := map[string]any{"instanceId": req.AgentID, "model": req.Model}
	if len(req.Options) > 0 {
		options := make([]map[string]any, 0, len(req.Options))
		for _, option := range req.Options {
			options = append(options, map[string]any{"id": option.ID, "value": option.Value})
		}
		selection["options"] = options
	}
	createdAt := now.UTC().Format("2006-01-02T15:04:05.000Z07:00")
	return map[string]any{
		"type":      "thread.turn.start",
		"commandId": newUUID(),
		"threadId":  threadID,
		"message": map[string]any{
			"messageId": newUUID(), "role": "user", "text": req.Prompt, "attachments": []any{},
		},
		"modelSelection":  selection,
		"titleSeed":       title,
		"runtimeMode":     runtimeMode,
		"interactionMode": interactionMode,
		"bootstrap": map[string]any{
			"createThread": map[string]any{
				"projectId":       projectID,
				"title":           title,
				"modelSelection":  selection,
				"runtimeMode":     runtimeMode,
				"interactionMode": interactionMode,
				"branch":          nil,
				"worktreePath":    nil,
				"createdAt":       createdAt,
			},
		},
		"createdAt": createdAt,
	}
}

// threadTitle derives a thread's title from its first prompt: whitespace
// runs collapsed to single spaces, capped at titleLimit characters with
// the last three replaced by an ellipsis, untitled for a blank prompt.
func threadTitle(prompt string) string {
	title := strings.Join(strings.Fields(prompt), " ")
	if title == "" {
		return untitled
	}
	if runes := []rune(title); len(runes) > titleLimit {
		return string(runes[:titleLimit-3]) + "..."
	}
	return title
}

// newUUID mints a random (version 4) UUID: T3's ids are opaque strings,
// and its own clients use UUIDs.
func newUUID() string {
	var b [16]byte
	rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
