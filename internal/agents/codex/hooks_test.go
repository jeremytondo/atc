package codex

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/agents/hookauth"
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

func (f *fakeObserver) lastSession(t *testing.T) threads.SessionObservation {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		t.Fatal("no session observation recorded")
	}
	return f.sessions[len(f.sessions)-1]
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
	codexHome string
}

func newHookFixture(t *testing.T) *hookFixture {
	t.Helper()
	observer := &fakeObserver{identity: map[string]string{}}
	terminals := &fakeTerminals{terminals: map[string]api.Terminal{
		"term-aaaaa": {ID: "term-aaaaa", ProjectID: "proj-aaaaa", Agent: "codex", Status: api.TerminalRunning},
	}}
	dir, codexHome := t.TempDir(), t.TempDir()
	hooks, err := NewHooks(HooksOptions{
		Dir:       dir,
		BaseURL:   "http://127.0.0.1:4779",
		CodexHome: codexHome,
		Threads:   observer,
		Terminals: terminals,
	})
	if err != nil {
		t.Fatal(err)
	}
	return &hookFixture{hooks: hooks, observer: observer, terminals: terminals, dir: dir, codexHome: codexHome}
}

// prepare runs the launch composition and returns the minted secret.
func (f *hookFixture) prepare(t *testing.T, terminalID string) string {
	t.Helper()
	headerPath, err := f.hooks.Prepare(terminalID)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(headerPath)
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

func start(session, source string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SessionStart","source":%q,"cwd":"/proj","model":"gpt-5.6-luna","permission_mode":"default"}`, session, source)
}

func event(session, name, turn string) string {
	return fmt.Sprintf(`{"session_id":%q,"hook_event_name":%q,"turn_id":%q}`, session, name, turn)
}

func TestDeliverySecretEnforcement(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	if code := f.post(t, "", start("s1", "startup")); code != http.StatusNotFound {
		t.Errorf("no secret: got %d, want 404", code)
	}
	if code := f.post(t, "wrong", start("s1", "startup")); code != http.StatusNotFound {
		t.Errorf("wrong secret: got %d, want 404", code)
	}
	if code := f.post(t, secret, `{not json`); code != http.StatusBadRequest {
		t.Errorf("malformed payload: got %d, want 400", code)
	}
	if code := f.post(t, secret, start("s1", "startup")); code != http.StatusNoContent {
		t.Errorf("valid delivery: got %d, want 204", code)
	}

	// A relaunch mints a new secret; the old one stops validating.
	fresh := f.prepare(t, "term-aaaaa")
	if code := f.post(t, secret, start("s1", "startup")); code != http.StatusNotFound {
		t.Errorf("stale secret: got %d, want 404", code)
	}
	if code := f.post(t, fresh, start("s1", "startup")); code != http.StatusNoContent {
		t.Errorf("fresh secret: got %d, want 204", code)
	}
}

// The proven approval turn lands the full status arc, and the session
// observation carries identity, project, and payload metadata.
func TestFreshSessionAndApprovalTurn(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	f.post(t, secret, start("s1", "startup"))
	session := f.observer.lastSession(t)
	if session.Agent != "codex" || session.ProviderID != "s1" || session.TerminalID != "term-aaaaa" ||
		session.ProjectID != "proj-aaaaa" || session.Status != api.ThreadIdle {
		t.Errorf("session observation = %+v", session)
	}
	if session.Metadata.Cwd != "/proj" || session.Metadata.Model != "gpt-5.6-luna" || session.Metadata.PermissionMode != "default" {
		t.Errorf("session metadata = %+v", session.Metadata)
	}

	// The first prompt supplies the title fallback and starts the turn.
	f.post(t, secret, `{"session_id":"s1","hook_event_name":"UserPromptSubmit","turn_id":"t1","prompt":"retry that exact curl command with elevated permissions because sandbox DNS blocked it"}`)
	status := f.observer.lastStatus(t)
	if status.Status != api.ThreadWorking || status.Metadata.Title == "" || len(status.Metadata.Title) > 50 {
		t.Errorf("prompt status = %+v", status)
	}

	for _, step := range []struct {
		event string
		want  api.ThreadStatus
	}{
		{"PreToolUse", api.ThreadWorking},
		{"PermissionRequest", api.ThreadWaitingForPermission},
		{"PostToolUse", api.ThreadWorking},
		{"Stop", api.ThreadIdle},
	} {
		f.post(t, secret, event("s1", step.event, "t1"))
		if got := f.observer.lastStatus(t); got.Status != step.want {
			t.Errorf("%s status = %s, want %s", step.event, got.Status, step.want)
		}
	}
}

// Every path that changes the active thread moves it through
// SessionStart: /new and /fork announce a new id with source startup,
// /resume and shell resume restore one with source resume — all land the
// same way.
func TestSessionSwitches(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	f.post(t, secret, start("s1", "startup")) // fresh
	f.post(t, secret, start("s2", "startup")) // /new
	f.post(t, secret, start("s1", "resume"))  // /resume back
	f.post(t, secret, start("s3", "startup")) // /fork
	if len(f.observer.sessions) != 4 {
		t.Fatalf("sessions = %+v", f.observer.sessions)
	}
	for i, want := range []string{"s1", "s2", "s1", "s3"} {
		got := f.observer.sessions[i]
		if got.ProviderID != want || got.Status != api.ThreadIdle || got.TerminalID != "term-aaaaa" {
			t.Errorf("switch %d = %+v, want %s at idle", i, got, want)
		}
	}

	// Evidence for the displaced session still reduces (a late Stop is
	// honest history), but never re-establishes the session.
	before := len(f.observer.sessions)
	f.post(t, secret, event("s2", "Stop", "t9"))
	if len(f.observer.sessions) != before {
		t.Errorf("displaced evidence re-established a session: %+v", f.observer.sessions[before:])
	}
	if got := f.observer.lastStatus(t); got.ProviderID != "s2" || got.Status != api.ThreadIdle {
		t.Errorf("displaced Stop = %+v", got)
	}
}

// One TUI exit emits a SessionEnd for every session it touched, keyed by
// session_id. Only the active session's end deactivates the terminal, and
// an ended session's stragglers are dropped until its next SessionStart.
func TestSessionEndFanOut(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	f.post(t, secret, start("s1", "startup"))
	f.post(t, secret, start("s2", "startup"))
	f.post(t, secret, start("s3", "startup")) // active at exit

	// Ends arrive for all three; s3 is the active one.
	for _, session := range []string{"s1", "s2"} {
		if code := f.post(t, secret, fmt.Sprintf(`{"session_id":%q,"hook_event_name":"SessionEnd","reason":"other"}`, session)); code != http.StatusNoContent {
			t.Errorf("end %s: got %d", session, code)
		}
		if len(f.observer.inactive) != 0 {
			t.Errorf("non-active end %s deactivated the terminal", session)
		}
	}
	f.post(t, secret, `{"session_id":"s3","hook_event_name":"SessionEnd","reason":"other"}`)
	if len(f.observer.inactive) != 1 || f.observer.inactive[0] != "term-aaaaa" {
		t.Errorf("active end did not deactivate: %v", f.observer.inactive)
	}

	// Stragglers for ended sessions are dropped.
	before := len(f.observer.statuses)
	for _, session := range []string{"s1", "s2", "s3"} {
		if code := f.post(t, secret, event(session, "PostToolUse", "t1")); code != http.StatusBadRequest {
			t.Errorf("straggler for ended %s: got %d, want 400", session, code)
		}
	}
	if len(f.observer.statuses) != before {
		t.Errorf("straggler observed: %+v", f.observer.statuses[before:])
	}

	// A SessionStart reopens the conversation (shell resume relaunches
	// through a fresh terminal in production, but the same registration
	// must also survive an in-TUI reopen).
	if code := f.post(t, secret, start("s1", "resume")); code != http.StatusNoContent {
		t.Errorf("reopen after end: got %d", code)
	}
}

// The interrupt latch holds across the ingest path: idle at Interrupt,
// unchanged by the same turn's late PostToolUse.
func TestInterruptThroughIngest(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	f.post(t, secret, start("s1", "startup"))
	f.post(t, secret, event("s1", "UserPromptSubmit", "t1"))
	f.post(t, secret, event("s1", "PreToolUse", "t1"))
	f.post(t, secret, event("s1", "Interrupt", "t1"))
	if got := f.observer.lastStatus(t); got.Status != api.ThreadIdle {
		t.Fatalf("interrupt status = %s", got.Status)
	}
	before := len(f.observer.statuses)
	f.post(t, secret, event("s1", "PostToolUse", "t1"))
	if len(f.observer.statuses) != before {
		t.Errorf("latched straggler still observed: %+v", f.observer.lastStatus(t))
	}
}

// A transient failure recording the session must not silence it: the next
// event retries the session observation before its evidence.
func TestSessionObservationFailureRetries(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	f.terminals.mu.Lock()
	saved := f.terminals.terminals["term-aaaaa"]
	delete(f.terminals.terminals, "term-aaaaa")
	f.terminals.mu.Unlock()
	if code := f.post(t, secret, start("s1", "startup")); code != http.StatusNoContent {
		t.Fatalf("SessionStart during outage: got %d", code)
	}
	if len(f.observer.sessions) != 0 {
		t.Fatalf("sessions during outage = %+v", f.observer.sessions)
	}

	f.terminals.mu.Lock()
	f.terminals.terminals["term-aaaaa"] = saved
	f.terminals.mu.Unlock()
	if code := f.post(t, secret, event("s1", "UserPromptSubmit", "t1")); code != http.StatusNoContent {
		t.Fatalf("event after outage: got %d", code)
	}
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].ProviderID != "s1" || f.observer.sessions[0].Status != "" {
		t.Errorf("session not re-established: %+v", f.observer.sessions)
	}
	if got := f.observer.lastStatus(t); got.Status != api.ThreadWorking {
		t.Errorf("evidence after retry = %s", got.Status)
	}
}

// After a server restart the reloaded registration has no sessions. Only
// a UserPromptSubmit — the one event a session the TUI does not display
// can never produce — re-seeds, and only for the conversation the
// identity mapping ties to this same terminal; a displaced session's
// straggling tool events must not seed the wrong conversation as active.
func TestPostRestartSeeding(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")

	// A new server process reloads the persisted secret; the launched TUI
	// keeps delivering with it.
	restarted, err := NewHooks(HooksOptions{
		Dir: f.dir, BaseURL: "http://127.0.0.1:4779", CodexHome: f.codexHome,
		Threads: f.observer, Terminals: f.terminals,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.LoadRegistrations(); err != nil {
		t.Fatal(err)
	}
	f.hooks = restarted
	f.observer.identity["s1"] = "term-aaaaa"
	f.observer.identity["s9"] = "term-other"

	prompt := func(session string) string {
		return fmt.Sprintf(`{"session_id":%q,"hook_event_name":"UserPromptSubmit","turn_id":"t1","prompt":"go"}`, session)
	}
	if code := f.post(t, secret, prompt("s404")); code != http.StatusBadRequest {
		t.Errorf("unmapped seed: got %d, want 400", code)
	}
	if code := f.post(t, secret, prompt("s9")); code != http.StatusBadRequest {
		t.Errorf("wrong-terminal seed: got %d, want 400", code)
	}
	// A mapped conversation's tool straggler is not seed material — an
	// interrupted turn can outlive a session switch and a restart.
	if code := f.post(t, secret, event("s1", "PostToolUse", "t1")); code != http.StatusBadRequest {
		t.Errorf("tool-event seed: got %d, want 400", code)
	}

	if code := f.post(t, secret, prompt("s1")); code != http.StatusNoContent {
		t.Errorf("mapped prompt seed: got %d, want 204", code)
	}
	if len(f.observer.sessions) != 1 || f.observer.sessions[0].ProviderID != "s1" || f.observer.sessions[0].Status != "" {
		t.Fatalf("seed session observation = %+v", f.observer.sessions)
	}
	if got := f.observer.lastStatus(t); got.Status != api.ThreadWorking {
		t.Errorf("seeded evidence status = %s", got.Status)
	}

	// Once a session holds the terminal, evidence for a different
	// unknown conversation is a straggler, not a second seed.
	f.observer.identity["s2"] = "term-aaaaa"
	if code := f.post(t, secret, prompt("s2")); code != http.StatusBadRequest {
		t.Errorf("second seed: got %d, want 400", code)
	}
}

// A post-restart TUI exit ends conversations this process never saw:
// mapped ones are honored (and recorded as ended), unmapped ones dropped.
func TestPostRestartSessionEnd(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")
	f.observer.identity["s1"] = "term-aaaaa"

	if code := f.post(t, secret, `{"session_id":"s404","hook_event_name":"SessionEnd","reason":"other"}`); code != http.StatusBadRequest {
		t.Errorf("unmapped end: got %d, want 400", code)
	}
	if code := f.post(t, secret, `{"session_id":"s1","hook_event_name":"SessionEnd","reason":"other"}`); code != http.StatusNoContent {
		t.Errorf("mapped end: got %d, want 204", code)
	}
	// No session held the terminal, so nothing deactivates; the straggler
	// gate remembers the end.
	if len(f.observer.inactive) != 0 {
		t.Errorf("end without an active session deactivated: %v", f.observer.inactive)
	}
	if code := f.post(t, secret, event("s1", "Stop", "t1")); code != http.StatusBadRequest {
		t.Errorf("straggler after post-restart end: got %d, want 400", code)
	}
}

func TestLoadRegistrationsCleansStaleFiles(t *testing.T) {
	f := newHookFixture(t)
	secret := f.prepare(t, "term-aaaaa")
	// A leftover for a terminal that no longer exists (deleted, or an
	// abandoned launch candidate).
	f.prepare(t, "term-zzzzz")

	reloaded, err := NewHooks(HooksOptions{
		Dir: f.dir, BaseURL: "http://127.0.0.1:4779", CodexHome: f.codexHome,
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
	req := httptest.NewRequest(http.MethodPost, HooksPath, strings.NewReader(start("s1", "startup")))
	req.Header.Set(hookauth.SecretHeader, secret)
	rec := httptest.NewRecorder()
	reloaded.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNoContent {
		t.Errorf("reloaded secret: got %d, want 204", rec.Code)
	}
}

// The launch is vanilla: profile selection and per-launch environment,
// no --remote, no --cd, and the profile file is brought current.
func TestCommandComposition(t *testing.T) {
	f := newHookFixture(t)
	entry := Entry(f.hooks)
	command, err := entry.TUI.Command(context.Background(), agents.LaunchContext{TerminalID: "term-aaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	want := "CODEX_HOME=" + agents.Quote(f.codexHome) +
		" " + envURL + "='http://127.0.0.1:4779" + HooksPath + "'" +
		" " + envHeader + "=" + agents.Quote(f.hooks.registry.HeaderPath("term-aaaaa")) +
		" codex -p " + profileName
	if command != want {
		t.Errorf("command = %q, want %q", command, want)
	}
	if _, err := os.Stat(profilePath(f.codexHome)); err != nil {
		t.Errorf("Command did not ensure the profile: %v", err)
	}
	// The resume form runs the resume subcommand under the same profile
	// and environment, with the exact session id quoted.
	command, err = entry.TUI.Command(context.Background(), agents.LaunchContext{TerminalID: "term-bbbbb", ResumeConversationID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	want = "CODEX_HOME=" + agents.Quote(f.codexHome) +
		" " + envURL + "='http://127.0.0.1:4779" + HooksPath + "'" +
		" " + envHeader + "=" + agents.Quote(f.hooks.registry.HeaderPath("term-bbbbb")) +
		" codex resume -p " + profileName + " 'sess-1'"
	if command != want {
		t.Errorf("resume command = %q, want %q", command, want)
	}
	// A foreign file at the profile path refuses the launch.
	if err := os.WriteFile(profilePath(f.codexHome), []byte("# someone else's\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := entry.TUI.Command(context.Background(), agents.LaunchContext{TerminalID: "term-aaaaa"}); err == nil {
		t.Error("foreign profile did not refuse the launch")
	}
}
