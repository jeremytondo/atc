package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// Four verbs on /v1/threads (ATC-255) plus one action (ATC-282):
// deliberately no POST /v1/threads — threads are observed into existence,
// not created — and archive/unarchive is a PATCH of archived. open is the
// one action route: it answers an intent (put me in front of this
// conversation) with a terminal, which no resource mutation expresses.
// Handlers are thin Huma wrappers around the shared wire structs; policy
// lives in the threads service.

type threadOutput struct {
	Body api.Thread
}

type threadListOutput struct {
	Body api.ThreadList
}

type threadOpenOutput struct {
	Body api.ThreadOpen
}

type threadIDInput struct {
	ID string `path:"id" doc:"Thread identifier."`
}

func registerThreads(humaAPI huma.API, service *threads.Service, agentService *agents.Service) {
	// The resume launch behind open is the agent catalog's; a server
	// without one can still reuse running terminals.
	var resume threads.Resumer
	if agentService != nil {
		resume = agentService.Resume
	}
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
		OperationID: "open-thread",
		Method:      http.MethodPost,
		Path:        "/v1/threads/{id}/open",
		Summary:     "Open a thread in a terminal",
		Description: "Resolves the thread to exactly one terminal under one server-side decision: a running terminal showing it is reused; else its last terminal, if still running with unknown contents, is reused rather than risk a second writer; else a new terminal runs the provider's exact resume and is recorded as the thread's terminal. Concurrent opens converge on one terminal. An archived thread is unarchived. The server never attaches.",
	}, func(ctx context.Context, input *threadIDInput) (*threadOpenOutput, error) {
		terminal, created, err := service.Open(ctx, input.ID, resume)
		if err != nil {
			return nil, mapThreadError(err)
		}
		terminal.ActiveThreadID = service.ActiveThreadID(terminal.ID)
		return &threadOpenOutput{Body: api.ThreadOpen{Terminal: terminal, Created: created}}, nil
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

// mapThreadError adds the thread mappings ahead of the agent and terminal
// ones, which open's resume launch can also surface (agent unavailable,
// unknown project, missing project directory).
func mapThreadError(err error) error {
	switch {
	case errors.Is(err, threads.ErrNotFound):
		return huma.Error404NotFound("thread not found")
	case errors.Is(err, threads.ErrActive):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, threads.ErrResumeUnavailable):
		return huma.Error422UnprocessableEntity("this server has no agent catalog")
	}
	return mapAgentError(err)
}
