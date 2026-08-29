package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/api"
)

// Exactly three routes on /v1/agents (ATC-254): the catalog surface is
// GET-only, and launch is an action producing a terminal, not a mutation
// of the catalog. Availability is re-probed on every read and the catalog
// emits no events; clients refetch on demand.

type agentOutput struct {
	Body api.Agent
}

type agentListOutput struct {
	Body api.AgentList
}

func registerAgents(humaAPI huma.API, service *agents.Service) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-agents",
		Method:      http.MethodGet,
		Path:        "/v1/agents",
		Summary:     "List agents",
		Description: "The compiled-in agent catalog in registration order, with per-capability availability probed against this machine's PATH on every request. Availability is advisory; launch re-probes.",
	}, func(ctx context.Context, _ *struct{}) (*agentListOutput, error) {
		return &agentListOutput{Body: api.AgentList{Agents: service.List()}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-agent",
		Method:      http.MethodGet,
		Path:        "/v1/agents/{id}",
		Summary:     "Get an agent",
	}, func(ctx context.Context, input *struct {
		ID string `path:"id" doc:"Agent catalog id."`
	}) (*agentOutput, error) {
		agent, err := service.Get(input.ID)
		if err != nil {
			return nil, mapAgentError(err)
		}
		return &agentOutput{Body: agent}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "launch-agent",
		Method:        http.MethodPost,
		Path:          "/v1/agents/{id}/launch",
		Summary:       "Launch an agent in a new terminal",
		Description:   "Equivalent to creating a terminal with an agent reference: the server resolves the launch command through the agent's tui adapter and runs the normal terminal create path. A missing binary is refused before any record exists; past that probe, failures surface as terminal exit evidence.",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Agent catalog id."`
		Body api.AgentLaunchParams
	}) (*terminalOutput, error) {
		terminal, err := service.Launch(ctx, input.ID, input.Body)
		if err != nil {
			return nil, mapAgentError(err)
		}
		return &terminalOutput{Body: terminal}, nil
	})
}

// mapAgentError adds the agent mappings ahead of the terminal ones, which
// launch can also surface (unknown project, missing project directory).
func mapAgentError(err error) error {
	switch {
	case errors.Is(err, agents.ErrNotFound):
		return huma.Error404NotFound("agent not found")
	case errors.Is(err, agents.ErrUnavailable):
		return huma.Error409Conflict(err.Error())
	}
	return mapError(err)
}
