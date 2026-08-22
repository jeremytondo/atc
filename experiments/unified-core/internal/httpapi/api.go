// Package httpapi exposes only canonical ATC resources. Provider protocol
// inputs are confined to internal ingestion routes and raw diagnostics is
// available only when the server is explicitly started in debug mode.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/elevenideas/atc/experiments/unified-core/internal/core"
	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
)

type API struct {
	core  *core.Service
	debug bool
}

func New(service *core.Service, debug bool) http.Handler {
	api := &API{core: service, debug: debug}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/threads", api.createThread)
	mux.HandleFunc("GET /v1/threads", api.listThreads)
	mux.HandleFunc("GET /v1/threads/{thread}", api.getThread)
	mux.HandleFunc("POST /v1/threads/{thread}/prompts", api.prompt)
	mux.HandleFunc("POST /v1/threads/{thread}/turns/{turn}/interrupt", api.interrupt)
	mux.HandleFunc("GET /v1/threads/{thread}/requests", api.requests)
	mux.HandleFunc("POST /v1/threads/{thread}/requests/{request}/answer", api.answer)
	mux.HandleFunc("POST /v1/threads/{thread}/terminal", api.openTerminal)
	mux.HandleFunc("GET /v1/threads/{thread}/events", api.threadEvents)
	mux.HandleFunc("GET /v1/terminals", api.listTerminals)
	mux.HandleFunc("GET /v1/terminals/{terminal}", api.getTerminal)
	mux.HandleFunc("DELETE /v1/terminals/{terminal}", api.closeTerminal)
	mux.HandleFunc("GET /v1/events", api.events)
	mux.HandleFunc("POST /internal/hooks/{provider}/terminal/{terminal}", api.providerHook)
	mux.HandleFunc("POST /internal/status/{provider}/thread/{thread}", api.providerStatus)
	mux.HandleFunc("POST /internal/terminals/reconcile", api.reconcileTerminals)
	mux.HandleFunc("POST /internal/terminals/cleanup", api.cleanupTerminals)
	if debug {
		mux.HandleFunc("GET /debug/timeline", api.timeline)
	}
	return api.contentType(mux)
}

func (a *API) contentType(next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(response, request)
	})
}

func (a *API) createThread(response http.ResponseWriter, request *http.Request) {
	if !isLoopback(request.RemoteAddr) {
		write(response, http.StatusForbidden, nil, domain.NewError("local_only", "thread creation is local-only"))
		return
	}
	var body core.CreateThread
	if !decode(response, request, &body) {
		return
	}
	thread, err := a.core.CreateThread(body)
	write(response, http.StatusCreated, thread, err)
}

func (a *API) reconcileTerminals(response http.ResponseWriter, request *http.Request) {
	terminals, err := a.core.ReconcileTerminals(request.Context())
	write(response, http.StatusOK, map[string]any{"terminals": terminals}, err)
}

func (a *API) cleanupTerminals(response http.ResponseWriter, request *http.Request) {
	result, err := a.core.CleanupTerminals(request.Context())
	write(response, http.StatusOK, result, err)
}

func (a *API) listThreads(response http.ResponseWriter, _ *http.Request) {
	write(response, http.StatusOK, map[string]any{"threads": a.core.Threads()}, nil)
}

func (a *API) getThread(response http.ResponseWriter, request *http.Request) {
	thread, err := a.core.Thread(request.PathValue("thread"))
	write(response, http.StatusOK, thread, err)
}

func (a *API) prompt(response http.ResponseWriter, request *http.Request) {
	var body core.Prompt
	if !decode(response, request, &body) {
		return
	}
	turn, err := a.core.Prompt(request.Context(), request.PathValue("thread"), body.Text)
	write(response, http.StatusAccepted, turn, err)
}

func (a *API) interrupt(response http.ResponseWriter, request *http.Request) {
	err := a.core.Interrupt(request.Context(), request.PathValue("thread"), request.PathValue("turn"))
	write(response, http.StatusAccepted, map[string]string{"scope": "foreground_turn", "backgroundWork": "not_verified_stopped"}, err)
}

func (a *API) requests(response http.ResponseWriter, request *http.Request) {
	requests, err := a.core.PendingRequests(request.PathValue("thread"))
	write(response, http.StatusOK, map[string]any{"requests": requests}, err)
}

func (a *API) answer(response http.ResponseWriter, request *http.Request) {
	var body core.Answer
	if !decode(response, request, &body) {
		return
	}
	err := a.core.AnswerRequest(request.Context(), request.PathValue("thread"), request.PathValue("request"), body)
	write(response, http.StatusNoContent, nil, err)
}

func (a *API) openTerminal(response http.ResponseWriter, request *http.Request) {
	var body core.OpenTerminal
	if request.ContentLength != 0 && !decode(response, request, &body) {
		return
	}
	terminal, err := a.core.OpenTerminal(request.Context(), request.PathValue("thread"), body)
	write(response, http.StatusCreated, terminal, err)
}

func (a *API) listTerminals(response http.ResponseWriter, request *http.Request) {
	terminals, err := a.core.ReconcileTerminals(request.Context())
	write(response, http.StatusOK, map[string]any{"terminals": terminals}, err)
}

func (a *API) getTerminal(response http.ResponseWriter, request *http.Request) {
	if _, err := a.core.ReconcileTerminals(request.Context()); err != nil {
		write(response, http.StatusOK, nil, err)
		return
	}
	terminal, err := a.core.Terminal(request.PathValue("terminal"))
	write(response, http.StatusOK, terminal, err)
}

func (a *API) closeTerminal(response http.ResponseWriter, request *http.Request) {
	err := a.core.TerminateTerminal(request.Context(), request.PathValue("terminal"))
	write(response, http.StatusNoContent, nil, err)
}

func (a *API) events(response http.ResponseWriter, request *http.Request) {
	a.streamEvents(response, request, "")
}

func (a *API) threadEvents(response http.ResponseWriter, request *http.Request) {
	a.streamEvents(response, request, request.PathValue("thread"))
}

func (a *API) streamEvents(response http.ResponseWriter, request *http.Request, threadID string) {
	after := parseSequence(request.URL.Query().Get("after"))
	if !strings.Contains(request.Header.Get("Accept"), "text/event-stream") {
		write(response, http.StatusOK, map[string]any{"events": a.core.EventsAfter(after, threadID)}, nil)
		return
	}
	flusher, ok := response.(http.Flusher)
	if !ok {
		write(response, http.StatusInternalServerError, nil, errors.New("streaming unsupported"))
		return
	}
	response.Header().Set("Content-Type", "text/event-stream")
	response.Header().Set("Cache-Control", "no-cache")
	response.WriteHeader(http.StatusOK)
	for {
		events, err := a.core.WaitEvents(request.Context(), after, threadID)
		if err != nil {
			return
		}
		for _, event := range events {
			encoded, _ := json.Marshal(event)
			fmt.Fprintf(response, "id: %d\nevent: %s\ndata: %s\n\n", event.Sequence, event.Type, encoded)
			after = event.Sequence
		}
		flusher.Flush()
	}
}

func (a *API) providerHook(response http.ResponseWriter, request *http.Request) {
	terminal, err := a.core.Terminal(request.PathValue("terminal"))
	if err != nil {
		write(response, http.StatusOK, nil, err)
		return
	}
	a.applyProviderEvidence(response, request, terminal.ThreadID)
}

func (a *API) providerStatus(response http.ResponseWriter, request *http.Request) {
	a.applyProviderEvidence(response, request, request.PathValue("thread"))
}

func (a *API) applyProviderEvidence(response http.ResponseWriter, request *http.Request, threadID string) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, 4<<20))
	if err != nil {
		write(response, http.StatusBadRequest, nil, err)
		return
	}
	provider := domain.Agent(request.PathValue("provider"))
	err = a.core.ApplyStatus(threadID, provider, raw)
	write(response, http.StatusNoContent, nil, err)
}

func (a *API) timeline(response http.ResponseWriter, request *http.Request) {
	write(response, http.StatusOK, map[string]any{"diagnostics": a.core.DiagnosticsAfter(parseSequence(request.URL.Query().Get("after")))}, nil)
}

func decode(response http.ResponseWriter, request *http.Request, target any) bool {
	decoder := json.NewDecoder(io.LimitReader(request.Body, 4<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		write(response, http.StatusBadRequest, nil, domain.NewError("invalid_request", err.Error()))
		return false
	}
	return true
}

func write(response http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		var domainError *domain.Error
		if !errors.As(err, &domainError) {
			domainError = domain.NewError("internal_error", err.Error())
		}
		status = statusFor(domainError.Code)
		value = map[string]any{"error": domainError}
	}
	if status == http.StatusNoContent && err == nil {
		response.WriteHeader(status)
		return
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func statusFor(code string) int {
	switch {
	case strings.HasSuffix(code, "_not_found"):
		return http.StatusNotFound
	case code == "local_only":
		return http.StatusForbidden
	case code == "wrong_thread_kind", code == "turn_in_progress", code == "turn_mismatch", code == "writer_conflict":
		return http.StatusConflict
	case code == "writer_unavailable", code == "terminal_unavailable", code == "terminal_inventory_unavailable", code == "status_unavailable":
		return http.StatusServiceUnavailable
	case code == "internal_error":
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}

func parseSequence(value string) uint64 {
	sequence, _ := strconv.ParseUint(value, 10, 64)
	return sequence
}

func isLoopback(remoteAddress string) bool {
	host := remoteAddress
	if index := strings.LastIndex(remoteAddress, ":"); index >= 0 {
		host = strings.Trim(remoteAddress[:index], "[]")
	}
	return host == "" || host == "127.0.0.1" || host == "::1" || host == "localhost"
}
