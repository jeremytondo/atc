// Package project owns atc Project semantics: named workstation
// directories that group sessions. Projects are records only — they carry no
// process state — and are archived rather than deleted.
package project

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/publicid"
	"github.com/jeremytondo/atc/internal/store"
)

// Sentinel errors let callers (notably the API) map failures to stable status
// codes.
var (
	// ErrProjectNotFound is returned when a project id does not exist.
	ErrProjectNotFound = errors.New("project not found")
	// ErrInvalidProject is returned when a project record field fails
	// validation.
	ErrInvalidProject = errors.New("invalid project")
	// ErrInvalidWorkingDir is returned when a working directory is relative,
	// missing, or not a directory.
	ErrInvalidWorkingDir = errors.New("invalid working directory")
	// ErrProjectArchived is returned when a session start references an
	// archived project.
	ErrProjectArchived = errors.New("project is archived")
	// ErrProjectHasActiveSessions is returned when an archive is rejected
	// because the project still has a starting or running session.
	ErrProjectHasActiveSessions = errors.New("project has active sessions")
)

// Project is atc's domain model for a project. WorkingDir is fixed after
// creation; Name is renameable; a nil ArchivedAt means active.
type Project struct {
	ID         string
	Name       string
	WorkingDir string
	CreatedAt  time.Time
	UpdatedAt  time.Time
	ArchivedAt *time.Time
}

// ValidateWorkingDir is the single working-directory rule for projects and
// session starts: the path must be absolute, exist, and be a directory.
func ValidateWorkingDir(path string) error {
	if strings.TrimSpace(path) == "" {
		return fmt.Errorf("%w: workingDir is required", ErrInvalidWorkingDir)
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("%w: %q is not an absolute path", ErrInvalidWorkingDir, path)
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%w: %q does not exist", ErrInvalidWorkingDir, path)
	}
	if err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidWorkingDir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("%w: %q is not a directory", ErrInvalidWorkingDir, path)
	}
	return nil
}

// Service implements project operations on top of durable metadata.
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

// Create validates and persists a new project. The working directory is
// stored cleaned and never changes afterwards.
func (s *Service) Create(ctx context.Context, name, workingDir string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("%w: name is required", ErrInvalidProject)
	}
	if err := ValidateWorkingDir(workingDir); err != nil {
		return Project{}, err
	}

	id, err := publicid.New("prj_")
	if err != nil {
		return Project{}, err
	}
	record, err := s.store.CreateProject(ctx, store.CreateProjectInput{
		ID:         id,
		Name:       name,
		WorkingDir: filepath.Clean(workingDir),
	})
	if err != nil {
		return Project{}, err
	}
	s.logger.Info("project created", "id", record.ID, "name", record.Name, "dir", record.WorkingDir)
	return domainProject(record), nil
}

// Get loads one project by id.
func (s *Service) Get(ctx context.Context, id string) (Project, error) {
	record, err := s.store.GetProject(ctx, id)
	if err != nil {
		return Project{}, translateStoreErr(err)
	}
	return domainProject(record), nil
}

// List returns projects newest-first, hiding archived projects unless
// includeArchived is true.
func (s *Service) List(ctx context.Context, includeArchived bool) ([]Project, error) {
	records, err := s.store.ListProjects(ctx, store.ProjectListFilter{IncludeArchived: includeArchived})
	if err != nil {
		return nil, err
	}
	projects := make([]Project, 0, len(records))
	for _, record := range records {
		projects = append(projects, domainProject(record))
	}
	return projects, nil
}

// Rename updates a project's name. The working directory cannot change.
func (s *Service) Rename(ctx context.Context, id, name string) (Project, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Project{}, fmt.Errorf("%w: name is required", ErrInvalidProject)
	}
	record, err := s.store.RenameProject(ctx, id, name)
	if err != nil {
		return Project{}, translateStoreErr(err)
	}
	return domainProject(record), nil
}

// Archive hides a project from default lists and blocks new session starts.
// Archiving an archived project is a no-op returning the current record. A
// project with a starting or running session cannot be archived.
func (s *Service) Archive(ctx context.Context, id string) (Project, error) {
	record, err := s.store.ArchiveProject(ctx, id)
	if errors.Is(err, store.ErrProjectHasActiveSessions) {
		return Project{}, fmt.Errorf("%w: %s", ErrProjectHasActiveSessions, id)
	}
	if err != nil {
		return Project{}, translateStoreErr(err)
	}
	return domainProject(record), nil
}

// Unarchive reactivates a project. Unarchiving an active project is a no-op
// returning the current record.
func (s *Service) Unarchive(ctx context.Context, id string) (Project, error) {
	record, err := s.store.UnarchiveProject(ctx, id)
	if err != nil {
		return Project{}, translateStoreErr(err)
	}
	return domainProject(record), nil
}

// ResolveForStart loads the project a session start references, rejecting
// archived projects and revalidating the working directory so a directory
// that vanished since creation fails fast.
func (s *Service) ResolveForStart(ctx context.Context, id string) (Project, error) {
	got, err := s.Get(ctx, id)
	if err != nil {
		return Project{}, err
	}
	if got.ArchivedAt != nil {
		return Project{}, fmt.Errorf("%w: %s", ErrProjectArchived, id)
	}
	if err := ValidateWorkingDir(got.WorkingDir); err != nil {
		return Project{}, err
	}
	return got, nil
}

func domainProject(record store.Project) Project {
	return Project{
		ID:         record.ID,
		Name:       record.Name,
		WorkingDir: record.WorkingDir,
		CreatedAt:  record.CreatedAt,
		UpdatedAt:  record.UpdatedAt,
		ArchivedAt: record.ArchivedAt,
	}
}

func translateStoreErr(err error) error {
	if errors.Is(err, store.ErrProjectNotFound) {
		return fmt.Errorf("%w: %v", ErrProjectNotFound, err)
	}
	return err
}
