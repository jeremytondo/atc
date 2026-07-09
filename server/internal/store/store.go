// Package store persists Atelier Code-owned state.
package store

import (
	"bytes"
	"context"
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

const sqliteDriver = "sqlite"

var (
	// ErrSessionNotFound is returned when a session id does not exist.
	ErrSessionNotFound = errors.New("session not found")
	// ErrProjectNotFound is returned when a project id does not exist.
	ErrProjectNotFound = errors.New("project not found")
	// ErrProjectArchived is returned when a session insert references an
	// archived project.
	ErrProjectArchived = errors.New("project is archived")
	// ErrProjectHasActiveSessions is returned when an archive is rejected
	// because the project still has a starting or running session.
	ErrProjectHasActiveSessions = errors.New("project has active sessions")
	// ErrInvalidStatus is returned when a status value is outside the session
	// status vocabulary.
	ErrInvalidStatus = errors.New("invalid session status")
)

// Status is the persisted lifecycle state for a session. Archived state
// is represented by ArchivedAt, not by a status value.
type Status string

const (
	StatusStarting   Status = "starting"
	StatusRunning    Status = "running"
	StatusFailed     Status = "failed"
	StatusTerminated Status = "terminated"
)

// Session is one persisted Atelier Code-owned session record. It is an internal
// domain value; the API layer owns its own wire types and serialization, so this
// struct carries no JSON tags.
type Session struct {
	ID            string
	Name          string
	Action        string
	Environment   string
	Params        json.RawMessage
	WorkingDir    string
	Prompt        string
	Status        Status
	FailureReason string
	FailureCode   string
	ProjectID     string
	Project       *SessionProject
	CreatedAt     time.Time
	UpdatedAt     time.Time
	TerminatedAt  *time.Time
	ArchivedAt    *time.Time
}

// SessionProject is the project slice hydrated onto sessions that belong to
// one, loaded through a LEFT JOIN on reads.
type SessionProject struct {
	ID         string
	Name       string
	WorkingDir string
	ArchivedAt *time.Time
}

// CreateSessionInput is the metadata stored when a start request is accepted
// but before the multiplexer launch result is known.
type CreateSessionInput struct {
	ID          string
	Name        string
	Action      string
	Environment string
	Params      json.RawMessage
	WorkingDir  string
	Prompt      string
	// ProjectID associates the session with a project; empty means unscoped.
	ProjectID string
}

// ListFilter controls session list queries. Archived sessions are hidden by
// default; Status and ProjectID are optional.
type ListFilter struct {
	IncludeArchived bool
	Status          Status
	// ProjectID restricts the list to one project's sessions when set.
	ProjectID string
}

// Store owns the SQLite connection and migration lifecycle.
type Store struct {
	db  *sql.DB
	now func() time.Time
}

// Tx is the session-store transaction handle passed to WithTx callbacks.
type Tx struct {
	q queries
}

type runner interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

type queries struct {
	runner runner
	now    func() time.Time
}

// Open creates the database parent directory, opens the SQLite database, and
// applies all embedded migrations.
func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("open store: database path is required")
	}
	if err := ensureParentDir(filepath.Clean(path)); err != nil {
		return nil, err
	}

	db, err := sql.Open(sqliteDriver, sqliteDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open store database %s: %w", path, err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, fmt.Errorf("open store database %s: %w", path, err)
	}
	if err := migrate(context.Background(), db); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate store database %s: %w", path, err)
	}

	return &Store{db: db, now: time.Now}, nil
}

// Close closes the underlying database connection.
func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

// WithTx runs fn in one database transaction, committing on nil error and
// rolling back otherwise.
func (s *Store) WithTx(ctx context.Context, fn func(*Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin store transaction: %w", err)
	}

	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()

	if err := fn(&Tx{q: queries{runner: tx, now: s.now}}); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit store transaction: %w", err)
	}
	committed = true
	return nil
}

// CreateStarting inserts a new session with status starting.
func (s *Store) CreateStarting(ctx context.Context, input CreateSessionInput) (Session, error) {
	return s.queries().CreateStarting(ctx, input)
}

// MarkRunning records a successful launch.
func (s *Store) MarkRunning(ctx context.Context, id string) (Session, error) {
	return s.queries().MarkRunning(ctx, id)
}

// MarkFailed records a failed launch with a safe reason and machine code.
func (s *Store) MarkFailed(ctx context.Context, id, reason, code string) (Session, error) {
	return s.queries().MarkFailed(ctx, id, reason, code)
}

// MarkTerminated marks a session terminated and sets terminatedAt once.
func (s *Store) MarkTerminated(ctx context.Context, id string) (Session, error) {
	return s.queries().MarkTerminated(ctx, id)
}

// MarkArchived archives a session without changing its status.
func (s *Store) MarkArchived(ctx context.Context, id string) (Session, error) {
	return s.queries().MarkArchived(ctx, id)
}

// Get loads a session by id.
func (s *Store) Get(ctx context.Context, id string) (Session, error) {
	return s.queries().Get(ctx, id)
}

// List loads sessions newest-first. Archived records are excluded unless
// IncludeArchived is true.
func (s *Store) List(ctx context.Context, filter ListFilter) ([]Session, error) {
	return s.queries().List(ctx, filter)
}

func (tx *Tx) CreateStarting(ctx context.Context, input CreateSessionInput) (Session, error) {
	return tx.q.CreateStarting(ctx, input)
}

func (tx *Tx) MarkRunning(ctx context.Context, id string) (Session, error) {
	return tx.q.MarkRunning(ctx, id)
}

func (tx *Tx) MarkFailed(ctx context.Context, id, reason, code string) (Session, error) {
	return tx.q.MarkFailed(ctx, id, reason, code)
}

func (tx *Tx) MarkTerminated(ctx context.Context, id string) (Session, error) {
	return tx.q.MarkTerminated(ctx, id)
}

func (tx *Tx) MarkArchived(ctx context.Context, id string) (Session, error) {
	return tx.q.MarkArchived(ctx, id)
}

func (tx *Tx) Get(ctx context.Context, id string) (Session, error) {
	return tx.q.Get(ctx, id)
}

func (tx *Tx) List(ctx context.Context, filter ListFilter) ([]Session, error) {
	return tx.q.List(ctx, filter)
}

func (s *Store) queries() queries {
	return queries{runner: s.db, now: s.now}
}

func (q queries) CreateStarting(ctx context.Context, input CreateSessionInput) (Session, error) {
	params, err := normalizeParams(input.Params)
	if err != nil {
		return Session{}, err
	}
	now := q.nowUTC()
	args := []any{
		input.ID,
		nullableString(input.Name),
		input.Action,
		input.Environment,
		params,
		input.WorkingDir,
		nullableString(input.Prompt),
		StatusStarting,
		nullableString(input.ProjectID),
		formatTime(now),
		formatTime(now),
	}
	query := `
	INSERT INTO sessions (
		id, name, action, environment, params, working_dir, prompt, status, project_id, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	RETURNING` + sessionColumnsSQL
	if input.ProjectID != "" {
		// The single-statement guard makes the insert atomic with the project
		// check, so a start racing a project archive cannot leave an active
		// session under an archived project (the mirror of the archive-side
		// active-session check).
		query = `
	INSERT INTO sessions (
		id, name, action, environment, params, working_dir, prompt, status, project_id, created_at, updated_at
	)
	SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
	WHERE EXISTS (SELECT 1 FROM projects WHERE id = ? AND archived_at IS NULL)
	RETURNING` + sessionColumnsSQL
		args = append(args, input.ProjectID)
	}
	created, err := q.scanOne(q.runner.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) && input.ProjectID != "" {
		return Session{}, q.classifyProjectGuardFailure(ctx, input.ProjectID)
	}
	if err != nil {
		return Session{}, fmt.Errorf("create starting session %s: %w", input.ID, err)
	}
	return q.hydrateProject(ctx, created)
}

// classifyProjectGuardFailure explains a guarded session insert that matched
// no project row. The guard itself is what prevents the bad insert; this
// follow-up read only picks the right error.
func (q queries) classifyProjectGuardFailure(ctx context.Context, projectID string) error {
	project, err := scanProject(q.runner.QueryRowContext(ctx, selectProjectSQL+` WHERE id = ?`, projectID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %s", ErrProjectNotFound, projectID)
	case err != nil:
		return fmt.Errorf("resolve project %s: %w", projectID, err)
	case project.ArchivedAt != nil:
		return fmt.Errorf("%w: %s", ErrProjectArchived, projectID)
	default:
		return fmt.Errorf("create session in project %s: insert matched no row", projectID)
	}
}

func (q queries) MarkRunning(ctx context.Context, id string) (Session, error) {
	now := q.nowUTC()
	return q.updateOne(ctx, id, "mark running", `
	UPDATE sessions
	SET status = ?,
		failure_reason = NULL,
		failure_code = NULL,
		updated_at = ?
	WHERE id = ?
	RETURNING`+sessionColumnsSQL,
		StatusRunning, formatTime(now), id,
	)
}

func (q queries) MarkFailed(ctx context.Context, id, reason, code string) (Session, error) {
	if strings.TrimSpace(reason) == "" {
		return Session{}, errors.New("mark session failed: failure reason is required")
	}
	if strings.TrimSpace(code) == "" {
		return Session{}, errors.New("mark session failed: failure code is required")
	}
	now := q.nowUTC()
	return q.updateOne(ctx, id, "mark failed", `
	UPDATE sessions
	SET status = ?, failure_reason = ?, failure_code = ?, updated_at = ?
	WHERE id = ?
	RETURNING`+sessionColumnsSQL,
		StatusFailed, reason, code, formatTime(now), id,
	)
}

func (q queries) MarkTerminated(ctx context.Context, id string) (Session, error) {
	now := q.nowUTC()
	return q.updateOne(ctx, id, "mark terminated", `
	UPDATE sessions
	SET status = ?,
		terminated_at = COALESCE(terminated_at, ?),
		updated_at = CASE WHEN terminated_at IS NULL THEN ? ELSE updated_at END
	WHERE id = ?
	RETURNING`+sessionColumnsSQL,
		StatusTerminated, formatTime(now), formatTime(now), id,
	)
}

func (q queries) MarkArchived(ctx context.Context, id string) (Session, error) {
	now := q.nowUTC()
	return q.updateOne(ctx, id, "mark archived", `
	UPDATE sessions
	SET archived_at = COALESCE(archived_at, ?),
		updated_at = CASE WHEN archived_at IS NULL THEN ? ELSE updated_at END
	WHERE id = ?
	RETURNING`+sessionColumnsSQL,
		formatTime(now), formatTime(now), id,
	)
}

func (q queries) Get(ctx context.Context, id string) (Session, error) {
	session, err := scanJoinedSession(q.runner.QueryRowContext(ctx, selectSessionSQL+` WHERE s.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if err != nil {
		return Session{}, fmt.Errorf("get session %s: %w", id, err)
	}
	return session, nil
}

func (q queries) List(ctx context.Context, filter ListFilter) ([]Session, error) {
	if filter.Status != "" && !filter.Status.valid() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidStatus, filter.Status)
	}

	clauses := make([]string, 0, 3)
	args := make([]any, 0, 2)
	if !filter.IncludeArchived {
		clauses = append(clauses, "s.archived_at IS NULL")
	}
	if filter.Status != "" {
		clauses = append(clauses, "s.status = ?")
		args = append(args, filter.Status)
	}
	if filter.ProjectID != "" {
		clauses = append(clauses, "s.project_id = ?")
		args = append(args, filter.ProjectID)
	}

	query := selectSessionSQL
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	// rowid breaks created_at ties by insertion order so sessions created in the
	// same instant still list newest-first (id is random and would not).
	query += " ORDER BY s.created_at DESC, s.rowid DESC"

	rows, err := q.runner.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	sessions := make([]Session, 0)
	for rows.Next() {
		session, err := scanJoinedSession(rows)
		if err != nil {
			return nil, fmt.Errorf("list sessions: %w", err)
		}
		sessions = append(sessions, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

func (q queries) updateOne(ctx context.Context, id, action, query string, args ...any) (Session, error) {
	session, err := q.scanOne(q.runner.QueryRowContext(ctx, query, args...))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if err != nil {
		return Session{}, fmt.Errorf("%s session %s: %w", action, id, err)
	}
	return q.hydrateProject(ctx, session)
}

func (q queries) scanOne(row *sql.Row) (Session, error) {
	return scanSession(row)
}

// hydrateProject attaches the project slice to a session returned by a write.
// Writes use RETURNING, which cannot join, so the project row is loaded with a
// follow-up query; reads join instead.
func (q queries) hydrateProject(ctx context.Context, session Session) (Session, error) {
	if session.ProjectID == "" {
		return session, nil
	}
	project, err := scanProject(q.runner.QueryRowContext(ctx, selectProjectSQL+` WHERE id = ?`, session.ProjectID))
	if err != nil {
		return Session{}, fmt.Errorf("hydrate project for session %s: %w", session.ID, err)
	}
	session.Project = &SessionProject{
		ID:         project.ID,
		Name:       project.Name,
		WorkingDir: project.WorkingDir,
		ArchivedAt: project.ArchivedAt,
	}
	return session, nil
}

func (q queries) nowUTC() time.Time {
	now := time.Now
	if q.now != nil {
		now = q.now
	}
	return now().UTC().Round(0)
}

// sessionColumnsSQL is the bare column list used with RETURNING on writes;
// sessionJoinColumnsSQL is the s.-qualified equivalent (plus joined project
// columns) used by reads, which LEFT JOIN projects.
const sessionColumnsSQL = `
		id,
		COALESCE(name, ''),
		action,
		environment,
		params,
		working_dir,
		COALESCE(prompt, ''),
		status,
		COALESCE(failure_reason, ''),
		COALESCE(failure_code, ''),
		COALESCE(project_id, ''),
		created_at,
		updated_at,
		terminated_at,
		archived_at`

const sessionJoinColumnsSQL = `
		s.id,
		COALESCE(s.name, ''),
		s.action,
		s.environment,
		s.params,
		s.working_dir,
		COALESCE(s.prompt, ''),
		s.status,
		COALESCE(s.failure_reason, ''),
		COALESCE(s.failure_code, ''),
		COALESCE(s.project_id, ''),
		s.created_at,
		s.updated_at,
		s.terminated_at,
		s.archived_at,
		p.name,
		p.working_dir,
		p.archived_at`

const selectSessionSQL = `
SELECT` + sessionJoinColumnsSQL + `
	FROM sessions s
	LEFT JOIN projects p ON p.id = s.project_id`

type scanner interface {
	Scan(...any) error
}

func scanSession(row scanner) (Session, error) {
	session, err := scanSessionColumns(row)
	if err != nil {
		return Session{}, err
	}
	return session, nil
}

func scanJoinedSession(row scanner) (Session, error) {
	var projectName, projectWorkingDir, projectArchivedAt sql.NullString
	session, err := scanSessionColumns(row, &projectName, &projectWorkingDir, &projectArchivedAt)
	if err != nil {
		return Session{}, err
	}
	if session.ProjectID != "" {
		archivedAt, err := parseOptionalTime(projectArchivedAt)
		if err != nil {
			return Session{}, fmt.Errorf("parse project archived_at: %w", err)
		}
		session.Project = &SessionProject{
			ID:         session.ProjectID,
			Name:       projectName.String,
			WorkingDir: projectWorkingDir.String,
			ArchivedAt: archivedAt,
		}
	}
	return session, nil
}

func scanSessionColumns(row scanner, extra ...any) (Session, error) {
	var session Session
	var params string
	var createdAt, updatedAt string
	var terminatedAt, archivedAt sql.NullString
	dests := []any{
		&session.ID,
		&session.Name,
		&session.Action,
		&session.Environment,
		&params,
		&session.WorkingDir,
		&session.Prompt,
		&session.Status,
		&session.FailureReason,
		&session.FailureCode,
		&session.ProjectID,
		&createdAt,
		&updatedAt,
		&terminatedAt,
		&archivedAt,
	}
	if err := row.Scan(append(dests, extra...)...); err != nil {
		return Session{}, err
	}

	var err error
	session.CreatedAt, err = parseTime(createdAt)
	if err != nil {
		return Session{}, fmt.Errorf("parse created_at: %w", err)
	}
	session.UpdatedAt, err = parseTime(updatedAt)
	if err != nil {
		return Session{}, fmt.Errorf("parse updated_at: %w", err)
	}
	session.TerminatedAt, err = parseOptionalTime(terminatedAt)
	if err != nil {
		return Session{}, fmt.Errorf("parse terminated_at: %w", err)
	}
	session.ArchivedAt, err = parseOptionalTime(archivedAt)
	if err != nil {
		return Session{}, fmt.Errorf("parse archived_at: %w", err)
	}
	session.Params = json.RawMessage(params)
	return session, nil
}

func migrate(ctx context.Context, db *sql.DB) error {
	migrationFS, err := fs.Sub(migrations, "migrations")
	if err != nil {
		return err
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationFS, goose.WithLogger(goose.NopLogger()))
	if err != nil {
		return err
	}
	_, err = provider.Up(ctx)
	return err
}

func ensureParentDir(path string) error {
	parent := filepath.Dir(path)
	if parent == "." || parent == "" {
		return fmt.Errorf("database path %q must include a parent directory", path)
	}
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return fmt.Errorf("create store directory %s: %w", parent, err)
	}
	return nil
}

func sqliteDSN(path string) string {
	u := url.URL{Scheme: "file", Path: path}
	q := url.Values{}
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	u.RawQuery = q.Encode()
	return u.String()
}

func normalizeParams(params json.RawMessage) (string, error) {
	if len(bytes.TrimSpace(params)) == 0 {
		return "{}", nil
	}
	var compacted bytes.Buffer
	if err := json.Compact(&compacted, params); err != nil {
		return "", fmt.Errorf("params must be valid JSON: %w", err)
	}
	raw := compacted.Bytes()
	if len(raw) == 0 || raw[0] != '{' || raw[len(raw)-1] != '}' {
		return "", errors.New("params must be a JSON object")
	}
	return compacted.String(), nil
}

func nullableString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// timestampLayout stores timestamps as UTC RFC 3339 with a fixed-width
// nine-digit fractional second. The fixed width is load-bearing: timestamps are
// stored as TEXT and ordered with a lexicographic ORDER BY, and only a
// constant-width fraction sorts chronologically. time.RFC3339Nano trims trailing
// zeros, so ".5" would sort before ".09" and a whole second after both.
const timestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func formatTime(t time.Time) string {
	return t.UTC().Round(0).Format(timestampLayout)
}

func parseTime(raw string) (time.Time, error) {
	t, err := time.Parse(timestampLayout, raw)
	if err != nil {
		return time.Time{}, err
	}
	return t.UTC().Round(0), nil
}

func parseOptionalTime(raw sql.NullString) (*time.Time, error) {
	if !raw.Valid {
		return nil, nil
	}
	t, err := parseTime(raw.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

func (s Status) valid() bool {
	switch s {
	case StatusStarting, StatusRunning, StatusFailed, StatusTerminated:
		return true
	default:
		return false
	}
}
