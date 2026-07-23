package api

import (
	"bytes"
	"encoding/json"
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
	WorkspaceID string `json:"workspaceId"`
	ActionID    string `json:"actionId,omitempty"`
	Name        string `json:"name,omitempty"`
}

type sendTextRequest struct {
	Text string `json:"text"`
}

type sendKeyRequest struct {
	Key string `json:"key"`
}

type renameSessionRequest struct {
	Name json.RawMessage `json:"name"`
}

// SessionListResponse is the wire envelope for session list endpoints. It is
// exported, along with the item and detail structs, so the CLI decodes the
// same types the server encodes instead of redeclaring them.
type SessionListResponse struct {
	Sessions []SessionResponse `json:"sessions"`
}

// SessionResponse is the wire shape shared by session list and detail
// endpoints. Action identity is omitted for Interactive Shell sessions.
type SessionResponse struct {
	ID           string            `json:"id"`
	SessionIndex int               `json:"sessionIndex"`
	Name         string            `json:"name,omitempty"`
	ActionID     string            `json:"actionId,omitempty"`
	ActionName   string            `json:"actionName,omitempty"`
	IsAgent      bool              `json:"isAgent"`
	WorkingDir   string            `json:"workingDir"`
	Status       session.Status    `json:"status"`
	CreatedAt    string            `json:"createdAt"`
	UpdatedAt    string            `json:"updatedAt"`
	Workspace    *SessionWorkspace `json:"workspace,omitempty"`
	Project      *SessionProject   `json:"project,omitempty"`
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

// SessionDetail is retained as an alias for CLI callers; list and detail use
// the same complete wire shape.
type SessionDetail = SessionResponse

func (routes apiRoutes) startSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	var req startRequest
	if !decodeJSONBody(w, r, &req, true, "request body must be a valid session start request") {
		return
	}
	if strings.TrimSpace(req.WorkspaceID) == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "workspaceId is required")
		return
	}
	started, err := routes.sessions.Start(r.Context(), session.StartInput{
		WorkspaceID: req.WorkspaceID,
		ActionID:    req.ActionID,
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
	statusFilter := session.Status(r.URL.Query().Get("status"))
	sessions, err := routes.sessions.List(r.Context(), statusFilter, session.ListScope{})
	if err != nil {
		writeSessionError(w, err)
		return
	}
	items := make([]SessionResponse, 0, len(sessions))
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

// patchSession renames a session. Strict decoding keeps this endpoint scoped
// to the display name and prevents clients from believing other fields changed.
func (routes apiRoutes) patchSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	var req renameSessionRequest
	if !decodeRenameJSON(w, r, &req) {
		return
	}
	name, ok := decodeSessionName(w, req.Name)
	if !ok {
		return
	}
	renamed, err := routes.sessions.Rename(r.Context(), r.PathValue("id"), name)
	if err != nil {
		writeSessionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, detailResponse(renamed))
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
		code := launchErr.Code
		if code == "" {
			code = "launch_failed"
		}
		writeJSON(w, status, errorResponse{
			Error:     code,
			Message:   launchErr.Error(),
			SessionID: launchErr.SessionID,
		})
	case errors.Is(err, session.ErrActionNotFound):
		writeError(w, http.StatusNotFound, "action_not_found", err.Error())
	case errors.Is(err, session.ErrInvalidWorkingDir):
		writeError(w, http.StatusBadRequest, "invalid_working_dir", err.Error())
	// workspace_not_found is a 400 on start because the request body, not the
	// URL, named the missing workspace.
	case errors.Is(err, workspace.ErrWorkspaceNotFound):
		writeError(w, http.StatusBadRequest, "workspace_not_found", err.Error())
	case errors.Is(err, workspace.ErrInvalidWorkspace):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, project.ErrProjectNotFound):
		writeError(w, http.StatusBadRequest, "project_not_found", err.Error())
	case errors.Is(err, session.ErrActionDisabled):
		writeError(w, http.StatusConflict, "action_disabled", err.Error())
	case errors.Is(err, session.ErrUnknownKey),
		errors.Is(err, session.ErrInvalidStatus),
		errors.Is(err, session.ErrInvalidSessionName):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, session.ErrSessionNotFound):
		writeError(w, http.StatusNotFound, "session_not_found", err.Error())
	case errors.Is(err, session.ErrSessionEnded):
		var endedErr *session.EndedError
		errors.As(err, &endedErr)
		id := ""
		if endedErr != nil {
			id = endedErr.SessionID
		}
		writeJSON(w, http.StatusConflict, errorResponse{Error: "session_ended", Message: err.Error(), SessionID: id})
	case errors.Is(err, session.ErrZmxUnavailable):
		writeError(w, http.StatusServiceUnavailable, "zmx_unavailable", "zmx session inventory is unavailable")
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func listItemResponse(s session.Session) SessionResponse {
	return SessionResponse{
		ID:           s.ID,
		SessionIndex: s.SessionIndex,
		Name:         s.Name,
		ActionID:     s.ActionID,
		ActionName:   s.ActionName,
		IsAgent:      s.IsAgent,
		WorkingDir:   s.WorkingDir,
		Status:       s.Status,
		CreatedAt:    formatTime(s.CreatedAt),
		UpdatedAt:    formatTime(s.UpdatedAt),
		Workspace:    sessionWorkspaceResponse(s.Workspace),
		Project:      sessionProjectResponse(s.Project),
	}
}

func decodeSessionName(w http.ResponseWriter, raw json.RawMessage) (*string, bool) {
	if len(raw) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "name is required")
		return nil, false
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, true
	}
	var name string
	if err := json.Unmarshal(raw, &name); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "name must be a string or null")
		return nil, false
	}
	return &name, true
}

func detailResponse(s session.Session) SessionResponse {
	return listItemResponse(s)
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
