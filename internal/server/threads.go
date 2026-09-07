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

// Five verbs on /v1/threads (ATC-255, ATC-289), plus the interactions
// that drive a conversation from outside (ATC-307, ATC-308): create
// starts a conversation in an Integration's program, a message continues
// one, an approval decision or a structured answer resolves what it is
// blocked on, a stop ends its work — the writes that reach outside ATC —
// and archive/unarchive is a PATCH of archived.
// Putting a user in front of a conversation is a terminal create with
// threadId (ATC-297), not an action here. Handlers are thin Huma
// wrappers around the shared wire structs; policy lives in the threads
// service and, for the writes that reach a program, the application
// coordinator.

type threadOutput struct {
	Body api.Thread
}

type threadListOutput struct {
	Body api.ThreadList
}

type threadIDInput struct {
	ID string `path:"id" doc:"Thread identifier."`
}

type threadMessageOutput struct {
	Body api.ThreadMessage
}

type threadApprovalOutput struct {
	Body api.ThreadApproval
}

type threadInputRequestOutput struct {
	Body api.ThreadInputRequest
}

type threadStopOutput struct {
	Body api.ThreadStop
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
		Description: "A merge patch of title, archived, and projectId: omitted fields are unchanged, null clears projectId (title and archived cannot be null). A title set here is never overwritten by observation. Archiving an active thread — one a terminal has open, or one its provider still reports — is refused, naming the holder. A project assignment may name any project; a cleared thread stays unassigned until a project is created or moved to contain its initial directory.",
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
		OperationID:   "send-thread-message",
		Method:        http.MethodPost,
		Path:          "/v1/threads/{id}/messages",
		Summary:       "Send a message to a thread",
		Description:   "Directs a text message at an existing conversation under its current agent, model, and settings: on an idle thread it starts the next turn (the thread's pendingTurn, bound to the provider's turn once it starts); while a turn runs it applies at the next opportunity the provider supports — folded into the running turn where the provider steers, started once it ends otherwise — with no queue mode of ATC's own. Returns 202 with the message and its turnId: the execution to wait on, followed through pendingTurn and latestTurn and never a later turn. delivery is accepted once the program committed the message, uncertain when it never answered; a resubmission with the same key returns the recorded message, retrying an uncertain delivery with the exact same command (the program deduplicates), so a lost answer never sends twice. A pending question or approval is not interpreted: the text is sent as any other, and the thread keeps showing what the provider does. Refusals: 400 for blank text or an Integration that cannot send, 404 for an unknown thread, 409 while another submission is pending on the thread, 503 while the Integration is not connected, 502 when the program rejects the message (the detail is its own; nothing remains to wait on).",
		DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Thread identifier."`
		Body api.ThreadMessageParams
	}) (*threadMessageOutput, error) {
		message, err := coordinator.SendMessage(ctx, input.ID, input.Body)
		if err != nil {
			return nil, mapThreadMessageError(err)
		}
		return &threadMessageOutput{Body: message}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "decide-thread-approval",
		Method:      http.MethodPost,
		Path:        "/v1/threads/{id}/approvals/{approvalId}/decide",
		Summary:     "Decide a pending approval request",
		Description: "Answers one approval request the thread's agent is blocked on (the thread's approvals) with one of the decisions it offers. Returns the request resolved with the decision once the program committed it; the provider's reaction then shows through the thread's status. Refusals: 400 for a decision the request does not offer or an Integration that cannot decide, 404 for an unknown thread or request, 409 for a request already resolved — through ATC or elsewhere — or one whose different decision still awaits the program's answer, 503 while the Integration is not connected, 502 when the program rejects the decision (the request stays open) or never answers it (the same decision again reconciles; no other is taken until it does).",
	}, func(ctx context.Context, input *struct {
		ID         string `path:"id" doc:"Thread identifier."`
		ApprovalID string `path:"approvalId" doc:"Approval request identifier."`
		Body       api.ApprovalDecisionParams
	}) (*threadApprovalOutput, error) {
		approval, err := coordinator.DecideApproval(ctx, input.ID, input.ApprovalID, input.Body)
		if err != nil {
			return nil, mapThreadApprovalError(err)
		}
		return &threadApprovalOutput{Body: approval}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-thread-input-request",
		Method:      http.MethodGet,
		Path:        "/v1/threads/{id}/input-requests/{requestId}",
		Summary:     "Get a structured request",
		Description: "One structured request the thread's agent asked, pending or resolved while remembered: its questions with the choices and answer forms each allows, the answer submitted through ATC with its outcome, and how the request was resolved. Pending requests also ride the thread as inputRequests. 404 for an unknown thread or request.",
	}, func(ctx context.Context, input *struct {
		ID        string `path:"id" doc:"Thread identifier."`
		RequestID string `path:"requestId" doc:"Input request identifier."`
	}) (*threadInputRequestOutput, error) {
		request, err := service.InputRequest(input.ID, input.RequestID)
		if err != nil {
			return nil, mapThreadAnswerError(err)
		}
		return &threadInputRequestOutput{Body: request}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "answer-thread-input-request",
		Method:        http.MethodPost,
		Path:          "/v1/threads/{id}/input-requests/{requestId}/answer",
		Summary:       "Answer a pending structured request",
		Description:   "Submits one complete answer set for a request the thread's agent is blocked on (the thread's inputRequests): every question answered exactly once, with the choices it offers or a custom text where it allows one. Returns 202 with the request carrying the answer: delivery is accepted once the program committed it, uncertain when it never answered (submit the same answers again to reconcile); state stays sent until the provider's evidence resolves the request — with exactly these answers, and the request reads resolved by answer — or reports a failure. The request is never resolved on the program's acceptance alone, and never on its disappearance. Resubmitting the same answers recovers the recorded answer; different answers are refused while it awaits evidence. Refusals: 400 for an answer set that does not answer the request, a request ATC cannot answer (its unanswerable reason), or an Integration that cannot answer; 404 for an unknown thread or request; 409 for a request already resolved, a different answer still awaiting evidence, or a stop being confirmed on the thread; 503 while the Integration is not connected; 502 when the program rejects the answer (the request takes another).",
		DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, input *struct {
		ID        string `path:"id" doc:"Thread identifier."`
		RequestID string `path:"requestId" doc:"Input request identifier."`
		Body      api.InputAnswerParams
	}) (*threadInputRequestOutput, error) {
		request, err := coordinator.AnswerInput(ctx, input.ID, input.RequestID, input.Body)
		if err != nil {
			return nil, mapThreadAnswerError(err)
		}
		return &threadInputRequestOutput{Body: request}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "stop-thread",
		Method:        http.MethodPost,
		Path:          "/v1/threads/{id}/stop",
		Summary:       "Stop a thread's work",
		Description:   "Stops the work on a thread: the turn running, a submitted turn that has not started, and the question or approval it is blocked on, while preserving the conversation for a later message. Returns 202 with the stop operation: delivery is accepted once the program committed it, uncertain when it never answered (submit the same key again to reconcile); state is stopping until the provider's evidence resolves it — stopped when the covered work was cut short, finished when it had already ended or nothing was running (a thread at rest resolves at once, with nothing sent) — or failed when the program refused. While a stop is stopping the thread refuses messages, answers, and decisions, across restarts; submitting a stop meanwhile returns the same operation, and the same key returns its stop in any state, never stopping later work. Confirmation withdraws a submitted turn that never started and closes the pending requests. Refusals: 400 for an Integration that cannot stop, 404 for an unknown thread, 503 while the Integration is not connected, 502 when the program rejects the stop.",
		DefaultStatus: http.StatusAccepted,
	}, func(ctx context.Context, input *struct {
		ID   string `path:"id" doc:"Thread identifier."`
		Body api.ThreadStopParams
	}) (*threadStopOutput, error) {
		stop, err := coordinator.StopThread(ctx, input.ID, input.Body)
		if err != nil {
			return nil, mapThreadStopError(err)
		}
		return &threadStopOutput{Body: stop}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-thread-stop",
		Method:      http.MethodGet,
		Path:        "/v1/threads/{id}/stops/{stopId}",
		Summary:     "Get a stop operation",
		Description: "One stop operation on the thread, as it stands. The stop still stopping also rides the thread as stop. 404 for an unknown thread or stop.",
	}, func(ctx context.Context, input *struct {
		ID     string `path:"id" doc:"Thread identifier."`
		StopID string `path:"stopId" doc:"Stop identifier."`
	}) (*threadStopOutput, error) {
		stop, err := service.Stop(input.ID, input.StopID)
		if err != nil {
			return nil, mapThreadStopError(err)
		}
		return &threadStopOutput{Body: stop}, nil
	})

	huma.Register(humaAPI, huma.Operation{
		OperationID:   "delete-thread",
		Method:        http.MethodDelete,
		Path:          "/v1/threads/{id}",
		Summary:       "Delete a thread",
		Description:   "Removes ATC's record and its private identity mapping only; the provider-side conversation is never touched. An active thread — one a terminal has open, or one its provider still reports — is refused, naming the holder.",
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

// mapThreadMessageError maps a message's refusals (ATC-307).
func mapThreadMessageError(err error) error {
	switch {
	case errors.Is(err, threads.ErrMessageInvalid):
		return problem(http.StatusBadRequest, api.CodeValidationFailed, err.Error())
	case errors.Is(err, integrations.ErrNotFound):
		return problem(http.StatusBadRequest, api.CodeIntegrationNotFound, err.Error())
	case errors.Is(err, integrations.ErrThreadSendUnsupported):
		return problem(http.StatusBadRequest, api.CodeThreadSendUnsupported, err.Error())
	case errors.Is(err, threads.ErrTurnPending):
		return problem(http.StatusConflict, api.CodeThreadTurnPending, err.Error())
	case errors.Is(err, threads.ErrMessageWithdrawn):
		return problem(http.StatusConflict, api.CodeThreadMessageWithdrawn, err.Error())
	case errors.Is(err, integrations.ErrNotConnected):
		return problem(http.StatusServiceUnavailable, api.CodeIntegrationNotConnected, err.Error())
	case errors.Is(err, integrations.ErrMessageRejected), errors.Is(err, threads.ErrMessageRejected):
		return problem(http.StatusBadGateway, api.CodeThreadMessageRejected, err.Error())
	}
	return mapThreadError(err)
}

// mapThreadAnswerError maps an answer's refusals (ATC-308).
func mapThreadAnswerError(err error) error {
	switch {
	case errors.Is(err, integrations.ErrNotFound):
		return problem(http.StatusBadRequest, api.CodeIntegrationNotFound, err.Error())
	case errors.Is(err, threads.ErrInputNotFound):
		return problem(http.StatusNotFound, api.CodeInputRequestNotFound, err.Error())
	case errors.Is(err, threads.ErrInputResolved):
		return problem(http.StatusConflict, api.CodeInputRequestResolved, err.Error())
	case errors.Is(err, threads.ErrInputUnanswerable):
		return problem(http.StatusBadRequest, api.CodeInputRequestUnanswerable, err.Error())
	case errors.Is(err, threads.ErrAnswerInvalid):
		return problem(http.StatusBadRequest, api.CodeInputAnswerInvalid, err.Error())
	case errors.Is(err, threads.ErrAnswerPending):
		return problem(http.StatusConflict, api.CodeInputAnswerPending, err.Error())
	case errors.Is(err, integrations.ErrThreadAnswerUnsupported):
		return problem(http.StatusBadRequest, api.CodeThreadAnswerUnsupported, err.Error())
	case errors.Is(err, integrations.ErrNotConnected):
		return problem(http.StatusServiceUnavailable, api.CodeIntegrationNotConnected, err.Error())
	case errors.Is(err, integrations.ErrAnswerRejected):
		return problem(http.StatusBadGateway, api.CodeInputAnswerFailed, err.Error())
	}
	return mapThreadError(err)
}

// mapThreadStopError maps a stop's refusals (ATC-308).
func mapThreadStopError(err error) error {
	switch {
	case errors.Is(err, integrations.ErrNotFound):
		return problem(http.StatusBadRequest, api.CodeIntegrationNotFound, err.Error())
	case errors.Is(err, threads.ErrStopNotFound):
		return problem(http.StatusNotFound, api.CodeStopNotFound, err.Error())
	case errors.Is(err, integrations.ErrThreadStopUnsupported):
		return problem(http.StatusBadRequest, api.CodeThreadStopUnsupported, err.Error())
	case errors.Is(err, integrations.ErrNotConnected):
		return problem(http.StatusServiceUnavailable, api.CodeIntegrationNotConnected, err.Error())
	case errors.Is(err, integrations.ErrStopRejected):
		return problem(http.StatusBadGateway, api.CodeThreadStopFailed, err.Error())
	}
	return mapThreadError(err)
}

// mapThreadApprovalError maps a decision's refusals (ATC-307).
func mapThreadApprovalError(err error) error {
	switch {
	case errors.Is(err, integrations.ErrNotFound):
		return problem(http.StatusBadRequest, api.CodeIntegrationNotFound, err.Error())
	case errors.Is(err, threads.ErrApprovalNotFound):
		return problem(http.StatusNotFound, api.CodeApprovalNotFound, err.Error())
	case errors.Is(err, threads.ErrApprovalResolved):
		return problem(http.StatusConflict, api.CodeApprovalResolved, err.Error())
	case errors.Is(err, threads.ErrDecisionPending):
		return problem(http.StatusConflict, api.CodeApprovalDecisionPending, err.Error())
	case errors.Is(err, threads.ErrDecisionNotOffered):
		return problem(http.StatusBadRequest, api.CodeApprovalDecisionInvalid, err.Error())
	case errors.Is(err, integrations.ErrThreadDecideUnsupported):
		return problem(http.StatusBadRequest, api.CodeThreadDecideUnsupported, err.Error())
	case errors.Is(err, integrations.ErrNotConnected):
		return problem(http.StatusServiceUnavailable, api.CodeIntegrationNotConnected, err.Error())
	case errors.Is(err, integrations.ErrDecisionRejected):
		return problem(http.StatusBadGateway, api.CodeApprovalDecisionFailed, err.Error())
	case errors.Is(err, integrations.ErrDeliveryUncertain):
		return problem(http.StatusBadGateway, api.CodeApprovalDecisionUnknown, err.Error())
	}
	return mapThreadError(err)
}

func mapThreadError(err error) error {
	switch {
	case errors.Is(err, threads.ErrNotFound):
		return problem(http.StatusNotFound, api.CodeThreadNotFound, "thread not found")
	case errors.Is(err, threads.ErrActive):
		return problem(http.StatusConflict, api.CodeThreadActive, err.Error())
	case errors.Is(err, threads.ErrThreadStopping):
		return problem(http.StatusConflict, api.CodeThreadStopping, err.Error())
	case errors.Is(err, threads.ErrProjectUnknown):
		return problem(http.StatusUnprocessableEntity, api.CodeProjectNotFound, err.Error())
	case errors.Is(err, threads.ErrInvalidUpdate):
		return problem(http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	}
	return err
}
