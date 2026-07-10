package api

import (
	"net/http"

	"github.com/jeremytondo/atc/internal/session"
)

type environmentsResponse struct {
	Environments []environmentResponse `json:"environments"`
}

type environmentResponse struct {
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Label       string `json:"label,omitempty"`
	Description string `json:"description,omitempty"`
	Default     bool   `json:"default,omitempty"`
}

func (routes apiRoutes) listEnvironments(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	environments := routes.sessions.Environments(r.Context())
	resp := environmentsResponse{Environments: make([]environmentResponse, 0, len(environments))}
	for _, environment := range environments {
		resp.Environments = append(resp.Environments, environmentDiscoveryResponse(environment))
	}
	writeJSON(w, http.StatusOK, resp)
}

func environmentDiscoveryResponse(environment session.EnvironmentDiscovery) environmentResponse {
	return environmentResponse{
		Name:        environment.Name,
		Kind:        string(environment.Kind),
		Label:       environment.Label,
		Description: environment.Description,
		Default:     environment.Default,
	}
}
