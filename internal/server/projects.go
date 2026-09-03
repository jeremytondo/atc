package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/application"
	"github.com/jeremytondo/atc/internal/projects"
)

// The five standard verbs on /v1/projects — exactly these, mirroring the
// terminals surface (ATC-256). Handlers are thin Huma wrappers around the
// shared wire structs; policy lives in the projects service, and the
// mutations that touch thread classification (ATC-295) run through the
// application coordinator.

type projectOutput struct {
	Body api.Project
}

type projectListOutput struct {
	Body api.ProjectList
}

type projectIDInput struct {
	ID string `path:"id" doc:"Project identifier."`
}

func registerProjects(humaAPI huma.API, service *projects.Service, coordinator *application.Coordinator) {
	huma.Register(humaAPI, huma.Operation{
		OperationID:   "create-project",
		Method:        http.MethodPost,
		Path:          "/v1/projects",
		Summary:       "Create a project",
		Description:   "Canonicalizes the directory (absolute, cleaned, symlinks resolved) and stores the canonical form; the path must exist and be a directory, and its canonical form must not already belong to a project. Unassigned threads whose initial directory the project contains join it when it is their most specific match, archived ones included.",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		Body api.ProjectCreateParams
	}) (*projectOutput, error) {
		project, err := coordinator.CreateProject(ctx, input.Body)
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
		Description: "A merge patch of name and directory: omitted fields are unchanged. A new directory is canonicalized and must not belong to another project; moving a project backfills unassigned threads under the new directory and never rewrites existing associations.",
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Project identifier."`
		Body api.ProjectUpdateParams
	}) (*projectOutput, error) {
		project, err := coordinator.UpdateProject(ctx, input.ID, input.Body)
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
		Description:   "Threads survive, unassigned; they are not reassigned to a less specific project. Terminals and spaces are untouched — projects own neither.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *projectIDInput) (*struct{}, error) {
		if err := coordinator.DeleteProject(ctx, input.ID); err != nil {
			return nil, mapProjectError(err)
		}
		return nil, nil
	})
}

func mapProjectError(err error) error {
	switch {
	case errors.Is(err, projects.ErrNotFound):
		return problem(http.StatusNotFound, api.CodeProjectNotFound, "project not found")
	case errors.Is(err, projects.ErrDirectoryInvalid):
		return problem(http.StatusUnprocessableEntity, api.CodeProjectDirectoryInvalid, err.Error())
	case errors.Is(err, projects.ErrNameInvalid), errors.Is(err, projects.ErrInvalidUpdate):
		return problem(http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	case errors.Is(err, projects.ErrDirectoryTaken):
		return problem(http.StatusConflict, api.CodeProjectDirectoryTaken, err.Error())
	}
	return err
}
