package api

import (
	"encoding/json"
	"net/http"

	"github.com/jeremytondo/atelier-code/internal/action"
	"github.com/jeremytondo/atelier-code/internal/diagnostics"
	"github.com/jeremytondo/atelier-code/internal/fs"
	"github.com/jeremytondo/atelier-code/internal/project"
	"github.com/jeremytondo/atelier-code/internal/session"
)

type apiRoutes struct {
	diagnostics diagnostics.Diagnostics
	sessions    *session.Service
	projects    *project.Service
	actions     *action.Store
	fs          *fs.Service
}

// Routes returns the HTTP handler for the Atelier Code API. The sessions, projects,
// and fs services may be nil when their routes are not needed (e.g.
// diagnostics-only tests).
func Routes(diagnostics diagnostics.Diagnostics, sessions *session.Service, projects *project.Service, actions *action.Store, fsService *fs.Service) http.Handler {
	routes := apiRoutes{diagnostics: diagnostics, sessions: sessions, projects: projects, actions: actions, fs: fsService}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", routes.health)
	mux.HandleFunc("GET /version", routes.version)
	mux.HandleFunc("GET /actions", routes.listActions)
	mux.HandleFunc("GET /actions/{name}", routes.getAction)
	mux.HandleFunc("POST /actions", routes.createAction)
	mux.HandleFunc("PUT /actions/{name}", routes.updateAction)
	mux.HandleFunc("PUT /actions/{name}/enabled", routes.setActionEnabled)
	mux.HandleFunc("DELETE /actions/{name}", routes.deleteAction)
	mux.HandleFunc("GET /environments", routes.listEnvironments)
	mux.HandleFunc("GET /fs/list", routes.fsList)
	mux.HandleFunc("POST /projects", routes.createProject)
	mux.HandleFunc("GET /projects", routes.listProjects)
	mux.HandleFunc("GET /projects/{id}", routes.getProject)
	mux.HandleFunc("PATCH /projects/{id}", routes.patchProject)
	mux.HandleFunc("POST /projects/{id}/archive", routes.archiveProject)
	mux.HandleFunc("POST /projects/{id}/unarchive", routes.unarchiveProject)
	mux.HandleFunc("GET /projects/{id}/sessions", routes.listProjectSessions)
	mux.HandleFunc("POST /sessions/start", routes.startSession)
	mux.HandleFunc("GET /sessions", routes.listSessions)
	mux.HandleFunc("GET /sessions/{id}", routes.readSession)
	mux.HandleFunc("POST /sessions/{id}/send-text", routes.sendText)
	mux.HandleFunc("POST /sessions/{id}/send-key", routes.sendKey)
	mux.HandleFunc("POST /sessions/{id}/terminate", routes.terminateSession)
	mux.HandleFunc("POST /sessions/{id}/archive", routes.archiveSession)
	mux.HandleFunc("GET /sessions/{id}/attach", routes.attachSession)
	mux.HandleFunc("/", routes.notFound)
	return mux
}

func (routes apiRoutes) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, routes.diagnostics.Health())
}

func (routes apiRoutes) version(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, routes.diagnostics.Version())
}

func (routes apiRoutes) notFound(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusNotFound, errorResponse{
		Error:   "not_found",
		Message: "API route not found",
	})
}

type errorResponse struct {
	Error     string `json:"error"`
	Message   string `json:"message"`
	SessionID string `json:"sessionId,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, errorResponse{Error: code, Message: message})
}
