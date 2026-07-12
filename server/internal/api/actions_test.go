package api

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	actionstore "github.com/jeremytondo/atc/internal/action"
	"github.com/jeremytondo/atc/internal/diagnostics"
	"github.com/jeremytondo/atc/internal/session"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/workspace"
)

func TestListActionsReturnsDiscoveryMetadata(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{
		"tool": {
			Label:       "Tool",
			Description: "Configured test tool",
			Command:     "tool",
			Args:        []string{"--operator-only"},
			Prompt:      &session.PromptSpec{Flag: "--prompt"},
			Params: map[string]session.ParamSpec{
				"model": {
					Type:        "enum",
					Values:      []string{"small", "large"},
					Default:     "large",
					Flag:        "--model",
					Label:       "Model",
					Description: "Model family",
				},
			},
		},
		"offline": {
			Command: "offline",
			Params: map[string]session.ParamSpec{
				"dry-run": {
					Type:        "bool",
					Default:     false,
					Flag:        "--dry-run",
					Label:       "Dry run",
					Description: "Avoid writes",
				},
			},
		},
	})

	rec := do(t, h, http.MethodGet, "/actions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, `"source"`) || strings.Contains(body, `"bin"`) || strings.Contains(body, `"kind"`) || strings.Contains(body, `"availability"`) || strings.Contains(body, `"args"`) || strings.Contains(body, "--operator-only") {
		t.Fatalf("response exposed launch internals: %s", body)
	}

	var got struct {
		Actions []struct {
			Name        string `json:"name"`
			Origin      string `json:"origin"`
			Label       string `json:"label"`
			Description string `json:"description"`
			Prompt      *struct {
				Flag string `json:"flag"`
			} `json:"prompt"`
			Params map[string]struct {
				Type        string   `json:"type"`
				Values      []string `json:"values"`
				Default     any      `json:"default"`
				Flag        string   `json:"flag"`
				Label       string   `json:"label"`
				Description string   `json:"description"`
			} `json:"params"`
		} `json:"actions"`
	}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.Actions) != 2 {
		t.Fatalf("actions = %+v, want 2", got.Actions)
	}
	if got.Actions[0].Name != "offline" || got.Actions[1].Name != "tool" {
		t.Fatalf("actions = %+v, want stable name order", got.Actions)
	}

	offline := got.Actions[0]
	if offline.Origin != "builtin" {
		t.Fatalf("offline origin = %q, want builtin", offline.Origin)
	}
	if offline.Params["dry-run"].Default != false {
		t.Fatalf("dry-run default = %#v, want false", offline.Params["dry-run"].Default)
	}

	tool := got.Actions[1]
	if tool.Origin != "builtin" {
		t.Fatalf("tool origin = %q, want builtin", tool.Origin)
	}
	if tool.Label != "Tool" || tool.Description != "Configured test tool" {
		t.Fatalf("tool metadata = %+v", tool)
	}
	if tool.Prompt == nil || tool.Prompt.Flag != "--prompt" {
		t.Fatalf("tool prompt = %+v", tool.Prompt)
	}
	model := tool.Params["model"]
	if model.Type != "enum" || model.Flag != "--model" || model.Label != "Model" || model.Description != "Model family" {
		t.Fatalf("model param = %+v", model)
	}
	if model.Default != "large" || len(model.Values) != 2 || model.Values[0] != "small" || model.Values[1] != "large" {
		t.Fatalf("model values/default = %+v", model)
	}
}

func TestActionCRUD(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{
		"tool": {
			Command: "tool",
			Label:   "Tool",
		},
	})

	rec := do(t, h, http.MethodPost, "/actions", `{"name":"custom","command":"echo","args":["hello"],"params":{"dry-run":{"type":"bool","flag":"--dry-run"}}}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	var created actionDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	if created.Name != "custom" || created.Origin != "custom" || created.Command != "echo" || len(created.Args) != 1 || created.Args[0] != "hello" {
		t.Fatalf("created = %+v", created)
	}

	rec = do(t, h, http.MethodGet, "/actions/custom", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var detail actionDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Origin != "custom" || detail.Params["dry-run"].Flag != "--dry-run" {
		t.Fatalf("detail = %+v", detail)
	}

	rec = do(t, h, http.MethodPost, "/actions", `{"name":"legacy","bin":"printf"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("legacy create status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode legacy create: %v", err)
	}
	if detail.Name != "legacy" || detail.Command != "printf" {
		t.Fatalf("legacy created = %+v", detail)
	}

	rec = do(t, h, http.MethodPut, "/actions/tool", `{"name":"tool","label":"Tool Pro","command":"/opt/tool","prompt":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode update: %v", err)
	}
	if detail.Origin != "modified" || detail.Label != "Tool Pro" || detail.Command != "/opt/tool" {
		t.Fatalf("updated detail = %+v", detail)
	}

	rec = do(t, h, http.MethodDelete, "/actions/tool", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete override status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/actions/tool", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get reverted status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode reverted: %v", err)
	}
	if detail.Origin != "builtin" || detail.Command != "tool" {
		t.Fatalf("reverted detail = %+v", detail)
	}
	if detail.Args == nil {
		t.Fatalf("reverted detail args = nil, want empty array shape")
	}
}

func TestBuiltInActionOriginTransitions(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{
		"codex": {
			Label:       "Codex",
			Description: "OpenAI Codex CLI",
			Command:     "codex",
			Prompt:      &session.PromptSpec{},
		},
	})

	rec := do(t, h, http.MethodGet, "/actions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var list struct {
		Actions []struct {
			Name   string `json:"name"`
			Origin string `json:"origin"`
		} `json:"actions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Actions) != 1 || list.Actions[0].Name != "codex" || list.Actions[0].Origin != "builtin" {
		t.Fatalf("list = %+v, want builtin codex", list.Actions)
	}

	rec = do(t, h, http.MethodGet, "/actions/codex", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("detail status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var detail actionDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if detail.Origin != "builtin" {
		t.Fatalf("detail origin = %q, want builtin", detail.Origin)
	}

	rec = do(t, h, http.MethodPut, "/actions/codex", `{"name":"codex","label":"Codex Pro","description":"OpenAI Codex CLI","command":"codex","prompt":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("modified update status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode modified detail: %v", err)
	}
	if detail.Origin != "modified" || detail.Label != "Codex Pro" {
		t.Fatalf("modified detail = %+v, want modified", detail)
	}

	rec = do(t, h, http.MethodPut, "/actions/codex", `{"name":"codex","label":"Codex","description":"OpenAI Codex CLI","command":"codex","prompt":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("default update status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	detail = actionDetailResponse{}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode default detail: %v", err)
	}
	if detail.Origin != "builtin" || detail.Label != "Codex" {
		t.Fatalf("default detail = %+v, want builtin", detail)
	}

	rec = do(t, h, http.MethodPut, "/actions/codex", `{"name":"codex","label":"Codex","description":"OpenAI Codex CLI","command":"/opt/codex","prompt":{}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("second modified update status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodDelete, "/actions/codex", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("delete modified status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodGet, "/actions/codex", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("detail after delete status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	detail = actionDetailResponse{}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode reverted detail: %v", err)
	}
	if detail.Origin != "builtin" || detail.Command != "codex" {
		t.Fatalf("reverted detail = %+v, want builtin codex", detail)
	}
}

func TestSetActionEnabledTogglesAndBlocksStart(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{
		"codex": {Command: "codex", Label: "Codex"},
	})

	// Built-ins start enabled.
	rec := do(t, h, http.MethodGet, "/actions/codex", "")
	var detail actionDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if !detail.Enabled {
		t.Fatalf("codex enabled = false, want true initially")
	}

	// Disable, then confirm it stays a built-in and the list reflects it.
	rec = do(t, h, http.MethodPut, "/actions/codex/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode disabled detail: %v", err)
	}
	if detail.Enabled || detail.Origin != "builtin" {
		t.Fatalf("disabled detail = %+v, want enabled=false origin=builtin", detail)
	}

	rec = do(t, h, http.MethodGet, "/actions", "")
	var list struct {
		Actions []struct {
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
			Origin  string `json:"origin"`
		} `json:"actions"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Actions) != 1 || list.Actions[0].Enabled || list.Actions[0].Origin != "builtin" {
		t.Fatalf("list = %+v, want disabled builtin codex", list.Actions)
	}

	// A disabled action cannot launch a session.
	rec = do(t, h, http.MethodPost, "/sessions/start", `{"action":"codex","workspaceId":"`+actionsTestWorkspaceID+`"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("start disabled status = %d, want 409 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "action_disabled")

	// Re-enable and confirm start works again.
	rec = do(t, h, http.MethodPut, "/actions/codex/enabled", `{"enabled":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("enable status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	rec = do(t, h, http.MethodPost, "/sessions/start", `{"action":"codex","workspaceId":"`+actionsTestWorkspaceID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("start re-enabled status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
}

func TestSetActionEnabledRequiresEnabledField(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{"codex": {Command: "codex"}})

	// An omitted enabled flag must be a 400, not a silent disable to false.
	rec := do(t, h, http.MethodPut, "/actions/codex/enabled", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("empty body status = %d, want 400 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "invalid_request")

	// The action is untouched — still enabled.
	rec = do(t, h, http.MethodGet, "/actions/codex", "")
	var detail actionDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode detail: %v", err)
	}
	if !detail.Enabled {
		t.Fatalf("codex enabled = false after malformed toggle, want unchanged (true)")
	}
}

func TestCreateActionDerivesNameFromLabel(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{"codex": {Command: "codex"}})

	// No name supplied: the id is derived from the human label.
	rec := do(t, h, http.MethodPost, "/actions", `{"label":"Claude Code","command":"claude"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	var detail actionDetailResponse
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if detail.Name != "claude-code" || detail.Label != "Claude Code" || detail.Origin != "custom" {
		t.Fatalf("created = %+v, want id claude-code", detail)
	}

	// An explicit name still wins over derivation.
	rec = do(t, h, http.MethodPost, "/actions", `{"name":"lg","label":"LazyGit","command":"lazygit"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("explicit-name create status = %d, want 201 (%s)", rec.Code, rec.Body)
	}
	if err := json.NewDecoder(rec.Body).Decode(&detail); err != nil {
		t.Fatalf("decode explicit: %v", err)
	}
	if detail.Name != "lg" {
		t.Fatalf("explicit created = %+v, want id lg", detail)
	}
}

func TestCreateActionRequiresNameOrLabel(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{"codex": {Command: "codex"}})
	for _, body := range []string{`{"command":"claude"}`, `{"label":"!!!","command":"claude"}`} {
		rec := do(t, h, http.MethodPost, "/actions", body)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("status = %d, want 400 for %s (%s)", rec.Code, body, rec.Body)
		}
		assertErrorCode(t, rec, "invalid_request")
	}
}

func TestSetActionEnabledMissing(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{"codex": {Command: "codex"}})
	rec := do(t, h, http.MethodPut, "/actions/missing/enabled", `{"enabled":false}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "action_not_found")
}

func TestActionWriteErrors(t *testing.T) {
	h := newActionsHandler(t, session.ActionRegistry{
		"tool": {Command: "tool"},
	})

	tests := []struct {
		name   string
		method string
		path   string
		body   string
		status int
		code   string
	}{
		{name: "duplicate create", method: http.MethodPost, path: "/actions", body: `{"name":"tool","command":"tool"}`, status: http.StatusConflict, code: "action_conflict"},
		{name: "missing command", method: http.MethodPost, path: "/actions", body: `{"name":"custom"}`, status: http.StatusBadRequest, code: "invalid_action"},
		{name: "bad name", method: http.MethodPost, path: "/actions", body: `{"name":"bad/name","command":"tool"}`, status: http.StatusBadRequest, code: "invalid_action"},
		{name: "bad kind", method: http.MethodPut, path: "/actions/custom", body: `{"kind":"weird","bin":"tool"}`, status: http.StatusBadRequest, code: "invalid_action"},
		{name: "conflicting command and bin", method: http.MethodPut, path: "/actions/custom", body: `{"command":"tool","bin":"/opt/tool"}`, status: http.StatusBadRequest, code: "invalid_action"},
		{name: "name mismatch", method: http.MethodPut, path: "/actions/custom", body: `{"name":"other","command":"tool"}`, status: http.StatusBadRequest, code: "invalid_request"},
		{name: "delete builtin", method: http.MethodDelete, path: "/actions/tool", status: http.StatusConflict, code: "action_conflict"},
		{name: "delete missing", method: http.MethodDelete, path: "/actions/missing", status: http.StatusNotFound, code: "action_not_found"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.method, tt.path, tt.body)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.status, rec.Body)
			}
			assertErrorCode(t, rec, tt.code)
		})
	}
}

func TestActionReadErrorsMapToInternalError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "actions.json")
	if err := os.WriteFile(path, []byte(`{not json`), 0o600); err != nil {
		t.Fatal(err)
	}
	actionStore := actionstore.NewStore(path, session.ActionRegistry{
		"tool": {Command: "tool"},
	}, nil)
	h := Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, actionStore, nil)

	rec := do(t, h, http.MethodGet, "/actions", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body)
	}
	assertErrorCode(t, rec, "internal_error")
}

func TestListActionsRequiresStore(t *testing.T) {
	rec := do(t, Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil, nil), http.MethodGet, "/actions", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body)
	}
}

// actionsTestWorkspaceID is the workspace newActionsHandler seeds so start
// requests through the actions tests have somewhere to land.
const actionsTestWorkspaceID = "wsp_actions"

func newActionsHandler(t *testing.T, actions session.ActionRegistry) http.Handler {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/atc.db")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })
	ctx := context.Background()
	if _, err := st.CreateProject(ctx, store.CreateProjectInput{ID: "prj_actions", Name: "Actions", WorkingDir: t.TempDir()}); err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if _, err := st.CreateWorkspace(ctx, store.CreateWorkspaceInput{ID: actionsTestWorkspaceID, ProjectID: "prj_actions", Name: "Actions"}); err != nil {
		t.Fatalf("CreateWorkspace: %v", err)
	}
	environments := session.EnvironmentRegistry{
		"host-login-shell": {Kind: session.EnvironmentKindHostLoginShell},
	}
	actionStore := actionstore.NewStore(filepath.Join(t.TempDir(), "actions.json"), actions, st)
	workspaces := workspace.NewService(st, nil)
	sessions := session.NewService(st, &fakeMux{}, actionStore, environments, workspaces, nil)
	return Routes(diagnostics.DefaultDiagnostics(), sessions, nil, workspaces, actionStore, nil)
}
