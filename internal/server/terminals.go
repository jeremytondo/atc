package server

import (
	"context"
	"errors"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/application"
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

// createOutput carries the create's status: 201 for a terminal created,
// 200 for one reused by a thread resume.
type createOutput struct {
	Status int
	Body   api.Terminal
}

type terminalListOutput struct {
	Body api.TerminalList
}

type terminalIDInput struct {
	ID string `path:"id" doc:"Terminal identifier."`
}

func registerTerminals(humaAPI huma.API, service *terminals.Service, threadService *threads.Service, coordinator *application.Coordinator) {
	// The terminals domain knows nothing of threads, so the activeThreadId
	// projection is grafted onto its wire shape here, from the threads
	// service that owns it (ATC-255).
	decorate := func(terminal api.Terminal) api.Terminal {
		if threadService != nil {
			terminal.ActiveThreadID = threadService.ActiveThreadID(terminal.ID)
		}
		return terminal
	}
	// The reused-terminal 200 carries the same body as the 201, and the
	// operation's refusals are Problems: both declared on the document
	// explicitly, because Huma attaches the body schema only to the default
	// status and drops its catch-all error response once a second success
	// status is declared.
	create := huma.Operation{
		OperationID: "create-terminal",
		Method:      http.MethodPost,
		Path:        "/v1/terminals",
		Summary:     "Create a terminal",
		Description: "The one launch surface: a plain shell, a command, an App, or a thread's resume (command, appId, and threadId are mutually exclusive). The terminal lands in the named space (the Default space when omitted) and starts in the given directory (the space's when omitted; it must exist) — never in a thread's recorded directory; the record persists, the session starts, and a short verification window settles the status (a fast-failing command returns exited with its evidence). An App launch composes its command privately, records the App, and creates no thread until the Integration observes one. A thread resume reuses the terminal already running (or unreachable) for the thread, answering 200 with it unchanged, else runs the exact resume through the thread's App in a new terminal, linked and unarchived, answering 201; concurrent resumes converge on one terminal. A thread without terminal-capable App provenance is refused with thread_not_terminal_resumable. The private command and provider identity never appear.",
		Responses: map[string]*huma.Response{
			"200": {Description: "An existing terminal reused for the thread."},
		},
		Errors:        []int{http.StatusConflict, http.StatusUnprocessableEntity, http.StatusInternalServerError},
		DefaultStatus: http.StatusCreated,
	}
	huma.Register(humaAPI, create, func(ctx context.Context, input *struct {
		Body api.TerminalCreateParams
	}) (*createOutput, error) {
		terminal, created, err := coordinator.CreateTerminal(ctx, input.Body)
		if err != nil {
			return nil, mapCreateError(err)
		}
		status := http.StatusCreated
		if !created {
			status = http.StatusOK
		}
		return &createOutput{Status: status, Body: decorate(terminal)}, nil
	})
	// Register fills the default status's response in place; the reuse
	// status shares it.
	create.Responses["200"].Content = create.Responses["201"].Content

	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-terminals",
		Method:      http.MethodGet,
		Path:        "/v1/terminals",
		Summary:     "List terminals",
		Description: "Served from the reconciled in-memory view; exited and missing terminals stay listed until deleted. Unfiltered, returns every terminal in every space.",
	}, func(ctx context.Context, input *struct {
		Space string `query:"space" doc:"Only terminals belonging to this space."`
	}) (*terminalListOutput, error) {
		terminals := service.List(input.Space)
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
		Description: "A merge patch of name and spaceId: an omitted field is unchanged, neither accepts null. Moving a terminal to another space changes nothing else — not the session, the directory, the app, or any thread. A space being deleted refuses the move.",
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Terminal identifier."`
		Body api.TerminalUpdateParams
	}) (*terminalOutput, error) {
		terminal, err := service.Update(ctx, input.ID, input.Body)
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
		Description:   "Best-effort: stop intent is persisted, the kill attempted, and the record removed even when the kill cannot be verified. Threads the terminal hosted survive, unlinked.",
		DefaultStatus: http.StatusNoContent,
	}, func(ctx context.Context, input *terminalIDInput) (*struct{}, error) {
		if err := coordinator.DeleteTerminal(ctx, input.ID); err != nil {
			return nil, mapError(err)
		}
		return nil, nil
	})
}

// mapCreateError is the one mapping for everything a create can refuse
// across its four modes: a body naming an unknown App or thread is an
// unprocessable reference (422), not a missing route; an App or origin
// that cannot act now conflicts (409); a resume whose association could
// not persist is a server failure. The failures wrap the error that
// caused them — a thread gone at link time wraps ErrNotFound — so they
// are matched first.
func mapCreateError(err error) error {
	switch {
	case errors.Is(err, threads.ErrCompensationFailed):
		return problem(http.StatusInternalServerError, api.CodeCompensationFailed, err.Error())
	case errors.Is(err, threads.ErrLinkFailed):
		return problem(http.StatusInternalServerError, api.CodePersistenceFailed, err.Error())
	case errors.Is(err, application.ErrLaunchModeConflict):
		return problem(http.StatusUnprocessableEntity, api.CodeLaunchModeConflict, err.Error())
	case errors.Is(err, integrations.ErrAppNotFound):
		return problem(http.StatusUnprocessableEntity, api.CodeAppNotFound, err.Error())
	case errors.Is(err, integrations.ErrAppNotTerminal):
		return problem(http.StatusUnprocessableEntity, api.CodeAppNotTerminalCapable, err.Error())
	case errors.Is(err, integrations.ErrUnavailable):
		return problem(http.StatusConflict, api.CodeAppUnavailable, err.Error())
	case errors.Is(err, threads.ErrNotFound):
		return problem(http.StatusUnprocessableEntity, api.CodeThreadNotFound, "thread not found")
	case errors.Is(err, integrations.ErrNotResumable):
		return problem(http.StatusConflict, api.CodeThreadNotResumable, err.Error())
	case errors.Is(err, integrations.ErrOriginUnavailable):
		return problem(http.StatusConflict, api.CodeThreadAppUnavailable, err.Error())
	}
	return mapError(err)
}

func mapError(err error) error {
	switch {
	case errors.Is(err, terminals.ErrNotFound):
		return problem(http.StatusNotFound, api.CodeTerminalNotFound, "terminal not found")
	case errors.Is(err, terminals.ErrDirectoryInvalid):
		return problem(http.StatusUnprocessableEntity, api.CodeTerminalDirectoryInvalid, err.Error())
	case errors.Is(err, terminals.ErrSpaceNotFound):
		return problem(http.StatusUnprocessableEntity, api.CodeSpaceNotFound, err.Error())
	case errors.Is(err, terminals.ErrSpaceDeleting):
		return problem(http.StatusConflict, api.CodeSpaceDeleting, err.Error())
	case errors.Is(err, terminals.ErrInvalidUpdate):
		return problem(http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	}
	return err
}
