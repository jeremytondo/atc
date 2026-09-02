package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

// The fixture catalog is the shipped one: claude's binary available,
// codex's not, and the T3 Code observer unavailable (no T3 home). Every
// read re-probes, so flipping availability between requests is visible
// immediately.

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
	// Derived from the adapters: claude and codex are launchable through
	// their own adapters and also produced by T3 Code; the agents only T3
	// produces are listed but not launchable.
	want := api.AgentList{Agents: []api.Agent{
		{ID: "claude", Name: "Claude Code", Available: true, Adapters: []string{"claude", "t3code"}},
		{ID: "codex", Name: "Codex", Available: false, Adapters: []string{"codex", "t3code"}},
		{ID: "cursor", Name: "Cursor", Adapters: []string{"t3code"}},
		{ID: "grok", Name: "Grok", Adapters: []string{"t3code"}},
		{ID: "opencode", Name: "OpenCode", Adapters: []string{"t3code"}},
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
	f.binaries["codex"] = true
	if got := decodeAgent(t, f.request(t, http.MethodGet, "/v1/agents/codex", "")); !got.Available {
		t.Errorf("availability not re-probed: %+v", got)
	}

	if rec := f.request(t, http.MethodGet, "/v1/agents/nonexistent", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown: got %d, want 404", rec.Code)
	}
}

// The adapter list is the catalog's source: launchers with their binary
// probe and install hint, the T3 observer with its connection.
func TestAgentAdaptersOverTheWire(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodGet, "/v1/agents/adapters", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d; body %s", rec.Code, rec.Body)
	}
	var list api.AgentAdapterList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Adapters) != 3 {
		t.Fatalf("adapters = %+v; want claude, codex, t3code", list.Adapters)
	}
	wantLaunchers := []api.AgentAdapter{
		{ID: "claude", Name: "Claude Code", Agents: []string{"claude"}, Available: true, InstallHint: "npm install -g @anthropic-ai/claude-code"},
		{ID: "codex", Name: "Codex", Agents: []string{"codex"}, Available: false, InstallHint: "npm install -g @openai/codex"},
	}
	if diff := cmp.Diff(wantLaunchers, list.Adapters[:2]); diff != "" {
		t.Errorf("launchers (-want +got):\n%s", diff)
	}
	t3 := list.Adapters[2]
	if t3.ID != "t3code" || t3.Name != "T3 Code" || t3.Available || t3.InstallHint != "" || t3.Connection == nil {
		t.Fatalf("t3code = %+v", t3)
	}
	if t3.Connection.State != api.AdapterUnavailable || t3.Connection.Since.IsZero() || t3.Connection.Detail == "" {
		t.Errorf("t3code connection = %+v; want unavailable with a reason", t3.Connection)
	}
	if diff := cmp.Diff([]string{"claude", "codex", "cursor", "grok", "opencode"}, t3.Agents); diff != "" {
		t.Errorf("t3code agents (-want +got):\n%s", diff)
	}

	rec = f.request(t, http.MethodGet, "/v1/agents/adapters/t3code", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d; body %s", rec.Code, rec.Body)
	}
	var got api.AgentAdapter
	decodeInto(t, rec, &got)
	if diff := cmp.Diff(t3, got); diff != "" {
		t.Errorf("get t3code (-want +got):\n%s", diff)
	}
	if rec := f.request(t, http.MethodGet, "/v1/agents/adapters/nonexistent", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get unknown adapter: got %d, want 404", rec.Code)
	}
}

// The launch alias is gone (ATC-285): terminal create with an agent
// reference is the one launch path, and the terminal carries the
// adapter's command and the agent label; a plain terminal omits agent.
func TestAgentLaunchRouteIsGone(t *testing.T) {
	f := newFixture(t)
	if rec := f.request(t, http.MethodPost, "/v1/agents/claude/launch", `{"projectId":"`+f.projectID+`"}`); rec.Code != http.StatusNotFound {
		t.Errorf("launch alias: got %d, want 404", rec.Code)
	}

	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Agent: "claude"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with agent: got %d; body %s", rec.Code, rec.Body)
	}
	launched := decodeTerminal(t, rec)
	if launched.Agent != "claude" || launched.Name != "Claude Code" || launched.Status != api.TerminalRunning {
		t.Errorf("launched = %+v", launched)
	}
	// The composed command injects this launch's hook settings (ATC-255),
	// quoted, keyed by the minted terminal id.
	if !strings.HasPrefix(launched.Command, "claude --settings '") || !strings.Contains(launched.Command, launched.ID+".json") {
		t.Errorf("launch command = %q", launched.Command)
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

// A missing binary refuses with the command and hint, creating nothing;
// an agent only an observer produces is known but not launchable.
func TestLaunchRefusalsCreateNothing(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Agent: "codex"}))
	if rec.Code != http.StatusConflict {
		t.Fatalf("missing binary: got %d, want 409; body %s", rec.Code, rec.Body)
	}
	if body := rec.Body.String(); !strings.Contains(body, `\"codex\"`) || !strings.Contains(body, "npm install -g @openai/codex") {
		t.Errorf("refusal names neither command nor hint: %s", body)
	}

	rec = f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Agent: "cursor"}))
	if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "no adapter can launch cursor") {
		t.Errorf("observer-only agent: got %d; body %s", rec.Code, rec.Body)
	}

	rec = f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Agent: "nonexistent"}))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown agent: got %d, want 404", rec.Code)
	}

	if rec := f.request(t, http.MethodGet, "/v1/terminals", ""); !strings.Contains(rec.Body.String(), `"terminals":[]`) {
		t.Errorf("a refused launch left a record: %s", rec.Body)
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
