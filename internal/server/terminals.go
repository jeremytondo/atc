package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
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

func registerTerminals(humaAPI huma.API, service *terminals.Service, catalog *integrations.Service,
	threadService *threads.Service, cleanups []func(terminalID string)) {
	// The terminals domain knows nothing of threads, so the activeThreadId
	// projection is grafted onto its wire shape here, from the threads
	// service that owns it (ATC-255).
	decorate := func(terminal api.Terminal) api.Terminal {
		if threadService != nil {
			terminal.ActiveThreadID = threadService.ActiveThreadID(terminal.ID)
		}
		return terminal
	}
	huma.Register(humaAPI, huma.Operation{
		OperationID:   "create-terminal",
		Method:        http.MethodPost,
		Path:          "/v1/terminals",
		Summary:       "Create a terminal",
		Description:   "Persists the record, starts the session, and waits a short verification window; a fast-failing command returns exited with its evidence. An appId launches that Integration-owned App instead: the Integration composes the command privately, the terminal records the App, and a thread appears once the Integration observes a conversation — the one launch path. appId and command are mutually exclusive.",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		Body api.TerminalCreateParams
	}) (*terminalOutput, error) {
		if input.Body.AppID != "" {
			if input.Body.Command != "" {
				return nil, problem(http.StatusUnprocessableEntity, api.CodeLaunchModeConflict, "appId and command are mutually exclusive")
			}
			if catalog == nil {
				return nil, problem(http.StatusUnprocessableEntity, api.CodeAppNotFound, "this server has no integration catalog")
			}
			terminal, err := catalog.Launch(ctx, input.Body.AppID, input.Body.ProjectID, input.Body.Name)
			if err != nil {
				return nil, mapIntegrationError(err)
			}
			return &terminalOutput{Body: decorate(terminal)}, nil
		}
		terminal, err := service.Create(ctx, input.Body)
		if err != nil {
			return nil, mapError(err)
		}
		return &terminalOutput{Body: decorate(terminal)}, nil
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
		terminals := service.List(input.Project)
		for i, terminal := range terminals {
			terminals[i] = decorate(terminal)
		}
		return &terminalListOutput{Body: api.TerminalList{Terminals: terminals}}, nil
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
		return &terminalOutput{Body: decorate(terminal)}, nil
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
		return &terminalOutput{Body: decorate(terminal)}, nil
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
		// Revoke the deleted terminal's hook secrets before converging the
		// threads view: each cleanup is a barrier (it returns only after
		// any in-flight delivery drains), so no late delivery can land
		// evidence over the convergence below.
		for _, cleanup := range cleanups {
			cleanup(input.ID)
		}
		if threadService != nil {
			// The schema's ON DELETE SET NULL already cleared the rows;
			// this converges the threads view and publishes the linkage
			// change. Wired here because the terminals domain must not
			// know threads exist. Detached like the delete itself: a
			// client disconnect after the commit must not leave the view
			// linked to a deleted terminal.
			threadService.TerminalRemoved(context.WithoutCancel(ctx), input.ID)
		}
		return nil, nil
	})
}

func mapError(err error) error {
	switch {
	case errors.Is(err, terminals.ErrNotFound):
		return problem(http.StatusNotFound, api.CodeTerminalNotFound, "terminal not found")
	case errors.Is(err, terminals.ErrProjectUnknown):
		return problem(http.StatusUnprocessableEntity, api.CodeProjectNotFound, err.Error())
	case errors.Is(err, terminals.ErrProjectDirectoryMissing):
		return problem(http.StatusConflict, api.CodeProjectDirectoryMissing, err.Error())
	}
	return err
}
