package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jeremytondo/atc/internal/action"
	"github.com/jeremytondo/atc/internal/diagnostics"
	"github.com/jeremytondo/atc/internal/fs"
	"github.com/jeremytondo/atc/internal/project"
	"github.com/jeremytondo/atc/internal/session"
)

type apiRoutes struct {
	diagnostics diagnostics.Diagnostics
	sessions    *session.Service
	projects    *project.Service
	actions     *action.Store
	fs          *fs.Service
}

// endpoints is the API's route inventory: every pattern and its handler.
// Contract tests iterate this table to require a fixture under
// packages/contracts/fixtures for each route, so all endpoints must be
// registered here rather than directly on the mux.
func (routes apiRoutes) endpoints() map[string]http.HandlerFunc {
	return map[string]http.HandlerFunc{
		"GET /health":                   routes.health,
		"GET /version":                  routes.version,
		"GET /actions":                  routes.listActions,
		"GET /actions/{name}":           routes.getAction,
		"POST /actions":                 routes.createAction,
		"PUT /actions/{name}":           routes.updateAction,
		"PUT /actions/{name}/enabled":   routes.setActionEnabled,
		"DELETE /actions/{name}":        routes.deleteAction,
		"GET /environments":             routes.listEnvironments,
		"GET /fs/list":                  routes.fsList,
		"POST /projects":                routes.createProject,
		"GET /projects":                 routes.listProjects,
		"GET /projects/{id}":            routes.getProject,
		"PATCH /projects/{id}":          routes.patchProject,
		"POST /projects/{id}/archive":   routes.archiveProject,
		"POST /projects/{id}/unarchive": routes.unarchiveProject,
		"GET /projects/{id}/sessions":   routes.listProjectSessions,
		"POST /sessions/start":          routes.startSession,
		"GET /sessions":                 routes.listSessions,
		"GET /sessions/{id}":            routes.readSession,
		"POST /sessions/{id}/send-text": routes.sendText,
		"POST /sessions/{id}/send-key":  routes.sendKey,
		"POST /sessions/{id}/terminate": routes.terminateSession,
		"POST /sessions/{id}/archive":   routes.archiveSession,
		"GET /sessions/{id}/attach":     routes.attachSession,
	}
}

// Routes returns the HTTP handler for the atc API. The sessions, projects,
// and fs services may be nil when their routes are not needed (e.g.
// diagnostics-only tests).
func Routes(diagnostics diagnostics.Diagnostics, sessions *session.Service, projects *project.Service, actions *action.Store, fsService *fs.Service) http.Handler {
	routes := apiRoutes{diagnostics: diagnostics, sessions: sessions, projects: projects, actions: actions, fs: fsService}
	mux := http.NewServeMux()
	for pattern, handler := range routes.endpoints() {
		mux.HandleFunc(pattern, handler)
	}
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

// maxJSONBodyBytes bounds every JSON request body. The largest legitimate
// payloads (action definitions with params) are far below 1 MiB.
const maxJSONBodyBytes = 1 << 20

// jsonBodyReadTimeout bounds how long a client may take to deliver a JSON
// body after its headers, so a trickling request can't hold a handler open.
const jsonBodyReadTimeout = 30 * time.Second

// decodeJSON decodes the request body into dst as a single bounded JSON
// value, writing the appropriate 4xx and returning false on failure.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	// Deadline errors surface through the size-limited reader as generic
	// read failures; SetReadDeadline is a no-op on connections that don't
	// support it (e.g. some test recorders), which is fine.
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(jsonBodyReadTimeout))
	body := http.MaxBytesReader(w, r.Body, maxJSONBodyBytes)
	decoder := json.NewDecoder(body)
	if err := decoder.Decode(dst); err != nil {
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			writeError(w, http.StatusRequestEntityTooLarge, "request_too_large", "request body exceeds the size limit")
			return false
		}
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be valid JSON")
		return false
	}
	if err := decoder.Decode(new(json.RawMessage)); !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid_request", "request body must be a single JSON value")
		return false
	}
	return true
}
