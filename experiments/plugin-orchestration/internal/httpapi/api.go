// Package httpapi exposes the canonical prototype surface. The transport knows
// the finite ATC capability vocabulary but no plugin-specific protocol shape.
package httpapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/core"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/model"
	"github.com/elevenideas/atc/experiments/plugin-orchestration/internal/plugin"
)

type API struct {
	core *core.Service
}

func New(service *core.Service) http.Handler {
	api := &API{core: service}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/plugins", api.plugins)
	mux.HandleFunc("POST /v1/plugins/{plugin}/refresh", api.refresh)
	mux.HandleFunc("GET /v1/resources", api.resources)
	mux.HandleFunc("POST /v1/resources", api.create)
	mux.HandleFunc("GET /v1/resources/{resource}", api.resource)
	mux.HandleFunc("POST /v1/resources/{resource}/actions/{action}", api.action)
	mux.HandleFunc("GET /v1/events", api.events)
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("X-Content-Type-Options", "nosniff")
		mux.ServeHTTP(response, request)
	})
}

func (a *API) plugins(response http.ResponseWriter, _ *http.Request) {
	write(response, http.StatusOK, map[string]any{"plugins": a.core.Plugins()}, nil)
}

func (a *API) refresh(response http.ResponseWriter, request *http.Request) {
	resources, err := a.core.Refresh(request.Context(), request.PathValue("plugin"))
	write(response, http.StatusOK, map[string]any{"resources": resources}, err)
}

func (a *API) resources(response http.ResponseWriter, request *http.Request) {
	write(response, http.StatusOK, map[string]any{
		"resources": a.core.Resources(request.URL.Query().Get("plugin"), request.URL.Query().Get("kind")),
	}, nil)
}

func (a *API) resource(response http.ResponseWriter, request *http.Request) {
	resource, err := a.core.Resource(request.PathValue("resource"))
	write(response, http.StatusOK, resource, err)
}

func (a *API) create(response http.ResponseWriter, request *http.Request) {
	var body struct {
		PluginID string `json:"pluginId"`
		Kind     string `json:"kind"`
		Title    string `json:"title"`
	}
	if !decode(response, request, &body, true) {
		return
	}
	resource, err := a.core.Create(request.Context(), body.PluginID, plugin.CreateRequest{Kind: body.Kind, Title: body.Title})
	write(response, http.StatusCreated, resource, err)
}

func (a *API) action(response http.ResponseWriter, request *http.Request) {
	action := model.Capability(request.PathValue("action"))
	var body plugin.ActionRequest
	required := action == model.CapabilityRespond || action == model.CapabilityControl
	if !decode(response, request, &body, required) {
		return
	}
	result, err := a.core.Act(request.Context(), request.PathValue("resource"), action, body)
	write(response, http.StatusOK, result, err)
}

func (a *API) events(response http.ResponseWriter, request *http.Request) {
	after, _ := strconv.ParseUint(request.URL.Query().Get("after"), 10, 64)
	write(response, http.StatusOK, map[string]any{"events": a.core.EventsAfter(after)}, nil)
}

func decode(response http.ResponseWriter, request *http.Request, target any, required bool) bool {
	if request.Body == nil || request.ContentLength == 0 {
		if required {
			write(response, http.StatusBadRequest, nil, model.NewError("invalid_request", "request body is required"))
			return false
		}
		return true
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		write(response, http.StatusBadRequest, nil, model.NewError("invalid_request", err.Error()))
		return false
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		write(response, http.StatusBadRequest, nil, model.NewError("invalid_request", "request body must contain one JSON value"))
		return false
	}
	return true
}

func write(response http.ResponseWriter, status int, value any, err error) {
	if err != nil {
		var domainError *model.Error
		if !errors.As(err, &domainError) {
			domainError = model.NewError("internal_error", err.Error())
		}
		status = statusFor(domainError.Code)
		value = map[string]any{"error": domainError}
	}
	response.Header().Set("Content-Type", "application/json")
	response.WriteHeader(status)
	_ = json.NewEncoder(response).Encode(value)
}

func statusFor(code string) int {
	switch code {
	case "plugin_not_found", "resource_not_found":
		return http.StatusNotFound
	case "action_unavailable", "capability_unavailable", "unsupported_action", "attach_unavailable", "open_unavailable":
		return http.StatusConflict
	case "internal_error":
		return http.StatusInternalServerError
	default:
		return http.StatusBadRequest
	}
}
