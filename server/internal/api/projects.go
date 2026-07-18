package api

import (
	"errors"
	"net/http"

	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/session"
)

// Project is the wire shape of project list and detail responses (the two are
// identical; detail never inlines sessions). Exported so the CLI decodes the
// same types the server encodes.
type Project struct {
	ID         string  `json:"id"`
	Name       string  `json:"name"`
	WorkingDir string  `json:"workingDir"`
	CreatedAt  string  `json:"createdAt"`
	UpdatedAt  string  `json:"updatedAt"`
	ArchivedAt *string `json:"archivedAt,omitempty"`
}

// ProjectListResponse is the wire envelope for GET /projects.
type ProjectListResponse struct {
	Projects []Project `json:"projects"`
}

type createProjectRequest struct {
	Name       string `json:"name"`
	WorkingDir string `json:"workingDir"`
}

type renameProjectRequest struct {
	Name string `json:"name"`
}

func (routes apiRoutes) createProject(w http.ResponseWriter, r *http.Request) {
	if !routes.requireProjects(w) {
		return
	}
	var req createProjectRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	created, err := routes.projects.Create(r.Context(), req.Name, req.WorkingDir)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, projectResponse(created))
}

func (routes apiRoutes) listProjects(w http.ResponseWriter, r *http.Request) {
	if !routes.requireProjects(w) {
		return
	}
	includeArchived, err := boolQuery(r, "includeArchived")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	projects, err := routes.projects.List(r.Context(), includeArchived)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	items := make([]Project, 0, len(projects))
	for _, p := range projects {
		items = append(items, projectResponse(p))
	}
	writeJSON(w, http.StatusOK, ProjectListResponse{Projects: items})
}

func (routes apiRoutes) getProject(w http.ResponseWriter, r *http.Request) {
	if !routes.requireProjects(w) {
		return
	}
	got, err := routes.projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse(got))
}

// patchProject renames a project. The body is decoded strictly so a client
// cannot believe it changed anything else — most importantly workingDir,
// which is fixed after creation.
func (routes apiRoutes) patchProject(w http.ResponseWriter, r *http.Request) {
	if !routes.requireProjects(w) {
		return
	}
	var req renameProjectRequest
	if !decodeRenameJSON(w, r, &req) {
		return
	}
	renamed, err := routes.projects.Rename(r.Context(), r.PathValue("id"), req.Name)
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse(renamed))
}

func (routes apiRoutes) archiveProject(w http.ResponseWriter, r *http.Request) {
	if !routes.requireProjects(w) {
		return
	}
	archived, err := routes.projects.Archive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse(archived))
}

func (routes apiRoutes) unarchiveProject(w http.ResponseWriter, r *http.Request) {
	if !routes.requireProjects(w) {
		return
	}
	unarchived, err := routes.projects.Unarchive(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, projectResponse(unarchived))
}

func (routes apiRoutes) deleteProject(w http.ResponseWriter, r *http.Request) {
	if !routes.requireProjects(w) {
		return
	}
	if err := routes.projects.Delete(r.Context(), r.PathValue("id")); err != nil {
		writeProjectError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

// listProjectSessions is the single project-scoped session listing route. It
// shares the session list serialization and returns 404 project_not_found for
// an unknown project instead of a silently empty list.
func (routes apiRoutes) listProjectSessions(w http.ResponseWriter, r *http.Request) {
	if !routes.requireProjects(w) || !routes.requireSessions(w) {
		return
	}
	got, err := routes.projects.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeProjectError(w, err)
		return
	}
	statusFilter := session.Status(r.URL.Query().Get("status"))
	sessions, err := routes.sessions.List(r.Context(), statusFilter, session.ListScope{ProjectID: got.ID})
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

func (routes apiRoutes) requireProjects(w http.ResponseWriter) bool {
	if routes.projects != nil {
		return true
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "project service is unavailable")
	return false
}

// writeProjectError maps a project-domain error to the appropriate status
// code, parallel to writeSessionError.
func writeProjectError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, project.ErrProjectNotFound):
		writeError(w, http.StatusNotFound, "project_not_found", err.Error())
	case errors.Is(err, project.ErrInvalidWorkingDir):
		writeError(w, http.StatusBadRequest, "invalid_working_dir", err.Error())
	case errors.Is(err, project.ErrInvalidProject):
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
	case errors.Is(err, project.ErrProjectArchived):
		writeError(w, http.StatusConflict, "project_archived", err.Error())
	case errors.Is(err, project.ErrProjectHasUnarchivedWorkspaces):
		writeError(w, http.StatusConflict, "project_has_unarchived_workspaces", err.Error())
	case errors.Is(err, project.ErrProjectHasWorkspaces):
		writeError(w, http.StatusConflict, "project_has_workspaces", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}

func projectResponse(p project.Project) Project {
	return Project{
		ID:         p.ID,
		Name:       p.Name,
		WorkingDir: p.WorkingDir,
		CreatedAt:  formatTime(p.CreatedAt),
		UpdatedAt:  formatTime(p.UpdatedAt),
		ArchivedAt: formatOptionalTime(p.ArchivedAt),
	}
}
