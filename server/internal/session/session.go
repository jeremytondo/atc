// Package session owns atc session semantics on top of persisted metadata
// and the multiplexer boundary. Public callers use atc-owned session ids;
// zmx names are derived internally and are never persisted or exposed.
package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/zmx"
)

const (
	// CodeLaunchFailed is stored and returned when the multiplexer launch fails
	// after a durable session record exists.
	CodeLaunchFailed = "launch_failed"

	launchFailedReason      = "action failed to launch"
	startupIncompleteReason = "session startup did not complete"
)

// Status is the persisted lifecycle state for a session.
type Status = store.Status

const (
	StatusStarting   = store.StatusStarting
	StatusRunning    = store.StatusRunning
	StatusFailed     = store.StatusFailed
	StatusTerminated = store.StatusTerminated
)

// Sentinel errors let callers (notably the API) map failures to stable status
// codes.
var (
	ErrUnknownKey      = errors.New("unknown key")
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionNotLive  = errors.New("session is not live")
	ErrSessionLive     = errors.New("session is live")
	ErrInvalidStatus   = store.ErrInvalidStatus
	// ErrInvalidWorkingDir is the project package's working-directory rule
	// (absolute, exists, is a directory), re-exported so session callers keep
	// one import. A bad directory fails fast instead of surfacing later as a
	// multiplexer launch failure.
	ErrInvalidWorkingDir = project.ErrInvalidWorkingDir
)

// LaunchError is returned when zmx launch fails after atc has already
// created the starting record. Error returns the safe user-facing reason; Err is
// kept only for logging and wrapping.
type LaunchError struct {
	SessionID   string
	FailureCode string
	Message     string
	Err         error
}

func (e *LaunchError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return launchFailedReason
}

func (e *LaunchError) Unwrap() error {
	return e.Err
}

// StartInput is the accepted shape for starting a new session. Exactly one of
// WorkingDir and ProjectID must be set; the API rejects the combination
// before the domain sees it.
type StartInput struct {
	Action      string
	Environment string
	Params      map[string]any
	WorkingDir  string
	Prompt      string
	Name        string
	// ProjectID scopes the session to a project, inheriting its working
	// directory.
	ProjectID string
}

// Session is atc's full domain model for a session. API list/detail
// serializers decide which fields are exposed on each endpoint.
type Session struct {
	ID            string
	Name          string
	Action        string
	Environment   string
	Params        map[string]any
	WorkingDir    string
	Prompt        string
	Status        Status
	FailureReason string
	FailureCode   string
	ProjectID     string
	Project       *ProjectRef
	CreatedAt     time.Time
	UpdatedAt     time.Time
	TerminatedAt  *time.Time
	ArchivedAt    *time.Time
	Attachable    bool
}

// ProjectRef is the project slice carried on project-scoped sessions.
type ProjectRef struct {
	ID         string
	Name       string
	WorkingDir string
	ArchivedAt *time.Time
}

// Multiplexer is the slice of internal/zmx the session domain depends on. It is
// an interface so the domain can be tested with a faked wrapper.
type Multiplexer interface {
	Start(ctx context.Context, name, dir string, argv []string) error
	Send(ctx context.Context, name string, payload []byte) error
	Attach(ctx context.Context, name string, rows, cols uint16) (zmx.PTY, error)
	List(ctx context.Context) ([]zmx.Session, error)
	Terminate(ctx context.Context, name string) error
}

// ActionLoader loads the launchable Action registry at point of use.
type ActionLoader interface {
	Load(ctx context.Context) (ActionRegistry, error)
}

// ProjectResolver resolves the project a start request references. It is an
// interface (following the ActionLoader precedent) so tests can fake it.
type ProjectResolver interface {
	ResolveForStart(ctx context.Context, id string) (project.Project, error)
}

// Service implements session operations on top of durable metadata and a
// Multiplexer.
type Service struct {
	store        *store.Store
	mux          Multiplexer
	actions      ActionLoader
	environments EnvironmentRegistry
	projects     ProjectResolver
	logger       *slog.Logger
}

// NewService returns a Service backed by st and mux that launches configured
// actions in configured environments. A nil projects resolver rejects
// project-scoped starts; a nil logger uses slog.Default.
func NewService(st *store.Store, mux Multiplexer, actions ActionLoader, environments EnvironmentRegistry, projects ProjectResolver, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: st, mux: mux, actions: actions, environments: environments, projects: projects, logger: logger}
}

// Environments returns configured launch environments with client-safe metadata.
func (s *Service) Environments(ctx context.Context) []EnvironmentDiscovery {
	return s.environments.Discover(ctx)
}

// Start validates, records, launches, and returns a new distinct session.
func (s *Service) Start(ctx context.Context, input StartInput) (Session, error) {
	actions, err := s.actions.Load(ctx)
	if err != nil {
		return Session{}, err
	}
	inner, accepted, err := actions.buildCommand(input.Action, input.Params, input.Prompt)
	if err != nil {
		return Session{}, err
	}
	environment, environmentName, err := s.environments.resolve(input.Environment)
	if err != nil {
		return Session{}, err
	}
	argv, err := environment.Command(inner)
	if err != nil {
		return Session{}, err
	}
	workingDir, err := s.resolveStartDir(ctx, input)
	if err != nil {
		return Session{}, err
	}

	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	params, err := marshalParams(accepted)
	if err != nil {
		return Session{}, err
	}

	record, err := s.store.CreateStarting(ctx, store.CreateSessionInput{
		ID:          id,
		Name:        input.Name,
		Action:      input.Action,
		Environment: environmentName,
		Params:      params,
		WorkingDir:  workingDir,
		Prompt:      input.Prompt,
		ProjectID:   input.ProjectID,
	})
	if err != nil {
		return Session{}, translateStoreErr(err)
	}

	name := zmx.NameForID(record.ID)
	s.logger.Info("starting session", "id", record.ID, "zmx_name", name, "dir", workingDir, "action", input.Action, "environment", environmentName)
	if err := s.mux.Start(ctx, name, workingDir, argv); err != nil {
		s.logger.Error("session launch failed", "id", record.ID, "zmx_name", name, "action", input.Action, "environment", environmentName, "err", err)
		failed, markErr := s.store.MarkFailed(ctx, record.ID, launchFailedReason, CodeLaunchFailed)
		if markErr != nil {
			return Session{}, fmt.Errorf("record launch failure for session %s: %w", record.ID, markErr)
		}
		return Session{}, &LaunchError{
			SessionID:   failed.ID,
			FailureCode: failed.FailureCode,
			Message:     failed.FailureReason,
			Err:         err,
		}
	}

	running, err := s.store.MarkRunning(ctx, record.ID)
	if err != nil {
		return Session{}, err
	}
	return domainSession(running, true)
}

// resolveStartDir returns the directory a start launches in: the validated
// explicit WorkingDir for unscoped starts, or the referenced project's
// directory (snapshotted onto the session record) for project-scoped starts.
func (s *Service) resolveStartDir(ctx context.Context, input StartInput) (string, error) {
	if input.ProjectID == "" {
		if err := project.ValidateWorkingDir(input.WorkingDir); err != nil {
			return "", err
		}
		return input.WorkingDir, nil
	}
	if s.projects == nil {
		return "", fmt.Errorf("%w: %s", project.ErrProjectNotFound, input.ProjectID)
	}
	resolved, err := s.projects.ResolveForStart(ctx, input.ProjectID)
	if err != nil {
		return "", err
	}
	return resolved.WorkingDir, nil
}

// List returns persisted sessions newest-first, reconciling running-session
// liveness when the multiplexer can be queried. Starting records are owned by
// the in-flight Start call and are never resolved by read paths. A non-empty
// projectID restricts the list to that project's sessions.
func (s *Service) List(ctx context.Context, includeArchived bool, statusFilter Status, projectID string) ([]Session, error) {
	if statusFilter != "" && !validStatus(statusFilter) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidStatus, statusFilter)
	}
	records, err := s.store.List(ctx, store.ListFilter{
		IncludeArchived: includeArchived,
		ProjectID:       projectID,
	})
	if err != nil {
		return nil, translateStoreErr(err)
	}

	live, ok := s.liveNameSet(ctx)
	sessions := make([]Session, 0, len(records))
	for _, record := range records {
		liveForRecord := false
		if ok {
			var err error
			record, liveForRecord, err = s.reconcileReadRecord(ctx, record, live)
			if err != nil {
				return nil, err
			}
		}
		if statusFilter != "" && record.Status != statusFilter {
			continue
		}
		session, err := domainSession(record, liveForRecord)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// Read loads one session by id and reconciles running-session liveness when it
// is available. Starting records are reported as starting until Start settles
// them.
func (s *Service) Read(ctx context.Context, id string) (Session, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return Session{}, translateStoreErr(err)
	}

	live, ok := s.liveNameSet(ctx)
	liveForRecord := false
	if ok {
		record, liveForRecord, err = s.reconcileReadRecord(ctx, record, live)
		if err != nil {
			return Session{}, err
		}
	}
	return domainSession(record, liveForRecord)
}

// SendText injects text into a live session verbatim, without submitting it.
func (s *Service) SendText(ctx context.Context, id, text string) error {
	record, err := s.requireLive(ctx, id)
	if err != nil {
		return err
	}
	name := zmx.NameForID(record.ID)
	if err := s.mux.Send(ctx, name, []byte(text)); err != nil {
		s.logger.Error("session send failed", "id", record.ID, "zmx_name", name, "err", err)
		return err
	}
	return nil
}

// SendKey injects the registry bytes for keyName into a live session.
func (s *Service) SendKey(ctx context.Context, id, keyName string) error {
	payload, ok := keyBytes(keyName)
	if !ok {
		return fmt.Errorf("%w %q: valid keys are %s", ErrUnknownKey, keyName, strings.Join(keyNames(), ", "))
	}
	record, err := s.requireLive(ctx, id)
	if err != nil {
		return err
	}
	name := zmx.NameForID(record.ID)
	if err := s.mux.Send(ctx, name, payload); err != nil {
		s.logger.Error("session key failed", "id", record.ID, "zmx_name", name, "key", keyName, "err", err)
		return err
	}
	return nil
}

// EnsureAttachable verifies that id currently names a live attachable session
// without spawning an attach client.
func (s *Service) EnsureAttachable(ctx context.Context, id string) error {
	_, err := s.requireLive(ctx, id)
	return err
}

// Attach opens a live, bidirectional PTY for id at the client's initial
// terminal size.
func (s *Service) Attach(ctx context.Context, id string, rows, cols uint16) (zmx.PTY, error) {
	record, err := s.requireLive(ctx, id)
	if err != nil {
		return nil, err
	}
	return s.mux.Attach(ctx, zmx.NameForID(record.ID), rows, cols)
}

// Terminate requests the multiplexer stop any live terminal for id and records
// the terminal as no longer reachable. It is idempotent for settled non-live
// records; starting records remain owned by the in-flight Start call.
func (s *Service) Terminate(ctx context.Context, id string) (Session, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return Session{}, translateStoreErr(err)
	}
	if record.Status == StatusFailed || record.Status == StatusTerminated {
		return domainSession(record, false)
	}
	if record.Status == StatusStarting {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionNotLive, id)
	}

	live, err := s.isLive(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if live {
		name := zmx.NameForID(id)
		if err := s.mux.Terminate(ctx, name); err != nil {
			s.logger.Error("session terminate failed", "id", id, "zmx_name", name, "err", err)
			return Session{}, err
		}
	}

	if record.Status != StatusRunning {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionNotLive, id)
	}
	record, err = s.store.MarkTerminated(ctx, id)
	if err != nil {
		return Session{}, translateStoreErr(err)
	}
	return domainSession(record, false)
}

// Archive hides settled non-live sessions from default lists without changing
// status.
func (s *Service) Archive(ctx context.Context, id string) (Session, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return Session{}, translateStoreErr(err)
	}
	if record.ArchivedAt != nil {
		return domainSession(record, false)
	}
	if record.Status == StatusStarting {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionNotLive, id)
	}

	live, err := s.isLive(ctx, id)
	if err != nil {
		return Session{}, err
	}
	if live {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionLive, id)
	}

	if record.Status == StatusRunning {
		record, err = s.store.MarkTerminated(ctx, id)
		if err != nil {
			return Session{}, translateStoreErr(err)
		}
	}

	archived, err := s.store.MarkArchived(ctx, id)
	if err != nil {
		return Session{}, translateStoreErr(err)
	}
	return domainSession(archived, false)
}

// Reconcile performs the startup pass over starting/running records. A
// multiplexer liveness failure is logged and leaves stored state untouched.
func (s *Service) Reconcile(ctx context.Context) error {
	records, err := s.store.List(ctx, store.ListFilter{})
	if err != nil {
		return translateStoreErr(err)
	}
	live, ok := s.liveNameSet(ctx)
	if !ok {
		return nil
	}
	for _, record := range records {
		if _, _, err := s.reconcileStartupRecord(ctx, record, live); err != nil {
			return err
		}
	}
	return nil
}

func (s *Service) requireLive(ctx context.Context, id string) (store.Session, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return store.Session{}, translateStoreErr(err)
	}
	if record.ArchivedAt != nil || record.Status == StatusFailed || record.Status == StatusTerminated {
		return store.Session{}, fmt.Errorf("%w: %s", ErrSessionNotLive, id)
	}
	if record.Status == StatusStarting {
		return store.Session{}, fmt.Errorf("%w: %s", ErrSessionNotLive, id)
	}

	live, err := s.isLive(ctx, id)
	if err != nil {
		return store.Session{}, err
	}
	if !live {
		if record.Status == StatusRunning {
			if _, err := s.store.MarkTerminated(ctx, id); err != nil {
				return store.Session{}, translateStoreErr(err)
			}
		}
		return store.Session{}, fmt.Errorf("%w: %s", ErrSessionNotLive, id)
	}
	if record.Status != StatusRunning {
		return store.Session{}, fmt.Errorf("%w: %s", ErrSessionNotLive, id)
	}
	return record, nil
}

func (s *Service) reconcileReadRecord(ctx context.Context, record store.Session, live map[string]bool) (store.Session, bool, error) {
	if record.ArchivedAt != nil {
		return record, false, nil
	}
	if record.Status != StatusRunning {
		return record, false, nil
	}
	if live[zmx.NameForID(record.ID)] {
		return record, true, nil
	}
	record, err := s.store.MarkTerminated(ctx, record.ID)
	if err != nil {
		return store.Session{}, false, translateStoreErr(err)
	}
	return record, false, nil
}

func (s *Service) reconcileStartupRecord(ctx context.Context, record store.Session, live map[string]bool) (store.Session, bool, error) {
	return s.reconcileStartupLiveness(ctx, record, live[zmx.NameForID(record.ID)])
}

func (s *Service) reconcileStartupLiveness(ctx context.Context, record store.Session, isLive bool) (store.Session, bool, error) {
	if record.ArchivedAt != nil {
		return record, false, nil
	}

	var err error
	switch record.Status {
	case StatusStarting:
		if isLive {
			record, err = s.store.MarkRunning(ctx, record.ID)
		} else {
			record, err = s.store.MarkFailed(ctx, record.ID, startupIncompleteReason, CodeLaunchFailed)
		}
	case StatusRunning:
		if !isLive {
			record, err = s.store.MarkTerminated(ctx, record.ID)
		}
	}
	if err != nil {
		return store.Session{}, false, translateStoreErr(err)
	}
	return record, record.Status == StatusRunning && isLive, nil
}

func (s *Service) isLive(ctx context.Context, id string) (bool, error) {
	live, err := s.liveNames(ctx)
	if err != nil {
		return false, err
	}
	return live[zmx.NameForID(id)], nil
}

func (s *Service) liveNameSet(ctx context.Context) (map[string]bool, bool) {
	live, err := s.liveNames(ctx)
	if err != nil {
		s.logger.Warn("session liveness query failed; leaving stored statuses unchanged", "err", err)
		return nil, false
	}
	return live, true
}

func (s *Service) liveNames(ctx context.Context) (map[string]bool, error) {
	raw, err := s.mux.List(ctx)
	if err != nil {
		return nil, err
	}
	live := make(map[string]bool, len(raw))
	for _, r := range raw {
		live[r.Name] = true
	}
	return live, nil
}

func domainSession(record store.Session, live bool) (Session, error) {
	params := map[string]any{}
	if len(record.Params) > 0 {
		if err := json.Unmarshal(record.Params, &params); err != nil {
			return Session{}, fmt.Errorf("decode session params for %s: %w", record.ID, err)
		}
	}
	var projectRef *ProjectRef
	if record.Project != nil {
		projectRef = &ProjectRef{
			ID:         record.Project.ID,
			Name:       record.Project.Name,
			WorkingDir: record.Project.WorkingDir,
			ArchivedAt: record.Project.ArchivedAt,
		}
	}
	return Session{
		ID:            record.ID,
		Name:          record.Name,
		Action:        record.Action,
		Environment:   record.Environment,
		Params:        params,
		WorkingDir:    record.WorkingDir,
		Prompt:        record.Prompt,
		Status:        record.Status,
		FailureReason: record.FailureReason,
		FailureCode:   record.FailureCode,
		ProjectID:     record.ProjectID,
		Project:       projectRef,
		CreatedAt:     record.CreatedAt,
		UpdatedAt:     record.UpdatedAt,
		TerminatedAt:  record.TerminatedAt,
		ArchivedAt:    record.ArchivedAt,
		Attachable:    live && record.Status == StatusRunning && record.ArchivedAt == nil,
	}, nil
}

func marshalParams(params map[string]any) (json.RawMessage, error) {
	if len(params) == 0 {
		return json.RawMessage(`{}`), nil
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return nil, fmt.Errorf("encode accepted params: %w", err)
	}
	return raw, nil
}

func translateStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrSessionNotFound):
		return fmt.Errorf("%w: %v", ErrSessionNotFound, err)
	// The store's guarded session insert reports the project state it saw;
	// re-home those on the project sentinels the API layer maps.
	case errors.Is(err, store.ErrProjectNotFound):
		return fmt.Errorf("%w: %v", project.ErrProjectNotFound, err)
	case errors.Is(err, store.ErrProjectArchived):
		return fmt.Errorf("%w: %v", project.ErrProjectArchived, err)
	}
	return err
}

func validStatus(status Status) bool {
	switch status {
	case StatusStarting, StatusRunning, StatusFailed, StatusTerminated:
		return true
	default:
		return false
	}
}
