package terminals

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/store"
)

// Spaces (ATC-296) are the containers terminals belong to: a name and the
// directory terminals created there default to, nothing more. They live
// in this package because every invariant they carry is about terminals
// — a terminal is created in a live space, moved between live spaces,
// and deleted with its space — and those invariants sit under the same
// mutation lock as the terminals themselves. The public surface is flat
// (/v1/spaces) regardless.

const spaceIDPrefix = "spce-"

// spaceResource is the event-payload resource kind.
const spaceResource = "space"

// DefaultSpaceName is the Default space's fixed name.
const DefaultSpaceName = "Default"

var (
	// ErrSpaceNotFound reports a space id with no record; the API layer
	// maps it to 404.
	ErrSpaceNotFound = errors.New("space not found")
	// ErrDefaultSpace refuses changing or deleting the Default space.
	ErrDefaultSpace = errors.New("the Default space cannot be changed or deleted")
	// ErrSpaceDirectoryInvalid wraps a directory that cannot be
	// canonicalized: it does not exist or is not a directory.
	ErrSpaceDirectoryInvalid = errors.New("invalid space directory")
	// ErrSpaceNameInvalid refuses an empty name.
	ErrSpaceNameInvalid = errors.New("invalid space name")
	// ErrSpaceDeleting refuses creating a terminal in, or moving one
	// into, a space whose deletion is in progress.
	ErrSpaceDeleting = errors.New("space is being deleted")
)

// loadSpaces rebuilds the space view from the database at boot, minting
// the Default space when none exists — the one space the server owns,
// rooted at its user's home directory, which must be a usable directory
// or the boot fails. The schema's one-default index backstops a race
// with another process minting it: a refused insert rereads the winner.
func (s *Service) loadSpaces(ctx context.Context) error {
	home, err := paths.CanonicalDir(s.homeDir)
	if err != nil {
		return fmt.Errorf("home directory for the Default space: %w", err)
	}
	s.homeDir = home
	for attempt := 0; ; attempt++ {
		records, err := s.spaces.List(ctx)
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.defaultSpace = ""
		for _, record := range records {
			s.spaceView[record.ID] = record
			if record.IsDefault {
				s.defaultSpace = record.ID
			}
		}
		hasDefault := s.defaultSpace != ""
		s.mu.Unlock()
		if hasDefault {
			return nil
		}
		now := s.now()
		record := store.SpaceRecord{Name: DefaultSpaceName, Directory: home, IsDefault: true, CreatedAt: now, UpdatedAt: now}
		err = s.insertSpace(ctx, &record)
		if errors.Is(err, store.ErrDefaultExists) && attempt == 0 {
			continue
		}
		if err != nil {
			return err
		}
		s.mu.Lock()
		s.defaultSpace = record.ID
		s.mu.Unlock()
		return nil
	}
}

// insertSpace mints the id and persists the record, installing it in the
// view. Insertion is the collision check: a taken ID inserts nothing and
// re-rolls.
func (s *Service) insertSpace(ctx context.Context, record *store.SpaceRecord) error {
	for {
		record.ID = ids.New(spaceIDPrefix)
		inserted, err := s.spaces.Insert(ctx, *record)
		if err != nil {
			return err
		}
		if inserted {
			break
		}
	}
	s.mu.Lock()
	s.spaceView[record.ID] = *record
	s.mu.Unlock()
	return nil
}

// CreateSpace canonicalizes the directory (the server user's home when
// omitted), defaults the name from its basename, and persists the
// record. Directories need not be unique: spaces may share or overlap.
func (s *Service) CreateSpace(ctx context.Context, params api.SpaceCreateParams) (api.Space, error) {
	directory := params.Directory
	if directory == "" {
		directory = s.homeDir
	}
	canonical, err := paths.CanonicalDir(directory)
	if err != nil {
		return api.Space{}, fmt.Errorf("%w: %w", ErrSpaceDirectoryInvalid, err)
	}
	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = filepath.Base(canonical)
	}
	now := s.now()
	record := store.SpaceRecord{Name: name, Directory: canonical, CreatedAt: now, UpdatedAt: now}
	s.ops.Lock()
	defer s.ops.Unlock()
	if err := s.insertSpace(ctx, &record); err != nil {
		return api.Space{}, err
	}
	s.hub.Publish(api.EventSpaceCreated, spaceResource, record.ID)
	return space(record), nil
}

// GetSpace serves one space from the view.
func (s *Service) GetSpace(id string) (api.Space, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.spaceView[id]
	if !ok {
		return api.Space{}, ErrSpaceNotFound
	}
	return space(record), nil
}

// ListSpaces serves every space in creation order.
func (s *Service) ListSpaces() []api.Space {
	s.mu.Lock()
	defer s.mu.Unlock()
	spaces := make([]api.Space, 0, len(s.spaceView))
	for _, record := range s.spaceView {
		spaces = append(spaces, space(record))
	}
	sort.Slice(spaces, func(i, j int) bool {
		if !spaces[i].CreatedAt.Equal(spaces[j].CreatedAt) {
			return spaces[i].CreatedAt.Before(spaces[j].CreatedAt)
		}
		return spaces[i].ID < spaces[j].ID
	})
	return spaces
}

// UpdateSpace applies a merge patch to a regular space's name and
// directory. A new directory is canonicalized and affects only later
// terminals; existing terminals keep theirs. The Default space refuses.
func (s *Service) UpdateSpace(ctx context.Context, id string, params api.SpaceUpdateParams) (api.Space, error) {
	if params.Name.Null() || params.Directory.Null() {
		return api.Space{}, fmt.Errorf("%w: name and directory cannot be null", ErrInvalidUpdate)
	}
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	current, ok := s.spaceView[id]
	s.mu.Unlock()
	if !ok {
		return api.Space{}, ErrSpaceNotFound
	}
	if current.IsDefault {
		return api.Space{}, ErrDefaultSpace
	}
	name, directory := current.Name, current.Directory
	if params.Name.Set {
		if name = strings.TrimSpace(*params.Name.Value); name == "" {
			return api.Space{}, fmt.Errorf("%w: name cannot be empty", ErrSpaceNameInvalid)
		}
	}
	if params.Directory.Set {
		var err error
		if directory, err = paths.CanonicalDir(*params.Directory.Value); err != nil {
			return api.Space{}, fmt.Errorf("%w: %w", ErrSpaceDirectoryInvalid, err)
		}
	}
	if name == current.Name && directory == current.Directory {
		return space(current), nil
	}
	record, ok, err := s.spaces.Update(ctx, id, name, directory, s.now())
	if err != nil {
		return api.Space{}, err
	}
	if !ok {
		return api.Space{}, ErrSpaceNotFound
	}
	s.mu.Lock()
	s.spaceView[id] = record
	s.mu.Unlock()
	s.hub.Publish(api.EventSpaceUpdated, spaceResource, id)
	return space(record), nil
}

// DeleteSpace deletes a regular space and every terminal in it, each
// through deleteTerminal — the application's complete terminal deletion
// workflow, so provider cleanup and thread-link convergence happen
// exactly as for an individual delete. The space is marked deleting
// first, under the mutation lock, so no create or move can land a
// terminal in it (or take one out) meanwhile; the terminals are then
// deleted outside the lock (each delete takes it), every one attempted
// whatever the others did, and the record goes last only when all of
// them went. A failure leaves the space marked so nothing joins it — a
// retry deletes what remains. The Default space refuses.
func (s *Service) DeleteSpace(ctx context.Context, id string, deleteTerminal func(ctx context.Context, terminalID string) error) error {
	s.ops.Lock()
	s.mu.Lock()
	record, ok := s.spaceView[id]
	var terminals []string
	if ok && !record.IsDefault {
		s.deletingSpaces[id] = struct{}{}
		for terminalID, e := range s.view {
			if e.record.SpaceID == id {
				terminals = append(terminals, terminalID)
			}
		}
	}
	s.mu.Unlock()
	s.ops.Unlock()
	if !ok {
		return ErrSpaceNotFound
	}
	if record.IsDefault {
		return ErrDefaultSpace
	}

	sort.Strings(terminals)
	var failures []error
	for _, terminalID := range terminals {
		if err := deleteTerminal(ctx, terminalID); err != nil && !errors.Is(err, ErrNotFound) {
			failures = append(failures, fmt.Errorf("deleting terminal %s: %w", terminalID, err))
		}
	}
	if len(failures) > 0 {
		return errors.Join(failures...)
	}

	s.ops.Lock()
	defer s.ops.Unlock()
	deleted, err := s.spaces.Delete(ctx, id)
	if errors.Is(err, store.ErrForeignKeyViolation) {
		// Impossible while the deleting mark holds creates and moves off;
		// the backstop still answers honestly.
		return fmt.Errorf("space %s still owns terminals", id)
	}
	if err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.deletingSpaces, id)
	delete(s.spaceView, id)
	s.mu.Unlock()
	if !deleted {
		return ErrSpaceNotFound
	}
	s.hub.Publish(api.EventSpaceDeleted, spaceResource, id)
	return nil
}

// liveSpace resolves a space a terminal may join or leave: a known space
// that is not being deleted. Callers hold ops.
func (s *Service) liveSpace(id string) (store.SpaceRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record, ok := s.spaceView[id]
	if !ok {
		return store.SpaceRecord{}, fmt.Errorf("%w: %q", ErrSpaceNotFound, id)
	}
	if _, deleting := s.deletingSpaces[id]; deleting {
		return store.SpaceRecord{}, fmt.Errorf("%w: %s", ErrSpaceDeleting, id)
	}
	return record, nil
}

func space(record store.SpaceRecord) api.Space {
	return api.Space{
		ID:        record.ID,
		Name:      record.Name,
		Directory: record.Directory,
		IsDefault: record.IsDefault,
		CreatedAt: record.CreatedAt,
		UpdatedAt: record.UpdatedAt,
	}
}
