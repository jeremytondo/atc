package api

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/diagnostics"
	"github.com/jeremytondo/atc/internal/store"
)

func TestActionCRUDAndDefaults(t *testing.T) {
	env := newHandlerEnv(t, &fakeMux{})

	rec := do(t, env.handler, http.MethodPost, "/actions", `{"name":"Neovim","command":"nvim"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	var created Action
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.ID == "" || created.Name != "Neovim" || !created.Enabled || created.Command != "nvim" || created.Args == nil || created.IsAgent {
		t.Fatalf("created = %+v", created)
	}

	rec = do(t, env.handler, http.MethodPost, "/actions", `{"name":"Neovim","command":"other","enabled":false,"isAgent":true}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("duplicate-name create status = %d (%s)", rec.Code, rec.Body)
	}

	rec = do(t, env.handler, http.MethodGet, "/actions/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d (%s)", rec.Code, rec.Body)
	}
	var got Action
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode get: %v", err)
	}
	if !reflect.DeepEqual(got, created) {
		t.Fatalf("get = %+v, want %+v", got, created)
	}

	rec = do(t, env.handler, http.MethodDelete, "/actions/"+created.ID, "")
	if rec.Code != http.StatusOK || rec.Body.String() != "{}\n" {
		t.Fatalf("delete = %d %q", rec.Code, rec.Body.String())
	}
	rec = do(t, env.handler, http.MethodGet, "/actions/"+created.ID, "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("get deleted status = %d, want 404", rec.Code)
	}
	assertErrorCode(t, rec, "action_not_found")
}

func TestActionPatchOmissionNullAndArgsNull(t *testing.T) {
	env := newHandlerEnv(t, &fakeMux{})
	created, err := env.store.CreateAction(context.Background(), store.Action{
		Name: "Tool", Description: "clear me", Enabled: true, Command: "tool",
		Args: []string{"one"}, IsAgent: false,
	})
	if err != nil {
		t.Fatalf("CreateAction: %v", err)
	}

	rec := do(t, env.handler, http.MethodPatch, "/actions/"+created.ID, `{"description":null,"args":null,"isAgent":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch status = %d (%s)", rec.Code, rec.Body)
	}
	var updated Action
	if err := json.Unmarshal(rec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode patch: %v", err)
	}
	if updated.Description != "" || updated.Name != "Tool" || updated.Command != "tool" || !updated.Enabled || !updated.IsAgent || len(updated.Args) != 0 {
		t.Fatalf("updated = %+v", updated)
	}
	if strings.Contains(rec.Body.String(), `"description"`) {
		t.Fatalf("cleared description was not omitted: %s", rec.Body)
	}

	for _, body := range []string{`{"name":null}`, `{"command":null}`, `{"enabled":null}`, `{"isAgent":null}`} {
		rec = do(t, env.handler, http.MethodPatch, "/actions/"+created.ID, body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("patch %s status = %d, want 400 (%s)", body, rec.Code, rec.Body)
		}
		assertErrorCode(t, rec, "invalid_action")
	}
}

func TestActionValidationAndStrictDecode(t *testing.T) {
	env := newHandlerEnv(t, &fakeMux{})
	for _, body := range []string{
		`{"name":"","command":"tool"}`,
		`{"name":"Tool","command":""}`,
		`{"name":"Tool","command":"tool","unknown":true}`,
	} {
		rec := do(t, env.handler, http.MethodPost, "/actions", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("create %s status = %d, want 400 (%s)", body, rec.Code, rec.Body)
		}
	}
}

func TestDeleteActionLeavesLiveSessionReadable(t *testing.T) {
	env := newHandlerEnv(t, &fakeMux{})
	start := do(t, env.handler, http.MethodPost, "/sessions/start",
		`{"workspaceId":"`+testWorkspaceID+`","actionId":"`+env.actionID+`"}`)
	if start.Code != http.StatusOK {
		t.Fatalf("start status = %d (%s)", start.Code, start.Body)
	}
	var session SessionResponse
	if err := json.Unmarshal(start.Body.Bytes(), &session); err != nil {
		t.Fatalf("decode session: %v", err)
	}

	deleted := do(t, env.handler, http.MethodDelete, "/actions/"+env.actionID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status = %d (%s)", deleted.Code, deleted.Body)
	}
	read := do(t, env.handler, http.MethodGet, "/sessions/"+session.ID, "")
	if read.Code != http.StatusOK {
		t.Fatalf("read status = %d (%s)", read.Code, read.Body)
	}
	var got SessionResponse
	if err := json.Unmarshal(read.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode read: %v", err)
	}
	if got.ActionID != env.actionID || got.ActionName != "Agent" || !got.IsAgent {
		t.Fatalf("session snapshot = %+v", got)
	}
}

func TestActionsUnavailable(t *testing.T) {
	rec := do(t, Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil, nil), http.MethodGet, "/actions", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
	assertErrorCode(t, rec, "internal_error")
}
