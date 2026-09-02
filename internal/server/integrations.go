package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
)

// Two GET routes under /v1/integrations (ATC-294): the compiled-in
// catalog, Apps and agent descriptors embedded. The surface is read-only
// — launching an App is a terminal create with an appId. Availability is
// re-probed on every read; connection-backed Integrations announce
// transitions as integration.updated.

type integrationOutput struct {
	Body api.Integration
}

type integrationListOutput struct {
	Body api.IntegrationList
}

func registerIntegrations(humaAPI huma.API, service *integrations.Service) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-integrations",
		Method:      http.MethodGet,
		Path:        "/v1/integrations",
		Summary:     "List integrations",
		Description: "The compiled-in Integrations in registration order, each with its Apps, agent descriptors, capability summary, and evidence-based health: an executable-backed Integration reports whether its binary resolves on this machine's PATH, a connection-backed one its live connection. Availability is advisory; launch re-probes.",
	}, func(ctx context.Context, _ *struct{}) (*integrationListOutput, error) {
		return &integrationListOutput{Body: api.IntegrationList{Integrations: service.List()}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-integration",
		Method:      http.MethodGet,
		Path:        "/v1/integrations/{id}",
		Summary:     "Get an integration",
	}, func(ctx context.Context, input *struct {
		ID string `path:"id" doc:"Integration id."`
	}) (*integrationOutput, error) {
		integration, err := service.Get(input.ID)
		if err != nil {
			return nil, mapIntegrationError(err)
		}
		return &integrationOutput{Body: integration}, nil
	})
}

// mapIntegrationError adds the catalog and launch mappings ahead of the
// terminal ones, which a launch can also surface (unknown project,
// missing project directory).
func mapIntegrationError(err error) error {
	switch {
	case errors.Is(err, integrations.ErrNotFound):
		return problem(http.StatusNotFound, api.CodeIntegrationNotFound, "integration not found")
	case errors.Is(err, integrations.ErrAppNotFound):
		return problem(http.StatusNotFound, api.CodeAppNotFound, err.Error())
	case errors.Is(err, integrations.ErrAppNotTerminal):
		return problem(http.StatusUnprocessableEntity, api.CodeAppNotTerminalCapable, err.Error())
	case errors.Is(err, integrations.ErrUnavailable):
		return problem(http.StatusConflict, api.CodeAppUnavailable, err.Error())
	case errors.Is(err, integrations.ErrNotResumable):
		return problem(http.StatusConflict, api.CodeThreadNotResumable, err.Error())
	}
	return mapError(err)
}
