package api

import (
	"errors"
	"fmt"
	"net/http"

	actionstore "github.com/jeremytondo/atc/internal/action"
	"github.com/jeremytondo/atc/internal/session"
)

type actionsResponse struct {
	Actions []actionResponse `json:"actions"`
}

type actionResponse struct {
	Name string `json:"name"`
	// Type is "action" or "agent"; immutable after creation.
	Type string `json:"type"`
	// Origin is the client affordance switch: custom -> Edit + Delete,
	// modified -> Edit + Revert to default through DELETE /actions/{name},
	// builtin -> Edit only.
	Origin      string                   `json:"origin"`
	Enabled     bool                     `json:"enabled"`
	Label       string                   `json:"label,omitempty"`
	Description string                   `json:"description,omitempty"`
	Prompt      *promptResponse          `json:"prompt,omitempty"`
	Params      map[string]paramResponse `json:"params"`
}

type actionDetailResponse struct {
	Name string `json:"name"`
	// Type is "action" or "agent"; immutable after creation.
	Type string `json:"type"`
	// Origin uses the same affordance mapping as actionResponse.
	Origin      string                   `json:"origin"`
	Enabled     bool                     `json:"enabled"`
	Label       string                   `json:"label,omitempty"`
	Description string                   `json:"description,omitempty"`
	Command     string                   `json:"command"`
	Args        []string                 `json:"args"`
	Prompt      *promptResponse          `json:"prompt,omitempty"`
	Params      map[string]paramResponse `json:"params"`
}

type actionWriteRequest struct {
	Name        string                       `json:"name,omitempty"`
	Type        session.ActionType           `json:"type"`
	Kind        string                       `json:"kind"`
	Label       string                       `json:"label"`
	Description string                       `json:"description"`
	Command     string                       `json:"command"`
	Bin         string                       `json:"bin"`
	Args        []string                     `json:"args"`
	Prompt      *session.PromptSpec          `json:"prompt"`
	Params      map[string]session.ParamSpec `json:"params"`
	// Enabled is a pointer so an omitted flag on create defaults to enabled.
	Enabled *bool `json:"enabled"`
}

type actionEnabledRequest struct {
	// Enabled is a pointer so an omitted flag is rejected instead of silently
	// decoding to false and disabling the action.
	Enabled *bool `json:"enabled"`
}

type promptResponse struct {
	Flag string `json:"flag,omitempty"`
}

type paramResponse struct {
	Type        string   `json:"type"`
	Values      []string `json:"values,omitempty"`
	Default     any      `json:"default,omitempty"`
	Flag        string   `json:"flag,omitempty"`
	Label       string   `json:"label,omitempty"`
	Description string   `json:"description,omitempty"`
}

func (routes apiRoutes) listActions(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	actions, err := routes.actions.Discover(r.Context())
	if err != nil {
		writeActionError(w, err)
		return
	}
	resp := actionsResponse{Actions: make([]actionResponse, 0, len(actions))}
	for _, action := range actions {
		resp.Actions = append(resp.Actions, actionDiscoveryResponse(action))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (routes apiRoutes) getAction(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	routes.writeActionDetail(w, r, http.StatusOK, r.PathValue("name"))
}

func (routes apiRoutes) createAction(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	var req actionWriteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	// name is optional: when omitted it is derived from the human label, so API,
	// CLI, and web clients all get the same id without reimplementing the rule.
	name := req.Name
	if name == "" {
		name = actionstore.Slugify(req.Label)
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "name or label is required")
		return
	}
	action, err := req.action()
	if err != nil {
		writeActionError(w, err)
		return
	}
	if err := routes.actions.Create(r.Context(), name, action); err != nil {
		writeActionError(w, err)
		return
	}
	routes.writeActionDetail(w, r, http.StatusCreated, name)
}

func (routes apiRoutes) updateAction(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	name := r.PathValue("name")
	var req actionWriteRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Name != "" && req.Name != name {
		writeError(w, http.StatusBadRequest, "invalid_request", "body name must match route name")
		return
	}
	action, err := req.action()
	if err != nil {
		writeActionError(w, err)
		return
	}
	if err := routes.actions.Update(r.Context(), name, action); err != nil {
		writeActionError(w, err)
		return
	}
	routes.writeActionDetail(w, r, http.StatusOK, name)
}

func (routes apiRoutes) setActionEnabled(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	name := r.PathValue("name")
	var req actionEnabledRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Enabled == nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "enabled is required")
		return
	}
	if err := routes.actions.SetEnabled(r.Context(), name, *req.Enabled); err != nil {
		writeActionError(w, err)
		return
	}
	routes.writeActionDetail(w, r, http.StatusOK, name)
}

func (routes apiRoutes) deleteAction(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	if err := routes.actions.Delete(r.Context(), r.PathValue("name")); err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func actionDiscoveryResponse(action actionstore.Discovery) actionResponse {
	discovery := action.ActionDiscovery
	params := paramResponses(discovery.Params)

	var prompt *promptResponse
	if discovery.Prompt != nil {
		prompt = &promptResponse{Flag: discovery.Prompt.Flag}
	}

	return actionResponse{
		Name:        discovery.Name,
		Type:        string(discovery.Type),
		Origin:      string(action.Origin),
		Enabled:     !discovery.Disabled,
		Label:       discovery.Label,
		Description: discovery.Description,
		Prompt:      prompt,
		Params:      params,
	}
}

// writeActionDetail loads the effective action for name and writes it as the
// detail response. The store returns a cloned, normalized action, so callers do
// not copy its fields defensively.
func (routes apiRoutes) writeActionDetail(w http.ResponseWriter, r *http.Request, status int, name string) {
	got, origin, err := routes.actions.Get(r.Context(), name)
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, status, actionDetail(name, got, origin))
}

func actionDetail(name string, got session.Action, origin actionstore.Origin) actionDetailResponse {
	var prompt *promptResponse
	if got.Prompt != nil {
		prompt = &promptResponse{Flag: got.Prompt.Flag}
	}
	return actionDetailResponse{
		Name:        name,
		Type:        string(got.Type),
		Origin:      string(origin),
		Enabled:     !got.Disabled,
		Label:       got.Label,
		Description: got.Description,
		Command:     got.Command,
		Args:        got.Args,
		Prompt:      prompt,
		Params:      paramResponses(got.Params),
	}
}

func paramResponses(specs map[string]session.ParamSpec) map[string]paramResponse {
	params := make(map[string]paramResponse, len(specs))
	for name, spec := range specs {
		params[name] = paramResponse{
			Type:        spec.Type,
			Values:      append([]string(nil), spec.Values...),
			Default:     spec.Default,
			Flag:        spec.Flag,
			Label:       spec.Label,
			Description: spec.Description,
		}
	}
	return params
}

func (req actionWriteRequest) action() (session.Action, error) {
	actionType, err := session.ResolveActionType(req.Type, req.Kind)
	if err != nil {
		return session.Action{}, fmt.Errorf("%w: %v", actionstore.ErrInvalidAction, err)
	}
	if req.Command != "" && req.Bin != "" && req.Command != req.Bin {
		return session.Action{}, fmt.Errorf("%w: command %q conflicts with legacy bin %q", actionstore.ErrInvalidAction, req.Command, req.Bin)
	}
	command := req.Command
	if command == "" {
		command = req.Bin
	}
	return session.Action{
		Type:        actionType,
		Label:       req.Label,
		Description: req.Description,
		Command:     command,
		Args:        append([]string(nil), req.Args...),
		Prompt:      req.Prompt,
		Params:      req.Params,
		Disabled:    req.Enabled != nil && !*req.Enabled,
	}, nil
}

func (routes apiRoutes) requireActions(w http.ResponseWriter) bool {
	if routes.actions != nil {
		return true
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "action store is unavailable")
	return false
}

func writeActionError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, actionstore.ErrDuplicate),
		errors.Is(err, actionstore.ErrBuiltinRemoval):
		writeError(w, http.StatusConflict, "action_conflict", err.Error())
	case errors.Is(err, actionstore.ErrActionInUse):
		writeError(w, http.StatusConflict, "action_in_use", err.Error())
	case errors.Is(err, actionstore.ErrNotFound):
		writeError(w, http.StatusNotFound, "action_not_found", err.Error())
	case errors.Is(err, actionstore.ErrInvalidAction):
		writeError(w, http.StatusBadRequest, "invalid_action", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
