// Package store persists atc-owned state.
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
	// ErrSessionActive is returned when a delete is rejected because the
	// session is still starting or live.
	ErrSessionActive = errors.New("session is active")
	// ErrSessionNotStarting is returned when a starting-only transition
	// (PromoteToLive, DeleteStarting) finds the session already settled.
	ErrSessionNotStarting = errors.New("session is not starting")
	// ErrProjectNotFound is returned when a project id does not exist.
	ErrProjectNotFound = errors.New("project not found")
	// ErrProjectArchived is returned when a write references an archived
	// project.
	ErrProjectArchived = errors.New("project is archived")
	// ErrProjectHasUnarchivedWorkspaces is returned when a project archive is
	// rejected because the project still has an unarchived workspace.
	ErrProjectHasUnarchivedWorkspaces = errors.New("project has unarchived workspaces")
	// ErrProjectHasWorkspaces is returned when a project delete is rejected
	// because the project still has workspaces.
	ErrProjectHasWorkspaces = errors.New("project has workspaces")
	// ErrWorkspaceNotFound is returned when a workspace id does not exist.
	ErrWorkspaceNotFound = errors.New("workspace not found")
	// ErrWorkspaceArchived is returned when a session insert references an
	// archived workspace.
	ErrWorkspaceArchived = errors.New("workspace is archived")
	// ErrWorkspaceHasActiveSessions is returned when a workspace archive or
	// delete is rejected because the workspace still has a starting or live
	// session.
	ErrWorkspaceHasActiveSessions = errors.New("workspace has active sessions")
	// ErrInvalidStatus is returned when a status value is outside the session
	// status vocabulary.
	ErrInvalidStatus = errors.New("invalid session status")
)

// RecordStatus is the internal persisted lifecycle for a session record.
// Starting is provisional and must never serialize as a public Session.
type RecordStatus string

const (
	StatusStarting RecordStatus = "starting"
	StatusLive     RecordStatus = "live"
	StatusEnded    RecordStatus = "ended"
)

// Session is one persisted atc-owned session record. It is an internal
// domain value; the API layer owns its own wire types and serialization, so this
// struct carries no JSON tags.
type Session struct {
	ID   string
	Name string
	// Action is the launch action name; empty means the Interactive Shell.
	Action      string
	Environment string
	Params      json.RawMessage
	WorkingDir  string
	Prompt      string
	Status      RecordStatus
	WorkspaceID string
	Workspace   *SessionWorkspace
	// Project is the derived project reference, reached through the session's
	// workspace, kept so clients that group sessions by project keep working.
	Project   *SessionProject
	CreatedAt time.Time
	UpdatedAt time.Time
}

// SessionWorkspace is the workspace slice hydrated onto sessions, loaded
// through a JOIN on reads.
type SessionWorkspace struct {
	ID   string
	Name string
}

// SessionProject is the derived project slice hydrated onto sessions through
// their workspace.
type SessionProject struct {
	ID   string
	Name string
}

// CreateSessionInput is the metadata stored when a start request is accepted
// but before the multiplexer launch result is known.
type CreateSessionInput struct {
	ID   string
	Name string
	// Action is the launch action name; empty means the Interactive Shell.
	Action      string
	Environment string
	Params      json.RawMessage
	WorkingDir  string
	Prompt      string
	// WorkspaceID scopes the session to a workspace and is required.
	WorkspaceID string
}

// ListFilter controls session list queries. Status, WorkspaceID, and ProjectID
// are optional.
type ListFilter struct {
	Status RecordStatus
	// WorkspaceID restricts the list to one workspace's sessions when set.
	WorkspaceID string
	// ProjectID restricts the list to sessions whose workspace belongs to the
	// project when set.
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
	ExecContext(context.Context, string, ...any) (sql.Result, error)
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

// PromoteToLive records a successful launch. It only settles a starting
// record; a session settled concurrently returns ErrSessionNotStarting.
func (s *Store) PromoteToLive(ctx context.Context, id string) (Session, error) {
	return s.queries().PromoteToLive(ctx, id)
}

// DeleteStarting removes a provisional launch attempt. It never removes a
// public Session.
func (s *Store) DeleteStarting(ctx context.Context, id string) error {
	return s.queries().DeleteStarting(ctx, id)
}

// MarkEnded moves a live record to ended. It is idempotent for an already
// ended record and never changes a provisional record.
func (s *Store) MarkEnded(ctx context.Context, id string) (Session, error) {
	return s.queries().MarkEnded(ctx, id)
}

// DeleteSession removes a session's metadata row. Deleting a starting or live
// session is rejected; the guard and the delete are one statement so
// nothing can slip between them. Files are never touched.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	q := s.queries()
	result, err := q.runner.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND status = ?`, id, StatusEnded)
	if err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete session %s: %w", id, err)
	}
	if affected > 0 {
		return nil
	}
	if _, err := q.Get(ctx, id); err != nil {
		return err
	}
	return fmt.Errorf("%w: %s", ErrSessionActive, id)
}

// CountActiveSessionsForAction reports how many starting or live sessions
// reference the named action, so action deletion can be guarded.
func (s *Store) CountActiveSessionsForAction(ctx context.Context, action string) (int, error) {
	var active int
	if err := s.queries().runner.QueryRowContext(ctx, `
	SELECT count(*) FROM sessions WHERE action = ? AND status IN (?, ?)`,
		action, StatusStarting, StatusLive,
	).Scan(&active); err != nil {
		return 0, fmt.Errorf("count active sessions for action %s: %w", action, err)
	}
	return active, nil
}

// Get loads a session by id.
func (s *Store) Get(ctx context.Context, id string) (Session, error) {
	return s.queries().Get(ctx, id)
}

// List loads public sessions newest-first, excluding provisional records.
func (s *Store) List(ctx context.Context, filter ListFilter) ([]Session, error) {
	return s.queries().List(ctx, filter)
}

// ListAll loads every record, including provisional launch attempts, for
// reconciliation and workspace lifecycle operations.
func (s *Store) ListAll(ctx context.Context, filter ListFilter) ([]Session, error) {
	return s.queries().list(ctx, filter, true)
}

func (tx *Tx) CreateStarting(ctx context.Context, input CreateSessionInput) (Session, error) {
	return tx.q.CreateStarting(ctx, input)
}

func (tx *Tx) PromoteToLive(ctx context.Context, id string) (Session, error) {
	return tx.q.PromoteToLive(ctx, id)
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
	if input.WorkspaceID == "" {
		return Session{}, errors.New("create starting session: workspace id is required")
	}
	params, err := normalizeParams(input.Params)
	if err != nil {
		return Session{}, err
	}
	now := q.nowUTC()
	// The single-statement guard makes the insert atomic with the workspace
	// check, so a start racing a workspace archive or delete cannot leave an
	// active session under an archived or missing workspace (the mirror of
	// the archive/delete-side active-session checks). The workspace guard
	// transitively covers the project: archive preconditions keep every
	// workspace of an archived project archived.
	created, err := q.scanOne(q.runner.QueryRowContext(ctx, `
	INSERT INTO sessions (
		id, name, action, environment, params, working_dir, prompt, status, workspace_id, created_at, updated_at
	)
	SELECT ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
	WHERE EXISTS (SELECT 1 FROM workspaces WHERE id = ? AND archived_at IS NULL)
	RETURNING`+sessionColumnsSQL,
		input.ID,
		nullableString(input.Name),
		nullableString(input.Action),
		input.Environment,
		params,
		input.WorkingDir,
		nullableString(input.Prompt),
		StatusStarting,
		input.WorkspaceID,
		formatTime(now),
		formatTime(now),
		input.WorkspaceID,
	))
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, q.classifyWorkspaceGuardFailure(ctx, input.WorkspaceID)
	}
	if err != nil {
		return Session{}, fmt.Errorf("create starting session %s: %w", input.ID, err)
	}
	return q.hydrateRefs(ctx, created)
}

// classifyProjectGuardFailure explains a guarded workspace insert that
// matched no project row. The guard itself is what prevents the bad insert;
// this follow-up read only picks the right error.
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
		return fmt.Errorf("create workspace in project %s: insert matched no row", projectID)
	}
}

// classifyWorkspaceGuardFailure explains a guarded session insert that
// matched no workspace row, mirroring classifyProjectGuardFailure.
func (q queries) classifyWorkspaceGuardFailure(ctx context.Context, workspaceID string) error {
	workspace, err := scanWorkspace(q.runner.QueryRowContext(ctx, selectWorkspaceSQL+` WHERE id = ?`, workspaceID))
	switch {
	case errors.Is(err, sql.ErrNoRows):
		return fmt.Errorf("%w: %s", ErrWorkspaceNotFound, workspaceID)
	case err != nil:
		return fmt.Errorf("resolve workspace %s: %w", workspaceID, err)
	case workspace.ArchivedAt != nil:
		return fmt.Errorf("%w: %s", ErrWorkspaceArchived, workspaceID)
	default:
		return fmt.Errorf("create session in workspace %s: insert matched no row", workspaceID)
	}
}

func (q queries) PromoteToLive(ctx context.Context, id string) (Session, error) {
	now := q.nowUTC()
	return q.settleStarting(ctx, id, "promote to live", `
	UPDATE sessions
	SET status = ?, updated_at = ?
	WHERE id = ? AND status = ?
	RETURNING`+sessionColumnsSQL,
		StatusLive, formatTime(now), id, StatusStarting,
	)
}

func (q queries) DeleteStarting(ctx context.Context, id string) error {
	result, err := q.runner.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND status = ?`, id, StatusStarting)
	if err != nil {
		return fmt.Errorf("delete provisional session %s: %w", id, err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("delete provisional session %s: %w", id, err)
	}
	if affected > 0 {
		return nil
	}
	if _, err := q.Get(ctx, id); err == nil {
		return fmt.Errorf("%w: %s", ErrSessionNotStarting, id)
	} else {
		return err
	}
}

// settleStarting runs a starting-only transition and separates "the session
// does not exist" from "the session exists but was settled concurrently", so
// an in-flight Start can tell it lost the provisional record concurrently.
func (q queries) settleStarting(ctx context.Context, id, action, query string, args ...any) (Session, error) {
	session, err := q.updateOne(ctx, id, action, query, args...)
	if errors.Is(err, ErrSessionNotFound) {
		if _, getErr := q.Get(ctx, id); getErr == nil {
			return Session{}, fmt.Errorf("%w: %s", ErrSessionNotStarting, id)
		}
	}
	return session, err
}

func (q queries) MarkEnded(ctx context.Context, id string) (Session, error) {
	now := q.nowUTC()
	record, err := q.updateOne(ctx, id, "mark ended", `
	UPDATE sessions
	SET status = ?,
		updated_at = CASE WHEN status = ? THEN ? ELSE updated_at END
	WHERE id = ? AND status IN (?, ?)
	RETURNING`+sessionColumnsSQL,
		StatusEnded, StatusLive, formatTime(now), id, StatusLive, StatusEnded,
	)
	if errors.Is(err, ErrSessionNotFound) {
		if existing, getErr := q.Get(ctx, id); getErr == nil && existing.Status == StatusStarting {
			return Session{}, fmt.Errorf("%w: %s", ErrSessionActive, id)
		}
	}
	return record, err
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
	return q.list(ctx, filter, false)
}

func (q queries) list(ctx context.Context, filter ListFilter, includeStarting bool) ([]Session, error) {
	if filter.Status != "" && !filter.Status.valid() {
		return nil, fmt.Errorf("%w: %s", ErrInvalidStatus, filter.Status)
	}

	clauses := make([]string, 0, 4)
	args := make([]any, 0, 2)
	if !includeStarting {
		clauses = append(clauses, "s.status != ?")
		args = append(args, StatusStarting)
	}
	if filter.Status != "" {
		clauses = append(clauses, "s.status = ?")
		args = append(args, filter.Status)
	}
	if filter.WorkspaceID != "" {
		clauses = append(clauses, "s.workspace_id = ?")
		args = append(args, filter.WorkspaceID)
	}
	if filter.ProjectID != "" {
		clauses = append(clauses, "w.project_id = ?")
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
	return q.hydrateRefs(ctx, session)
}

func (q queries) scanOne(row *sql.Row) (Session, error) {
	return scanSession(row)
}

// hydrateRefs attaches the workspace and derived project slices to a session
// returned by a write. Writes use RETURNING, which cannot join, so the rows
// are loaded with a follow-up query; reads join instead.
func (q queries) hydrateRefs(ctx context.Context, session Session) (Session, error) {
	var workspaceName, projectID, projectName string
	err := q.runner.QueryRowContext(ctx, `
	SELECT w.name, p.id, p.name
	FROM workspaces w
	JOIN projects p ON p.id = w.project_id
	WHERE w.id = ?`, session.WorkspaceID,
	).Scan(&workspaceName, &projectID, &projectName)
	if err != nil {
		return Session{}, fmt.Errorf("hydrate workspace for session %s: %w", session.ID, err)
	}
	session.Workspace = &SessionWorkspace{ID: session.WorkspaceID, Name: workspaceName}
	session.Project = &SessionProject{ID: projectID, Name: projectName}
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
// sessionJoinColumnsSQL is the s.-qualified equivalent (plus joined workspace
// and project columns) used by reads, which JOIN workspaces and projects.
const sessionColumnsSQL = `
		id,
		COALESCE(name, ''),
		COALESCE(action, ''),
		environment,
		params,
		working_dir,
		COALESCE(prompt, ''),
		status,
		workspace_id,
		created_at,
		updated_at`

const sessionJoinColumnsSQL = `
		s.id,
		COALESCE(s.name, ''),
		COALESCE(s.action, ''),
		s.environment,
		s.params,
		s.working_dir,
		COALESCE(s.prompt, ''),
		s.status,
		s.workspace_id,
		s.created_at,
		s.updated_at,
		w.name,
		p.id,
		p.name`

const selectSessionSQL = `
SELECT` + sessionJoinColumnsSQL + `
	FROM sessions s
	JOIN workspaces w ON w.id = s.workspace_id
	JOIN projects p ON p.id = w.project_id`

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
	var workspaceName, projectID, projectName string
	session, err := scanSessionColumns(row, &workspaceName, &projectID, &projectName)
	if err != nil {
		return Session{}, err
	}
	session.Workspace = &SessionWorkspace{ID: session.WorkspaceID, Name: workspaceName}
	session.Project = &SessionProject{ID: projectID, Name: projectName}
	return session, nil
}

func scanSessionColumns(row scanner, extra ...any) (Session, error) {
	var session Session
	var params string
	var createdAt, updatedAt string
	dests := []any{
		&session.ID,
		&session.Name,
		&session.Action,
		&session.Environment,
		&params,
		&session.WorkingDir,
		&session.Prompt,
		&session.Status,
		&session.WorkspaceID,
		&createdAt,
		&updatedAt,
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

func (s RecordStatus) valid() bool {
	switch s {
	case StatusStarting, StatusLive, StatusEnded:
		return true
	default:
		return false
	}
}
