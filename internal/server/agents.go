package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/api"
)

// Four GET routes under /v1/agents (ATC-254, ATC-285): the derived agent
// catalog and the adapter list behind it. The surface is read-only —
// launching is a terminal create with an agent reference. Availability
// is re-probed on every read; launchers emit no events, and observing
// adapters announce connection changes as agent_adapter.updated.

type agentOutput struct {
	Body api.Agent
}

type agentListOutput struct {
	Body api.AgentList
}

type agentAdapterOutput struct {
	Body api.AgentAdapter
}

type agentAdapterListOutput struct {
	Body api.AgentAdapterList
}

func registerAgents(humaAPI huma.API, service *agents.Service) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-agents",
		Method:      http.MethodGet,
		Path:        "/v1/agents",
		Summary:     "List agents",
		Description: "The launchable agents, derived from the adapter catalog: each names the adapters that produce threads for it, and is available when some adapter can launch it — probed against this machine's PATH on every request. Availability is advisory; launch re-probes.",
	}, func(ctx context.Context, _ *struct{}) (*agentListOutput, error) {
		return &agentListOutput{Body: api.AgentList{Agents: service.List()}}, nil
	})

	// Registered before the id route: the mux prefers the literal segment,
	// and the OpenAPI document lists the routes in registration order.
	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-agent-adapters",
		Method:      http.MethodGet,
		Path:        "/v1/agents/adapters",
		Summary:     "List agent adapters",
		Description: "The compiled-in adapters that produce threads, in registration order: launchers of local agent TUIs report whether their binary resolves; observers of external programs report their live connection.",
	}, func(ctx context.Context, _ *struct{}) (*agentAdapterListOutput, error) {
		return &agentAdapterListOutput{Body: api.AgentAdapterList{Adapters: service.Adapters()}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-agent-adapter",
		Method:      http.MethodGet,
		Path:        "/v1/agents/adapters/{id}",
		Summary:     "Get an agent adapter",
	}, func(ctx context.Context, input *struct {
		ID string `path:"id" doc:"Adapter id."`
	}) (*agentAdapterOutput, error) {
		adapter, err := service.Adapter(input.ID)
		if err != nil {
			return nil, mapAgentError(err)
		}
		return &agentAdapterOutput{Body: adapter}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-agent",
		Method:      http.MethodGet,
		Path:        "/v1/agents/{id}",
		Summary:     "Get an agent",
	}, func(ctx context.Context, input *struct {
		ID string `path:"id" doc:"Agent id."`
	}) (*agentOutput, error) {
		agent, err := service.Get(input.ID)
		if err != nil {
			return nil, mapAgentError(err)
		}
		return &agentOutput{Body: agent}, nil
	})
}

// mapAgentError adds the agent mappings ahead of the terminal ones, which
// a launch can also surface (unknown project, missing project directory).
func mapAgentError(err error) error {
	switch {
	case errors.Is(err, agents.ErrNotFound):
		return huma.Error404NotFound("agent not found")
	case errors.Is(err, agents.ErrUnavailable):
		return huma.Error409Conflict(err.Error())
	}
	return mapError(err)
}
