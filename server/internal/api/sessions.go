package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/session"
	"github.com/jeremytondo/atc/internal/workspace"
)

type startRequest struct {
	WorkspaceID string         `json:"workspaceId"`
	Action      string         `json:"action"`
	Environment string         `json:"environment"`
	Params      map[string]any `json:"params"`
	Prompt      string         `json:"prompt"`
	Name        string         `json:"name"`
}

type sendTextRequest struct {
	Text string `json:"text"`
}

type sendKeyRequest struct {
	Key string `json:"key"`
}

// SessionListResponse is the wire envelope for session list endpoints. It is
// exported, along with the item and detail structs, so the CLI decodes the
// same types the server encodes instead of redeclaring them.
type SessionListResponse struct {
	Sessions []SessionListItem `json:"sessions"`
}

// SessionListItem is the wire shape of one session in list responses. Action
// is omitted for Interactive Shell sessions (a null action on the wire).
type SessionListItem struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Action        string            `json:"action,omitempty"`
	Environment   string            `json:"environment"`
	WorkingDir    string            `json:"workingDir"`
	Status        string            `json:"status"`
	Attachable    bool              `json:"attachable"`
	FailureReason string            `json:"failureReason,omitempty"`
	FailureCode   string            `json:"failureCode,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	UpdatedAt     string            `json:"updatedAt"`
	TerminatedAt  *string           `json:"terminatedAt,omitempty"`
	ArchivedAt    *string           `json:"archivedAt,omitempty"`
	Workspace     *SessionWorkspace `json:"workspace,omitempty"`
	Project       *SessionProject   `json:"project,omitempty"`
}

// SessionWorkspace is the workspace object nested on sessions.
type SessionWorkspace struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SessionProject is the derived project object nested on sessions, kept so
// clients that group by project keep working.
type SessionProject struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// SessionDetail is the wire shape of session detail responses.
type SessionDetail struct {
	ID            string            `json:"id"`
	Name          string            `json:"name,omitempty"`
	Action        string            `json:"action,omitempty"`
	Environment   string            `json:"environment"`
	Params        map[string]any    `json:"params"`
	WorkingDir    string            `json:"workingDir"`
	Prompt        string            `json:"prompt,omitempty"`
	Status        string            `json:"status"`
	Attachable    bool              `json:"attachable"`
	FailureReason string            `json:"failureReason,omitempty"`
	FailureCode   string            `json:"failureCode,omitempty"`
	CreatedAt     string            `json:"createdAt"`
	UpdatedAt     string            `json:"updatedAt"`
	TerminatedAt  *string           `json:"terminatedAt,omitempty"`
	ArchivedAt    *string           `json:"archivedAt,omitempty"`
	Workspace     *SessionWorkspace `json:"workspace,omitempty"`
	Project       *SessionProject   `json:"project,omitempty"`
}

func (routes apiRoutes) startSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	var req startRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "workspaceId is required")
		return
	}
	// action is optional: omitted, the server launches the Interactive Shell.
	// params and prompt are rejected by the domain when action is omitted.
	started, err := routes.sessions.Start(r.Context(), session.StartInput{
		WorkspaceID: req.WorkspaceID,
		Action:      req.Action,
		Environment: req.Environment,
		Params:      req.Params,
		Prompt:      req.Prompt,
		Name:        req.Name,
	})
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detailResponse(started))
}

func (routes apiRoutes) listSessions(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	includeArchived, err := boolQuery(r, "includeArchived")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	statusFilter := session.Status(r.URL.Query().Get("status"))
	sessions, err := routes.sessions.List(r.Context(), includeArchived, statusFilter, session.ListScope{})
	if err != nil {
		writeSessionError(w, err)
		return
	}
	items := make([]SessionListItem, 0, len(sessions))
	for _, s := range sessions {
		items = append(items, listItemResponse(s))
	}
	writeJSON(w, http.StatusOK, SessionListResponse{Sessions: items})
}

func (routes apiRoutes) readSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	got, err := routes.sessions.Read(r.Context(), r.PathValue("id"))
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detailResponse(got))
}

func (routes apiRoutes) sendText(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	var req sendTextRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if err := routes.sessions.SendText(r.Context(), r.PathValue("id"), req.Text); err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (routes apiRoutes) sendKey(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	var req sendKeyRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Key == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "key is required")
		return
	}
	if err := routes.sessions.SendKey(r.Context(), r.PathValue("id"), req.Key); err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (routes apiRoutes) terminateSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	terminated, err := routes.sessions.Terminate(r.Context(), r.PathValue("id"))
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detailResponse(terminated))
}

func (routes apiRoutes) archiveSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	archived, err := routes.sessions.Archive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detailResponse(archived))
}

func (routes apiRoutes) unarchiveSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	unarchived, err := routes.sessions.Unarchive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detailResponse(unarchived))
}

func (routes apiRoutes) deleteSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	if err := routes.sessions.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (routes apiRoutes) requireSessions(w http.ResponseWriter) bool {
	if routes.sessions != nil {
		return true
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "session service is unavailable")
	return false
}

// writeSessionError maps a session-domain error to the appropriate status code.
func writeSessionError(w http.ResponseWriter, err error) {
	var launchErr *session.LaunchError
	switch {
	case errors.As(err, &launchErr):
		status := http.StatusBadGateway
		code := launchErr.FailureCode
		if code == "" {
			code = "launch_failed"
		}
		writeJSON(w, status, errorResponse{
			Error:     code,
			Message:   launchErr.Error(),
			SessionID: launchErr.SessionID,
		})
	case errors.Is(err, session.ErrUnknownAction):
		writeError(w, http.StatusBadRequest, "unknown_action", err.Error())
	case errors.Is(err, session.ErrUnknownEnvironment):
		writeError(w, http.StatusBadRequest, "unknown_environment", err.Error())
	case errors.Is(err, session.ErrInvalidParam):
		writeError(w, http.StatusBadRequest, "invalid_params", err.Error())
	case errors.Is(err, session.ErrInvalidWorkingDir):
		writeError(w, http.StatusBadRequest, "invalid_working_dir", err.Error())
	// workspace_not_found is a 400 on start (mirroring unknown_action): the
	// request body, not the URL, named the missing workspace.
	case errors.Is(err, workspace.ErrWorkspaceNotFound):
		writeError(w, http.StatusBadRequest, "workspace_not_found", err.Error())
	case errors.Is(err, workspace.ErrInvalidWorkspace):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, workspace.ErrWorkspaceArchived):
		writeError(w, http.StatusConflict, "workspace_archived", err.Error())
	case errors.Is(err, project.ErrProjectNotFound):
		writeError(w, http.StatusBadRequest, "project_not_found", err.Error())
	case errors.Is(err, project.ErrProjectArchived):
		writeError(w, http.StatusConflict, "project_archived", err.Error())
	case errors.Is(err, session.ErrActionDisabled):
		writeError(w, http.StatusConflict, "action_disabled", err.Error())
	case errors.Is(err, session.ErrUnknownKey),
		errors.Is(err, session.ErrInvalidStatus):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, session.ErrActionMisconfigured):
		writeError(w, http.StatusInternalServerError, "action_misconfigured", err.Error())
	case errors.Is(err, session.ErrEnvironmentMisconfigured):
		writeError(w, http.StatusInternalServerError, "environment_misconfigured", err.Error())
	case errors.Is(err, session.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "session_not_found", err.Error())
	case errors.Is(err, session.ErrSessionNotLive):
		writeError(w, http.StatusConflict, "session_not_live", err.Error())
	case errors.Is(err, session.ErrSessionLive):
		writeError(w, http.StatusConflict, "session_live", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func listItemResponse(s session.Session) SessionListItem {
	return SessionListItem{
		ID:            s.ID,
		Name:          s.Name,
		Action:        s.Action,
		Environment:   s.Environment,
		WorkingDir:    s.WorkingDir,
		Status:        string(s.Status),
		Attachable:    s.Attachable,
		FailureReason: s.FailureReason,
		FailureCode:   s.FailureCode,
		CreatedAt:     formatTime(s.CreatedAt),
		UpdatedAt:     formatTime(s.UpdatedAt),
		TerminatedAt:  formatOptionalTime(s.TerminatedAt),
		ArchivedAt:    formatOptionalTime(s.ArchivedAt),
		Workspace:     sessionWorkspaceResponse(s.Workspace),
		Project:       sessionProjectResponse(s.Project),
	}
}

func detailResponse(s session.Session) SessionDetail {
	return SessionDetail{
		ID:            s.ID,
		Name:          s.Name,
		Action:        s.Action,
		Environment:   s.Environment,
		Params:        s.Params,
		WorkingDir:    s.WorkingDir,
		Prompt:        s.Prompt,
		Status:        string(s.Status),
		Attachable:    s.Attachable,
		FailureReason: s.FailureReason,
		FailureCode:   s.FailureCode,
		CreatedAt:     formatTime(s.CreatedAt),
		UpdatedAt:     formatTime(s.UpdatedAt),
		TerminatedAt:  formatOptionalTime(s.TerminatedAt),
		ArchivedAt:    formatOptionalTime(s.ArchivedAt),
		Workspace:     sessionWorkspaceResponse(s.Workspace),
		Project:       sessionProjectResponse(s.Project),
	}
}

func sessionWorkspaceResponse(ref *session.WorkspaceRef) *SessionWorkspace {
	if ref == nil {
		return nil
	}
	return &SessionWorkspace{ID: ref.ID, Name: ref.Name}
}

func sessionProjectResponse(ref *session.ProjectRef) *SessionProject {
	if ref == nil {
		return nil
	}
	return &SessionProject{ID: ref.ID, Name: ref.Name}
}

func formatTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

func formatOptionalTime(t *time.Time) *string {
	if t == nil {
		return nil
	}
	formatted := formatTime(*t)
	return &formatted
}

func boolQuery(r *http.Request, name string) (bool, error) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return false, nil
	}
	v, err := strconv.ParseBool(raw)
	if err != nil {
		return false, err
	}
	return v, nil
}
