package api

import (
	"context"
	"errors"
	"net/http"

	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/session"
	"github.com/jeremytondo/atc/internal/workspace"
)

// Workspace is the wire shape of workspace list and detail responses.
// Exported so the CLI decodes the same types the server encodes.
type Workspace struct {
	ID        string `json:"id"`
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// WorkspaceListResponse is the wire envelope for GET /workspaces.
type WorkspaceListResponse struct {
	Workspaces []Workspace `json:"workspaces"`
}

type createWorkspaceRequest struct {
	ProjectID string `json:"projectId"`
	Name      string `json:"name"`
}

type renameWorkspaceRequest struct {
	Name string `json:"name"`
}

func (routes apiRoutes) createWorkspace(w http.ResponseWriter, r *http.Request) {
	if !routes.requireWorkspaces(w) {
		return
	}
	var req createWorkspaceRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	created, err := routes.workspaces.Create(r.Context(), req.ProjectID, req.Name)
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, workspaceResponse(created))
}

func (routes apiRoutes) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	if !routes.requireWorkspaces(w) {
		return
	}
	workspaces, err := routes.workspaces.List(r.Context(), r.URL.Query().Get("projectId"))
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	items := make([]Workspace, 0, len(workspaces))
	for _, ws := range workspaces {
		items = append(items, workspaceResponse(ws))
	}
	writeJSON(w, http.StatusOK, WorkspaceListResponse{Workspaces: items})
}

func (routes apiRoutes) getWorkspace(w http.ResponseWriter, r *http.Request) {
	if !routes.requireWorkspaces(w) {
		return
	}
	got, err := routes.workspaces.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceResponse(got))
}

// patchWorkspace renames a workspace. The body is decoded strictly so a
// client cannot believe it changed anything else — most importantly the
// project, which is fixed after creation.
func (routes apiRoutes) patchWorkspace(w http.ResponseWriter, r *http.Request) {
	if !routes.requireWorkspaces(w) {
		return
	}
	var req renameWorkspaceRequest
	if !decodeRenameJSON(w, r, &req) {
		return
	}
	renamed, err := routes.workspaces.Rename(r.Context(), r.PathValue("id"), req.Name)
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, workspaceResponse(renamed))
}

func (routes apiRoutes) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	if !routes.requireWorkspaces(w) || !routes.requireSessions(w) {
		return
	}
	err := routes.workspaces.Delete(r.Context(), r.PathValue("id"), sessionEnder{routes.sessions})
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// listWorkspaceSessions is the workspace-scoped session listing route. It
// shares the session list serialization and returns 404 workspace_not_found
// for an unknown workspace instead of a silently empty list.
func (routes apiRoutes) listWorkspaceSessions(w http.ResponseWriter, r *http.Request) {
	if !routes.requireWorkspaces(w) || !routes.requireSessions(w) {
		return
	}
	got, err := routes.workspaces.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeWorkspaceError(w, err)
		return
	}
	statusFilter := session.Status(r.URL.Query().Get("status"))
	sessions, err := routes.sessions.List(r.Context(), statusFilter, session.ListScope{WorkspaceID: got.ID})
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

// sessionEnder adapts the session service to the workspace domain's internal
// SessionEnder slice.
type sessionEnder struct {
	sessions *session.Service
}

func (e sessionEnder) End(ctx context.Context, id string) error {
	return e.sessions.End(ctx, id)
}

func (routes apiRoutes) requireWorkspaces(w http.ResponseWriter) bool {
	if routes.workspaces != nil {
		return true
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "workspace service is unavailable")
	return false
}

// writeWorkspaceError maps a workspace-domain error to the appropriate status
// code, parallel to writeProjectError and writeSessionError.
func writeWorkspaceError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, workspace.ErrWorkspaceNotFound):
		writeError(w, http.StatusNotFound, "workspace_not_found", err.Error())
	case errors.Is(err, workspace.ErrInvalidWorkspace):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, workspace.ErrWorkspaceHasActiveSessions):
		writeError(w, http.StatusConflict, "workspace_has_active_sessions", err.Error())
	// A session that could not be ended is a multiplexer failure, surfaced
	// as 502 like launch failures; no metadata has been deleted.
	case errors.Is(err, workspace.ErrSessionEndFailed):
		writeError(w, http.StatusBadGateway, "session_end_failed", err.Error())
	case errors.Is(err, project.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func workspaceResponse(ws workspace.Workspace) Workspace {
	return Workspace{
		ID:        ws.ID,
		ProjectID: ws.ProjectID,
		Name:      ws.Name,
		CreatedAt: formatTime(ws.CreatedAt),
		UpdatedAt: formatTime(ws.UpdatedAt),
	}
}
