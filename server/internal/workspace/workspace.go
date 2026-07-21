// Package workspace owns atc Workspace semantics: named units of work
// inside a Project that group sessions. Workspaces are records only — they
// carry no process state — and deleting one removes metadata, never files
// (ADR 0008).
package workspace

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/publicid"
	"github.com/jeremytondo/atc/internal/store"
)

// Sentinel errors let callers (notably the API) map failures to stable status
// codes.
var (
	// ErrWorkspaceNotFound is returned when a workspace id does not exist.
	ErrWorkspaceNotFound = errors.New("workspace not found")
	// ErrInvalidWorkspace is returned when a workspace record field fails
	// validation.
	ErrInvalidWorkspace = errors.New("invalid workspace")
	// ErrWorkspaceHasActiveSessions is returned when a delete is
	// rejected because the workspace still has a provisional or Live record.
	ErrWorkspaceHasActiveSessions = errors.New("workspace has active sessions")
	// ErrSessionEndFailed is returned when a delete aborts because one of the
	// workspace's active sessions could not be ended. No metadata has been
	// deleted when it is returned.
	ErrSessionEndFailed = errors.New("session end failed")
)

// Workspace is atc's domain model for a workspace. Name is renameable.
type Workspace struct {
	ID        string
	ProjectID string
	Name      string
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SessionEnder is the slice of the session service that workspace deletion
// depends on: ending one active record. It is an interface so the domain
// can be tested with a fake, and so this package never imports the session
// package (which imports this one for start resolution).
type SessionEnder interface {
	End(ctx context.Context, id string) error
}

// Service implements workspace operations on top of durable metadata.
type Service struct {
	store  *store.Store
	logger *slog.Logger
}

// NewService returns a Service backed by st. A nil logger uses slog.Default.
func NewService(st *store.Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, logger: logger}
}

// Create validates and persists a new workspace in a project. Names are not
// unique: two workspaces in one project may share a name.
func (s *Service) Create(ctx context.Context, projectID, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Workspace{}, fmt.Errorf("%w: name is required", ErrInvalidWorkspace)
	}
	if strings.TrimSpace(projectID) == "" {
		return Workspace{}, fmt.Errorf("%w: projectId is required", ErrInvalidWorkspace)
	}

	id, err := publicid.New("wsp_")
	if err != nil {
		return Workspace{}, err
	}
	record, err := s.store.CreateWorkspace(ctx, store.CreateWorkspaceInput{
		ID:        id,
		ProjectID: projectID,
		Name:      name,
	})
	if err != nil {
		return Workspace{}, translateStoreErr(err)
	}
	s.logger.Info("workspace created", "id", record.ID, "project_id", record.ProjectID, "name", record.Name)
	return domainWorkspace(record), nil
}

// Get loads one workspace by id.
func (s *Service) Get(ctx context.Context, id string) (Workspace, error) {
	record, err := s.store.GetWorkspace(ctx, id)
	if err != nil {
		return Workspace{}, translateStoreErr(err)
	}
	return domainWorkspace(record), nil
}

// List returns workspaces newest-first. An empty projectID lists all workspaces, which
// clients showing every workspace per connection rely on.
func (s *Service) List(ctx context.Context, projectID string) ([]Workspace, error) {
	records, err := s.store.ListWorkspaces(ctx, store.WorkspaceListFilter{
		ProjectID: projectID,
	})
	if err != nil {
		return nil, translateStoreErr(err)
	}
	workspaces := make([]Workspace, 0, len(records))
	for _, record := range records {
		workspaces = append(workspaces, domainWorkspace(record))
	}
	return workspaces, nil
}

// Rename updates a workspace's name.
func (s *Service) Rename(ctx context.Context, id, name string) (Workspace, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Workspace{}, fmt.Errorf("%w: name is required", ErrInvalidWorkspace)
	}
	record, err := s.store.RenameWorkspace(ctx, id, name)
	if err != nil {
		return Workspace{}, translateStoreErr(err)
	}
	return domainWorkspace(record), nil
}

// Delete removes a workspace and its session metadata, ending active sessions
// first. Deletion deliberately has no intermediate state: the first end failure
// aborts the whole delete with ErrSessionEndFailed and no metadata has been
// deleted (ends already performed are not rolled back; ending is safe to
// repeat). The store's final transaction re-checks for
// active sessions, so a session start that slips in concurrently fails the
// delete with ErrWorkspaceHasActiveSessions and the user retries. Files are
// never touched.
func (s *Service) Delete(ctx context.Context, id string, ender SessionEnder) error {
	if _, err := s.store.GetWorkspace(ctx, id); err != nil {
		return translateStoreErr(err)
	}
	sessions, err := s.store.ListAll(ctx, store.ListFilter{WorkspaceID: id})
	if err != nil {
		return translateStoreErr(err)
	}
	for _, record := range sessions {
		if record.Status != store.StatusStarting && record.Status != store.StatusLive {
			continue
		}
		if err := ender.End(ctx, record.ID); err != nil {
			return fmt.Errorf("%w: session %s: %v", ErrSessionEndFailed, record.ID, err)
		}
	}
	if err := s.store.DeleteWorkspace(ctx, id); err != nil {
		return translateStoreErr(err)
	}
	s.logger.Info("workspace deleted", "id", id, "sessions", len(sessions))
	return nil
}

// ResolveForStart loads the workspace a session start references and resolves
// it to the project working directory the session will launch in. It revalidates the working
// directory so a directory that vanished since creation fails fast.
func (s *Service) ResolveForStart(ctx context.Context, id string) (string, error) {
	got, err := s.Get(ctx, id)
	if err != nil {
		return "", err
	}
	record, err := s.store.GetProject(ctx, got.ProjectID)
	if err != nil {
		return "", translateStoreErr(err)
	}
	if err := project.ValidateWorkingDir(record.WorkingDir); err != nil {
		return "", err
	}
	return record.WorkingDir, nil
}

func domainWorkspace(record store.Workspace) Workspace {
	return Workspace{
		ID:        record.ID,
		ProjectID: record.ProjectID,
		Name:      record.Name,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}

func translateStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrWorkspaceNotFound):
		return fmt.Errorf("%w: %v", ErrWorkspaceNotFound, err)
	case errors.Is(err, store.ErrWorkspaceHasActiveSessions):
		return fmt.Errorf("%w: %v", ErrWorkspaceHasActiveSessions, err)
	case errors.Is(err, store.ErrProjectNotFound):
		return fmt.Errorf("%w: %v", project.ErrProjectNotFound, err)
	}
	return err
}
