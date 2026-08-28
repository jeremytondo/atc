package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/projects"
)

// The five standard verbs on /v1/projects — exactly these, mirroring the
// terminals surface (ATC-256). Handlers are thin Huma wrappers around the
// shared wire structs; policy lives in the projects service.

type projectOutput struct {
	Body api.Project
}

type projectListOutput struct {
	Body api.ProjectList
}

type projectIDInput struct {
	ID string `path:"id" doc:"Project identifier."`
}

func registerProjects(humaAPI huma.API, service *projects.Service) {
	huma.Register(humaAPI, huma.Operation{
		OperationID:   "create-project",
		Method:        http.MethodPost,
		Path:          "/v1/projects",
		Summary:       "Create a project",
		Description:   "Canonicalizes the directory (absolute, cleaned, symlinks resolved) and stores the canonical form; the path must exist and be a directory, and its canonical form must not already belong to a project.",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		Body api.ProjectCreateParams
	}) (*projectOutput, error) {
		project, err := service.Create(ctx, input.Body)
		if err != nil {
			return nil, mapProjectError(err)
		}
		return &projectOutput{Body: project}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-projects",
		Method:      http.MethodGet,
		Path:        "/v1/projects",
		Summary:     "List projects",
	}, func(ctx context.Context, _ *struct{}) (*projectListOutput, error) {
		list, err := service.List(ctx)
		if err != nil {
			return nil, mapProjectError(err)
		}
		return &projectListOutput{Body: api.ProjectList{Projects: list}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-project",
		Method:      http.MethodGet,
		Path:        "/v1/projects/{id}",
		Summary:     "Get a project",
	}, func(ctx context.Context, input *projectIDInput) (*projectOutput, error) {
		project, err := service.Get(ctx, input.ID)
		if err != nil {
			return nil, mapProjectError(err)
		}
		return &projectOutput{Body: project}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "update-project",
		Method:      http.MethodPatch,
		Path:        "/v1/projects/{id}",
		Summary:     "Update a project",
		Description: "Name is the only mutable field; unknown or immutable fields are rejected.",
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Project identifier."`
		Body api.ProjectUpdateParams
	}) (*projectOutput, error) {
		project, err := service.UpdateName(ctx, input.ID, input.Body.Name)
		if err != nil {
			return nil, mapProjectError(err)
		}
		return &projectOutput{Body: project}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "delete-project",
		Method:        http.MethodDelete,
		Path:          "/v1/projects/{id}",
		Summary:       "Delete a project",
		Description:   "Refused while any terminal still belongs to the project, reporting what remains. No cascade.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *projectIDInput) (*struct{}, error) {
		if err := service.Delete(ctx, input.ID); err != nil {
			return nil, mapProjectError(err)
		}
		return nil, nil
	})
}

func mapProjectError(err error) error {
	var notEmpty *projects.NotEmptyError
	switch {
	case errors.Is(err, projects.ErrNotFound):
		return huma.Error404NotFound("project not found")
	case errors.Is(err, projects.ErrDirectoryInvalid):
		return huma.Error422UnprocessableEntity(err.Error())
	case errors.Is(err, projects.ErrDirectoryTaken):
		return huma.Error409Conflict(err.Error())
	case errors.As(err, &notEmpty):
		return huma.Error409Conflict(err.Error())
	}
	return err
}
