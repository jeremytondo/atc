package api

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/jeremytondo/atelier-code/internal/project"
	"github.com/jeremytondo/atelier-code/internal/session"
)

type startRequest struct {
	Action      string         `json:"action"`
	Environment string         `json:"environment"`
	Params      map[string]any `json:"params"`
	WorkingDir  string         `json:"workingDir"`
	Prompt      string         `json:"prompt"`
	Name        string         `json:"name"`
	ProjectID   string         `json:"projectId"`
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

// SessionListItem is the wire shape of one session in list responses.
type SessionListItem struct {
	ID            string          `json:"id"`
	Name          string          `json:"name,omitempty"`
	Action        string          `json:"action"`
	Environment   string          `json:"environment"`
	WorkingDir    string          `json:"workingDir"`
	Status        string          `json:"status"`
	Attachable    bool            `json:"attachable"`
	FailureReason string          `json:"failureReason,omitempty"`
	FailureCode   string          `json:"failureCode,omitempty"`
	CreatedAt     string          `json:"createdAt"`
	UpdatedAt     string          `json:"updatedAt"`
	TerminatedAt  *string         `json:"terminatedAt,omitempty"`
	ArchivedAt    *string         `json:"archivedAt,omitempty"`
	Project       *SessionProject `json:"project,omitempty"`
}

// SessionProject is the project object nested on project-scoped sessions.
type SessionProject struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	WorkingDir string  `json:"workingDir"`
	ArchivedAt *string `json:"archivedAt,omitempty"`
}

// SessionDetail is the wire shape of session detail responses.
type SessionDetail struct {
	ID            string          `json:"id"`
	Name          string          `json:"name,omitempty"`
	Action        string          `json:"action"`
	Environment   string          `json:"environment"`
	Params        map[string]any  `json:"params"`
	WorkingDir    string          `json:"workingDir"`
	Prompt        string          `json:"prompt,omitempty"`
	Status        string          `json:"status"`
	Attachable    bool            `json:"attachable"`
	FailureReason string          `json:"failureReason,omitempty"`
	FailureCode   string          `json:"failureCode,omitempty"`
	CreatedAt     string          `json:"createdAt"`
	UpdatedAt     string          `json:"updatedAt"`
	TerminatedAt  *string         `json:"terminatedAt,omitempty"`
	ArchivedAt    *string         `json:"archivedAt,omitempty"`
	Project       *SessionProject `json:"project,omitempty"`
}

func (routes apiRoutes) startSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	var req startRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Action == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "action is required")
		return
	}
	hasWorkingDir := strings.TrimSpace(req.WorkingDir) != ""
	hasProject := strings.TrimSpace(req.ProjectID) != ""
	if hasWorkingDir && hasProject {
		writeError(w, http.StatusBadRequest, "invalid_request", "workingDir and projectId are mutually exclusive; a project session inherits the project's directory")
		return
	}
	if !hasWorkingDir && !hasProject {
		writeError(w, http.StatusBadRequest, "invalid_request", "workingDir or projectId is required")
		return
	}

	started, err := routes.sessions.Start(r.Context(), session.StartInput{
		Action:      req.Action,
		Environment: req.Environment,
		Params:      req.Params,
		WorkingDir:  req.WorkingDir,
		Prompt:      req.Prompt,
		Name:        req.Name,
		ProjectID:   req.ProjectID,
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
	sessions, err := routes.sessions.List(r.Context(), includeArchived, statusFilter, "")
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
	// project_not_found is a 400 on start (mirroring unknown_action): the
	// request body, not the URL, named the missing project.
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
		Project:       sessionProjectResponse(s.Project),
	}
}

func sessionProjectResponse(ref *session.ProjectRef) *SessionProject {
	if ref == nil {
		return nil
	}
	return &SessionProject{
		ID:         ref.ID,
		Name:       ref.Name,
		WorkingDir: ref.WorkingDir,
		ArchivedAt: formatOptionalTime(ref.ArchivedAt),
	}
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
