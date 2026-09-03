// Package projects is the Projects domain (ATC-256, ATC-295): the
// resource behind /v1/projects, durable codebase context. A project
// carries a name and a canonical directory; the directory is unique and
// editable, and threads are classified into the most specific project
// containing their origin (the threads domain owns that policy; the
// composition root wires its backfill to every create and move here).
// Presentation — grouping, scoping views — belongs to UIs; the API only
// provides filters.
//
// Unlike terminals, projects have no external backend and no derived
// status, so the service is a thin policy layer over the repository: no
// in-memory view, no reconciliation.
package projects

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/store"
)

var (
	// ErrNotFound reports an id with no record; the API layer maps it to 404.
	ErrNotFound = errors.New("project not found")
	// ErrDirectoryInvalid wraps a directory that cannot be canonicalized:
	// it does not exist or is not a directory.
	ErrDirectoryInvalid = errors.New("invalid project directory")
	// ErrDirectoryTaken reports a directory that already belongs to another
	// project — same canonical form or the same real folder.
	ErrDirectoryTaken = errors.New("directory already belongs to a project")
	// ErrNameInvalid refuses an empty name.
	ErrNameInvalid = errors.New("invalid project name")
	// ErrInvalidUpdate refuses a PATCH that nulls a field that cannot be
	// cleared (name, directory).
	ErrInvalidUpdate = errors.New("invalid update")
)

// resource is the event-payload resource kind.
const resource = "project"

const idPrefix = "proj-"

// Options wires a Service. Now defaults to time.Now.
type Options struct {
	Repository *store.Projects
	Hub        *events.Hub
	Now        func() time.Time
}

// Service owns project policy: canonical-directory uniqueness and name
// defaulting. ops serializes each mutation's commit so the
// check-then-write directory uniqueness sequence cannot interleave.
type Service struct {
	repository *store.Projects
	hub        *events.Hub
	now        func() time.Time

	ops sync.Mutex
}

func NewService(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{
		repository: opts.Repository,
		hub:        opts.Hub,
		now:        opts.Now,
	}
}

// Create canonicalizes the directory (absolute, cleaned, symlinks
// resolved — the canonical form is what directory means everywhere),
// defaults the name from its basename, and persists the record. The path
// must exist and be a directory; the canonical form must be unclaimed.
func (s *Service) Create(ctx context.Context, params api.ProjectCreateParams) (api.Project, error) {
	canonical, err := paths.CanonicalDir(params.Directory)
	if err != nil {
		return api.Project{}, fmt.Errorf("%w: %w", ErrDirectoryInvalid, err)
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = filepath.Base(canonical)
	}
	now := s.now()
	record := store.ProjectRecord{
		Name: name, Directory: canonical,
		CreatedAt: now, UpdatedAt: now,
	}

	s.ops.Lock()
	defer s.ops.Unlock()
	if err := s.unclaimed(ctx, canonical, ""); err != nil {
		return api.Project{}, err
	}
	// Insertion is the collision check: a taken ID inserts nothing and
	// re-rolls, with no check-then-insert window.
	for {
		record.ID = ids.New(idPrefix)
		inserted, err := s.repository.Insert(ctx, record)
		if err != nil {
			return api.Project{}, err
		}
		if inserted {
			break
		}
	}
	s.hub.Publish(api.EventProjectCreated, resource, record.ID)
	return project(record), nil
}

// Get serves one project.
func (s *Service) Get(ctx context.Context, id string) (api.Project, error) {
	record, ok, err := s.repository.Get(ctx, id)
	if err != nil {
		return api.Project{}, err
	}
	if !ok {
		return api.Project{}, ErrNotFound
	}
	return project(record), nil
}

// List serves every project in creation order.
func (s *Service) List(ctx context.Context) ([]api.Project, error) {
	records, err := s.repository.List(ctx)
	if err != nil {
		return nil, err
	}
	projects := make([]api.Project, 0, len(records))
	for _, record := range records {
		projects = append(projects, project(record))
	}
	return projects, nil
}

// unclaimed refuses a canonical directory another project holds: a
// symlinked spelling canonicalizes to the claimed string, and a
// differently-spelled path to the same folder (a case-insensitive
// filesystem) is caught by filesystem identity. except is the project
// being moved, whose own claim does not count. ops serializes the check
// against the write, and the schema's UNIQUE backstops external writers.
// Caller holds ops.
func (s *Service) unclaimed(ctx context.Context, canonical, except string) error {
	info, err := os.Stat(canonical)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrDirectoryInvalid, err)
	}
	existing, err := s.repository.List(ctx)
	if err != nil {
		return err
	}
	for _, project := range existing {
		if project.ID == except {
			continue
		}
		if project.Directory == canonical {
			return fmt.Errorf("%w: %s", ErrDirectoryTaken, canonical)
		}
		// A project whose folder is gone cannot be the same folder; its
		// canonical string above is still its claim.
		if projectInfo, err := os.Stat(project.Directory); err == nil && os.SameFile(info, projectInfo) {
			return fmt.Errorf("%w: %s is %s", ErrDirectoryTaken, canonical, project.Directory)
		}
	}
	return nil
}

// Update applies a merge patch to the name and directory, reporting
// whether the directory moved — the caller backfills threads when it
// did. A new directory is canonicalized and must be unclaimed; existing
// thread associations are never rewritten. The write returns the
// committed row in one repository operation, so a committed write can
// never surface as an error with its event unpublished.
func (s *Service) Update(ctx context.Context, id string, params api.ProjectUpdateParams) (api.Project, bool, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	current, ok, err := s.repository.Get(ctx, id)
	if err != nil {
		return api.Project{}, false, err
	}
	if !ok {
		return api.Project{}, false, ErrNotFound
	}
	name, directory := current.Name, current.Directory
	if params.Name.Set {
		if params.Name.Null() {
			return api.Project{}, false, fmt.Errorf("%w: name cannot be null", ErrInvalidUpdate)
		}
		if name = strings.TrimSpace(*params.Name.Value); name == "" {
			return api.Project{}, false, fmt.Errorf("%w: name cannot be empty", ErrNameInvalid)
		}
	}
	if params.Directory.Set {
		if params.Directory.Null() {
			return api.Project{}, false, fmt.Errorf("%w: directory cannot be null", ErrInvalidUpdate)
		}
		if directory, err = paths.CanonicalDir(*params.Directory.Value); err != nil {
			return api.Project{}, false, fmt.Errorf("%w: %w", ErrDirectoryInvalid, err)
		}
		if directory != current.Directory {
			if err := s.unclaimed(ctx, directory, id); err != nil {
				return api.Project{}, false, err
			}
		}
	}
	if name == current.Name && directory == current.Directory {
		return project(current), false, nil
	}
	record, ok, err := s.repository.Update(ctx, id, name, directory, s.now())
	if err != nil {
		return api.Project{}, false, err
	}
	if !ok {
		return api.Project{}, false, ErrNotFound
	}
	s.hub.Publish(api.EventProjectUpdated, resource, id)
	return project(record), directory != current.Directory, nil
}

// Delete removes a project, whatever references it: threads are
// unassigned by the schema and survive, and nothing else refers to a
// project.
func (s *Service) Delete(ctx context.Context, id string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	deleted, err := s.repository.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	s.hub.Publish(api.EventProjectDeleted, resource, id)
	return nil
}

func project(record store.ProjectRecord) api.Project {
	return api.Project{
		ID:        record.ID,
		Name:      record.Name,
		Directory: record.Directory,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}
