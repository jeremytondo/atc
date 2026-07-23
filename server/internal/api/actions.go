package api

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/jeremytondo/atc/internal/store"
)

type actionsResponse struct {
	Actions []Action `json:"actions"`
}

// Action is the complete wire shape used by Action list and detail endpoints.
type Action struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Enabled     bool     `json:"enabled"`
	Command     string   `json:"command"`
	Args        []string `json:"args"`
	IsAgent     bool     `json:"isAgent"`
}

type actionCreateRequest struct {
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Command     string   `json:"command"`
	Args        []string `json:"args,omitempty"`
	Enabled     *bool    `json:"enabled,omitempty"`
	IsAgent     bool     `json:"isAgent,omitempty"`
}

// RawMessage preserves omitted-vs-null PATCH semantics. An omitted field is
// nil; an explicit JSON null contains the bytes "null".
type actionPatchRequest struct {
	Name        json.RawMessage `json:"name,omitempty"`
	Description json.RawMessage `json:"description,omitempty"`
	Command     json.RawMessage `json:"command,omitempty"`
	Args        json.RawMessage `json:"args,omitempty"`
	Enabled     json.RawMessage `json:"enabled,omitempty"`
	IsAgent     json.RawMessage `json:"isAgent,omitempty"`
}

func (routes apiRoutes) listActions(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	actions, err := routes.actions.ListActions(r.Context())
	if err != nil {
		writeActionError(w, err)
		return
	}
	resp := actionsResponse{Actions: make([]Action, 0, len(actions))}
	for _, action := range actions {
		resp.Actions = append(resp.Actions, actionResponse(action))
	}
	writeJSON(w, http.StatusOK, resp)
}

func (routes apiRoutes) getAction(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	action, err := routes.actions.GetAction(r.Context(), r.PathValue("id"))
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, actionResponse(action))
}

func (routes apiRoutes) createAction(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	var req actionCreateRequest
	if !decodeJSONBody(w, r, &req, true, "request body must be a valid Action") {
		return
	}
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	created, err := routes.actions.CreateAction(r.Context(), store.Action{
		Name:        req.Name,
		Description: req.Description,
		Enabled:     enabled,
		Command:     req.Command,
		Args:        req.Args,
		IsAgent:     req.IsAgent,
	})
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, actionResponse(created))
}

func (routes apiRoutes) updateAction(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	var req actionPatchRequest
	if !decodeJSONBody(w, r, &req, true, "request body must be a valid Action patch") {
		return
	}
	input, err := req.updateInput()
	if err != nil {
		writeActionError(w, err)
		return
	}
	updated, err := routes.actions.UpdateAction(r.Context(), r.PathValue("id"), input)
	if err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, actionResponse(updated))
}

func (routes apiRoutes) deleteAction(w http.ResponseWriter, r *http.Request) {
	if !routes.requireActions(w) {
		return
	}
	if err := routes.actions.DeleteAction(r.Context(), r.PathValue("id")); err != nil {
		writeActionError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, struct{}{})
}

func (req actionPatchRequest) updateInput() (store.UpdateActionInput, error) {
	var input store.UpdateActionInput
	var err error
	if input.Name, err = decodePatchValue[string]("name", req.Name, false); err != nil {
		return store.UpdateActionInput{}, err
	}
	if req.Description != nil {
		input.DescriptionSet = true
		if !isJSONNull(req.Description) {
			if input.Description, err = decodePatchValue[string]("description", req.Description, false); err != nil {
				return store.UpdateActionInput{}, err
			}
		}
	}
	if input.Command, err = decodePatchValue[string]("command", req.Command, false); err != nil {
		return store.UpdateActionInput{}, err
	}
	if req.Args != nil {
		if isJSONNull(req.Args) {
			empty := []string{}
			input.Args = &empty
		} else if input.Args, err = decodePatchValue[[]string]("args", req.Args, false); err != nil {
			return store.UpdateActionInput{}, err
		}
	}
	if input.Enabled, err = decodePatchValue[bool]("enabled", req.Enabled, false); err != nil {
		return store.UpdateActionInput{}, err
	}
	if input.IsAgent, err = decodePatchValue[bool]("isAgent", req.IsAgent, false); err != nil {
		return store.UpdateActionInput{}, err
	}
	return input, nil
}

func decodePatchValue[T any](name string, raw json.RawMessage, allowNull bool) (*T, error) {
	if raw == nil {
		return nil, nil
	}
	if isJSONNull(raw) && !allowNull {
		return nil, fmt.Errorf("%w: %s cannot be null", store.ErrInvalidAction, name)
	}
	var value T
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, fmt.Errorf("%w: %s has the wrong type", store.ErrInvalidAction, name)
	}
	return &value, nil
}

func isJSONNull(raw json.RawMessage) bool {
	return bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

func actionResponse(action store.Action) Action {
	args := make([]string, len(action.Args))
	copy(args, action.Args)
	return Action{
		ID:          action.ID,
		Name:        action.Name,
		Description: action.Description,
		Enabled:     action.Enabled,
		Command:     action.Command,
		Args:        args,
		IsAgent:     action.IsAgent,
	}
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
	case errors.Is(err, store.ErrActionNotFound):
		writeError(w, http.StatusNotFound, "action_not_found", err.Error())
	case errors.Is(err, store.ErrInvalidAction):
		writeError(w, http.StatusBadRequest, "invalid_action", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
