package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/terminals"
)

// The five standard verbs on /v1/terminals — exactly these, no custom
// action routes in v1 (ATC-251). Handlers are thin Huma wrappers around
// the shared wire structs; policy lives in the terminals service.

type terminalOutput struct {
	Body api.Terminal
}

type terminalListOutput struct {
	Body api.TerminalList
}

type terminalIDInput struct {
	ID string `path:"id" doc:"Terminal identifier."`
}

func registerTerminals(humaAPI huma.API, service *terminals.Service) {
	huma.Register(humaAPI, huma.Operation{
		OperationID:   "create-terminal",
		Method:        http.MethodPost,
		Path:          "/v1/terminals",
		Summary:       "Create a terminal",
		Description:   "Persists the record, starts the session, and waits a short verification window; a fast-failing app returns exited with its evidence.",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		Body api.TerminalCreateParams
	}) (*terminalOutput, error) {
		terminal, err := service.Create(ctx, input.Body)
		if err != nil {
			return nil, mapError(err)
		}
		return &terminalOutput{Body: terminal}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-terminals",
		Method:      http.MethodGet,
		Path:        "/v1/terminals",
		Summary:     "List terminals",
		Description: "Served from the reconciled in-memory view; exited and missing terminals stay listed until deleted. Unfiltered, returns every terminal.",
	}, func(ctx context.Context, input *struct {
		Project string `query:"project" doc:"Only terminals belonging to this project."`
	}) (*terminalListOutput, error) {
		return &terminalListOutput{Body: api.TerminalList{Terminals: service.List(input.Project)}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-terminal",
		Method:      http.MethodGet,
		Path:        "/v1/terminals/{id}",
		Summary:     "Get a terminal",
	}, func(ctx context.Context, input *terminalIDInput) (*terminalOutput, error) {
		terminal, err := service.Get(input.ID)
		if err != nil {
			return nil, mapError(err)
		}
		return &terminalOutput{Body: terminal}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "update-terminal",
		Method:      http.MethodPatch,
		Path:        "/v1/terminals/{id}",
		Summary:     "Update a terminal",
		Description: "Name is the only mutable field; unknown or immutable fields are rejected.",
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Terminal identifier."`
		Body api.TerminalUpdateParams
	}) (*terminalOutput, error) {
		terminal, err := service.UpdateName(ctx, input.ID, input.Body.Name)
		if err != nil {
			return nil, mapError(err)
		}
		return &terminalOutput{Body: terminal}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "delete-terminal",
		Method:        http.MethodDelete,
		Path:          "/v1/terminals/{id}",
		Summary:       "Delete a terminal",
		Description:   "Best-effort: stop intent is persisted, the kill attempted, and the record removed even when the kill cannot be verified.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *terminalIDInput) (*struct{}, error) {
		if err := service.Delete(ctx, input.ID); err != nil {
			return nil, mapError(err)
		}
		return nil, nil
	})
}

func mapError(err error) error {
	switch {
	case errors.Is(err, terminals.ErrNotFound):
		return huma.Error404NotFound("terminal not found")
	case errors.Is(err, terminals.ErrProjectUnknown):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, terminals.ErrProjectDirectoryMissing):
		return huma.Error409Conflict(err.Error())
	}
	return err
}
