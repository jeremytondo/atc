// Package artifacts is the Artifacts domain (ATC-318): published documents
// as an artifact of mutable metadata over a history of immutable
// versions. Metadata and history live in the store; each version's
// static build and source snapshot live on disk under Root, placed
// complete before the version row that makes them visible exists, so a
// crash at any point leaves the previous current version intact and the
// leftovers reconciled on the next start. Mutations are serialized so
// the base-version check and the append cannot interleave; the content
// upload itself is validated and staged outside the lock.
package artifacts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/store"
)

const (
	resource = "artifact"
	idPrefix = "artf-"
	// stagingDir holds publications being assembled; anything left in it
	// after a restart is an interrupted publication and is discarded.
	stagingDir = ".staging"
	// buildDir and sourceFile are a version's two snapshots.
	buildDir   = "build"
	sourceFile = "source.tar.gz"
	// indexFile is what a build must contain to be servable.
	indexFile = "index.html"
)

var (
	// ErrNotFound: no such artifact (404).
	ErrNotFound = errors.New("artifact not found")
	// ErrVersionNotFound: the artifact exists but not that version (404).
	ErrVersionNotFound = errors.New("artifact version not found")
	// ErrProjectUnknown refuses assigning an artifact to a project with no
	// record (422).
	ErrProjectUnknown = errors.New("unknown project")
	// ErrInvalidUpdate refuses a merge patch that changes nothing or sets
	// a field to a value it cannot take (422).
	ErrInvalidUpdate = errors.New("invalid update")
	// ErrPublicationTaken refuses a publication id already committed to a
	// different artifact (409).
	ErrPublicationTaken = errors.New("publication id belongs to another artifact")
)

// StaleBaseError refuses a publication based on a version that is no
// longer current (409); the publisher must catch up to Current.
type StaleBaseError struct {
	Base    int
	Current int
}

func (e *StaleBaseError) Error() string {
	return fmt.Sprintf("base version %d is not current: version %d is", e.Base, e.Current)
}

// PackageError refuses an upload whose archives are unusable (422):
// unsafe paths, special files, missing content, or exceeded limits.
type PackageError struct {
	Detail string
}

func (e *PackageError) Error() string { return e.Detail }

// Limits bound what one publication may contain, per archive.
type Limits struct {
	MaxFiles      int
	MaxFileBytes  int64
	MaxTotalBytes int64
}

// DefaultLimits are the production bounds: generous for a static site,
// far below anything that could exhaust the host.
var DefaultLimits = Limits{MaxFiles: 4096, MaxFileBytes: 64 << 20, MaxTotalBytes: 256 << 20}

// Options wires the service.
type Options struct {
	Repository *store.Artifacts
	Hub        *events.Hub
	// Root is the content directory (paths.ArtifactDir); created if
	// missing.
	Root string
	// Bases reports the document origin's local and tailnet base URLs
	// (tailnet "" when not exposed) for the reader links on every
	// artifact and version; nil renders paths only.
	Bases func() (local, tailnet string)
	// Limits zero means DefaultLimits.
	Limits Limits
	// Now is the clock; nil means time.Now.
	Now    func() time.Time
	Logger *slog.Logger
}

// Service is the domain service.
type Service struct {
	repository *store.Artifacts
	hub        *events.Hub
	root       string
	bases      func() (string, string)
	limits     Limits
	now        func() time.Time
	logger     *slog.Logger

	// ops serializes mutations: the current-version check, the content
	// placement, and the row append are one critical section.
	ops sync.Mutex
}

// New builds the service and reconciles Root against the store:
// interrupted stagings and content whose rows never committed (or were
// deleted) are removed, so what is on disk is exactly what is visible.
func New(ctx context.Context, opts Options) (*Service, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Bases == nil {
		opts.Bases = func() (string, string) { return "", "" }
	}
	if opts.Limits == (Limits{}) {
		opts.Limits = DefaultLimits
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{
		repository: opts.Repository, hub: opts.Hub, root: opts.Root, bases: opts.Bases,
		limits: opts.Limits, now: opts.Now, logger: opts.Logger,
	}
	if err := os.MkdirAll(filepath.Join(s.root, stagingDir), 0o700); err != nil {
		return nil, err
	}
	if err := s.reconcile(ctx); err != nil {
		return nil, fmt.Errorf("reconciling artifact content: %w", err)
	}
	return s, nil
}

// ReaderPath is the document origin path of an artifact's main link
// (version 0) or of one fixed version.
func ReaderPath(artifactID string, version int) string {
	if version == 0 {
		return "/a/" + artifactID + "/"
	}
	return "/a/" + artifactID + "/v/" + strconv.Itoa(version) + "/"
}

// Publish commits one publication. With artifactID empty it creates an
// artifact whose first version is the upload; otherwise it appends a
// version to that artifact, requiring params.BaseVersion to be current.
// params.RestoreFrom republishes a stored version's snapshots instead
// (build and source must then be nil). A publication id already
// committed returns that result with replayed true and changes nothing.
func (s *Service) Publish(ctx context.Context, artifactID string, params api.ArtifactPublishParams, build, source io.Reader) (api.ArtifactPublication, bool, error) {
	params.Title = strings.TrimSpace(params.Title)
	if params.Title == "" {
		return api.ArtifactPublication{}, false, &PackageError{Detail: "title must not be blank"}
	}
	if params.PublicationID == "" {
		return api.ArtifactPublication{}, false, &PackageError{Detail: "publicationId is required"}
	}
	restore := params.RestoreFrom > 0
	switch {
	case restore && artifactID == "":
		return api.ArtifactPublication{}, false, &PackageError{Detail: "restoreFrom needs an existing artifact"}
	case restore && (build != nil || source != nil):
		return api.ArtifactPublication{}, false, &PackageError{Detail: "a restoration takes no archives"}
	case !restore && (build == nil || source == nil):
		return api.ArtifactPublication{}, false, &PackageError{Detail: "build and source archives are required"}
	}

	// A completed publication answers without touching the upload.
	if result, ok, err := s.replay(ctx, artifactID, params.PublicationID); err != nil || ok {
		return result, ok, err
	}
	// A stale base fails before the upload is processed; the check under
	// the lock is the one that counts.
	if artifactID != "" {
		artifact, ok, err := s.repository.Get(ctx, artifactID)
		if err != nil {
			return api.ArtifactPublication{}, false, err
		}
		if !ok {
			return api.ArtifactPublication{}, false, ErrNotFound
		}
		if params.BaseVersion != artifact.CurrentVersion {
			return api.ArtifactPublication{}, false, &StaleBaseError{Base: params.BaseVersion, Current: artifact.CurrentVersion}
		}
	}

	var staged string
	var err error
	if !restore {
		if staged, err = s.stage(build, source); err != nil {
			return api.ArtifactPublication{}, false, err
		}
	}
	// Whatever happens below, a staging left behind is removed; a
	// committed one has been renamed away and this is a no-op.
	defer func() {
		if staged != "" {
			_ = os.RemoveAll(staged)
		}
	}()

	s.ops.Lock()
	defer s.ops.Unlock()
	if result, ok, err := s.replay(ctx, artifactID, params.PublicationID); err != nil || ok {
		return result, ok, err
	}
	now := s.now()
	version := store.ArtifactVersionRecord{
		Title: params.Title, Platform: params.Platform, PublishedAt: now, PublicationID: params.PublicationID,
		Thread: params.Source.Thread, Revision: params.Source.Revision, Links: params.Source.Links,
	}
	if artifactID == "" {
		return s.create(ctx, version, staged, now)
	}
	artifact, ok, err := s.repository.Get(ctx, artifactID)
	if err != nil {
		return api.ArtifactPublication{}, false, err
	}
	if !ok {
		return api.ArtifactPublication{}, false, ErrNotFound
	}
	if params.BaseVersion != artifact.CurrentVersion {
		return api.ArtifactPublication{}, false, &StaleBaseError{Base: params.BaseVersion, Current: artifact.CurrentVersion}
	}
	if restore {
		restored, ok, err := s.repository.Version(ctx, artifactID, params.RestoreFrom)
		if err != nil {
			return api.ArtifactPublication{}, false, err
		}
		if !ok {
			return api.ArtifactPublication{}, false, ErrVersionNotFound
		}
		// The frozen snapshots and the platform that produced them come
		// along unchanged; only the publication itself is new.
		version.RestoredFrom, version.Platform = params.RestoreFrom, restored.Platform
		if staged, err = s.copyVersion(artifactID, params.RestoreFrom); err != nil {
			return api.ArtifactPublication{}, false, err
		}
	}
	version.ArtifactID = artifactID
	version.Number = artifact.CurrentVersion + 1
	placed, err := s.commit(staged, artifactID, version.Number)
	if err != nil {
		return api.ArtifactPublication{}, false, err
	}
	staged = ""
	if err := s.repository.AppendVersion(ctx, version, artifact.CurrentVersion); err != nil {
		_ = os.RemoveAll(placed)
		var stale *store.StaleBaseError
		switch {
		case errors.As(err, &stale):
			return api.ArtifactPublication{}, false, &StaleBaseError{Base: params.BaseVersion, Current: stale.Current}
		case errors.Is(err, store.ErrArtifactMissing):
			return api.ArtifactPublication{}, false, ErrNotFound
		}
		return api.ArtifactPublication{}, false, err
	}
	s.hub.Publish(api.EventArtifactUpdated, resource, artifactID)
	return s.publication(ctx, artifactID, version.Number)
}

// create mints an artifact around its first version. The content is
// placed under the minted id before the rows exist; an id collision
// takes the content back and re-rolls.
func (s *Service) create(ctx context.Context, version store.ArtifactVersionRecord, staged string, now time.Time) (api.ArtifactPublication, bool, error) {
	version.Number = 1
	for {
		id := ids.New(idPrefix)
		placed, err := s.commit(staged, id, 1)
		if err != nil {
			return api.ArtifactPublication{}, false, err
		}
		version.ArtifactID = id
		inserted, err := s.repository.Create(ctx, store.ArtifactRecord{
			ID: id, Title: version.Title, CreatedAt: now, UpdatedAt: now,
		}, version)
		if err != nil {
			_ = os.RemoveAll(placed)
			return api.ArtifactPublication{}, false, err
		}
		if inserted {
			s.hub.Publish(api.EventArtifactCreated, resource, id)
			return s.publication(ctx, id, 1)
		}
		// Collision: move the content back to staging and try another id.
		if err := os.Rename(placed, staged); err != nil {
			return api.ArtifactPublication{}, false, err
		}
		_ = os.Remove(filepath.Dir(placed))
	}
}

// replay answers a repeated publication id with the version it
// committed; a publication id committed to another artifact is refused.
func (s *Service) replay(ctx context.Context, artifactID, publicationID string) (api.ArtifactPublication, bool, error) {
	version, ok, err := s.repository.VersionByPublication(ctx, publicationID)
	if err != nil || !ok {
		return api.ArtifactPublication{}, false, err
	}
	if artifactID != "" && version.ArtifactID != artifactID {
		return api.ArtifactPublication{}, false, ErrPublicationTaken
	}
	result, _, err := s.publication(ctx, version.ArtifactID, version.Number)
	return result, err == nil, err
}

func (s *Service) publication(ctx context.Context, artifactID string, number int) (api.ArtifactPublication, bool, error) {
	artifact, ok, err := s.repository.Get(ctx, artifactID)
	if err != nil {
		return api.ArtifactPublication{}, false, err
	}
	if !ok {
		return api.ArtifactPublication{}, false, ErrNotFound
	}
	version, ok, err := s.repository.Version(ctx, artifactID, number)
	if err != nil {
		return api.ArtifactPublication{}, false, err
	}
	if !ok {
		return api.ArtifactPublication{}, false, ErrVersionNotFound
	}
	return api.ArtifactPublication{Artifact: s.artifact(artifact), Version: s.version(version)}, false, nil
}

// Get returns one artifact.
func (s *Service) Get(ctx context.Context, id string) (api.Artifact, error) {
	record, ok, err := s.repository.Get(ctx, id)
	if err != nil {
		return api.Artifact{}, err
	}
	if !ok {
		return api.Artifact{}, ErrNotFound
	}
	return s.artifact(record), nil
}

// List returns every artifact in creation order, or projectID's only.
func (s *Service) List(ctx context.Context, projectID string) ([]api.Artifact, error) {
	records, err := s.repository.List(ctx, projectID)
	if err != nil {
		return nil, err
	}
	artifacts := make([]api.Artifact, 0, len(records))
	for _, record := range records {
		artifacts = append(artifacts, s.artifact(record))
	}
	return artifacts, nil
}

// Update applies the merge patch: a new current title, a Project
// assignment, or (null) an unassignment. Archived snapshots and URLs
// are untouched.
func (s *Service) Update(ctx context.Context, id string, params api.ArtifactUpdateParams) (api.Artifact, error) {
	if !params.Title.Set && !params.ProjectID.Set {
		return api.Artifact{}, fmt.Errorf("%w: nothing to change", ErrInvalidUpdate)
	}
	if params.Title.Set && (params.Title.Value == nil || strings.TrimSpace(*params.Title.Value) == "") {
		return api.Artifact{}, fmt.Errorf("%w: title must not be blank", ErrInvalidUpdate)
	}
	if params.ProjectID.Set && params.ProjectID.Value != nil && *params.ProjectID.Value == "" {
		return api.Artifact{}, fmt.Errorf("%w: projectId must name a project or be null", ErrInvalidUpdate)
	}
	s.ops.Lock()
	defer s.ops.Unlock()
	current, ok, err := s.repository.Get(ctx, id)
	if err != nil {
		return api.Artifact{}, err
	}
	if !ok {
		return api.Artifact{}, ErrNotFound
	}
	title, projectID := current.Title, current.ProjectID
	if params.Title.Set {
		title = strings.TrimSpace(*params.Title.Value)
	}
	if params.ProjectID.Set {
		projectID = ""
		if params.ProjectID.Value != nil {
			projectID = *params.ProjectID.Value
		}
	}
	updated, ok, err := s.repository.Update(ctx, id, title, projectID, s.now())
	if errors.Is(err, store.ErrForeignKeyViolation) {
		return api.Artifact{}, fmt.Errorf("%w %q", ErrProjectUnknown, projectID)
	}
	if err != nil {
		return api.Artifact{}, err
	}
	if !ok {
		return api.Artifact{}, ErrNotFound
	}
	s.hub.Publish(api.EventArtifactUpdated, resource, id)
	return s.artifact(updated), nil
}

// Delete removes an artifact, its history, and its content; its reader
// links stop resolving. Working copies elsewhere are untouched, and a
// later publication naming the id fails rather than recreating it.
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
	// Rows first, then content: content without rows is invisible and
	// reconciled away on the next start if this removal is interrupted.
	if err := os.RemoveAll(s.artifactDir(id)); err != nil {
		s.logger.Warn("artifact content not removed", "artifact", id, "error", err)
	}
	s.hub.Publish(api.EventArtifactDeleted, resource, id)
	return nil
}

// Versions returns an artifact's history, oldest first.
func (s *Service) Versions(ctx context.Context, id string) ([]api.ArtifactVersion, error) {
	if _, ok, err := s.repository.Get(ctx, id); err != nil {
		return nil, err
	} else if !ok {
		return nil, ErrNotFound
	}
	records, err := s.repository.Versions(ctx, id)
	if err != nil {
		return nil, err
	}
	versions := make([]api.ArtifactVersion, 0, len(records))
	for _, record := range records {
		versions = append(versions, s.version(record))
	}
	return versions, nil
}

// Version returns one version.
func (s *Service) Version(ctx context.Context, id string, number int) (api.ArtifactVersion, error) {
	record, ok, err := s.repository.Version(ctx, id, number)
	if err != nil {
		return api.ArtifactVersion{}, err
	}
	if !ok {
		if _, found, err := s.repository.Get(ctx, id); err != nil {
			return api.ArtifactVersion{}, err
		} else if !found {
			return api.ArtifactVersion{}, ErrNotFound
		}
		return api.ArtifactVersion{}, ErrVersionNotFound
	}
	return s.version(record), nil
}

// SourcePath is the path of a version's source snapshot (a tar.gz).
func (s *Service) SourcePath(ctx context.Context, id string, number int) (string, error) {
	if _, err := s.Version(ctx, id, number); err != nil {
		return "", err
	}
	return filepath.Join(s.versionDir(id, number), sourceFile), nil
}

// Document is what the reader serves for one artifact at one version:
// the version's build directory plus the current metadata and history
// the reader header shows.
type Document struct {
	Artifact api.Artifact
	Version  api.ArtifactVersion
	// Versions is the artifact's history, oldest first.
	Versions []api.ArtifactVersion
	BuildDir string
}

// Latest reports whether the document is the artifact's current version.
func (d Document) Latest() bool { return d.Version.Number == d.Artifact.CurrentVersion }

// Document resolves an artifact at version number, or at its current
// version for 0.
func (s *Service) Document(ctx context.Context, id string, number int) (Document, error) {
	artifact, err := s.Get(ctx, id)
	if err != nil {
		return Document{}, err
	}
	if number == 0 {
		number = artifact.CurrentVersion
	}
	versions, err := s.Versions(ctx, id)
	if err != nil {
		return Document{}, err
	}
	for _, version := range versions {
		if version.Number == number {
			return Document{Artifact: artifact, Version: version, Versions: versions, BuildDir: filepath.Join(s.versionDir(id, number), buildDir)}, nil
		}
	}
	return Document{}, ErrVersionNotFound
}

func (s *Service) artifactDir(id string) string {
	return filepath.Join(s.root, id)
}

func (s *Service) versionDir(id string, number int) string {
	return filepath.Join(s.root, id, strconv.Itoa(number))
}

func (s *Service) artifact(record store.ArtifactRecord) api.Artifact {
	local, tailnet := s.bases()
	artifact := api.Artifact{
		ID: record.ID, Title: record.Title, ProjectID: record.ProjectID, CurrentVersion: record.CurrentVersion,
		CreatedAt: record.CreatedAt, UpdatedAt: record.UpdatedAt,
		URL: local + ReaderPath(record.ID, 0),
	}
	if tailnet != "" {
		artifact.TailnetURL = tailnet + ReaderPath(record.ID, 0)
	}
	return artifact
}

func (s *Service) version(record store.ArtifactVersionRecord) api.ArtifactVersion {
	local, tailnet := s.bases()
	version := api.ArtifactVersion{
		ArtifactID: record.ArtifactID, Number: record.Number, Title: record.Title, Platform: record.Platform,
		PublishedAt: record.PublishedAt, RestoredFrom: record.RestoredFrom,
		Source: api.ArtifactSource{Thread: record.Thread, Revision: record.Revision, Links: record.Links},
		URL:    local + ReaderPath(record.ArtifactID, record.Number),
	}
	if tailnet != "" {
		version.TailnetURL = tailnet + ReaderPath(record.ArtifactID, record.Number)
	}
	return version
}
