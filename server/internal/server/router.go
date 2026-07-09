package server

import (
	"net/http"

	"github.com/jeremytondo/atelier-code/internal/action"
	"github.com/jeremytondo/atelier-code/internal/api"
	"github.com/jeremytondo/atelier-code/internal/assets"
	"github.com/jeremytondo/atelier-code/internal/diagnostics"
	"github.com/jeremytondo/atelier-code/internal/fs"
	"github.com/jeremytondo/atelier-code/internal/project"
	"github.com/jeremytondo/atelier-code/internal/session"
)

// Router builds the HTTP handler, wiring the API (including session routes
// backed by sessions, project routes backed by projects, and filesystem
// browsing backed by fsService) and the embedded web UI. The API is guarded by
// authToken on the TCP listener; an empty token disables TCP authentication.
func Router(sessions *session.Service, projects *project.Service, actions *action.Store, fsService *fs.Service, authToken string) http.Handler {
	webFS, err := assets.WebFS()
	if err != nil {
		return routerWithWeb(sessions, projects, actions, fsService, authToken, webAssetError(http.StatusInternalServerError, "embedded web assets are unavailable"))
	}
	return routerWithWeb(sessions, projects, actions, fsService, authToken, webAssetRoutes(webFS))
}

func routerWithWeb(sessions *session.Service, projects *project.Service, actions *action.Store, fsService *fs.Service, authToken string, web http.Handler) http.Handler {
	mux := http.NewServeMux()
	apiHandler := http.StripPrefix("/api", api.Routes(diagnostics.DefaultDiagnostics(), sessions, projects, actions, fsService))
	mux.Handle("/api/", withAuth(authToken, apiHandler))
	mux.Handle("/", web)
	return mux
}
