package server

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"

	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/agents/claude"
	"github.com/jeremytondo/atc/internal/agents/codex"
	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/terminals/exitmarker"
	"github.com/jeremytondo/atc/internal/threads"
)

// fakeAdapter is the hand-written session backend for API-contract tests:
// Create births a reachable session unless a hook says otherwise, Kill
// removes it unless failing is set.
type fakeAdapter struct {
	mu       sync.Mutex
	sessions map[string]bool
	invErr   error
	killErr  error
	onCreate func(id string, spec terminals.CreateSpec) error
}

func (a *fakeAdapter) Inventory(context.Context) ([]terminals.Session, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.invErr != nil {
		return nil, a.invErr
	}
	sessions := make([]terminals.Session, 0, len(a.sessions))
	for name, reachable := range a.sessions {
		sessions = append(sessions, terminals.Session{Name: name, Reachable: reachable})
	}
	return sessions, nil
}

func (a *fakeAdapter) Create(_ context.Context, id string, spec terminals.CreateSpec) error {
	a.mu.Lock()
	hook := a.onCreate
	a.mu.Unlock()
	if hook != nil {
		if err := hook(id, spec); err != nil {
			return err
		}
	}
	a.mu.Lock()
	a.sessions[id] = true
	a.mu.Unlock()
	return nil
}

func (a *fakeAdapter) Kill(_ context.Context, id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.killErr != nil {
		return a.killErr
	}
	delete(a.sessions, id)
	return nil
}

// fixture is a full chassis over fakes: real store in a temp dir, real
// hub, fake adapter, fake clock — the server's existing test seam. One
// project rooted at a real temp directory exists for terminals to belong
// to; the agent catalog is the shipped one (claude, codex), probing
// binaries against the fixture map (claude available by default), never
// the developer machine's PATH.
type fixture struct {
	handler    http.Handler
	adapter    *fakeAdapter
	hub        *events.Hub
	service    *terminals.Service
	threads    *threads.Service
	binaries   map[string]bool
	markers    string
	projectID  string
	projectDir string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	adapter := &fakeAdapter{sessions: map[string]bool{}}
	// A small ring so overflow is testable, pinned to sequence 1 so the
	// SSE assertions are deterministic (production bases are random).
	hub := events.NewHubAt(8, 1)
	var clock struct {
		sync.Mutex
		now time.Time
	}
	clock.now = time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC)
	markers := t.TempDir()
	now := func() time.Time {
		clock.Lock()
		defer clock.Unlock()
		clock.now = clock.now.Add(time.Millisecond)
		return clock.now
	}
	service := terminals.NewService(terminals.Options{
		Repository: db.Terminals(),
		Adapter:    adapter,
		Projects:   db.Projects(),
		MarkerDir:  markers,
		Hub:        hub,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        now,
	})
	projectService := projects.NewService(projects.Options{
		Repository: db.Projects(),
		Terminals:  db.Terminals(),
		Hub:        hub,
		Now:        now,
	})
	threadService := threads.NewService(threads.Options{
		Repository: db.Threads(),
		Terminals:  service,
		Hub:        hub,
		Now:        now,
	})
	if err := threadService.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	claudeHooks, err := claude.NewHooks(claude.HooksOptions{
		Dir:       t.TempDir(),
		BaseURL:   "http://127.0.0.1:0",
		Threads:   threadService,
		Terminals: service,
		Now:       now,
	})
	if err != nil {
		t.Fatal(err)
	}
	// A codex observer over a private temp CODEX_HOME: constructed for
	// registration only — codex stays unavailable in the fixture, so no
	// launch ever reaches the supervisor.
	codexObserver := codex.NewObserver(codex.ObserverOptions{
		Supervisor: codex.NewSupervisor(codex.SupervisorOptions{
			CodexHome:    t.TempDir(),
			IdentityFile: filepath.Join(t.TempDir(), "codex-app-server.json"),
			LogFile:      filepath.Join(t.TempDir(), "codex-app-server.log"),
			SpawnDir:     t.TempDir(),
		}),
		Threads:   threadService,
		Terminals: service,
		Now:       now,
	})
	binaries := map[string]bool{"claude": true}
	agentService, err := agents.NewService(agents.Options{
		Entries:   []agents.Entry{claude.Entry(claudeHooks), codex.Entry(codexObserver)},
		Terminals: service,
		LookPath: func(name string) (string, error) {
			if binaries[name] {
				return "/bin/" + name, nil
			}
			return "", errors.New("executable file not found in $PATH")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{
		Verify:            testVerify,
		Version:           testVersion,
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
		Terminals:         service,
		Projects:          projectService,
		Agents:            agentService,
		Threads:           threadService,
		Events:            hub,
		InternalRoutes:    map[string]http.Handler{"POST " + claude.HooksPath: claudeHooks.Handler()},
		HeartbeatInterval: 50 * time.Millisecond,
	})
	f := &fixture{handler: handler, adapter: adapter, hub: hub, service: service, threads: threadService,
		binaries: binaries, markers: markers}
	// Planted through the repository, not the API: the fixture project must
	// not consume an event sequence number the SSE assertions rely on.
	f.projectDir = t.TempDir()
	f.projectID = "proj-fixtr"
	if ok, err := db.Projects().Insert(context.Background(), store.ProjectRecord{
		ID: f.projectID, Name: "fixture", Directory: f.projectDir,
		CreatedAt: now(), UpdatedAt: now(),
	}); err != nil || !ok {
		t.Fatalf("planting fixture project = %v, %v", ok, err)
	}
	return f
}

// createProject registers a project over the wire and returns it.
func (f *fixture) createProject(t *testing.T, dir string) api.Project {
	t.Helper()
	body, err := json.Marshal(api.ProjectCreateParams{Directory: dir})
	if err != nil {
		t.Fatal(err)
	}
	rec := f.request(t, http.MethodPost, "/v1/projects", string(body))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create project: got %d; body %s", rec.Code, rec.Body)
	}
	var project api.Project
	if err := json.Unmarshal(rec.Body.Bytes(), &project); err != nil {
		t.Fatal(err)
	}
	return project
}

// createTerminalBody is a create request body against the fixture project.
func (f *fixture) createTerminalBody(t *testing.T, params api.TerminalCreateParams) string {
	t.Helper()
	if params.ProjectID == "" {
		params.ProjectID = f.projectID
	}
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// request drives the handler with the test token and returns the recorder.
func (f *fixture) request(t *testing.T, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, req)
	return rec
}

func decodeTerminal(t *testing.T, rec *httptest.ResponseRecorder) api.Terminal {
	t.Helper()
	var terminal api.Terminal
	if err := json.Unmarshal(rec.Body.Bytes(), &terminal); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return terminal
}

func TestTerminalCRUDOverTheWire(t *testing.T) {
	f := newFixture(t)

	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Command: "hx"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: got %d, want 201; body %s", rec.Code, rec.Body)
	}
	created := decodeTerminal(t, rec)
	if created.Status != api.TerminalRunning || created.Name != "hx" ||
		created.ProjectID != f.projectID || created.Directory != f.projectDir {
		t.Fatalf("created = %+v", created)
	}

	rec = f.request(t, http.MethodGet, "/v1/terminals/"+created.ID, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get: got %d", rec.Code)
	}
	if diff := cmp.Diff(created, decodeTerminal(t, rec)); diff != "" {
		t.Errorf("get (-created +got):\n%s", diff)
	}

	rec = f.request(t, http.MethodGet, "/v1/terminals", "")
	var list api.TerminalList
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Terminals) != 1 || list.Terminals[0].ID != created.ID {
		t.Errorf("list = %+v", list)
	}

	rec = f.request(t, http.MethodPatch, "/v1/terminals/"+created.ID, `{"name":"build watcher"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("update: got %d; body %s", rec.Code, rec.Body)
	}
	if got := decodeTerminal(t, rec); got.Name != "build watcher" {
		t.Errorf("updated name = %q", got.Name)
	}

	rec = f.request(t, http.MethodDelete, "/v1/terminals/"+created.ID, "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete: got %d", rec.Code)
	}
	if rec := f.request(t, http.MethodGet, "/v1/terminals/"+created.ID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("get after delete: got %d, want 404", rec.Code)
	}
}

// Update accepts only name: unknown and immutable fields are rejected by
// schema, so the contract cannot silently widen.
func TestUpdateRejectsUnknownAndImmutableFields(t *testing.T) {
	f := newFixture(t)
	created := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{})))
	for name, body := range map[string]string{
		"immutable directory": `{"name":"x","directory":"/elsewhere"}`,
		"immutable command":   `{"command":"vim"}`,
		"immutable agent":     `{"name":"x","agent":"claude"}`,
		"unknown field":       `{"name":"x","frobnicate":true}`,
		"missing name":        `{}`,
	} {
		rec := f.request(t, http.MethodPatch, "/v1/terminals/"+created.ID, body)
		if rec.Code != http.StatusUnprocessableEntity {
			t.Errorf("%s: got %d, want 422; body %s", name, rec.Code, rec.Body)
		}
	}
}

func TestCreateWithFailingCommand(t *testing.T) {
	f := newFixture(t)
	f.adapter.onCreate = func(id string, _ terminals.CreateSpec) error {
		writeExitMarker(t, f.markers, id, 127)
		return errors.New("client exited before the session settled")
	}
	rec := f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{Command: "no-such-tool"}))
	if rec.Code != http.StatusCreated {
		t.Fatalf("got %d; body %s", rec.Code, rec.Body)
	}
	terminal := decodeTerminal(t, rec)
	if terminal.Status != api.TerminalExited || terminal.ExitCode == nil || *terminal.ExitCode != 127 {
		t.Errorf("terminal = %+v, want exited with code 127", terminal)
	}
}

func TestDeleteUnderUnreachableBackend(t *testing.T) {
	f := newFixture(t)
	created := decodeTerminal(t, f.request(t, http.MethodPost, "/v1/terminals", f.createTerminalBody(t, api.TerminalCreateParams{})))
	f.adapter.mu.Lock()
	f.adapter.killErr = errors.New("session still present after inventory passes")
	f.adapter.invErr = errors.New("zmx down")
	f.adapter.mu.Unlock()

	if rec := f.request(t, http.MethodDelete, "/v1/terminals/"+created.ID, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete under unreachable backend: got %d", rec.Code)
	}
	if rec := f.request(t, http.MethodGet, "/v1/terminals", ""); !strings.Contains(rec.Body.String(), `"terminals":[]`) {
		t.Errorf("list after delete = %s, want empty", rec.Body)
	}
}

func writeExitMarker(t *testing.T, dir, id string, code int) {
	t.Helper()
	now := time.Now().UTC()
	err := exitmarker.Write(exitmarker.Path(dir, id), exitmarker.Marker{
		TerminalID: id, StartedAt: now.Add(-time.Second), ExitedAt: &now, Code: &code,
	})
	if err != nil {
		t.Fatal(err)
	}
}

// sseClient reads one SSE stream over a real HTTP connection (flushing
// needs the network path, not a recorder).
type sseClient struct {
	resp    *http.Response
	scanner *bufio.Scanner
}

type sseEvent struct {
	ID      string
	Event   string
	Data    string
	Comment string
}

func dialSSE(t *testing.T, baseURL, lastEventID string) *sseClient {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)
	if lastEventID != "" {
		req.Header.Set("Last-Event-ID", lastEventID)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		t.Fatalf("events: got %d; body %s", resp.StatusCode, body)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	return &sseClient{resp: resp, scanner: bufio.NewScanner(resp.Body)}
}

// next returns the next SSE message (event or comment-only block).
func (c *sseClient) next(t *testing.T) sseEvent {
	t.Helper()
	var event sseEvent
	seen := false
	for c.scanner.Scan() {
		line := c.scanner.Text()
		switch {
		case line == "":
			if seen {
				return event
			}
		case strings.HasPrefix(line, ": "):
			event.Comment += strings.TrimPrefix(line, ": ")
			seen = true
		case strings.HasPrefix(line, "id: "):
			event.ID = strings.TrimPrefix(line, "id: ")
			seen = true
		case strings.HasPrefix(line, "event: "):
			event.Event = strings.TrimPrefix(line, "event: ")
			seen = true
		case strings.HasPrefix(line, "data: "):
			event.Data += strings.TrimPrefix(line, "data: ")
			seen = true
		}
	}
	t.Fatalf("stream ended before a full message: %v", c.scanner.Err())
	return event
}

func TestSSEStreamLiveEventsAndHeartbeat(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)

	client := dialSSE(t, srv.URL, "")
	if opening := client.next(t); opening.Comment != "connected" {
		t.Fatalf("opening message = %+v, want the connected comment", opening)
	}

	f.hub.Publish(api.EventTerminalCreated, "terminal", "term-x7k2f")
	event := client.next(t)
	if event.Event != api.EventTerminalCreated || event.ID != "1" {
		t.Fatalf("event = %+v", event)
	}
	var change api.ChangeEvent
	if err := json.Unmarshal([]byte(event.Data), &change); err != nil {
		t.Fatal(err)
	}
	want := api.ChangeEvent{Seq: 1, Resource: "terminal", ID: "term-x7k2f"}
	if diff := cmp.Diff(want, change); diff != "" {
		t.Errorf("change (-want +got):\n%s", diff)
	}

	// With nothing published, the next message is a heartbeat comment.
	if hb := client.next(t); hb.Comment != "heartbeat" {
		t.Errorf("expected heartbeat, got %+v", hb)
	}
}

func TestSSELastEventIDCatchUp(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)

	for i := 1; i <= 3; i++ {
		f.hub.Publish(api.EventTerminalUpdated, "terminal", fmt.Sprintf("term-%05d", i))
	}
	client := dialSSE(t, srv.URL, "1")
	if opening := client.next(t); opening.Comment != "connected" {
		t.Fatalf("opening = %+v", opening)
	}
	if event := client.next(t); event.ID != "2" || event.Event != api.EventTerminalUpdated {
		t.Errorf("first replayed = %+v, want seq 2", event)
	}
	if event := client.next(t); event.ID != "3" {
		t.Errorf("second replayed = %+v, want seq 3", event)
	}
}

func TestSSEResyncAfterBacklogOverflow(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)

	// The fixture ring holds 8; 20 events push cursor 1 off the backlog.
	for i := 1; i <= 20; i++ {
		f.hub.Publish(api.EventTerminalUpdated, "terminal", "term-x7k2f")
	}
	client := dialSSE(t, srv.URL, "1")
	if opening := client.next(t); opening.Comment != "connected" {
		t.Fatalf("opening = %+v", opening)
	}
	event := client.next(t)
	if event.Event != api.EventResync {
		t.Fatalf("expected resync, got %+v", event)
	}
	var resync api.ResyncEvent
	if err := json.Unmarshal([]byte(event.Data), &resync); err != nil {
		t.Fatal(err)
	}
	if resync.Seq != 20 {
		t.Errorf("resync head = %d, want 20", resync.Seq)
	}
	// The stream then resumes live from the head.
	f.hub.Publish(api.EventTerminalDeleted, "terminal", "term-x7k2f")
	if event := client.next(t); event.Event != api.EventTerminalDeleted || event.ID != "21" {
		t.Errorf("post-resync event = %+v", event)
	}
}

// A client that cannot keep up is disconnected — its stream ends — and
// catches up over a fresh connection instead of back-pressuring the hub.
func TestSSESlowClientIsDisconnected(t *testing.T) {
	f := newFixture(t)
	srv := httptest.NewServer(f.handler)
	t.Cleanup(srv.Close)

	client := dialSSE(t, srv.URL, "")
	if opening := client.next(t); opening.Comment != "connected" {
		t.Fatalf("opening = %+v", opening)
	}
	// Without the client reading, the handler blocks on the socket once
	// kernel buffers fill and the subscriber's fixed channel overflows;
	// far more bytes than any loopback buffer guarantees the overflow.
	for range 100_000 {
		f.hub.Publish(api.EventTerminalUpdated, "terminal", "term-x7k2f")
	}
	done := make(chan struct{})
	go func() {
		for client.scanner.Scan() {
		}
		close(done)
	}()
	select {
	case <-done: // stream ended: disconnected, as designed
	case <-time.After(10 * time.Second):
		t.Fatal("slow client was never disconnected")
	}
}

func TestEventsEndpointRequiresToken(t *testing.T) {
	f := newFixture(t)
	// The authorized path is covered above; only the tokenless rejection
	// can use a recorder (an authorized SSE stream never returns).
	req := httptest.NewRequest(http.MethodGet, "/v1/events", nil)
	unauth := httptest.NewRecorder()
	f.handler.ServeHTTP(unauth, req)
	if unauth.Code != http.StatusUnauthorized {
		t.Errorf("events without token: got %d, want 401", unauth.Code)
	}
}
