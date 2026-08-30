package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// Four verbs on /v1/threads (ATC-255): deliberately no POST — threads are
// observed into existence, not created — and no custom action routes;
// archive/unarchive is a PATCH of archived. Handlers are thin Huma
// wrappers around the shared wire structs; policy lives in the threads
// service.

type threadOutput struct {
	Body api.Thread
}

type threadListOutput struct {
	Body api.ThreadList
}

type threadIDInput struct {
	ID string `path:"id" doc:"Thread identifier."`
}

func registerThreads(humaAPI huma.API, service *threads.Service) {
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
		Description: "Title and archived are the only mutable fields. A title set here is never overwritten by observation. Archiving a thread a terminal has open is refused, naming the terminal.",
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
		Description:   "Removes ATC's record and its private identity mapping only; the provider-side conversation is never touched. A thread a terminal has open is refused, naming the terminal.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *threadIDInput) (*struct{}, error) {
		if err := service.Delete(ctx, input.ID); err != nil {
			return nil, mapThreadError(err)
		}
		return nil, nil
	})
}

func mapThreadError(err error) error {
	switch {
	case errors.Is(err, threads.ErrNotFound):
		return huma.Error404NotFound("thread not found")
	case errors.Is(err, threads.ErrActive):
		return huma.Error409Conflict(err.Error())
	}
	return err
}
