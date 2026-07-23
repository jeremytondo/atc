// Package session owns atc session semantics on top of persisted metadata
// and the multiplexer boundary. Public callers use atc-owned session ids;
// zmx names are derived internally and are never persisted or exposed.
package session

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/workspace"
	"github.com/jeremytondo/atc/internal/zmx"
)

const (
	// CodeLaunchFailed is returned when the multiplexer launch fails after a
	// provisional record exists.
	CodeLaunchFailed = "launch_failed"

	launchFailedReason = "action failed to launch"
)

// Status is the closed public lifecycle vocabulary for Sessions. It is
// intentionally separate from store.RecordStatus so provisional records
// cannot accidentally serialize through the API.
type Status string

const (
	StatusLive  Status = "live"
	StatusEnded Status = "ended"
)

// Sentinel errors let callers (notably the API) map failures to stable status
// codes.
var (
	ErrUnknownKey         = errors.New("unknown key")
	ErrSessionNotFound    = errors.New("session not found")
	ErrSessionEnded       = errors.New("session ended")
	ErrZmxUnavailable     = errors.New("zmx session inventory is unavailable")
	ErrInvalidSessionName = errors.New("invalid session name")
	ErrActionNotFound     = errors.New("action not found")
	ErrActionDisabled     = errors.New("action is disabled")
	ErrInvalidStatus      = store.ErrInvalidStatus
	// ErrInvalidWorkingDir is the project package's working-directory rule
	// (absolute, exists, is a directory), re-exported so session callers keep
	// one import. A bad directory fails fast instead of surfacing later as a
	// multiplexer launch failure.
	ErrInvalidWorkingDir = project.ErrInvalidWorkingDir
)

// ZmxUnavailableError preserves the failed inventory query for logs while
// exposing ErrZmxUnavailable as the stable domain error.
type ZmxUnavailableError struct {
	Err error
}

func (e *ZmxUnavailableError) Error() string {
	return fmt.Sprintf("%s: %v", ErrZmxUnavailable, e.Err)
}

func (e *ZmxUnavailableError) Unwrap() error { return ErrZmxUnavailable }

// EndedError identifies the stale Session involved in an interaction while
// preserving ErrSessionEnded for errors.Is checks.
type EndedError struct {
	SessionID string
}

func (e *EndedError) Error() string { return fmt.Sprintf("%s: %s", ErrSessionEnded, e.SessionID) }
func (e *EndedError) Unwrap() error { return ErrSessionEnded }

// LaunchError is returned when startup fails after atc creates a provisional
// record. The provisional record is removed before this error is returned.
type LaunchError struct {
	SessionID string
	Code      string
	Message   string
	Err       error
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

// StartInput is the accepted shape for starting a new session. WorkspaceID
// is required; the session launches in the workspace's project working
// directory. An empty ActionID launches the Interactive Shell.
type StartInput struct {
	ActionID string
	Name     string
	// WorkspaceID scopes the session to a workspace and is required.
	WorkspaceID string
}

// Session is atc's full domain model for a session. API list/detail
// serializers decide which fields are exposed on each endpoint.
type Session struct {
	ID          string
	Name        string
	ActionID    string
	ActionName  string
	IsAgent     bool
	WorkingDir  string
	Status      Status
	WorkspaceID string
	Workspace   *WorkspaceRef
	// Project is the derived project reference, reached through the
	// workspace, kept so clients that group sessions by project keep working.
	Project   *ProjectRef
	CreatedAt time.Time
	UpdatedAt time.Time
}

// WorkspaceRef is the workspace slice carried on sessions.
type WorkspaceRef struct {
	ID   string
	Name string
}

// ProjectRef is the derived project slice carried on sessions.
type ProjectRef struct {
	ID   string
	Name string
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

// WorkspaceResolver resolves the workspace a start request references to the
// validated working directory the session launches in. It is an interface so
// tests can fake it.
type WorkspaceResolver interface {
	ResolveForStart(ctx context.Context, id string) (string, error)
}

// Service implements session operations on top of durable metadata and a
// Multiplexer.
type Service struct {
	store      *store.Store
	mux        Multiplexer
	workspaces WorkspaceResolver
	logger     *slog.Logger
	inFlightMu sync.Mutex
	inFlight   map[string]struct{}
}

// NewService returns a Service backed by st and mux. A nil workspaces resolver
// rejects all starts; a nil logger uses slog.Default.
func NewService(st *store.Store, mux Multiplexer, workspaces WorkspaceResolver, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store: st, mux: mux, workspaces: workspaces, logger: logger,
		inFlight: make(map[string]struct{}),
	}
}

// Start validates, records, launches, and returns a new distinct session. An
// empty ActionID launches the Interactive Shell.
func (s *Service) Start(ctx context.Context, input StartInput) (Session, error) {
	input.Name = strings.TrimSpace(input.Name)
	workingDir, err := s.resolveStartDir(ctx, input)
	if err != nil {
		return Session{}, err
	}
	var argv []string
	var action store.Action
	if input.ActionID == "" {
		argv = interactiveShellCommand()
	} else {
		action, err = s.store.GetAction(ctx, input.ActionID)
		if err != nil {
			if errors.Is(err, store.ErrActionNotFound) {
				return Session{}, fmt.Errorf("%w: %s", ErrActionNotFound, input.ActionID)
			}
			return Session{}, err
		}
		if !action.Enabled {
			return Session{}, fmt.Errorf("%w: %s", ErrActionDisabled, input.ActionID)
		}
		argv = actionLaunchCommand(action)
	}

	id, err := newID()
	if err != nil {
		return Session{}, err
	}
	s.setStartInFlight(id, true)
	defer s.setStartInFlight(id, false)
	record, err := s.store.CreateStarting(ctx, store.CreateSessionInput{
		ID:          id,
		Name:        input.Name,
		ActionID:    action.ID,
		ActionName:  action.Name,
		IsAgent:     action.IsAgent,
		WorkingDir:  workingDir,
		WorkspaceID: input.WorkspaceID,
	})
	if err != nil {
		return Session{}, translateStoreErr(err)
	}

	name := zmx.NameForID(record.ID)
	s.logger.Info("starting session", "id", record.ID, "zmx_name", name, "dir", workingDir, "action_id", action.ID)
	if err := s.mux.Start(ctx, name, workingDir, argv); err != nil {
		s.logger.Error("session launch failed", "id", record.ID, "zmx_name", name, "action_id", action.ID, "err", err)
		if deleteErr := s.store.DeleteStarting(ctx, record.ID); deleteErr != nil &&
			!errors.Is(deleteErr, store.ErrSessionNotStarting) && !errors.Is(deleteErr, store.ErrSessionNotFound) {
			return Session{}, fmt.Errorf("remove failed launch attempt %s: %w", record.ID, deleteErr)
		}
		return Session{}, &LaunchError{
			SessionID: record.ID,
			Code:      CodeLaunchFailed,
			Message:   launchFailedReason,
			Err:       err,
		}
	}

	live, err := s.store.PromoteToLive(ctx, record.ID)
	switch {
	case errors.Is(err, store.ErrSessionNotStarting):
		// The provisional record settled concurrently. Startup reconciliation
		// may have promoted it after seeing the process alive; that Live
		// record is this launch's outcome and the process must keep running.
		if settled, getErr := s.store.Get(ctx, record.ID); getErr == nil && settled.Status == store.StatusLive {
			return domainSession(settled)
		}
		fallthrough
	case errors.Is(err, store.ErrSessionNotFound):
		// Workspace deletion removed the provisional record while this launch
		// was in flight; the process must not outlive that decision.
		s.terminateAbandonedLaunch(ctx, record.ID, name)
		return Session{}, fmt.Errorf("%w: %s", ErrSessionNotFound, record.ID)
	case err != nil:
		// The process launched but the promotion write failed. The caller
		// receives a failure, so neither the process nor the provisional
		// record may survive it.
		s.terminateAbandonedLaunch(ctx, record.ID, name)
		if deleteErr := s.store.DeleteStarting(ctx, record.ID); deleteErr != nil &&
			!errors.Is(deleteErr, store.ErrSessionNotStarting) && !errors.Is(deleteErr, store.ErrSessionNotFound) {
			s.logger.Error("remove unpromoted launch attempt failed", "id", record.ID, "err", deleteErr)
		}
		return Session{}, translateStoreErr(err)
	}
	return domainSession(live)
}

// terminateAbandonedLaunch stops a process whose launch will be reported as
// failed. Termination failure is logged only: the record-side outcome is
// already decided and reconciliation removes stragglers at next startup.
func (s *Service) terminateAbandonedLaunch(ctx context.Context, id, name string) {
	if err := s.mux.Terminate(ctx, name); err != nil {
		s.logger.Error("terminate abandoned launch failed", "id", id, "zmx_name", name, "err", err)
	}
}

// resolveStartDir resolves the referenced workspace to its project's working
// directory (snapshotted onto the session record). The guarded insert
// re-checks the workspace atomically; this resolution exists to fail fast
// with precise errors and to revalidate the directory on disk.
func (s *Service) resolveStartDir(ctx context.Context, input StartInput) (string, error) {
	if input.WorkspaceID == "" {
		return "", fmt.Errorf("%w: workspaceId is required", workspace.ErrInvalidWorkspace)
	}
	if s.workspaces == nil {
		return "", fmt.Errorf("%w: %s", workspace.ErrWorkspaceNotFound, input.WorkspaceID)
	}
	return s.workspaces.ResolveForStart(ctx, input.WorkspaceID)
}

// ListScope optionally restricts a session list to one workspace or to one
// project's workspaces.
type ListScope struct {
	WorkspaceID string
	ProjectID   string
}

// List returns public Sessions newest-first, reconciling every stored record
// when the multiplexer can be queried. Provisional records remain private.
func (s *Service) List(ctx context.Context, statusFilter Status, scope ListScope) ([]Session, error) {
	if statusFilter != "" && !validStatus(statusFilter) {
		return nil, fmt.Errorf("%w: %s", ErrInvalidStatus, statusFilter)
	}
	records, err := s.reconcileStored(ctx, store.ListFilter{
		WorkspaceID: scope.WorkspaceID,
		ProjectID:   scope.ProjectID,
	})
	if err != nil {
		return nil, err
	}

	sessions := make([]Session, 0, len(records))
	for _, record := range records {
		if record.Status == store.StatusStarting {
			continue
		}
		publicStatus, err := publicStatus(record.Status)
		if err != nil {
			return nil, err
		}
		if statusFilter != "" && publicStatus != statusFilter {
			continue
		}
		session, err := domainSession(record)
		if err != nil {
			return nil, err
		}
		sessions = append(sessions, session)
	}
	return sessions, nil
}

// Read loads a public Session after a demand-driven reconciliation sweep.
// Provisional records still in flight behave as not found.
func (s *Service) Read(ctx context.Context, id string) (Session, error) {
	records, err := s.reconcileStored(ctx, store.ListFilter{})
	if err != nil {
		return Session{}, err
	}

	for _, record := range records {
		if record.ID == id && record.Status != store.StatusStarting {
			return domainSession(record)
		}
	}
	return Session{}, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
}

// Rename updates only a Live session's persisted display name.
func (s *Service) Rename(ctx context.Context, id, name string) (Session, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Session{}, fmt.Errorf("%w: name is required", ErrInvalidSessionName)
	}
	current, err := s.store.Get(ctx, id)
	if err != nil {
		return Session{}, translateStoreErr(err)
	}
	if current.Status == store.StatusStarting {
		return Session{}, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if current.Status == store.StatusEnded {
		return Session{}, &EndedError{SessionID: id}
	}
	record, err := s.store.RenameSession(ctx, id, name)
	if err != nil {
		if errors.Is(err, store.ErrSessionNotFound) {
			if settled, getErr := s.store.Get(ctx, id); getErr == nil && settled.Status == store.StatusEnded {
				return Session{}, &EndedError{SessionID: id}
			}
		}
		return Session{}, translateStoreErr(err)
	}
	return domainSession(record)
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
		return s.confirmSendFailure(ctx, record.ID, err)
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
		return s.confirmSendFailure(ctx, record.ID, err)
	}
	return nil
}

// EnsureAttachable verifies that id currently names a Live Session without
// spawning an attach client.
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

// confirmSendFailure applies the authoritative absence rule after a failed
// send. The send error stands while the Session is still listed; a successful
// inventory that omits it converts the failure into session_ended, and an
// unavailable inventory is reported as the availability failure it is.
func (s *Service) confirmSendFailure(ctx context.Context, id string, cause error) error {
	ended, err := s.ConfirmEnded(ctx, id)
	if ended {
		return &EndedError{SessionID: id}
	}
	if errors.Is(err, ErrZmxUnavailable) {
		return err
	}
	return cause
}

// ConfirmEnded applies the authoritative absence rule after an attach failure.
// Only a complete inventory that omits the zmx name may end the Session.
func (s *Service) ConfirmEnded(ctx context.Context, id string) (bool, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return false, translateStoreErr(err)
	}
	if record.Status == store.StatusStarting {
		return false, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if record.Status == store.StatusEnded {
		return true, nil
	}
	live, err := s.liveNames(ctx)
	if err != nil {
		return false, err
	}
	if live[zmx.NameForID(id)] {
		return false, nil
	}
	_, _, err = s.reconcileRecord(ctx, record, live)
	if err != nil {
		return false, err
	}
	return true, nil
}

// Delete is the sole public lifecycle action. A Live process is ended and
// forgotten; an ended tombstone is removed directly. Provisional records
// behave as not found.
func (s *Service) Delete(ctx context.Context, id string) error {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return translateStoreErr(err)
	}
	if record.Status == store.StatusStarting {
		return fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if record.Status == store.StatusLive {
		isLive, err := s.isLive(ctx, id)
		if err != nil {
			return err
		}
		if isLive {
			name := zmx.NameForID(id)
			if err := s.mux.Terminate(ctx, name); err != nil {
				s.logger.Error("session terminate during delete failed", "id", id, "zmx_name", name, "err", err)
				stillLive, inventoryErr := s.isLive(ctx, id)
				if inventoryErr != nil || stillLive {
					return err
				}
			}
		}
	}
	if err := s.store.ForgetSession(ctx, id); err != nil {
		return translateStoreErr(err)
	}
	s.logger.Info("session deleted", "id", id)
	return nil
}

// End stops an active record for internal workspace deletion. It is not a
// public lifecycle operation.
func (s *Service) End(ctx context.Context, id string) error {
	for {
		record, err := s.store.Get(ctx, id)
		if errors.Is(err, store.ErrSessionNotFound) {
			// A concurrent delete already removed the record; the
			// postcondition (no active session) holds.
			return nil
		}
		if err != nil {
			return translateStoreErr(err)
		}
		if record.Status == store.StatusEnded {
			return nil
		}
		isLive, err := s.isLive(ctx, id)
		if err != nil {
			return err
		}
		if isLive {
			name := zmx.NameForID(id)
			if err := s.mux.Terminate(ctx, name); err != nil {
				s.logger.Error("session end failed", "id", id, "zmx_name", name, "err", err)
				return err
			}
			isLive, err = s.isLive(ctx, id)
			if err != nil {
				return err
			}
			if isLive {
				return fmt.Errorf("session %s remained in zmx inventory after termination", id)
			}
		}
		if record.Status == store.StatusStarting {
			err := s.store.DeleteStarting(ctx, id)
			if errors.Is(err, store.ErrSessionNotStarting) {
				continue
			}
			if errors.Is(err, store.ErrSessionNotFound) {
				return nil
			}
			return err
		}
		_, err = s.store.MarkEnded(ctx, id)
		return translateStoreErr(err)
	}
}

// Reconcile performs the startup pass over provisional/live records. A
// multiplexer liveness failure is logged and leaves stored state untouched.
func (s *Service) Reconcile(ctx context.Context) error {
	_, err := s.reconcileStored(ctx, store.ListFilter{})
	return err
}

func (s *Service) requireLive(ctx context.Context, id string) (store.Session, error) {
	record, err := s.store.Get(ctx, id)
	if err != nil {
		return store.Session{}, translateStoreErr(err)
	}
	if record.Status == store.StatusStarting {
		return store.Session{}, fmt.Errorf("%w: %s", ErrSessionNotFound, id)
	}
	if record.Status == store.StatusEnded {
		return store.Session{}, &EndedError{SessionID: id}
	}

	live, err := s.isLive(ctx, id)
	if err != nil {
		return store.Session{}, err
	}
	if !live {
		if _, err := s.store.MarkEnded(ctx, id); err != nil {
			return store.Session{}, translateStoreErr(err)
		}
		return store.Session{}, &EndedError{SessionID: id}
	}
	return record, nil
}

func (s *Service) reconcileRecord(ctx context.Context, record store.Session, live map[string]bool) (store.Session, bool, error) {
	isLive := live[zmx.NameForID(record.ID)]
	switch record.Status {
	case store.StatusStarting:
		if s.startInFlight(record.ID) {
			return record, true, nil
		}
		if isLive {
			promoted, err := s.store.PromoteToLive(ctx, record.ID)
			if errors.Is(err, store.ErrSessionNotFound) {
				return store.Session{}, false, nil
			}
			if errors.Is(err, store.ErrSessionNotStarting) {
				settled, getErr := s.store.Get(ctx, record.ID)
				if errors.Is(getErr, store.ErrSessionNotFound) {
					return store.Session{}, false, nil
				}
				if getErr != nil {
					return store.Session{}, false, translateStoreErr(getErr)
				}
				return s.reconcileRecord(ctx, settled, live)
			}
			if err != nil {
				return store.Session{}, false, translateStoreErr(err)
			}
			return promoted, true, nil
		}
		err := s.store.DeleteStarting(ctx, record.ID)
		if errors.Is(err, store.ErrSessionNotFound) {
			return store.Session{}, false, nil
		}
		if errors.Is(err, store.ErrSessionNotStarting) {
			settled, getErr := s.store.Get(ctx, record.ID)
			if errors.Is(getErr, store.ErrSessionNotFound) {
				return store.Session{}, false, nil
			}
			if getErr != nil {
				return store.Session{}, false, translateStoreErr(getErr)
			}
			return s.reconcileRecord(ctx, settled, live)
		}
		return store.Session{}, false, translateStoreErr(err)
	case store.StatusLive:
		if !isLive {
			ended, err := s.store.MarkEnded(ctx, record.ID)
			if errors.Is(err, store.ErrSessionNotFound) {
				return store.Session{}, false, nil
			}
			if err != nil {
				return store.Session{}, false, translateStoreErr(err)
			}
			return ended, true, nil
		}
	}
	return record, true, nil
}

func (s *Service) reconcileStored(ctx context.Context, filter store.ListFilter) ([]store.Session, error) {
	records, err := s.store.ListAll(ctx, filter)
	if err != nil {
		return nil, translateStoreErr(err)
	}
	live, ok := s.liveNameSet(ctx)
	if !ok {
		return records, nil
	}
	reconciled := make([]store.Session, 0, len(records))
	for _, record := range records {
		record, exists, err := s.reconcileRecord(ctx, record, live)
		if err != nil {
			return nil, err
		}
		if exists {
			reconciled = append(reconciled, record)
		}
	}
	return reconciled, nil
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
		return nil, &ZmxUnavailableError{Err: err}
	}
	live := make(map[string]bool, len(raw))
	for _, r := range raw {
		live[r.Name] = true
	}
	return live, nil
}

func (s *Service) setStartInFlight(id string, inFlight bool) {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	if inFlight {
		s.inFlight[id] = struct{}{}
	} else {
		delete(s.inFlight, id)
	}
}

func (s *Service) startInFlight(id string) bool {
	s.inFlightMu.Lock()
	defer s.inFlightMu.Unlock()
	_, ok := s.inFlight[id]
	return ok
}

func domainSession(record store.Session) (Session, error) {
	var workspaceRef *WorkspaceRef
	if record.Workspace != nil {
		workspaceRef = &WorkspaceRef{ID: record.Workspace.ID, Name: record.Workspace.Name}
	}
	var projectRef *ProjectRef
	if record.Project != nil {
		projectRef = &ProjectRef{ID: record.Project.ID, Name: record.Project.Name}
	}
	status, err := publicStatus(record.Status)
	if err != nil {
		return Session{}, err
	}
	return Session{
		ID:          record.ID,
		Name:        record.Name,
		ActionID:    record.ActionID,
		ActionName:  record.ActionName,
		IsAgent:     record.IsAgent,
		WorkingDir:  record.WorkingDir,
		Status:      status,
		WorkspaceID: record.WorkspaceID,
		Workspace:   workspaceRef,
		Project:     projectRef,
		CreatedAt:   record.CreatedAt,
		UpdatedAt:   record.UpdatedAt,
	}, nil
}

func publicStatus(status store.RecordStatus) (Status, error) {
	switch status {
	case store.StatusLive:
		return StatusLive, nil
	case store.StatusEnded:
		return StatusEnded, nil
	default:
		return "", fmt.Errorf("provisional session cannot be exposed: %s", status)
	}
}

func translateStoreErr(err error) error {
	switch {
	case errors.Is(err, store.ErrSessionNotFound):
		return fmt.Errorf("%w: %v", ErrSessionNotFound, err)
	// The store's guarded session insert reports the workspace state it saw;
	// re-home those on the workspace sentinels the API layer maps.
	case errors.Is(err, store.ErrWorkspaceNotFound):
		return fmt.Errorf("%w: %v", workspace.ErrWorkspaceNotFound, err)
	}
	return err
}

func validStatus(status Status) bool {
	switch status {
	case StatusLive, StatusEnded:
		return true
	default:
		return false
	}
}
