package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/jeremytondo/atc/internal/api"
)

// The fixture catalog is the shipped one: claude's binary available,
// codex's not. Every read re-probes, so flipping availability between
// requests is visible immediately.

func decodeAgent(t *testing.T, rec *httptest.ResponseRecorder) api.Agent {
	t.Helper()
	var agent api.Agent
	if err := json.Unmarshal(rec.Body.Bytes(), &agent); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return agent
}

func TestAgentCatalogListAndGet(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodGet, "/v1/agents", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d; body %s", rec.Code, rec.Body)
	}
	var list api.AgentList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	want := api.AgentList{Agents: []api.Agent{
		{ID: "claude", Name: "Claude Code", Capabilities: []api.AgentCapability{
			{Capability: "tui", Available: true, InstallHint: "npm install -g @anthropic-ai/claude-code"},
		}},
		{ID: "codex", Name: "Codex", Capabilities: []api.AgentCapability{
			{Capability: "tui", Available: false, InstallHint: "npm install -g @openai/codex"},
		}},
	}}
	if diff := cmp.Diff(want, list); diff != "" {
		t.Errorf("list (-want +got):\n%s", diff)
	}

	rec = f.request(t, http.MethodGet, "/v1/agents/codex", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d", rec.Code)
	}
	if diff := cmp.Diff(want.Agents[1], decodeAgent(t, rec)); diff != "" {
		t.Errorf("get codex (-want +got):\n%s", diff)
	}

	// No cache: installing the binary flips the next read's availability.
	f.lookPath.set("codex", true)
	if got := decodeAgent(t, f.request(t, http.MethodGet, "/v1/agents/codex", "")); !got.Capabilities[0].Available {
		t.Errorf("availability not re-probed: %+v", got)
	}

	if rec := f.request(t, http.MethodGet, "/v1/agents/nonexistent", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown: got %d, want 404", rec.Code)
	}
}

// Launch through both routes: the terminal carries the adapter's command
// and the agent label, and a plain terminal omits agent entirely.
func TestAgentLaunchCreatesTheTerminal(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodPost, "/v1/agents/claude/launch", `{"projectId":"`+f.projectID+`"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("launch: got %d; body %s", rec.Code, rec.Body)
	}
	launched := decodeTerminal(t, rec)
	if launched.Agent != "claude" || launched.Command != "claude" ||
		launched.Name != "Claude Code" || launched.Status != api.TerminalRunning {
		t.Errorf("launched = %+v", launched)
	}
	if !strings.Contains(rec.Body.String(), `"agent":"claude"`) {
		t.Errorf("launch body has no agent field: %s", rec.Body)
	}

	plain := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{}))
	if strings.Contains(plain.Body.String(), `"agent"`) {
		t.Errorf("plain terminal carries an agent field: %s", plain.Body)
	}

	// Get and list show the label the same way the create response did.
	rec = f.request(t, http.MethodGet, "/v1/terminals/"+launched.ID, "")
	if got := decodeTerminal(t, rec); got.Agent != "claude" {
		t.Errorf("get = %+v, want agent claude", got)
	}
}

func TestTerminalCreateWithAgentMatchesLaunch(t *testing.T) {
	f := newFixture(t)

	viaTerminals := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals",
		f.createTerminalBody(t, api.TerminalCreateParams{Agent: "claude"})))
	viaAlias := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/agents/claude/launch",
		`{"projectId":"`+f.projectID+`"}`))
	// Identity and clock fields necessarily differ; everything the routes
	// decide must not.
	ignore := cmpopts.IgnoreFields(api.Terminal{}, "ID", "CreatedAt", "UpdatedAt")
	if diff := cmp.Diff(viaAlias, viaTerminals, ignore); diff != "" {
		t.Errorf("routes disagree (-alias +terminals):\n%s", diff)
	}
}

// A missing binary refuses with the command and hint, creating nothing —
// through either route.
func TestLaunchMissingBinaryCreatesNothing(t *testing.T) {
	f := newFixture(t)

	for name, launch := range map[string]func() *httptest.ResponseRecorder{
		"alias": func() *httptest.ResponseRecorder {
			return f.request(t, http.MethodPost, "/v1/agents/codex/launch", `{"projectId":"`+f.projectID+`"}`)
		},
		"terminal create": func() *httptest.ResponseRecorder {
			return f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Agent: "codex"}))
		},
	} {
		rec := launch()
		if rec.Code != http.StatusConflict {
			t.Fatalf("%s: got %d, want 409; body %s", name, rec.Code, rec.Body)
		}
		if body := rec.Body.String(); !strings.Contains(body, `\"codex\"`) || !strings.Contains(body, "npm install -g @openai/codex") {
			t.Errorf("%s refusal names neither command nor hint: %s", name, body)
		}
	}
	if rec := f.request(t, http.MethodGet, "/v1/terminals", ""); !strings.Contains(rec.Body.String(), `"terminals":[]`) {
		t.Errorf("a refused launch left a record: %s", rec.Body)
	}
}

func TestLaunchUnknownAgent(t *testing.T) {
	f := newFixture(t)
	if rec := f.request(t, http.MethodPost, "/v1/agents/nonexistent/launch", `{"projectId":"`+f.projectID+`"}`); rec.Code != http.StatusNotFound {
		t.Errorf("alias: got %d, want 404", rec.Code)
	}
	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Agent: "nonexistent"}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("terminal create: got %d, want 404", rec.Code)
	}
}

func TestTerminalCreateRejectsAgentWithCommand(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodPost, "/v1/terminals",
		f.createTerminalBody(t, api.TerminalCreateParams{Agent: "claude", Command: "hx"}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), "mutually exclusive") {
		t.Errorf("agent+command: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodGet, "/v1/terminals", ""); !strings.Contains(rec.Body.String(), `"terminals":[]`) {
		t.Errorf("a rejected create left a record: %s", rec.Body)
	}
}
