package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/application"
	"github.com/jeremytondo/atc/internal/terminals"
)

// The five standard verbs on /v1/spaces (ATC-296): flat and top-level,
// never nested under terminals. Handlers are thin Huma wrappers around
// the shared wire structs; policy lives in the terminals service, and
// deletion runs through the application coordinator so every terminal
// in the space gets the complete deletion workflow.

type spaceOutput struct {
	Body api.Space
}

type spaceListOutput struct {
	Body api.SpaceList
}

type spaceIDInput struct {
	ID string `path:"id" doc:"Space identifier."`
}

func registerSpaces(humaAPI huma.API, service *terminals.Service, coordinator *application.Coordinator) {
	huma.Register(humaAPI, huma.Operation{
		OperationID:   "create-space",
		Method:        http.MethodPost,
		Path:          "/v1/spaces",
		Summary:       "Create a space",
		Description:   "Canonicalizes the directory (absolute, cleaned, symlinks resolved; the server user's home when omitted) and stores the canonical form; the path must exist and be a directory. Spaces may share or overlap directories.",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		Body api.SpaceCreateParams
	}) (*spaceOutput, error) {
		space, err := service.CreateSpace(ctx, input.Body)
		if err != nil {
			return nil, mapSpaceError(err)
		}
		return &spaceOutput{Body: space}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-spaces",
		Method:      http.MethodGet,
		Path:        "/v1/spaces",
		Summary:     "List spaces",
		Description: "Every space in creation order, the Default space first.",
	}, func(ctx context.Context, _ *struct{}) (*spaceListOutput, error) {
		return &spaceListOutput{Body: api.SpaceList{Spaces: service.ListSpaces()}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-space",
		Method:      http.MethodGet,
		Path:        "/v1/spaces/{id}",
		Summary:     "Get a space",
	}, func(ctx context.Context, input *spaceIDInput) (*spaceOutput, error) {
		space, err := service.GetSpace(input.ID)
		if err != nil {
			return nil, mapSpaceError(err)
		}
		return &spaceOutput{Body: space}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "update-space",
		Method:      http.MethodPatch,
		Path:        "/v1/spaces/{id}",
		Summary:     "Update a space",
		Description: "A merge patch of name and directory: omitted fields are unchanged. A new directory applies to terminals created afterwards; existing terminals keep theirs. The Default space is refused.",
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Space identifier."`
		Body api.SpaceUpdateParams
	}) (*spaceOutput, error) {
		space, err := service.UpdateSpace(ctx, input.ID, input.Body)
		if err != nil {
			return nil, mapSpaceError(err)
		}
		return &spaceOutput{Body: space}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "delete-space",
		Method:        http.MethodDelete,
		Path:          "/v1/spaces/{id}",
		Summary:       "Delete a space and its terminals",
		Description:   "Deletes every terminal in the space through the normal terminal deletion (stop intent, best-effort kill, cleanup, thread linkage cleared; threads survive), then the space. No confirmation step: clients own that. The Default space is refused.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *spaceIDInput) (*struct{}, error) {
		if err := coordinator.DeleteSpace(ctx, input.ID); err != nil {
			return nil, mapSpaceError(err)
		}
		return nil, nil
	})
}

func mapSpaceError(err error) error {
	switch {
	case errors.Is(err, terminals.ErrSpaceNotFound):
		return problem(http.StatusNotFound, api.CodeSpaceNotFound, err.Error())
	case errors.Is(err, terminals.ErrDefaultSpace):
		return problem(http.StatusConflict, api.CodeSpaceDefault, err.Error())
	case errors.Is(err, terminals.ErrSpaceDeleting):
		return problem(http.StatusConflict, api.CodeSpaceDeleting, err.Error())
	case errors.Is(err, terminals.ErrSpaceDirectoryInvalid):
		return problem(http.StatusUnprocessableEntity, api.CodeSpaceDirectoryInvalid, err.Error())
	case errors.Is(err, terminals.ErrSpaceNameInvalid), errors.Is(err, terminals.ErrInvalidUpdate):
		return problem(http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	}
	return mapError(err)
}
