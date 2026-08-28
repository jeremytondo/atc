// Package projects is the Projects domain (ATC-256): the resource behind
// /v1/projects, ATC's unit of organization. A project carries a name and
// a canonical directory; the directory is the identity (unique, immutable)
// and answers where new things in the project start. Presentation —
// grouping, scoping views — belongs to UIs; the API only provides filters.
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
	// ErrDirectoryInvalid wraps a create directory that cannot be
	// canonicalized: it does not exist or is not a directory.
	ErrDirectoryInvalid = errors.New("invalid project directory")
	// ErrDirectoryTaken reports a create whose directory already belongs to
	// a project — same canonical form or the same real folder.
	ErrDirectoryTaken = errors.New("directory already belongs to a project")
	// ErrNotEmpty refuses a delete while terminals still belong to the
	// project; the wrapped message reports what remains. There is no
	// cascade and no --force.
	ErrNotEmpty = errors.New("project is not empty")
)

// resource is the event-payload resource kind.
const resource = "project"

const idPrefix = "proj-"

// Options wires a Service. Now defaults to time.Now.
type Options struct {
	Repository *store.Projects
	// Terminals is read for the delete-refusal check: a project with
	// terminals is never deleted.
	Terminals *store.Terminals
	Hub       *events.Hub
	Now       func() time.Time
}

// Service owns project policy: canonical-directory identity, name
// defaulting, and the refuse-when-non-empty delete. ops serializes each
// mutation's commit so check-then-write sequences (directory uniqueness,
// the empty check) cannot interleave.
type Service struct {
	repository *store.Projects
	terminals  *store.Terminals
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
		terminals:  opts.Terminals,
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
	name := params.Name
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
	// One project per real folder: a symlinked spelling canonicalizes to
	// the claimed string, and a differently-spelled path to the same folder
	// (a case-insensitive filesystem) is caught by filesystem identity.
	// ops serializes the check against the insert, and the schema's UNIQUE
	// backstops external writers.
	info, err := os.Stat(canonical)
	if err != nil {
		return api.Project{}, fmt.Errorf("%w: %w", ErrDirectoryInvalid, err)
	}
	existing, err := s.repository.List(ctx)
	if err != nil {
		return api.Project{}, err
	}
	for _, project := range existing {
		if project.Directory == canonical {
			return api.Project{}, fmt.Errorf("%w: %s", ErrDirectoryTaken, canonical)
		}
		// A project whose folder is gone cannot be the same folder; its
		// canonical string above is still its claim.
		if projectInfo, err := os.Stat(project.Directory); err == nil && os.SameFile(info, projectInfo) {
			return api.Project{}, fmt.Errorf("%w: %s is %s", ErrDirectoryTaken, canonical, project.Directory)
		}
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

// UpdateName renames the project — the only mutable field. The rename
// returns the committed row in one repository operation, so a committed
// write can never surface as an error with its event unpublished.
func (s *Service) UpdateName(ctx context.Context, id, name string) (api.Project, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	record, ok, err := s.repository.UpdateName(ctx, id, name, s.now())
	if err != nil {
		return api.Project{}, err
	}
	if !ok {
		return api.Project{}, ErrNotFound
	}
	s.hub.Publish(api.EventProjectUpdated, resource, id)
	return project(record), nil
}

// Delete removes an empty project. While any terminal still belongs to it
// the delete is refused with what remains; the schema's foreign key
// backstops the race against a concurrent terminal create.
func (s *Service) Delete(ctx context.Context, id string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	terminalIDs, err := s.terminals.ListIDsByProject(ctx, id)
	if err != nil {
		return err
	}
	if len(terminalIDs) > 0 {
		return fmt.Errorf("%w: %d terminal(s) remain: %s",
			ErrNotEmpty, len(terminalIDs), strings.Join(terminalIDs, ", "))
	}
	deleted, err := s.repository.Delete(ctx, id)
	if errors.Is(err, store.ErrForeignKeyViolation) {
		// A terminal create slipped in after the empty check; the foreign
		// key kept the state consistent, so answer with the refusal.
		return fmt.Errorf("%w: a terminal was just created in it", ErrNotEmpty)
	}
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
