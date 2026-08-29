package codex

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// fakeAppServer speaks the app-server's wire protocol over a real unix
// WebSocket: it answers initialize and thread/read, records requests, and
// broadcasts whatever a test pushes.
type fakeAppServer struct {
	t        *testing.T
	socket   string
	server   *http.Server
	listener net.Listener

	mu       sync.Mutex
	conns    []*websocket.Conn
	requests []string
	previews map[string]string
	loaded   []string
}

func newFakeAppServer(t *testing.T) *fakeAppServer {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "app-server-control.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	f := &fakeAppServer{t: t, socket: socket, listener: listener, previews: map[string]string{}}
	f.server = &http.Server{Handler: http.HandlerFunc(f.handle)}
	go func() { _ = f.server.Serve(listener) }()
	t.Cleanup(func() { _ = f.server.Close() })
	return f
}

func (f *fakeAppServer) handle(w http.ResponseWriter, r *http.Request) {
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conns = append(f.conns, ws)
	f.mu.Unlock()
	ctx := context.Background()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var message rpcMessage
		if err := json.Unmarshal(data, &message); err != nil {
			return
		}
		f.mu.Lock()
		f.requests = append(f.requests, message.Method)
		previews := f.previews
		f.mu.Unlock()
		if message.ID == nil {
			continue // notification (initialized)
		}
		var result any = map[string]any{}
		if message.Method == "thread/loaded/list" {
			f.mu.Lock()
			result = map[string]any{"data": append([]string(nil), f.loaded...)}
			f.mu.Unlock()
		}
		if message.Method == "thread/read" {
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(message.Params, &params)
			result = map[string]any{"thread": map[string]any{"id": params.ThreadID, "preview": previews[params.ThreadID]}}
		}
		reply, _ := json.Marshal(map[string]any{"id": *message.ID, "result": result})
		_ = ws.Write(ctx, websocket.MessageText, reply)
	}
}

// broadcast pushes one notification to every connection.
func (f *fakeAppServer) broadcast(method string, params any) {
	data, err := json.Marshal(map[string]any{"method": method, "params": params})
	if err != nil {
		f.t.Fatal(err)
	}
	f.mu.Lock()
	conns := append([]*websocket.Conn(nil), f.conns...)
	f.mu.Unlock()
	for _, ws := range conns {
		_ = ws.Write(context.Background(), websocket.MessageText, data)
	}
}

func (f *fakeAppServer) closeConns() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ws := range f.conns {
		_ = ws.CloseNow()
	}
	f.conns = nil
}

// fakeThreads records observations; fakeTerminalDir serves terminals.
type fakeThreads struct {
	mu       sync.Mutex
	sessions []threads.SessionObservation
	statuses []threads.StatusObservation
}

func (f *fakeThreads) ObserveSession(_ context.Context, o threads.SessionObservation) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions = append(f.sessions, o)
	return "thrd-aaaaa", nil
}

func (f *fakeThreads) ObserveStatus(_ context.Context, o threads.StatusObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, o)
	return nil
}

// LookupIdentity mirrors the real mapping: a conversation is known only
// once a session observation recorded it, bound to that observation's
// terminal. Tests pre-seed f.sessions to fabricate restart state.
func (f *fakeThreads) LookupIdentity(_, providerID string) (string, string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, session := range f.sessions {
		if session.ProviderID == providerID {
			return "thrd-aaaaa", session.TerminalID, true
		}
	}
	return "", "", false
}

func (f *fakeThreads) wait(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		done := check()
		f.mu.Unlock()
		if done {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

type fakeTerminalDir struct {
	mu        sync.Mutex
	terminals map[string]api.Terminal
}

func (f *fakeTerminalDir) Get(id string) (api.Terminal, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	terminal, ok := f.terminals[id]
	if !ok {
		return api.Terminal{}, errors.New("terminal not found")
	}
	return terminal, nil
}

func (f *fakeTerminalDir) List(string) []api.Terminal {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := make([]api.Terminal, 0, len(f.terminals))
	for _, terminal := range f.terminals {
		list = append(list, terminal)
	}
	return list
}

type observerFixture struct {
	observer  *Observer
	appServer *fakeAppServer
	threads   *fakeThreads
	terminals *fakeTerminalDir
}

func newObserverFixture(t *testing.T) *observerFixture {
	t.Helper()
	appServer := newFakeAppServer(t)
	threadsSeam := &fakeThreads{}
	terminals := &fakeTerminalDir{terminals: map[string]api.Terminal{
		"term-aaaaa": {ID: "term-aaaaa", ProjectID: "proj-aaaaa", Agent: "codex", Directory: "/proj-a", Status: api.TerminalRunning},
	}}
	observer := NewObserver(ObserverOptions{
		Supervisor: NewSupervisor(SupervisorOptions{
			CodexHome:    t.TempDir(),
			IdentityFile: filepath.Join(t.TempDir(), "id.json"),
			LogFile:      filepath.Join(t.TempDir(), "log"),
			SpawnDir:     t.TempDir(),
		}),
		Threads:   threadsSeam,
		Terminals: terminals,
	})
	if err := observer.connect(context.Background(), appServer.socket); err != nil {
		t.Fatal(err)
	}
	return &observerFixture{observer: observer, appServer: appServer, threads: threadsSeam, terminals: terminals}
}

func TestObserverCapturesArmedLaunch(t *testing.T) {
	f := newObserverFixture(t)
	f.observer.mu.Lock()
	f.observer.captures = append(f.observer.captures, capture{terminalID: "term-aaaaa", cwd: "/proj-a", armedAt: time.Now()})
	f.observer.mu.Unlock()
	f.appServer.mu.Lock()
	f.appServer.previews["conv-1"] = "please fix the flaky build"
	f.appServer.mu.Unlock()

	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{"id": "conv-1", "cwd": "/proj-a"}})
	f.threads.wait(t, func() bool { return len(f.threads.sessions) == 1 })

	session := f.threads.sessions[0]
	if session.Agent != "codex" || session.ProviderID != "conv-1" || session.TerminalID != "term-aaaaa" ||
		session.ProjectID != "proj-aaaaa" || session.Metadata.Cwd != "/proj-a" {
		t.Errorf("session = %+v", session)
	}
	if session.Metadata.Title != "please fix the flaky build" {
		t.Errorf("title from preview = %q", session.Metadata.Title)
	}
	// The capture is consumed: a second broadcast falls back to the
	// single-candidate rule (still term-aaaaa here).
	f.observer.mu.Lock()
	remaining := len(f.observer.captures)
	f.observer.mu.Unlock()
	if remaining != 0 {
		t.Errorf("capture not consumed: %d armed", remaining)
	}
}

// A subagent's thread/started (string parentThreadId) never binds a
// terminal.
func TestObserverIgnoresSubagentBroadcasts(t *testing.T) {
	f := newObserverFixture(t)
	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{
		"id": "conv-child", "cwd": "/proj-a", "parentThreadId": "conv-parent",
	}})
	// Follow with a capturable broadcast so there is a sync point: the
	// dispatch queue is ordered, so the subagent one was processed first.
	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{"id": "conv-sync", "cwd": "/proj-a"}})
	f.threads.wait(t, func() bool { return len(f.threads.sessions) == 1 })
	if f.threads.sessions[0].ProviderID != "conv-sync" {
		t.Errorf("subagent broadcast captured: %+v", f.threads.sessions)
	}
}

// An in-TUI switch (/new): no armed capture, one running codex terminal
// at the cwd — attributed; ambiguity drops it.
func TestObserverUnarmedAttribution(t *testing.T) {
	f := newObserverFixture(t)
	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{"id": "conv-2", "cwd": "/proj-a"}})
	f.threads.wait(t, func() bool { return len(f.threads.sessions) == 1 })
	if f.threads.sessions[0].TerminalID != "term-aaaaa" {
		t.Errorf("session = %+v", f.threads.sessions[0])
	}

	// A second running codex terminal at the same directory makes the next
	// broadcast ambiguous: dropped, never guessed.
	f.terminals.mu.Lock()
	f.terminals.terminals["term-bbbbb"] = api.Terminal{
		ID: "term-bbbbb", ProjectID: "proj-aaaaa", Agent: "codex", Directory: "/proj-a", Status: api.TerminalRunning,
	}
	f.terminals.mu.Unlock()
	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{"id": "conv-3", "cwd": "/proj-a"}})
	// Sync on a status for the already-mapped conv-2: ordered dispatch
	// means conv-3 was decided first.
	f.appServer.broadcast("thread/status/changed", map[string]any{"threadId": "conv-2", "status": map[string]any{"type": "idle"}})
	f.threads.wait(t, func() bool { return len(f.threads.statuses) == 1 })
	if len(f.threads.sessions) != 1 {
		t.Errorf("ambiguous broadcast attributed: %+v", f.threads.sessions)
	}
}

func TestObserverStatusMapping(t *testing.T) {
	f := newObserverFixture(t)
	// Map the conversation first: status evidence flows only for known
	// identities.
	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{"id": "conv-1", "cwd": "/proj-a"}})
	f.threads.wait(t, func() bool { return len(f.threads.sessions) == 1 })
	cases := []struct {
		status any
		want   api.ThreadStatus
	}{
		{map[string]any{"type": "idle"}, api.ThreadIdle},
		{map[string]any{"type": "active", "activeFlags": []string{}}, api.ThreadWorking},
		{map[string]any{"type": "active", "activeFlags": []string{"waitingOnUserInput"}}, api.ThreadWaitingForInput},
		{map[string]any{"type": "active", "activeFlags": []string{"waitingOnApproval"}}, api.ThreadWaitingForPermission},
		{map[string]any{"type": "somethingNew"}, api.ThreadUnknown},
	}
	for i, c := range cases {
		f.appServer.broadcast("thread/status/changed", map[string]any{"threadId": "conv-1", "status": c.status})
		want := i + 1
		f.threads.wait(t, func() bool { return len(f.threads.statuses) == want })
		if got := f.threads.statuses[i]; got.Status != c.want || got.ProviderID != "conv-1" {
			t.Errorf("case %d: %+v; want %s", i, got, c.want)
		}
	}
}

// A lost connection coerces everything this connection observed to
// unknown — honest ignorance rather than a stale busy.
func TestObserverTeardownEmitsUnknown(t *testing.T) {
	f := newObserverFixture(t)
	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{"id": "conv-1", "cwd": "/proj-a"}})
	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{"id": "conv-idle", "cwd": "/proj-a"}})
	f.threads.wait(t, func() bool { return len(f.threads.sessions) == 2 })
	f.appServer.broadcast("thread/status/changed", map[string]any{"threadId": "conv-1", "status": map[string]any{"type": "active", "activeFlags": []string{}}})
	f.appServer.broadcast("thread/status/changed", map[string]any{"threadId": "conv-idle", "status": map[string]any{"type": "idle"}})
	f.threads.wait(t, func() bool { return len(f.threads.statuses) == 2 })

	f.appServer.closeConns()
	// Only the live thread coerces; the idle one persists as idle — the
	// same rule the threads domain applies to inactive threads.
	f.threads.wait(t, func() bool { return len(f.threads.statuses) == 3 })
	last := f.threads.statuses[2]
	if last.ProviderID != "conv-1" || last.Status != api.ThreadUnknown {
		t.Errorf("teardown observation = %+v; want unknown for conv-1 only", last)
	}
	f.observer.mu.Lock()
	conn := f.observer.conn
	f.observer.mu.Unlock()
	if conn != nil {
		t.Error("connection still installed after teardown")
	}
}

// A fresh connection reconciles the server's loaded threads: boot
// re-adoption returns live work to observation without waiting for the
// next broadcast.
func TestObserverReconcilesLoadedThreadsOnConnect(t *testing.T) {
	appServer := newFakeAppServer(t)
	appServer.mu.Lock()
	appServer.loaded = []string{"conv-9"}
	appServer.previews["conv-9"] = "resume the migration work"
	appServer.mu.Unlock()
	threadsSeam := &fakeThreads{}
	terminals := &fakeTerminalDir{terminals: map[string]api.Terminal{
		"term-aaaaa": {ID: "term-aaaaa", ProjectID: "proj-aaaaa", Agent: "codex", Directory: "", Status: api.TerminalRunning},
	}}
	observer := NewObserver(ObserverOptions{
		Supervisor: NewSupervisor(SupervisorOptions{
			CodexHome:    t.TempDir(),
			IdentityFile: filepath.Join(t.TempDir(), "id.json"),
			LogFile:      filepath.Join(t.TempDir(), "log"),
			SpawnDir:     t.TempDir(),
		}),
		Threads:   threadsSeam,
		Terminals: terminals,
	})
	// The fake's thread/read reports no cwd, matching the terminal whose
	// Directory is empty — the single-candidate rule still decides.
	if err := observer.connect(context.Background(), appServer.socket); err != nil {
		t.Fatal(err)
	}
	threadsSeam.wait(t, func() bool { return len(threadsSeam.sessions) == 1 })
	session := threadsSeam.sessions[0]
	if session.ProviderID != "conv-9" || session.TerminalID != "term-aaaaa" {
		t.Errorf("reconciled session = %+v", session)
	}
	if session.Metadata.Title != "resume the migration work" {
		t.Errorf("reconciled title = %q", session.Metadata.Title)
	}
}

// Status broadcasts for conversations the identity mapping does not know
// (other clients on the shared server) never reach the threads seam.
func TestObserverDropsForeignStatusBroadcasts(t *testing.T) {
	f := newObserverFixture(t)
	f.appServer.broadcast("thread/started", map[string]any{"thread": map[string]any{"id": "conv-1", "cwd": "/proj-a"}})
	f.threads.wait(t, func() bool { return len(f.threads.sessions) == 1 })
	f.appServer.broadcast("thread/status/changed", map[string]any{"threadId": "foreign-1", "status": map[string]any{"type": "active", "activeFlags": []string{}}})
	f.appServer.broadcast("thread/status/changed", map[string]any{"threadId": "conv-1", "status": map[string]any{"type": "systemError"}})
	f.threads.wait(t, func() bool { return len(f.threads.statuses) == 1 })
	got := f.threads.statuses[0]
	if got.ProviderID != "conv-1" || got.Status != api.ThreadError {
		t.Errorf("status = %+v; want conv-1 error (foreign dropped, systemError mapped)", got)
	}
}

// A loaded root the identity mapping already knows goes straight back to
// its recorded terminal — the restart seed check — even when cwd alone
// could not disambiguate.
func TestObserverReconcileReattachesMapped(t *testing.T) {
	appServer := newFakeAppServer(t)
	appServer.mu.Lock()
	appServer.loaded = []string{"conv-a", "conv-b"}
	appServer.mu.Unlock()
	threadsSeam := &fakeThreads{}
	// Two running codex terminals in one directory: cwd attribution alone
	// would drop both roots as ambiguous.
	terminals := &fakeTerminalDir{terminals: map[string]api.Terminal{
		"term-aaaaa": {ID: "term-aaaaa", ProjectID: "proj-aaaaa", Agent: "codex", Directory: "", Status: api.TerminalRunning},
		"term-bbbbb": {ID: "term-bbbbb", ProjectID: "proj-aaaaa", Agent: "codex", Directory: "", Status: api.TerminalRunning},
	}}
	// The persisted mapping from before the restart: conv-a was held by
	// term-aaaaa, conv-b by term-bbbbb.
	threadsSeam.sessions = []threads.SessionObservation{
		{Agent: "codex", ProviderID: "conv-a", TerminalID: "term-aaaaa"},
		{Agent: "codex", ProviderID: "conv-b", TerminalID: "term-bbbbb"},
	}
	observer := NewObserver(ObserverOptions{
		Supervisor: NewSupervisor(SupervisorOptions{
			CodexHome:    t.TempDir(),
			IdentityFile: filepath.Join(t.TempDir(), "id.json"),
			LogFile:      filepath.Join(t.TempDir(), "log"),
			SpawnDir:     t.TempDir(),
		}),
		Threads:   threadsSeam,
		Terminals: terminals,
	})
	if err := observer.connect(context.Background(), appServer.socket); err != nil {
		t.Fatal(err)
	}
	threadsSeam.wait(t, func() bool { return len(threadsSeam.sessions) == 4 })
	got := map[string]string{}
	for _, session := range threadsSeam.sessions[2:] {
		got[session.ProviderID] = session.TerminalID
	}
	if got["conv-a"] != "term-aaaaa" || got["conv-b"] != "term-bbbbb" {
		t.Errorf("reattached bindings = %v; want the persisted mapping honored", got)
	}
}
