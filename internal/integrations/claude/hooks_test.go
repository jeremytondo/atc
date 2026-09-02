package claude

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations/hookauth"
	"github.com/jeremytondo/atc/internal/threads"
)

// fakeObserver records what reaches the threads seam.
type fakeObserver struct {
	mu       sync.Mutex
	sessions []threads.SessionObservation
	statuses []threads.StatusObservation
	inactive []string
	// identity is the mapping LookupIdentity serves: provider id →
	// terminal id.
	identity map[string]string
}

// ObserveSession records the observation and, like the real service,
// maps the identity from then on.
func (f *fakeObserver) ObserveSession(_ context.Context, o threads.SessionObservation) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, o)
	f.identity[o.ProviderID] = o.TerminalID
	return "thrd-aaaaa", nil
}

func (f *fakeObserver) ObserveStatus(_ context.Context, o threads.StatusObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, o)
	return nil
}

func (f *fakeObserver) Deactivate(_ context.Context, terminalID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inactive = append(f.inactive, terminalID)
}

func (f *fakeObserver) LookupIdentity(_, providerID string) (string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminalID, ok := f.identity[providerID]
	return "thrd-aaaaa", terminalID, ok
}

func (f *fakeObserver) lastStatus(t *testing.T) threads.StatusObservation {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		t.Fatal("no status observation recorded")
	}
	return f.statuses[len(f.statuses)-1]
}

// fakeTerminals serves terminal records for project resolution and boot
// reload.
type fakeTerminals struct {
	mu        sync.Mutex
	terminals map[string]api.Terminal
}

func (f *fakeTerminals) Get(id string) (api.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal, ok := f.terminals[id]
	if !ok {
		return api.Terminal{}, errors.New("terminal not found")
	}
	return terminal, nil
}

type hookFixture struct {
	hooks     *Hooks
	observer  *fakeObserver
	terminals *fakeTerminals
	dir       string
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	observer := &fakeObserver{identity: map[string]string{}}
	terminals := &fakeTerminals{terminals: map[string]api.Terminal{
		"term-aaaaa": {ID: "term-aaaaa", ProjectID: "proj-aaaaa", Agent: "claude", Status: api.TerminalRunning},
	}}
	dir := t.TempDir()
	hooks, err := NewHooks(HooksOptions{
		Dir:       dir,
		BaseURL:   "http://127.0.0.1:4779",
		Threads:   observer,
		Terminals: terminals,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &hookFixture{hooks: hooks, observer: observer, terminals: terminals, dir: dir}
}

// prepare runs the launch composition and returns the minted secret.
func (f *hookFixture) prepare(t *testing.T, terminalID string) string {
	t.Helper()
	if _, err := f.hooks.Prepare(terminalID); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(f.hooks.registry.HeaderPath(terminalID))
	if err != nil {
		t.Fatal(err)
	}
	secret, ok := strings.CutPrefix(string(content), hookauth.SecretHeader+": ")
	if !ok {
		t.Fatalf("header file %q lacks the header prefix", content)
	}
	return secret
}

// post delivers one hook payload and returns the status code.
func (f *hookFixture) post(t *testing.T, secret, body string) int {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, HooksPath, strings.NewReader(body))
	if secret != "" {
		req.Header.Set(hookauth.SecretHeader, secret)
	}
	rec := httptest.NewRecorder()
	f.hooks.Handler().ServeHTTP(rec, req)
	return rec.Code
}

func TestPrepareWritesPrivateFiles(t *testing.T) {
	f := newHookFixture(t)
	settings, err := f.hooks.Prepare("term-aaaaa")
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{settings, f.hooks.registry.HeaderPath("term-aaaaa")} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("%s mode = %o, want 0600", path, info.Mode().Perm())
		}
	}

	content, err := os.ReadFile(settings)
	if err != nil {
		t.Fatal(err)
	}
	header, err := os.ReadFile(f.hooks.registry.HeaderPath("term-aaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimPrefix(string(header), hookauth.SecretHeader+": ")
	// The secret rides only the header file: never argv (the settings
	// command), never a URL.
	if strings.Contains(string(content), secret) {
		t.Error("settings file leaks the secret")
	}
	for _, want := range []string{
		`"SessionStart"`, `"Stop"`, `"Notification"`, `"PermissionRequest"`,
		"curl -fsS -m 5", "-H @'", "http://127.0.0.1:4779" + HooksPath,
	} {
		if !strings.Contains(string(content), want) {
			t.Errorf("settings file missing %q:\n%s", want, content)
		}
	}
}

// Deleting the terminal deregisters its launch: the secret stops
// validating and both per-launch files go.
func TestDeregisterRemovesLaunchFiles(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	f.hooks.Deregister("term-aaaaa")
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`); code != http.StatusNotFound {
		t.Errorf("deregistered secret: got %d, want 404", code)
	}
	for _, path := range []string{f.hooks.settingsPath("term-aaaaa"), f.hooks.registry.HeaderPath("term-aaaaa")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Errorf("per-launch file %s survived deregistration", path)
		}
	}
}

func TestDeliverySecretEnforcement(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")
	start := `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`

	if code := f.post(t, "", start); code != http.StatusNotFound {
		t.Errorf("no secret: got %d, want 404", code)
	}
	if code := f.post(t, "wrong", start); code != http.StatusNotFound {
		t.Errorf("wrong secret: got %d, want 404", code)
	}
	if code := f.post(t, secret, `{not json`); code != http.StatusBadRequest {
		t.Errorf("malformed payload: got %d, want 400", code)
	}
	if code := f.post(t, secret, start); code != http.StatusNoContent {
		t.Errorf("valid delivery: got %d, want 204", code)
	}

	// A payload whose session disagrees with the registration is dropped.
	if code := f.post(t, secret, `{"session_id":"s-other","hook_event_name":"Stop"}`); code != http.StatusBadRequest {
		t.Errorf("mismatched session: got %d, want 400", code)
	}
	if len(f.observer.statuses) != 0 {
		t.Errorf("dropped payload still observed: %+v", f.observer.statuses)
	}

	// A relaunch mints a new secret; the old one stops validating.
	fresh := f.prepare(t, "term-aaaaa")
	if code := f.post(t, secret, start); code != http.StatusNotFound {
		t.Errorf("stale secret: got %d, want 404", code)
	}
	if code := f.post(t, fresh, start); code != http.StatusNoContent {
		t.Errorf("fresh secret: got %d, want 204", code)
	}
}

func TestSessionLifecycleObservations(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	// SessionStart alone mints nothing (ATC-282): a zero-turn TUI has no
	// thread. Whatever the terminal showed before leaves it — for a fresh
	// launch, nothing. Chatter before the first prompt is accepted and
	// dropped.
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`)
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"Notification","notification_type":"idle_prompt"}`); code != http.StatusNoContent {
		t.Errorf("pre-prompt event: got %d, want 204", code)
	}
	if len(f.observer.sessions) != 0 || len(f.observer.statuses) != 0 {
		t.Fatalf("observed before the first prompt: %+v %+v", f.observer.sessions, f.observer.statuses)
	}
	if len(f.observer.inactive) != 1 {
		t.Fatalf("fresh start deactivations = %v, want one (a no-op downstream)", f.observer.inactive)
	}

	// The first prompt mints the thread — identity, project, and payload
	// metadata — then supplies the title fallback and starts the turn.
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"fix the flaky build on CI so releases stop breaking every night","cwd":"/proj","permission_mode":"default","effort":{"level":"high"}}`)
	if len(f.observer.sessions) != 1 {
		t.Fatalf("sessions = %+v", f.observer.sessions)
	}
	session := f.observer.sessions[0]
	if session.Adapter != "claude" || session.Agent != "claude" || session.ProviderID != "s1" || session.TerminalID != "term-aaaaa" ||
		session.ProjectID != "proj-aaaaa" || session.Status != "" {
		t.Errorf("session observation = %+v", session)
	}
	if session.Metadata.Cwd != "/proj" || session.Metadata.PermissionMode != "default" || session.Metadata.Effort != "high" {
		t.Errorf("session metadata = %+v", session.Metadata)
	}
	status := f.observer.lastStatus(t)
	if status.Status != api.ThreadWorking {
		t.Errorf("prompt status = %s", status.Status)
	}
	if status.Metadata.Title == "" || len(status.Metadata.Title) > 50 {
		t.Errorf("title fallback = %q", status.Metadata.Title)
	}

	// /clear: SessionEnd(reason=clear) does not deactivate — the end
	// refreshes evidence without a status claim — but the successor's
	// SessionStart, having no thread to move the terminal to, does: the
	// old conversation has left the screen.
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionEnd","reason":"clear"}`)
	if got := f.observer.lastStatus(t); got.Status != "" {
		t.Errorf("session end status claim = %q; want evidence-only", got.Status)
	}
	if len(f.observer.inactive) != 1 {
		t.Errorf("clear deactivated the terminal: %v", f.observer.inactive)
	}
	f.post(t, secret, `{"session_id":"s2","hook_event_name":"SessionStart","source":"clear"}`)
	if len(f.observer.sessions) != 1 {
		t.Fatalf("SessionStart after clear minted: %+v", f.observer.sessions)
	}
	if len(f.observer.inactive) != 2 || f.observer.inactive[1] != "term-aaaaa" {
		t.Errorf("successor start did not deactivate: %v", f.observer.inactive)
	}
	f.post(t, secret, `{"session_id":"s2","hook_event_name":"UserPromptSubmit","prompt":"next"}`)
	if len(f.observer.sessions) != 2 || f.observer.sessions[1].ProviderID != "s2" {
		t.Fatalf("sessions after clear = %+v", f.observer.sessions)
	}

	// Evidence for the departed session is now dropped.
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"Stop"}`); code != http.StatusBadRequest {
		t.Errorf("stale session evidence: got %d, want 400", code)
	}

	// TUI exit: SessionEnd with any other reason deactivates.
	f.post(t, secret, `{"session_id":"s2","hook_event_name":"SessionEnd","reason":"other"}`)
	if len(f.observer.inactive) != 3 || f.observer.inactive[2] != "term-aaaaa" {
		t.Errorf("exit did not deactivate: %v", f.observer.inactive)
	}

	// The ended session's stragglers are dropped, never re-seeded; the
	// next SessionStart opens the next chapter.
	before := len(f.observer.statuses)
	if code := f.post(t, secret, `{"session_id":"s2","hook_event_name":"Stop"}`); code != http.StatusBadRequest {
		t.Errorf("straggler after end: got %d, want 400", code)
	}
	if len(f.observer.statuses) != before {
		t.Errorf("straggler observed: %+v", f.observer.statuses[before:])
	}
	if code := f.post(t, secret, `{"session_id":"s3","hook_event_name":"SessionStart","source":"startup"}`); code != http.StatusNoContent {
		t.Errorf("SessionStart after end: got %d, want 204", code)
	}
}

// A compact re-announces the same session id: identity and reducer state
// stand, and no idle is claimed for a possibly mid-turn conversation.
func TestSessionStartCompactIsIdempotent(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`)
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"go"}`)
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"compact"}`)
	// The compact's session observation claims no status...
	last := f.observer.sessions[len(f.observer.sessions)-1]
	if last.Status != "" {
		t.Errorf("compact session status = %q; want no claim", last.Status)
	}
	// ...and the reducer still believes the turn is running: the next
	// evidence continues from working, not from a reset.
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"PostToolUse","tool_name":"Bash"}`)
	if got := f.observer.lastStatus(t); got.Status != api.ThreadWorking {
		t.Errorf("status after compact = %s; want working preserved", got.Status)
	}
}

// A resumed conversation — one the identity mapping already knows — is
// observed at SessionStart, in whichever terminal announces it: the
// thread updates immediately, at its prompt, with no minting deferral.
func TestResumeObservedAtSessionStart(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")
	f.observer.identity["s7"] = "term-other"

	if code := f.post(t, secret, `{"session_id":"s7","hook_event_name":"SessionStart","source":"resume","cwd":"/proj"}`); code != http.StatusNoContent {
		t.Fatalf("resume SessionStart: got %d", code)
	}
	if len(f.observer.sessions) != 1 {
		t.Fatalf("sessions = %+v", f.observer.sessions)
	}
	session := f.observer.sessions[0]
	if session.ProviderID != "s7" || session.TerminalID != "term-aaaaa" || session.Status != api.ThreadIdle || session.Metadata.Cwd != "/proj" {
		t.Errorf("resume observation = %+v", session)
	}
	// Established: ordinary evidence reduces without re-observing.
	f.post(t, secret, `{"session_id":"s7","hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	if len(f.observer.sessions) != 1 || f.observer.lastStatus(t).Status != api.ThreadWorking {
		t.Errorf("after resume: sessions %+v, statuses %+v", f.observer.sessions, f.observer.statuses)
	}
}

// A transient failure recording a resumed session must not silence it:
// the next event retries the session observation before its evidence.
func TestSessionObservationFailureRetries(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")
	f.observer.identity["s1"] = "term-other"

	// The terminal record is briefly unavailable at SessionStart.
	f.terminals.mu.Lock()
	saved := f.terminals.terminals["term-aaaaa"]
	delete(f.terminals.terminals, "term-aaaaa")
	f.terminals.mu.Unlock()
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"resume"}`); code != http.StatusNoContent {
		t.Fatalf("SessionStart during outage: got %d", code)
	}
	if len(f.observer.sessions) != 0 {
		t.Fatalf("sessions during outage = %+v", f.observer.sessions)
	}

	// The outage heals; the next ordinary event — not only a prompt, since
	// the conversation is mapped — re-establishes and lands.
	f.terminals.mu.Lock()
	f.terminals.terminals["term-aaaaa"] = saved
	f.terminals.mu.Unlock()
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash"}`); code != http.StatusNoContent {
		t.Fatalf("event after outage: got %d", code)
	}
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].ProviderID != "s1" {
		t.Errorf("session not re-established: %+v", f.observer.sessions)
	}
	if got := f.observer.lastStatus(t); got.Status != api.ThreadWorking {
		t.Errorf("evidence after retry = %s", got.Status)
	}
}

// After a server restart the registration has no session. Only a root
// UserPromptSubmit for a conversation the identity mapping ties to the
// same terminal re-seeds — re-establishing the session first. Any other
// event is dropped: a displaced session's straggler must not seed the
// wrong conversation as active.
func TestPostRestartSeeding(t *testing.T) {
	f := newHookFixture(t)
	f.prepare(t, "term-aaaaa")
	if err := f.hooks.LoadRegistrations(); err != nil {
		t.Fatal(err)
	}
	secret := f.prepare(t, "term-aaaaa") // fresh handle to the same terminal
	f.observer.identity["s1"] = "term-aaaaa"
	f.observer.identity["s9"] = "term-other"

	// Even a mapped session's straggler is refused: only a prompt proves
	// the TUI displays the conversation. A subagent's prompt proves
	// nothing about the root either.
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"Stop"}`); code != http.StatusBadRequest {
		t.Errorf("straggler seed: got %d, want 400", code)
	}
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","agent_id":"sub-1","prompt":"go"}`); code != http.StatusBadRequest {
		t.Errorf("subagent seed: got %d, want 400", code)
	}
	// A conversation mapped to another terminal is dropped.
	if code := f.post(t, secret, `{"session_id":"s9","hook_event_name":"UserPromptSubmit","prompt":"go"}`); code != http.StatusBadRequest {
		t.Errorf("wrong-terminal seed: got %d, want 400", code)
	}
	if len(f.observer.sessions) != 0 {
		t.Fatalf("refused seeds still observed: %+v", f.observer.sessions)
	}

	// A root prompt for a mapped session seeds: session re-established,
	// then the evidence lands.
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"go"}`); code != http.StatusNoContent {
		t.Errorf("mapped seed: got %d, want 204", code)
	}
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].ProviderID != "s1" || f.observer.sessions[0].Status != "" {
		t.Fatalf("seed session observation = %+v", f.observer.sessions)
	}
	if got := f.observer.lastStatus(t); got.Status != api.ThreadWorking {
		t.Errorf("seeded evidence status = %s", got.Status)
	}
}

// A TUI that sat at its prompt across a restart has no thread and no
// mapping; its first root prompt is the mint, exactly as without the
// restart.
func TestPostRestartFirstPromptMints(t *testing.T) {
	f := newHookFixture(t)
	f.prepare(t, "term-aaaaa")
	if err := f.hooks.LoadRegistrations(); err != nil {
		t.Fatal(err)
	}
	secret := f.prepare(t, "term-aaaaa")

	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"Notification","notification_type":"idle_prompt"}`); code != http.StatusBadRequest {
		t.Errorf("unmapped straggler seed: got %d, want 400", code)
	}
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"first words"}`); code != http.StatusNoContent {
		t.Errorf("unmapped first prompt: got %d, want 204", code)
	}
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].ProviderID != "s1" || f.observer.sessions[0].Metadata.Title != "first words" {
		t.Fatalf("mint after restart = %+v", f.observer.sessions)
	}
	if got := f.observer.lastStatus(t); got.Status != api.ThreadWorking {
		t.Errorf("status after mint = %s", got.Status)
	}
}

// A transient failure on the minting prompt itself must not lose the
// thread for the whole turn: the prompt is remembered, the next event
// mints — with the prompt's title — and the turn's evidence lands.
func TestFirstPromptFailureRetriesOnNextEvent(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`)

	f.terminals.mu.Lock()
	saved := f.terminals.terminals["term-aaaaa"]
	delete(f.terminals.terminals, "term-aaaaa")
	f.terminals.mu.Unlock()
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"fix the build"}`); code != http.StatusNoContent {
		t.Fatalf("prompt during outage: got %d", code)
	}
	if len(f.observer.sessions) != 0 {
		t.Fatalf("sessions during outage = %+v", f.observer.sessions)
	}

	f.terminals.mu.Lock()
	f.terminals.terminals["term-aaaaa"] = saved
	f.terminals.mu.Unlock()
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"PreToolUse","tool_name":"Bash"}`)
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].Metadata.Title != "fix the build" {
		t.Fatalf("retry mint = %+v; want one observation titled from the prompt", f.observer.sessions)
	}
	if got := f.observer.lastStatus(t); got.Status != api.ThreadWorking {
		t.Errorf("evidence after retry = %s", got.Status)
	}
}

// The TUI leaving right after a failed minting prompt still gets its
// thread: SessionEnd is the last retry, so the conversation stays in the
// index as resumable.
func TestFirstPromptFailureRetriesAtSessionEnd(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`)

	f.terminals.mu.Lock()
	saved := f.terminals.terminals["term-aaaaa"]
	delete(f.terminals.terminals, "term-aaaaa")
	f.terminals.mu.Unlock()
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"fix the build"}`)
	f.terminals.mu.Lock()
	f.terminals.terminals["term-aaaaa"] = saved
	f.terminals.mu.Unlock()

	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionEnd","reason":"other"}`)
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].Metadata.Title != "fix the build" || f.observer.sessions[0].Status != "" {
		t.Fatalf("mint at SessionEnd = %+v; want one titled observation with no status claim", f.observer.sessions)
	}
	if len(f.observer.inactive) != 2 || f.observer.inactive[1] != "term-aaaaa" {
		t.Errorf("exit did not deactivate: %v", f.observer.inactive)
	}
}

func TestLoadRegistrationsCleansStaleFiles(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")
	// A leftover for a terminal that no longer exists (deleted, or an
	// abandoned launch candidate).
	f.prepare(t, "term-zzzzz")

	reloaded, err := NewHooks(HooksOptions{
		Dir: f.dir, BaseURL: "http://127.0.0.1:4779",
		Threads: f.observer, Terminals: f.terminals,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reloaded.LoadRegistrations(); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(reloaded.registry.HeaderPath("term-zzzzz")); !os.IsNotExist(err) {
		t.Error("stale hook files survived the reload")
	}
	if _, err := os.Stat(reloaded.registry.HeaderPath("term-aaaaa")); err != nil {
		t.Error("live hook files were removed")
	}
	// The reloaded registry accepts the persisted secret.
	req := httptest.NewRequest(http.MethodPost, HooksPath,
		strings.NewReader(`{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`))
	req.Header.Set(hookauth.SecretHeader, secret)
	rec := httptest.NewRecorder()
	reloaded.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("reloaded secret: got %d, want 204", rec.Code)
	}
}

func TestCommandComposition(t *testing.T) {
	f := newHookFixture(t)
	adapter := Adapter(f.hooks)
	if len(adapter.Agents) != 1 || adapter.Agents[0].ID != "claude" || adapter.Agents[0].TUI == nil {
		t.Fatalf("registration = %+v; want one launchable claude agent", adapter)
	}
	launcher := adapter.Agents[0].TUI
	command, err := launcher.Command(context.Background(), agents.LaunchContext{TerminalID: "term-aaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	if command != "claude --settings "+agents.Quote(f.hooks.settingsPath("term-aaaaa")) {
		t.Errorf("command = %q", command)
	}
	if _, err := os.Stat(f.hooks.settingsPath("term-aaaaa")); err != nil {
		t.Errorf("Command did not prepare the settings file: %v", err)
	}

	// The resume form reopens the exact session, quoted, with the same
	// hook wiring.
	command, err = launcher.Command(context.Background(), agents.LaunchContext{TerminalID: "term-bbbbb", ResumeConversationID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if command != "claude --settings "+agents.Quote(f.hooks.settingsPath("term-bbbbb"))+" --resume 'sess-1'" {
		t.Errorf("resume command = %q", command)
	}
}
