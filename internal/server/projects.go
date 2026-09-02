package server

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/threads"
)

// The five standard verbs on /v1/projects — exactly these, mirroring the
// terminals surface (ATC-256). Handlers are thin Huma wrappers around the
// shared wire structs; policy lives in the projects service. Thread
// classification is the threads domain's (ATC-295): a create or move
// backfills through it here, so neither domain imports the other.

type projectOutput struct {
	Body api.Project
}

type projectListOutput struct {
	Body api.ProjectList
}

type projectIDInput struct {
	ID string `path:"id" doc:"Project identifier."`
}

func registerProjects(humaAPI huma.API, service *projects.Service, threadService *threads.Service, logger *slog.Logger) {
	// backfill reclassifies the unassigned threads after a project is
	// created or moved. Detached: the project is committed, and a client
	// that disconnects must not leave threads it should own unassigned. A
	// failure is logged, not surfaced — the project exists either way, and
	// because the backfill scans every unassigned thread, the next create
	// or move repairs it.
	backfill := func(ctx context.Context, projectID string) {
		if threadService == nil {
			return
		}
		if err := threadService.Backfill(context.WithoutCancel(ctx)); err != nil {
			logger.Error("backfilling threads after a project change", "project", projectID, "error", err)
		}
	}
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
		project, err := service.Create(ctx, input.Body)
		if err != nil {
			return nil, mapProjectError(err)
		}
		backfill(ctx, project.ID)
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
		project, moved, err := service.Update(ctx, input.ID, input.Body)
		if err != nil {
			return nil, mapProjectError(err)
		}
		if moved {
			backfill(ctx, project.ID)
		}
		return &projectOutput{Body: project}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "delete-project",
		Method:        http.MethodDelete,
		Path:          "/v1/projects/{id}",
		Summary:       "Delete a project",
		Description:   "Refused while any terminal still belongs to the project, reporting what remains. Threads survive, unassigned; they are not reassigned to a less specific project.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *projectIDInput) (*struct{}, error) {
		remove := func() error { return service.Delete(ctx, input.ID) }
		if threadService == nil {
			if err := remove(); err != nil {
				return nil, mapProjectError(err)
			}
			return nil, nil
		}
		// The delete runs under the threads domain's mutation lock: the
		// schema clears the project's thread associations and the threads
		// view converges before any observation can copy the stale id
		// back. Wired here because the projects domain must not know
		// threads exist.
		if err := threadService.DeleteProject(ctx, input.ID, remove); err != nil {
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
	case errors.Is(err, projects.ErrNotEmpty):
		return problem(http.StatusConflict, api.CodeProjectNotEmpty, err.Error())
	}
	return err
}
