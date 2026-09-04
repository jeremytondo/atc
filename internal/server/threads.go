package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/application"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/threads"
)

// Five verbs on /v1/threads (ATC-255, ATC-289): create starts a
// conversation in an Integration's program — the one write that reaches
// outside ATC — and archive/unarchive is a PATCH of archived. Putting a
// user in front of a conversation is a terminal create with threadId
// (ATC-297), not an action here. Handlers are thin Huma wrappers around
// the shared wire structs; policy lives in the threads service and, for
// create, the application coordinator.

type threadOutput struct {
	Body api.Thread
}

type threadListOutput struct {
	Body api.ThreadList
}

type threadIDInput struct {
	ID string `path:"id" doc:"Thread identifier."`
}

func registerThreads(humaAPI huma.API, service *threads.Service, coordinator *application.Coordinator) {
	huma.Register(humaAPI, huma.Operation{
		OperationID:   "create-thread",
		Method:        http.MethodPost,
		Path:          "/v1/threads",
		Summary:       "Create a thread",
		Description:   "Starts a new conversation with its first prompt in the named Integration's program (only t3code creates threads). The Project resolves by directory to the program's own project. The thread is returned as soon as the program has committed the thread and its first turn, working on a provisional latestTurn until the program reports it; the program's events drive it from there exactly as for a conversation started inside the program. Model and options are opaque and never validated by ATC: a value the program rejects surfaces later as the thread's status and detail. Refusals: 400 for the request (unknown or non-creating Integration, unlisted agent, empty prompt or model, option without an id), 404 for an unknown Project, 409 for a Project the program has not registered, 503 while the Integration is not connected (the detail names the state), 502 when the program rejects the command (the detail is its message; no thread remains).",
		DefaultStatus: http.StatusCreated,
	}, func(ctx context.Context, input *struct {
		Body api.ThreadCreateParams
	}) (*threadOutput, error) {
		thread, err := coordinator.CreateThread(ctx, input.Body)
		if err != nil {
			return nil, mapThreadCreateError(err)
		}
		return &threadOutput{Body: thread}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-threads",
		Method:      http.MethodGet,
		Path:        "/v1/threads",
		Summary:     "List threads",
		Description: "Served from the in-memory view. Archived threads are hidden unless includeArchived; unfiltered, returns every unarchived thread.",
	}, func(ctx context.Context, input *struct {
		Project         string `query:"project" doc:"Only threads belonging to this project."`
		Terminal        string `query:"terminal" doc:"Only threads whose last observed terminal is this one."`
		IncludeArchived bool   `query:"includeArchived" doc:"Include archived threads."`
	}) (*threadListOutput, error) {
		return &threadListOutput{Body: api.ThreadList{
			Threads: service.List(input.Project, input.Terminal, input.IncludeArchived),
		}}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-thread",
		Method:      http.MethodGet,
		Path:        "/v1/threads/{id}",
		Summary:     "Get a thread",
	}, func(ctx context.Context, input *threadIDInput) (*threadOutput, error) {
		thread, err := service.Get(input.ID)
		if err != nil {
			return nil, mapThreadError(err)
		}
		return &threadOutput{Body: thread}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "update-thread",
		Method:      http.MethodPatch,
		Path:        "/v1/threads/{id}",
		Summary:     "Update a thread",
		Description: "A merge patch of title, archived, and projectId: omitted fields are unchanged, null clears projectId (title and archived cannot be null). A title set here is never overwritten by observation. Archiving an active thread — one a terminal has open, or one its external program still reports — is refused, naming the holder. A project assignment may name any project; a cleared thread stays unassigned until a project is created or moved to contain its initial directory.",
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Thread identifier."`
		Body api.ThreadUpdateParams
	}) (*threadOutput, error) {
		thread, err := service.Update(ctx, input.ID, input.Body)
		if err != nil {
			return nil, mapThreadError(err)
		}
		return &threadOutput{Body: thread}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "delete-thread",
		Method:        http.MethodDelete,
		Path:          "/v1/threads/{id}",
		Summary:       "Delete a thread",
		Description:   "Removes ATC's record and its private identity mapping only; the provider-side conversation is never touched. An active thread — one a terminal has open, or one its external program still reports — is refused, naming the holder.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *threadIDInput) (*struct{}, error) {
		if err := service.Delete(ctx, input.ID); err != nil {
			return nil, mapThreadError(err)
		}
		return nil, nil
	})
}

// mapThreadCreateError maps the create's refusals, each to the status the
// caller acts on: fix the request (400), the Project (404), register it
// in the program (409), start or pair the program (503), or read the
// program's own rejection (502).
func mapThreadCreateError(err error) error {
	switch {
	case errors.Is(err, application.ErrThreadCreateInvalid):
		return problem(http.StatusBadRequest, api.CodeValidationFailed, err.Error())
	case errors.Is(err, integrations.ErrNotFound):
		return problem(http.StatusBadRequest, api.CodeIntegrationNotFound, err.Error())
	case errors.Is(err, integrations.ErrThreadCreationUnsupported):
		return problem(http.StatusBadRequest, api.CodeThreadCreationUnsupported, err.Error())
	case errors.Is(err, integrations.ErrAgentNotFound):
		return problem(http.StatusBadRequest, api.CodeAgentNotFound, err.Error())
	case errors.Is(err, projects.ErrNotFound):
		return problem(http.StatusNotFound, api.CodeProjectNotFound, "project not found")
	case errors.Is(err, integrations.ErrProjectNotRegistered):
		return problem(http.StatusConflict, api.CodeProjectNotRegistered, err.Error())
	case errors.Is(err, integrations.ErrNotConnected):
		return problem(http.StatusServiceUnavailable, api.CodeIntegrationNotConnected, err.Error())
	case errors.Is(err, integrations.ErrThreadCreationFailed):
		return problem(http.StatusBadGateway, api.CodeThreadCreationFailed, err.Error())
	case errors.Is(err, threads.ErrNoLocalDirectory):
		return problem(http.StatusUnprocessableEntity, api.CodeProjectDirectoryInvalid, err.Error())
	}
	return mapThreadError(err)
}

func mapThreadError(err error) error {
	switch {
	case errors.Is(err, threads.ErrNotFound):
		return problem(http.StatusNotFound, api.CodeThreadNotFound, "thread not found")
	case errors.Is(err, threads.ErrActive):
		return problem(http.StatusConflict, api.CodeThreadActive, err.Error())
	case errors.Is(err, threads.ErrProjectUnknown):
		return problem(http.StatusUnprocessableEntity, api.CodeProjectNotFound, err.Error())
	case errors.Is(err, threads.ErrInvalidUpdate):
		return problem(http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	}
	return err
}
