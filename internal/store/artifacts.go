package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/store/gen"
)

// ArtifactRecord is one artifacts row in domain terms. CurrentVersion is
// derived from the version rows (the highest number) — versions are only
// ever appended, so nothing separate can drift.
type ArtifactRecord struct {
	ID    string
	Title string
	// ProjectID is "" when unassigned.
	ProjectID      string
	CurrentVersion int
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// ArtifactVersionRecord is one artifact_versions row in domain terms.
type ArtifactVersionRecord struct {
	ArtifactID  string
	Number      int
	Title       string
	Platform    string
	PublishedAt time.Time
	// RestoredFrom is the version this one copies, 0 for an uploaded build.
	RestoredFrom  int
	PublicationID string
	// Thread, Revision, and Links are the source context as reported.
	Thread   string
	Revision string
	Links    []string
}

// StaleBaseError reports an append whose base is no longer the current
// version; Current is the version the caller must catch up to.
type StaleBaseError struct {
	Current int
}

func (e *StaleBaseError) Error() string {
	return fmt.Sprintf("version %d is current", e.Current)
}

// ErrArtifactMissing marks an append against an artifact that no longer
// exists — a publication racing a whole-artifact deletion.
var ErrArtifactMissing = errors.New("artifact does not exist")

// Artifacts is the repository for artifacts and their versions. Reads go
// to the read pool, mutations to the single-writer pool; db is that
// pool's handle for the multi-statement writes.
type Artifacts struct {
	reads  *gen.Queries
	writes *gen.Queries
	db     *sql.DB
}

// Artifacts returns the artifacts repository.
func (s *Store) Artifacts() *Artifacts {
	return &Artifacts{reads: gen.New(s.reads), writes: gen.New(s.writes), db: s.writes}
}

// Create persists a new artifact with its first version in one
// transaction; false reports an ID collision (the caller re-rolls). A
// project that does not exist surfaces as ErrForeignKeyViolation.
func (a *Artifacts) Create(ctx context.Context, record ArtifactRecord, version ArtifactVersionRecord) (bool, error) {
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	n, err := queries.InsertArtifact(ctx, gen.InsertArtifactParams{
		ID:        record.ID,
		Title:     record.Title,
		ProjectID: nullString(record.ProjectID),
		CreatedAt: formatTime(record.CreatedAt),
		UpdatedAt: formatTime(record.UpdatedAt),
	})
	if err != nil {
		return false, foreignKeyError(err)
	}
	if n == 0 {
		return false, nil
	}
	if err := insertVersion(ctx, queries, version); err != nil {
		return false, err
	}
	return true, tx.Commit()
}

// AppendVersion adds version to its artifact when base is still the
// current version, in one transaction with the artifact's current title
// (the version's) and updated_at:
// a stale base fails with *StaleBaseError naming the current version, a
// deleted artifact with ErrArtifactMissing. version.Number must be
// base + 1 — the caller names the version so its content can be placed
// before the row exists.
func (a *Artifacts) AppendVersion(ctx context.Context, version ArtifactVersionRecord, base int) error {
	if version.Number != base+1 {
		return fmt.Errorf("version %d does not follow base %d", version.Number, base)
	}
	tx, err := a.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	queries := gen.New(tx)
	row, err := queries.GetArtifact(ctx, version.ArtifactID)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrArtifactMissing
	}
	if err != nil {
		return err
	}
	if int(row.CurrentVersion) != base {
		return &StaleBaseError{Current: int(row.CurrentVersion)}
	}
	if err := insertVersion(ctx, queries, version); err != nil {
		return err
	}
	if _, err := queries.RetitleArtifact(ctx, gen.RetitleArtifactParams{
		Title: version.Title, UpdatedAt: formatTime(version.PublishedAt), ID: version.ArtifactID,
	}); err != nil {
		return err
	}
	return tx.Commit()
}

// Get returns one artifact by ID; false means no such artifact.
func (a *Artifacts) Get(ctx context.Context, id string) (ArtifactRecord, bool, error) {
	row, err := a.reads.GetArtifact(ctx, id)
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactRecord{}, false, nil
	}
	if err != nil {
		return ArtifactRecord{}, false, err
	}
	record, err := artifactFrom(row)
	return record, err == nil, err
}

// List returns every artifact in creation order, or only projectID's
// when it is set.
func (a *Artifacts) List(ctx context.Context, projectID string) ([]ArtifactRecord, error) {
	var filter any
	if projectID != "" {
		filter = projectID
	}
	rows, err := a.reads.ListArtifacts(ctx, filter)
	if err != nil {
		return nil, err
	}
	records := make([]ArtifactRecord, 0, len(rows))
	for _, row := range rows {
		record, err := artifactFrom(gen.GetArtifactRow(row))
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// Update writes the title and project association and returns the
// committed artifact in the same statement (RETURNING); false means no
// such artifact. A project that does not exist surfaces as
// ErrForeignKeyViolation.
func (a *Artifacts) Update(ctx context.Context, id, title, projectID string, at time.Time) (ArtifactRecord, bool, error) {
	row, err := a.writes.UpdateArtifact(ctx, gen.UpdateArtifactParams{
		Title: title, ProjectID: nullString(projectID), UpdatedAt: formatTime(at), ID: id,
	})
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactRecord{}, false, nil
	}
	if err != nil {
		return ArtifactRecord{}, false, foreignKeyError(err)
	}
	// sqlc names the derived column positionally in RETURNING.
	record, err := artifactFrom(gen.GetArtifactRow{
		ID: row.ID, Title: row.Title, ProjectID: row.ProjectID, CreatedAt: row.CreatedAt, UpdatedAt: row.UpdatedAt, CurrentVersion: row.Column6,
	})
	return record, err == nil, err
}

// Delete removes the artifact and, by the schema, every version row;
// false means no such artifact.
func (a *Artifacts) Delete(ctx context.Context, id string) (bool, error) {
	n, err := a.writes.DeleteArtifact(ctx, id)
	return n > 0, err
}

// Version returns one version; false means no such artifact or number.
func (a *Artifacts) Version(ctx context.Context, artifactID string, number int) (ArtifactVersionRecord, bool, error) {
	row, err := a.reads.GetArtifactVersion(ctx, gen.GetArtifactVersionParams{ArtifactID: artifactID, Number: int64(number)})
	if errors.Is(err, sql.ErrNoRows) {
		return ArtifactVersionRecord{}, false, nil
	}
	if err != nil {
		return ArtifactVersionRecord{}, false, err
	}
	record, err := versionFrom(row)
	return record, err == nil, err
}

// Publication reports what a publication id committed: the version when
// it still exists, deleted when its artifact has since been removed, and
// neither when the publication never completed.
func (a *Artifacts) Publication(ctx context.Context, publicationID string) (version ArtifactVersionRecord, found, deleted bool, err error) {
	row, err := a.reads.GetArtifactVersionByPublication(ctx, publicationID)
	if err == nil {
		version, err = versionFrom(row)
		return version, err == nil, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return ArtifactVersionRecord{}, false, false, err
	}
	if _, err := a.reads.GetArtifactPublication(ctx, publicationID); err == nil {
		return ArtifactVersionRecord{}, false, true, nil
	} else if !errors.Is(err, sql.ErrNoRows) {
		return ArtifactVersionRecord{}, false, false, err
	}
	return ArtifactVersionRecord{}, false, false, nil
}

// Versions returns an artifact's versions in number order; empty for an
// unknown artifact.
func (a *Artifacts) Versions(ctx context.Context, artifactID string) ([]ArtifactVersionRecord, error) {
	rows, err := a.reads.ListArtifactVersions(ctx, artifactID)
	if err != nil {
		return nil, err
	}
	records := make([]ArtifactVersionRecord, 0, len(rows))
	for _, row := range rows {
		record, err := versionFrom(row)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	return records, nil
}

// VersionKeys returns every stored version as artifact id → numbers, for
// reconciling on-disk content against the database.
func (a *Artifacts) VersionKeys(ctx context.Context) (map[string][]int, error) {
	rows, err := a.reads.ListArtifactVersionKeys(ctx)
	if err != nil {
		return nil, err
	}
	keys := make(map[string][]int)
	for _, row := range rows {
		keys[row.ArtifactID] = append(keys[row.ArtifactID], int(row.Number))
	}
	return keys, nil
}

// insertVersion writes the version row and its publication record, which
// outlives the version so a repeated publication id is recognized even
// after the artifact is deleted.
func insertVersion(ctx context.Context, queries *gen.Queries, v ArtifactVersionRecord) error {
	links := v.Links
	if links == nil {
		links = []string{}
	}
	encoded, err := json.Marshal(links)
	if err != nil {
		return err
	}
	if _, err := queries.InsertArtifactVersion(ctx, gen.InsertArtifactVersionParams{
		ArtifactID:    v.ArtifactID,
		Number:        int64(v.Number),
		Title:         v.Title,
		Platform:      v.Platform,
		PublishedAt:   formatTime(v.PublishedAt),
		RestoredFrom:  sql.NullInt64{Int64: int64(v.RestoredFrom), Valid: v.RestoredFrom != 0},
		PublicationID: v.PublicationID,
		ThreadID:      nullString(v.Thread),
		Revision:      nullString(v.Revision),
		Links:         string(encoded),
	}); err != nil {
		return err
	}
	_, err = queries.InsertArtifactPublication(ctx, gen.InsertArtifactPublicationParams{
		PublicationID: v.PublicationID, ArtifactID: v.ArtifactID, Number: int64(v.Number),
	})
	return err
}

func artifactFrom(row gen.GetArtifactRow) (ArtifactRecord, error) {
	record := ArtifactRecord{
		ID:             row.ID,
		Title:          row.Title,
		ProjectID:      row.ProjectID.String,
		CurrentVersion: int(row.CurrentVersion),
	}
	var err error
	if record.CreatedAt, err = parseTime(row.CreatedAt); err != nil {
		return ArtifactRecord{}, fmt.Errorf("artifact %s created_at: %w", row.ID, err)
	}
	if record.UpdatedAt, err = parseTime(row.UpdatedAt); err != nil {
		return ArtifactRecord{}, fmt.Errorf("artifact %s updated_at: %w", row.ID, err)
	}
	return record, nil
}

func versionFrom(row gen.ArtifactVersion) (ArtifactVersionRecord, error) {
	record := ArtifactVersionRecord{
		ArtifactID:    row.ArtifactID,
		Number:        int(row.Number),
		Title:         row.Title,
		Platform:      row.Platform,
		RestoredFrom:  int(row.RestoredFrom.Int64),
		PublicationID: row.PublicationID,
		Thread:        row.ThreadID.String,
		Revision:      row.Revision.String,
	}
	var err error
	if record.PublishedAt, err = parseTime(row.PublishedAt); err != nil {
		return ArtifactVersionRecord{}, fmt.Errorf("artifact %s version %d published_at: %w", row.ArtifactID, row.Number, err)
	}
	if err := json.Unmarshal([]byte(row.Links), &record.Links); err != nil {
		return ArtifactVersionRecord{}, fmt.Errorf("artifact %s version %d links: %w", row.ArtifactID, row.Number, err)
	}
	if len(record.Links) == 0 {
		record.Links = nil
	}
	return record, nil
}
