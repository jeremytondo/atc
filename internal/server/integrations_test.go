package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

// The fixture catalog is the shipped one: claude's and zmx's binaries
// available, codex's not, and the T3 Code Integration unavailable (no T3
// home). Every read re-probes, so flipping availability between requests
// is visible immediately.

func decodeIntegration(t *testing.T, rec *httptest.ResponseRecorder) api.Integration {
	t.Helper()
	var integration api.Integration
	decodeInto(t, rec, &integration)
	return integration
}

func TestIntegrationCatalogListAndGet(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodGet, "/v1/integrations", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list: got %d; body %s", rec.Code, rec.Body)
	}
	var list api.IntegrationList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Integrations) != 4 {
		t.Fatalf("integrations = %+v; want claude, codex, t3code, zmx", list.Integrations)
	}
	available, unavailable := true, false
	terminal := []api.AppInteraction{api.AppTerminalStart, api.AppTerminalResume}
	observes := []api.IntegrationCapability{api.CapabilityThreadObservation}
	want := []api.Integration{
		{ID: "claude", Name: "Claude Code", Capabilities: observes,
			Agents:    []api.IntegrationAgent{{ID: "claude", Name: "Claude Code"}},
			Apps:      []api.App{{ID: "claude/tui", Name: "Claude Code", Agents: []string{"claude"}, Interactions: terminal, Available: &available}},
			Available: true, InstallHint: "npm install -g @anthropic-ai/claude-code"},
		{ID: "codex", Name: "Codex", Capabilities: observes,
			Agents:    []api.IntegrationAgent{{ID: "codex", Name: "Codex"}},
			Apps:      []api.App{{ID: "codex/tui", Name: "Codex", Agents: []string{"codex"}, Interactions: terminal, Available: &unavailable}},
			Available: false, InstallHint: "npm install -g @openai/codex"},
	}
	if diff := cmp.Diff(want, list.Integrations[:2]); diff != "" {
		t.Errorf("executable-backed integrations (-want +got):\n%s", diff)
	}

	// T3 Code: several agents, two handoff Apps with no availability
	// claim, and a connection instead of an executable.
	t3 := list.Integrations[2]
	if t3.ID != "t3code" || t3.Name != "T3 Code" || t3.Available || t3.InstallHint != "" || t3.Connection == nil {
		t.Fatalf("t3code = %+v", t3)
	}
	if t3.Connection.State != api.IntegrationUnavailable || t3.Connection.Since.IsZero() || t3.Connection.Detail == "" {
		t.Errorf("t3code connection = %+v; want unavailable with a reason", t3.Connection)
	}
	var agentIDs []string
	for _, agent := range t3.Agents {
		agentIDs = append(agentIDs, agent.ID)
	}
	if diff := cmp.Diff([]string{"claudeAgent", "codex", "cursor", "grok", "opencode"}, agentIDs); diff != "" {
		t.Errorf("t3code agents (-want +got):\n%s", diff)
	}
	wantApps := []api.App{
		{ID: "t3code/web", Name: "T3 Code (web)", Agents: agentIDs, Interactions: []api.AppInteraction{api.AppHandoff}},
		{ID: "t3code/desktop", Name: "T3 Code (desktop)", Agents: agentIDs, Interactions: []api.AppInteraction{api.AppHandoff}},
	}
	if diff := cmp.Diff(wantApps, t3.Apps); diff != "" {
		t.Errorf("t3code apps (-want +got):\n%s", diff)
	}

	// zmx: an infrastructure Integration with no Apps or agents.
	wantZmx := api.Integration{ID: "zmx", Name: "zmx", Capabilities: []api.IntegrationCapability{api.CapabilityTerminalDriver},
		Agents: []api.IntegrationAgent{}, Apps: []api.App{}, Available: true, InstallHint: "install zmx from https://github.com/neurosnap/zmx"}
	if diff := cmp.Diff(wantZmx, list.Integrations[3]); diff != "" {
		t.Errorf("zmx (-want +got):\n%s", diff)
	}

	rec = f.request(t, http.MethodGet, "/v1/integrations/codex", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d", rec.Code)
	}
	if diff := cmp.Diff(want[1], decodeIntegration(t, rec)); diff != "" {
		t.Errorf("get codex (-want +got):\n%s", diff)
	}

	// No cache: installing the binary flips the next read's availability,
	// for the Integration and its terminal App alike.
	f.binaries["codex"] = true
	if got := decodeIntegration(t, f.request(t, http.MethodGet, "/v1/integrations/codex", "")); !got.Available || got.Apps[0].Available == nil || !*got.Apps[0].Available {
		t.Errorf("availability not re-probed: %+v", got)
	}

	rec = f.request(t, http.MethodGet, "/v1/integrations/nonexistent", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"integration_not_found"`) {
		t.Errorf("get unknown: got %d; body %s", rec.Code, rec.Body)
	}
}

// The Agent catalog is gone (ATC-294): its routes answer 404 like any
// unknown path.
func TestAgentRoutesAreGone(t *testing.T) {
	f := newFixture(t)
	for _, path := range []string{"/v1/agents", "/v1/agents/claude", "/v1/agents/adapters", "/v1/agents/adapters/claude"} {
		if rec := f.request(t, http.MethodGet, path, ""); rec.Code != http.StatusNotFound {
			t.Errorf("GET %s: got %d, want 404", path, rec.Code)
		}
	}
}

// An App launch is a terminal create with appId: the terminal records the
// App, the Integration-composed command never appears on the wire, and no
// thread exists until the Integration observes one.
func TestAppLaunchThroughTerminalCreate(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{AppID: "claude/tui"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create with app: got %d; body %s", rec.Code, rec.Body)
	}
	launched := decodeTerminal(t, rec)
	if launched.AppID != "claude/tui" || launched.Name != filepath.Base(f.projectDir) || launched.Status != api.TerminalRunning || launched.Command != "" {
		t.Errorf("launched = %+v", launched)
	}
	if body := rec.Body.String(); strings.Contains(body, "--settings") || strings.Contains(body, `"command"`) {
		t.Errorf("the composed command leaked onto the wire: %s", body)
	}
	// The driver ran the composed command, with this launch's hook
	// settings (ATC-255) quoted and keyed by the minted terminal id.
	if command := f.driverCommand(launched.ID); !strings.HasPrefix(command, "claude --settings '") || !strings.Contains(command, launched.ID+".json") {
		t.Errorf("driver command = %q", command)
	}
	if rec := f.request(t, http.MethodGet, "/v1/threads", ""); !strings.Contains(rec.Body.String(), `"threads":[]`) {
		t.Errorf("launch minted a thread eagerly: %s", rec.Body)
	}

	plain := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Command: "hx"}))
	if body := plain.Body.String(); strings.Contains(body, `"appId"`) || !strings.Contains(body, `"command":"hx"`) {
		t.Errorf("plain terminal = %s", body)
	}

	// Get and list show the App the same way the create response did.
	if got := decodeTerminal(t, f.request(t, http.MethodGet, "/v1/terminals/"+launched.ID, "")); got.AppID != "claude/tui" || got.Command != "" {
		t.Errorf("get = %+v, want app claude/tui and no command", got)
	}
}

// Every launch refusal is typed and creates nothing: a missing executable
// names the command and hint; a handoff App does not run in a terminal;
// an unknown App is 404.
func TestAppLaunchRefusalsCreateNothing(t *testing.T) {
	f := newFixture(t)
	cases := []struct {
		app    string
		status int
		code   string
		detail string
	}{
		{"codex/tui", http.StatusConflict, api.CodeAppUnavailable, "npm install -g @openai/codex"},
		{"t3code/web", http.StatusUnprocessableEntity, api.CodeAppNotTerminalCapable, "t3code/web"},
		{"claude/desktop", http.StatusNotFound, api.CodeAppNotFound, "claude/desktop"},
		{"nonexistent/tui", http.StatusNotFound, api.CodeAppNotFound, "nonexistent/tui"},
		{"claude", http.StatusNotFound, api.CodeAppNotFound, "integration/app"},
	}
	for _, tc := range cases {
		rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{AppID: tc.app}))
		body := rec.Body.String()
		if rec.Code != tc.status || !strings.Contains(body, `"code":"`+tc.code+`"`) || !strings.Contains(body, tc.detail) {
			t.Errorf("launch %s: got %d; body %s; want %d %s mentioning %q", tc.app, rec.Code, body, tc.status, tc.code, tc.detail)
		}
	}
	if rec := f.request(t, http.MethodGet, "/v1/terminals", ""); !strings.Contains(rec.Body.String(), `"terminals":[]`) {
		t.Errorf("a refused launch left a record: %s", rec.Body)
	}
}

func TestTerminalCreateRejectsAppWithCommand(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodPost, "/v1/terminals",
		f.createTerminalBody(t, api.TerminalCreateParams{AppID: "claude/tui", Command: "hx"}))
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"launch_mode_conflict"`) {
		t.Errorf("app+command: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodGet, "/v1/terminals", ""); !strings.Contains(rec.Body.String(), `"terminals":[]`) {
		t.Errorf("a rejected create left a record: %s", rec.Body)
	}
}

// Every error is a Problem with a code: the handlers' typed refusals,
// Huma's validation failures, the auth middleware's 401, and the mux's
// own 404 and 405 for paths and methods no operation claims.
func TestProblemsCarryCodes(t *testing.T) {
	f := newFixture(t)
	rec := f.request(t, http.MethodPost, "/v1/terminals", `{"projectId":""}`)
	if rec.Code != http.StatusUnprocessableEntity || !strings.Contains(rec.Body.String(), `"code":"validation_failed"`) {
		t.Errorf("validation: got %d; body %s", rec.Code, rec.Body)
	}
	req := httptest.NewRequest(http.MethodGet, "/v1/terminals", nil)
	rec = httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || !strings.Contains(rec.Body.String(), `"code":"unauthorized"`) {
		t.Errorf("unauthorized: got %d; body %s", rec.Code, rec.Body)
	}
	if rec := f.request(t, http.MethodGet, "/v1/terminals/term-nonex", ""); rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"terminal_not_found"`) {
		t.Errorf("typed refusal: got %d; body %s", rec.Code, rec.Body)
	}
	rec = f.request(t, http.MethodGet, "/v1/nonexistent", "")
	if rec.Code != http.StatusNotFound || !strings.Contains(rec.Body.String(), `"code":"not_found"`) || rec.Header().Get("Content-Type") != "application/problem+json" {
		t.Errorf("unknown route: got %d %q; body %s", rec.Code, rec.Header().Get("Content-Type"), rec.Body)
	}
	rec = f.request(t, http.MethodPut, "/v1/terminals", "")
	if rec.Code != http.StatusMethodNotAllowed || !strings.Contains(rec.Body.String(), `"code":"method_not_allowed"`) || rec.Header().Get("Allow") == "" {
		t.Errorf("method mismatch: got %d allow=%q; body %s", rec.Code, rec.Header().Get("Allow"), rec.Body)
	}
}
