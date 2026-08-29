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

func (f *fakeObserver) ObserveSession(_ context.Context, o threads.SessionObservation) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, o)
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
	content, err := os.ReadFile(f.hooks.headerPath(terminalID))
	if err != nil {
		t.Fatal(err)
	}
	secret, ok := strings.CutPrefix(string(content), SecretHeader+": ")
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
		req.Header.Set(SecretHeader, secret)
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

	for _, path := range []string{settings, f.hooks.headerPath("term-aaaaa")} {
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
	header, err := os.ReadFile(f.hooks.headerPath("term-aaaaa"))
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.TrimPrefix(string(header), SecretHeader+": ")
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

	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup","cwd":"/proj","permission_mode":"default","effort":{"level":"high"}}`)
	if len(f.observer.sessions) != 1 {
		t.Fatalf("sessions = %+v", f.observer.sessions)
	}
	session := f.observer.sessions[0]
	if session.Agent != "claude" || session.ProviderID != "s1" || session.TerminalID != "term-aaaaa" ||
		session.ProjectID != "proj-aaaaa" || session.Status != api.ThreadIdle {
		t.Errorf("session observation = %+v", session)
	}
	if session.Metadata.Cwd != "/proj" || session.Metadata.PermissionMode != "default" || session.Metadata.Effort != "high" {
		t.Errorf("session metadata = %+v", session.Metadata)
	}

	// The first prompt supplies the title fallback and starts the turn.
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"fix the flaky build on CI so releases stop breaking every night"}`)
	status := f.observer.lastStatus(t)
	if status.Status != api.ThreadWorking {
		t.Errorf("prompt status = %s", status.Status)
	}
	if status.Metadata.Title == "" || len(status.Metadata.Title) > 50 {
		t.Errorf("title fallback = %q", status.Metadata.Title)
	}

	// /clear: SessionEnd(reason=clear) does not deactivate — the new
	// SessionStart moves the terminal itself.
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionEnd","reason":"clear"}`)
	if len(f.observer.inactive) != 0 {
		t.Errorf("clear deactivated the terminal: %v", f.observer.inactive)
	}
	f.post(t, secret, `{"session_id":"s2","hook_event_name":"SessionStart","source":"clear"}`)
	if len(f.observer.sessions) != 2 || f.observer.sessions[1].ProviderID != "s2" {
		t.Fatalf("sessions after clear = %+v", f.observer.sessions)
	}

	// Evidence for the departed session is now dropped.
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"Stop"}`); code != http.StatusBadRequest {
		t.Errorf("stale session evidence: got %d, want 400", code)
	}

	// TUI exit: SessionEnd with any other reason deactivates.
	f.post(t, secret, `{"session_id":"s2","hook_event_name":"SessionEnd","reason":"other"}`)
	if len(f.observer.inactive) != 1 || f.observer.inactive[0] != "term-aaaaa" {
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

// A transient failure recording the session must not silence it: the next
// event retries the session observation before its evidence.
func TestSessionObservationFailureRetries(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	// The terminal record is briefly unavailable at SessionStart.
	f.terminals.mu.Lock()
	saved := f.terminals.terminals["term-aaaaa"]
	delete(f.terminals.terminals, "term-aaaaa")
	f.terminals.mu.Unlock()
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`); code != http.StatusNoContent {
		t.Fatalf("SessionStart during outage: got %d", code)
	}
	if len(f.observer.sessions) != 0 {
		t.Fatalf("sessions during outage = %+v", f.observer.sessions)
	}

	// The outage heals; the next ordinary event re-establishes and lands.
	f.terminals.mu.Lock()
	f.terminals.terminals["term-aaaaa"] = saved
	f.terminals.mu.Unlock()
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","prompt":"go"}`); code != http.StatusNoContent {
		t.Fatalf("event after outage: got %d", code)
	}
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].ProviderID != "s1" {
		t.Errorf("session not re-established: %+v", f.observer.sessions)
	}
	if got := f.observer.lastStatus(t); got.Status != api.ThreadWorking {
		t.Errorf("evidence after retry = %s", got.Status)
	}
}

// After a server restart the registration has no session. Evidence for a
// conversation the identity mapping ties to the same terminal re-seeds —
// re-establishing the session first — and anything else is dropped.
func TestPostRestartSeeding(t *testing.T) {
	f := newHookFixture(t)
	f.prepare(t, "term-aaaaa")
	if err := f.hooks.LoadRegistrations(); err != nil {
		t.Fatal(err)
	}
	secret := f.prepare(t, "term-aaaaa") // fresh handle to the same terminal
	f.observer.identity["s1"] = "term-aaaaa"
	f.observer.identity["s9"] = "term-other"

	// Unmapped and wrong-terminal sessions are dropped.
	if code := f.post(t, secret, `{"session_id":"s404","hook_event_name":"Stop"}`); code != http.StatusBadRequest {
		t.Errorf("unmapped seed: got %d, want 400", code)
	}
	if code := f.post(t, secret, `{"session_id":"s9","hook_event_name":"Stop"}`); code != http.StatusBadRequest {
		t.Errorf("wrong-terminal seed: got %d, want 400", code)
	}

	// A mapped session for this terminal seeds: session re-established,
	// then the evidence lands.
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"Stop"}`); code != http.StatusNoContent {
		t.Errorf("mapped seed: got %d, want 204", code)
	}
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].ProviderID != "s1" || f.observer.sessions[0].Status != "" {
		t.Fatalf("seed session observation = %+v", f.observer.sessions)
	}
	if got := f.observer.lastStatus(t); got.Status != api.ThreadIdle {
		t.Errorf("seeded evidence status = %s", got.Status)
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

	if _, err := os.Stat(reloaded.headerPath("term-zzzzz")); !os.IsNotExist(err) {
		t.Error("stale hook files survived the reload")
	}
	if _, err := os.Stat(reloaded.headerPath("term-aaaaa")); err != nil {
		t.Error("live hook files were removed")
	}
	// The reloaded registry accepts the persisted secret.
	req := httptest.NewRequest(http.MethodPost, HooksPath,
		strings.NewReader(`{"session_id":"s1","hook_event_name":"SessionStart","source":"startup"}`))
	req.Header.Set(SecretHeader, secret)
	rec := httptest.NewRecorder()
	reloaded.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("reloaded secret: got %d, want 204", rec.Code)
	}
}

func TestCommandComposition(t *testing.T) {
	f := newHookFixture(t)
	entry := Entry(f.hooks)
	command, err := entry.TUI.Command(context.Background(), agents.LaunchContext{TerminalID: "term-aaaaa", Directory: "/proj"})
	if err != nil {
		t.Fatal(err)
	}
	if command != "claude --settings "+agents.Quote(f.hooks.settingsPath("term-aaaaa")) {
		t.Errorf("command = %q", command)
	}
	if _, err := os.Stat(f.hooks.settingsPath("term-aaaaa")); err != nil {
		t.Errorf("Command did not prepare the settings file: %v", err)
	}
}
