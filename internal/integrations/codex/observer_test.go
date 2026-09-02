package codex

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/threads"
)

// fakeAppServer speaks the shared app-server's wire protocol over a real
// unix WebSocket at the well-known path inside a private Codex home: it
// answers initialize and thread/read, records requests, broadcasts
// whatever a test pushes, and can drop its clients or go away entirely.
type fakeAppServer struct {
	t      *testing.T
	socket string

	mu         sync.Mutex
	server     *http.Server
	conns      []*websocket.Conn
	requests   []rpcMessage
	threads    map[string]fakeThread
	refuseInit bool
}

// fakeThread is what thread/read answers for one id; a nil Status makes
// the read fail with an RPC error, as the server does for an unknown
// thread.
type fakeThread struct {
	Name, Preview string
	Status        any
}

func status(kind string, flags ...string) map[string]any {
	s := map[string]any{"type": kind}
	if kind == "active" {
		if flags == nil {
			flags = []string{}
		}
		s["activeFlags"] = flags
	}
	return s
}

func (f *fakeAppServer) start() {
	f.t.Helper()
	if err := os.MkdirAll(filepath.Dir(f.socket), 0o700); err != nil {
		f.t.Fatal(err)
	}
	_ = os.Remove(f.socket)
	listener, err := net.Listen("unix", f.socket)
	if err != nil {
		f.t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(f.handle)}
	f.mu.Lock()
	f.server = server
	f.mu.Unlock()
	go func() { _ = server.Serve(listener) }()
}

// stop takes the server away: listener closed, clients dropped, socket
// file removed — the "no shared server" world a cold start meets.
func (f *fakeAppServer) stop() {
	f.mu.Lock()
	server := f.server
	f.server = nil
	f.mu.Unlock()
	if server != nil {
		_ = server.Close()
	}
	f.closeConns()
	_ = os.Remove(f.socket)
}

// closeConns drops every client connection while the server keeps
// listening — a server restart from the client's point of view.
func (f *fakeAppServer) closeConns() {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, ws := range conns {
		_ = ws.CloseNow()
	}
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
		f.requests = append(f.requests, message)
		f.mu.Unlock()
		if len(message.ID) == 0 {
			continue // initialized
		}
		reply := map[string]any{"id": json.RawMessage(message.ID)}
		f.mu.Lock()
		refuseInit := f.refuseInit
		f.mu.Unlock()
		switch message.Method {
		case "initialize":
			if refuseInit {
				reply["error"] = map[string]any{"code": -32600, "message": "incompatible client"}
			} else {
				reply["result"] = map[string]any{}
			}
		case "thread/read":
			var params struct {
				ThreadID string `json:"threadId"`
			}
			_ = json.Unmarshal(message.Params, &params)
			f.mu.Lock()
			thread, ok := f.threads[params.ThreadID]
			f.mu.Unlock()
			if !ok || thread.Status == nil {
				reply["error"] = map[string]any{"code": -32600, "message": "thread not found"}
			} else {
				reply["result"] = map[string]any{"thread": map[string]any{
					"id": params.ThreadID, "name": thread.Name, "preview": thread.Preview, "status": thread.Status,
				}}
			}
		default:
			reply["result"] = map[string]any{}
		}
		data, _ = json.Marshal(reply)
		_ = ws.Write(ctx, websocket.MessageText, data)
	}
}

func (f *fakeAppServer) setThread(id string, thread fakeThread) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.threads[id] = thread
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

// request sends a server-to-client request over every connection.
func (f *fakeAppServer) request(id int, method string) {
	data, _ := json.Marshal(map[string]any{"id": id, "method": method, "params": map[string]any{}})
	f.mu.Lock()
	conns := append([]*websocket.Conn(nil), f.conns...)
	f.mu.Unlock()
	for _, ws := range conns {
		_ = ws.Write(context.Background(), websocket.MessageText, data)
	}
}

func (f *fakeAppServer) count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r.Method == method {
			n++
		}
	}
	return n
}

func (f *fakeAppServer) connections() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.conns)
}

// started is a thread/started payload for a terminal-started TUI.
func started(id, cwd string) map[string]any {
	return map[string]any{"thread": map[string]any{
		"id": id, "cwd": cwd, "source": "cli", "name": nil, "preview": "", "status": status("idle"),
	}}
}

func changed(id string, s map[string]any) map[string]any {
	return map[string]any{"threadId": id, "status": s}
}

// fakeThreads records what reaches the threads seam.
type fakeThreads struct {
	mu          sync.Mutex
	sessions    []threads.SessionObservation
	statuses    []threads.StatusObservation
	inactive    []string
	failSession error
}

func (f *fakeThreads) ObserveSession(_ context.Context, o threads.SessionObservation) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failSession != nil {
		return "", f.failSession
	}
	f.sessions = append(f.sessions, o)
	return "thrd-aaaaa", nil
}

func (f *fakeThreads) ObserveStatus(_ context.Context, o threads.StatusObservation) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.statuses = append(f.statuses, o)
	return nil
}

func (f *fakeThreads) Deactivate(_ context.Context, terminalID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.inactive = append(f.inactive, terminalID)
}

func (f *fakeThreads) sessionCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sessions)
}

func (f *fakeThreads) statusCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.statuses)
}

func (f *fakeThreads) lastStatus(t *testing.T) threads.StatusObservation {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		t.Fatal("no status observation recorded")
	}
	return f.statuses[len(f.statuses)-1]
}

func (f *fakeThreads) lastSession(t *testing.T) threads.SessionObservation {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.sessions) == 0 {
		t.Fatal("no session observation recorded")
	}
	return f.sessions[len(f.sessions)-1]
}

func (f *fakeThreads) deactivated() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.inactive...)
}

// fakeTerminals serves terminal records, statuses mutable by the test.
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

func (f *fakeTerminals) set(id string, status api.TerminalStatus) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.terminals[id] = api.Terminal{ID: id, ProjectID: "proj-aaaaa", AppID: AppID, Status: status}
}

func (f *fakeTerminals) remove(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.terminals, id)
}

type fixture struct {
	observer  *Observer
	app       tui
	server    *fakeAppServer
	threads   *fakeThreads
	terminals *fakeTerminals
	home      string
	dir       string
	starts    int
	ctx       context.Context
	logs      *lockedLog
}

type lockedLog struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedLog) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// shortTempDir keeps unix socket paths well under the platform limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "cx")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

// newFixture builds an observer over a fake server (unless withoutServer)
// and runs it, with every cadence shrunk. Start is the cold-start seam:
// it brings the fake server up, standing in for the detached spawn.
func newFixture(t *testing.T, withoutServer bool) *fixture {
	t.Helper()
	home := shortTempDir(t)
	f := &fixture{
		threads:   &fakeThreads{},
		terminals: &fakeTerminals{terminals: map[string]api.Terminal{}},
		home:      home,
		dir:       shortTempDir(t),
		logs:      &lockedLog{},
	}
	f.terminals.set("term-aaaaa", api.TerminalRunning)
	f.terminals.set("term-bbbbb", api.TerminalRunning)
	f.server = &fakeAppServer{t: t, socket: ControlSocketPath(home), threads: map[string]fakeThread{}}
	if !withoutServer {
		f.server.start()
	}
	t.Cleanup(f.server.stop)
	f.observer = NewObserver(ObserverOptions{
		CodexHome: home,
		Threads:   f.threads,
		Terminals: f.terminals,
		Logger:    slog.New(slog.NewTextHandler(f.logs, nil)),
		Start: func(context.Context) error {
			f.starts++
			f.server.start()
			return nil
		},
	})
	f.observer.window = 400 * time.Millisecond
	f.observer.grace = 80 * time.Millisecond
	f.observer.backoffMin = 10 * time.Millisecond
	f.observer.backoffMax = 40 * time.Millisecond
	f.observer.startWait = 2 * time.Second
	f.observer.startPoll = 20 * time.Millisecond
	f.observer.callTimeout = 2 * time.Second
	f.app = tui{observer: f.observer}
	ctx, cancel := context.WithCancel(context.Background())
	f.ctx = ctx
	done := make(chan struct{})
	go func() {
		defer close(done)
		f.observer.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	if !withoutServer {
		// Settled before the test body: connected, boot reconcile done.
		waitFor(t, func() bool { return f.server.count("initialize") == 1 })
		f.observer.reads.Wait()
	}
	return f
}

// launch runs the App's launch composition for a fresh TUI: prepare
// (outside the commit lock), then command (under it).
func (f *fixture) launch(t *testing.T, terminalID, dir string) string {
	t.Helper()
	if _, err := f.app.PrepareLaunch(f.ctx, integrations.LaunchContext{Directory: dir}); err != nil {
		t.Fatalf("prepare: %v", err)
	}
	command, err := f.app.Command(f.ctx, integrations.LaunchContext{TerminalID: terminalID, Directory: dir})
	if err != nil {
		t.Fatalf("command: %v", err)
	}
	return command
}

// bind launches, announces one matching thread, and waits for the
// pairing to be held.
func (f *fixture) bind(t *testing.T, terminalID, threadID string) {
	t.Helper()
	f.launch(t, terminalID, f.dir)
	f.server.broadcast("thread/started", started(threadID, f.dir))
	waitFor(t, func() bool { return f.paired(threadID) == terminalID })
	// The binding's follow-up read has answered (nothing, for an unknown
	// thread) before the test decides what the server knows.
	f.observer.reads.Wait()
}

// mint binds and drives the first prompt, leaving an established
// pairing.
func (f *fixture) mint(t *testing.T, terminalID, threadID string) {
	t.Helper()
	f.bind(t, terminalID, threadID)
	f.server.setThread(threadID, fakeThread{Preview: "fix the build", Status: status("active")})
	f.server.broadcast("thread/status/changed", changed(threadID, status("active")))
	waitFor(t, func() bool { return f.threads.sessionCount() == 1 })
}

// rejectInitialize makes the fake answer initialize with an error.
func (f *fakeAppServer) rejectInitialize() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refuseInit = true
}

func (f *fixture) paired(threadID string) string {
	f.observer.mu.Lock()
	defer f.observer.mu.Unlock()
	if p, ok := f.observer.held[threadID]; ok {
		return p.terminalID
	}
	return ""
}

func (f *fixture) pendingIn(dir string) bool {
	f.observer.mu.Lock()
	defer f.observer.mu.Unlock()
	slot, ok := f.observer.slots[canonical(dir)]
	return ok && slot.launch != nil
}

func (f *fixture) markTUI(t *testing.T, threadID string) {
	t.Helper()
	dir := tuiCapabilitiesDir(f.home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, threadID), nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if check() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition never held")
}

// settle gives asynchronous paths that should do nothing a chance to do
// it before a negative assertion.
func settle() { time.Sleep(150 * time.Millisecond) }

// A launch composes plain codex, its announcement binds the terminal
// privately, the first prompt mints the thread through the session seam
// — with the terminal's project, the announced cwd, and the server's
// preview as title — and later statuses ride the status seam.
func TestLaunchBindsAndMintsAtFirstPrompt(t *testing.T) {
	f := newFixture(t, false)
	command := f.launch(t, "term-aaaaa", f.dir)
	if command != "codex" {
		t.Fatalf("command = %q, want plain codex", command)
	}
	waitFor(t, func() bool { return f.server.count("initialize") == 1 })

	f.server.broadcast("thread/started", started("t1", f.dir))
	waitFor(t, func() bool { return f.paired("t1") == "term-aaaaa" })
	settle()
	if f.threads.sessionCount() != 0 {
		t.Fatalf("thread minted before the first prompt: %+v", f.threads.sessions)
	}

	// An idle change before the prompt is not a prompt.
	f.server.broadcast("thread/status/changed", changed("t1", status("idle")))
	settle()
	if f.threads.sessionCount() != 0 || f.threads.statusCount() != 0 {
		t.Fatal("idle evidence reached the seam before minting")
	}

	f.server.setThread("t1", fakeThread{Preview: "fix the build please", Status: status("active")})
	f.server.broadcast("thread/status/changed", changed("t1", status("active")))
	waitFor(t, func() bool { return f.threads.sessionCount() == 1 })
	session := f.threads.lastSession(t)
	if session.IntegrationID != "codex" || session.AppID != "codex/tui" || session.AgentID != "codex" || session.ProviderID != "t1" || session.TerminalID != "term-aaaaa" ||
		session.InitialDirectory != f.dir || session.Status != api.ThreadWorking ||
		session.Metadata.Cwd != f.dir || session.Metadata.Title != "fix the build please" ||
		session.Metadata.Model != "" {
		t.Errorf("session observation = %+v", session)
	}

	f.server.broadcast("thread/status/changed", changed("t1", status("idle")))
	waitFor(t, func() bool { return f.threads.statusCount() == 1 })
	if got := f.threads.lastStatus(t); got.ProviderID != "t1" || got.Status != api.ThreadIdle || got.Metadata.Title != "" {
		t.Errorf("status observation = %+v", got)
	}
}

// Each Codex status maps to exactly one thread status.
func TestStatusMapping(t *testing.T) {
	f := newFixture(t, false)
	f.mint(t, "term-aaaaa", "t1")
	cases := []struct {
		wire map[string]any
		want api.ThreadStatus
	}{
		{status("idle"), api.ThreadIdle},
		{status("active"), api.ThreadWorking},
		{status("active", "waitingOnApproval"), api.ThreadWaitingForPermission},
		{status("active", "waitingOnUserInput"), api.ThreadWaitingForInput},
		{status("systemError"), api.ThreadError},
		{status("active"), api.ThreadWorking},
		{map[string]any{"type": "somethingNew"}, api.ThreadUnknown},
	}
	for i, c := range cases {
		f.server.broadcast("thread/status/changed", changed("t1", c.wire))
		waitFor(t, func() bool { return f.threads.statusCount() == i+1 })
		if got := f.threads.lastStatus(t).Status; got != c.want {
			t.Errorf("case %d: status = %s, want %s", i, got, c.want)
		}
	}
}

// thread/closed is session end: the terminal deactivates and the pairing
// ends, so nothing later lands.
func TestClosedDeactivates(t *testing.T) {
	f := newFixture(t, false)
	f.mint(t, "term-aaaaa", "t1")
	f.server.broadcast("thread/closed", map[string]any{"threadId": "t1"})
	waitFor(t, func() bool { return len(f.threads.deactivated()) == 1 })
	if f.paired("t1") != "" {
		t.Error("pairing survived thread/closed")
	}
	f.server.broadcast("thread/status/changed", changed("t1", status("active")))
	settle()
	if f.threads.statusCount() != 0 {
		t.Error("evidence after close reached the seam")
	}
}

// A TUI that exits before its first prompt leaves no record and no
// deactivation — there was never anything to deactivate.
func TestExitBeforePromptLeavesNoRecord(t *testing.T) {
	f := newFixture(t, false)
	f.bind(t, "term-aaaaa", "t1")
	f.server.broadcast("thread/closed", map[string]any{"threadId": "t1"})
	waitFor(t, func() bool { return f.paired("t1") == "" })
	settle()
	if f.threads.sessionCount() != 0 || len(f.threads.deactivated()) != 0 {
		t.Errorf("sessions = %d, deactivated = %v", f.threads.sessionCount(), f.threads.deactivated())
	}
}

// With no announcement inside the window the terminal is left untracked
// for good: a later announcement in the directory binds nothing.
func TestNoCandidateLeavesUntracked(t *testing.T) {
	f := newFixture(t, false)
	f.launch(t, "term-aaaaa", f.dir)
	waitFor(t, func() bool { return !f.pendingIn(f.dir) })
	f.server.broadcast("thread/started", started("t1", f.dir))
	f.server.setThread("t1", fakeThread{Status: status("active")})
	f.server.broadcast("thread/status/changed", changed("t1", status("active")))
	settle()
	if f.paired("t1") != "" || f.threads.sessionCount() != 0 {
		t.Error("a late announcement bound the terminal")
	}
}

// Two announcements inside the window are indistinguishable: fail
// closed, at once.
func TestTwoCandidatesFailClosed(t *testing.T) {
	f := newFixture(t, false)
	f.launch(t, "term-aaaaa", f.dir)
	f.server.broadcast("thread/started", started("t1", f.dir))
	time.Sleep(f.observer.grace + 20*time.Millisecond)
	second := started("t2", f.dir)
	second["thread"].(map[string]any)["source"] = "vscode"
	f.markTUI(t, "t2")
	f.server.broadcast("thread/started", second)
	waitFor(t, func() bool { return !f.pendingIn(f.dir) })
	if f.paired("t1") != "" || f.paired("t2") != "" {
		t.Error("an ambiguous launch bound a thread")
	}
	for _, id := range []string{"t1", "t2"} {
		f.server.broadcast("thread/status/changed", changed(id, status("active")))
	}
	settle()
	if f.threads.sessionCount() != 0 {
		t.Error("evidence for an unbound thread minted a record")
	}
}

func TestLaunchSourceEligibility(t *testing.T) {
	for _, tc := range []struct {
		name   string
		source any
		bind   bool
	}{
		{name: "cli", source: "cli", bind: true},
		{name: "vscode compatibility value", source: "vscode", bind: true},
		{name: "exec", source: "exec"},
		{name: "app server", source: "appServer"},
		{name: "unknown", source: "unknown"},
		{name: "custom", source: map[string]any{"custom": "other"}},
		{name: "subagent", source: map[string]any{"subAgent": "review"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, false)
			f.launch(t, "term-aaaaa", f.dir)
			announcement := started("observed", f.dir)
			announcement["thread"].(map[string]any)["source"] = tc.source
			if tc.source == "vscode" {
				f.markTUI(t, "observed")
			}
			f.server.broadcast("thread/started", announcement)
			if tc.bind {
				waitFor(t, func() bool { return f.paired("observed") == "term-aaaaa" })
				return
			}

			// An ineligible source must not consume the launch; a later CLI
			// announcement inside the same window still binds it.
			f.server.broadcast("thread/started", started("cli", f.dir))
			waitFor(t, func() bool { return f.paired("cli") == "term-aaaaa" })
			if f.paired("observed") != "" {
				t.Error("ineligible source bound the terminal")
			}
		})
	}
}

// The shared server labels programmatic roots "vscode" too. Without the
// external TUI's capability marker that value is not identity and must not
// consume the pending launch.
func TestMislabelledProgrammaticThreadIgnored(t *testing.T) {
	f := newFixture(t, false)
	f.launch(t, "term-aaaaa", f.dir)
	programmatic := started("programmatic", f.dir)
	programmatic["thread"].(map[string]any)["source"] = "vscode"
	f.server.broadcast("thread/started", programmatic)

	f.markTUI(t, "tui")
	tui := started("tui", f.dir)
	tui["thread"].(map[string]any)["source"] = "vscode"
	f.server.broadcast("thread/started", tui)
	waitFor(t, func() bool { return f.paired("tui") == "term-aaaaa" })
	if f.paired("programmatic") != "" {
		t.Error("programmatic thread bound the terminal")
	}
}

func TestVscodeCandidateWaitsForTUIMarker(t *testing.T) {
	f := newFixture(t, false)
	f.launch(t, "term-aaaaa", f.dir)
	f.observer.mu.Lock()
	launch := f.observer.slots[canonical(f.dir)].launch
	c := candidate{
		threadID: "tui",
		cwd:      f.dir,
		source:   "vscode",
		at:       launch.armedAt.Add(time.Millisecond),
		seq:      launch.armedSeq + 1,
	}
	f.observer.mu.Unlock()
	go f.observer.awaitTUIMarker(c)
	time.Sleep(20 * time.Millisecond)
	f.observer.mu.Lock()
	acceptedBeforeMarker := len(launch.candidates)
	f.observer.mu.Unlock()
	if acceptedBeforeMarker != 0 || f.paired("tui") != "" {
		t.Fatal("vscode candidate was accepted before its TUI marker")
	}
	f.markTUI(t, "tui")
	waitFor(t, func() bool { return f.paired("tui") == "term-aaaaa" })
}

func TestVscodeCandidateWithNoGraceFailsClosed(t *testing.T) {
	f := newFixture(t, false)
	f.observer.grace = 0
	f.launch(t, "term-aaaaa", f.dir)
	announcement := started("programmatic", f.dir)
	announcement["thread"].(map[string]any)["source"] = "vscode"
	f.server.broadcast("thread/started", announcement)
	f.server.broadcast("thread/started", started("tui", f.dir))
	waitFor(t, func() bool { return f.paired("tui") == "term-aaaaa" })
	if f.paired("programmatic") != "" {
		t.Error("unconfirmed vscode candidate bound the terminal")
	}
}

func TestMissingTUIMarkerIsDiagnosed(t *testing.T) {
	f := newFixture(t, false)
	f.launch(t, "term-aaaaa", f.dir)
	announcement := started("programmatic", f.dir)
	announcement["thread"].(map[string]any)["source"] = "vscode"
	f.server.broadcast("thread/started", announcement)

	marker := filepath.Join(tuiCapabilitiesDir(f.home), "programmatic")
	waitFor(t, func() bool {
		logs := f.logs.String()
		return strings.Contains(logs, "thread=programmatic") && strings.Contains(logs, "marker="+marker)
	})
}

func TestStopJoinsTUIMarkerWait(t *testing.T) {
	home := shortTempDir(t)
	dir := shortTempDir(t)
	terminals := &fakeTerminals{terminals: map[string]api.Terminal{}}
	terminals.set("term-aaaaa", api.TerminalRunning)
	observer := NewObserver(ObserverOptions{
		CodexHome: home,
		Threads:   &fakeThreads{},
		Terminals: terminals,
	})
	observer.window = 400 * time.Millisecond
	observer.grace = 80 * time.Millisecond
	canonicalDir := canonical(dir)
	if _, err := observer.reserve(context.Background(), canonicalDir); err != nil {
		t.Fatal(err)
	}
	if err := observer.arm("term-aaaaa", dir); err != nil {
		t.Fatal(err)
	}
	observer.mu.Lock()
	launch := observer.slots[canonicalDir].launch
	c := candidate{
		threadID: "tui",
		cwd:      dir,
		source:   "vscode",
		at:       launch.armedAt.Add(time.Millisecond),
		seq:      launch.armedSeq + 1,
	}
	observer.mu.Unlock()
	observer.announced(c)

	stopped := make(chan struct{})
	go func() {
		observer.stop()
		close(stopped)
	}()
	select {
	case <-stopped:
		t.Fatal("observer stopped while its TUI marker poller was still running")
	case <-time.After(20 * time.Millisecond):
	}
	dir = tuiCapabilitiesDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "tui"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("observer did not stop after its TUI marker poller finished")
	}
	observer.closeLaunch(launch, "test cleanup")
}

// An announcement from another directory (here, via a symlink to a
// different one) never matches; one whose path is a symlink to the
// launch directory does — both sides canonicalize.
func TestDirectoryMatchingCanonicalizes(t *testing.T) {
	f := newFixture(t, false)
	other := shortTempDir(t)
	link := filepath.Join(shortTempDir(t), "link")
	if err := os.Symlink(f.dir, link); err != nil {
		t.Fatal(err)
	}
	f.launch(t, "term-aaaaa", link)
	f.server.broadcast("thread/started", started("elsewhere", other))
	f.server.broadcast("thread/started", started("t1", f.dir))
	waitFor(t, func() bool { return f.paired("t1") == "term-aaaaa" })
	if f.paired("elsewhere") != "" {
		t.Error("another directory's thread bound")
	}
}

// Same-directory launches take turns: the second prepares only once the
// first's window has closed. Different directories proceed at once.
func TestSameDirectoryLaunchesSerialize(t *testing.T) {
	f := newFixture(t, false)
	f.launch(t, "term-aaaaa", f.dir)

	second := make(chan struct{})
	var secondErr error
	go func() {
		// Fatalf may only run on the test goroutine; the error is asserted
		// after the receive.
		defer close(second)
		if _, err := f.app.PrepareLaunch(f.ctx, integrations.LaunchContext{Directory: f.dir}); err != nil {
			secondErr = err
			return
		}
		_, secondErr = f.app.Command(f.ctx, integrations.LaunchContext{TerminalID: "term-bbbbb", Directory: f.dir})
	}()
	other := shortTempDir(t)
	f.terminals.set("term-ccccc", api.TerminalRunning)
	f.launch(t, "term-ccccc", other) // returns without waiting
	select {
	case <-second:
		t.Fatal("second launch proceeded while the first's window was open")
	case <-time.After(50 * time.Millisecond):
	}

	f.server.broadcast("thread/started", started("t1", f.dir))
	waitFor(t, func() bool { return f.paired("t1") == "term-aaaaa" })
	select {
	case <-second:
	case <-time.After(3 * time.Second):
		t.Fatal("second launch never proceeded after the first bound")
	}
	if secondErr != nil {
		t.Fatalf("second launch: %v", secondErr)
	}
	f.server.broadcast("thread/started", started("t2", f.dir))
	waitFor(t, func() bool { return f.paired("t2") == "term-bbbbb" })
}

// A prepared launch whose create fails frees its directory and, when
// Command already armed it, closes the window without binding.
func TestAbortFreesTheDirectory(t *testing.T) {
	f := newFixture(t, false)
	abort, err := f.app.PrepareLaunch(f.ctx, integrations.LaunchContext{Directory: f.dir})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Command(f.ctx, integrations.LaunchContext{TerminalID: "term-aaaaa", Directory: f.dir}); err != nil {
		t.Fatal(err)
	}
	abort()
	if f.pendingIn(f.dir) {
		t.Fatal("aborted launch still pending")
	}
	// The directory is free: the next launch prepares without waiting.
	ctx, cancel := context.WithTimeout(f.ctx, time.Second)
	defer cancel()
	if _, err := f.app.PrepareLaunch(ctx, integrations.LaunchContext{Directory: f.dir}); err != nil {
		t.Fatalf("directory still reserved after abort: %v", err)
	}
}

// Evidence for a thread no live terminal is paired with is dropped
// entirely — other clients' threads never reach the seam.
func TestUnpairedEvidenceDropped(t *testing.T) {
	f := newFixture(t, false)
	f.mint(t, "term-aaaaa", "t1")
	f.server.broadcast("thread/status/changed", changed("desktop-thread", status("active")))
	f.server.broadcast("thread/closed", map[string]any{"threadId": "desktop-thread"})
	settle()
	if f.threads.statusCount() != 0 || len(f.threads.deactivated()) != 0 {
		t.Error("evidence for an unpaired thread reached the seam")
	}
}

// Once the terminal has exited, the thread's evidence — an iOS-driven
// turn — no longer applies: the terminal deactivates and the pairing
// ends.
func TestExitedTerminalEndsThePairing(t *testing.T) {
	f := newFixture(t, false)
	f.mint(t, "term-aaaaa", "t1")
	f.terminals.set("term-aaaaa", api.TerminalExited)
	f.server.broadcast("thread/status/changed", changed("t1", status("active")))
	waitFor(t, func() bool { return len(f.threads.deactivated()) == 1 })
	if f.threads.statusCount() != 0 || f.paired("t1") != "" {
		t.Errorf("statuses = %d, paired = %q", f.threads.statusCount(), f.paired("t1"))
	}
	// A merely unreachable terminal is no evidence of leaving.
	f.mint(t, "term-bbbbb", "t2")
	f.terminals.set("term-bbbbb", api.TerminalUnreachable)
	f.server.broadcast("thread/status/changed", changed("t2", status("idle")))
	waitFor(t, func() bool { return f.threads.statusCount() == 1 })
}

// Forget (the terminal delete) drops the pairing and any pending launch
// for the terminal.
func TestForgetDropsTerminalState(t *testing.T) {
	f := newFixture(t, false)
	f.mint(t, "term-aaaaa", "t1")
	f.launch(t, "term-bbbbb", f.dir)
	f.observer.Forget("term-aaaaa")
	f.observer.Forget("term-bbbbb")
	if f.paired("t1") != "" || f.pendingIn(f.dir) {
		t.Error("state survived Forget")
	}
	f.terminals.remove("term-aaaaa")
	f.server.broadcast("thread/status/changed", changed("t1", status("active")))
	settle()
	if f.threads.statusCount() != 0 || len(f.threads.deactivated()) != 0 {
		t.Error("evidence for a forgotten terminal reached the seam")
	}
}

// Losing the connection coerces every live paired thread to unknown;
// idle ones stay. The reconnect's reconcile reads each paired thread and
// restores what the server reports — including session end for one no
// longer loaded.
func TestConnectionLossCoercesAndReconcileRestores(t *testing.T) {
	f := newFixture(t, false)
	f.mint(t, "term-aaaaa", "t1")
	f.mint(t, "term-bbbbb", "t2")
	f.server.broadcast("thread/status/changed", changed("t2", status("idle")))
	waitFor(t, func() bool { return f.threads.statusCount() == 1 })

	f.server.setThread("t1", fakeThread{Status: status("active", "waitingOnApproval")})
	f.server.setThread("t2", fakeThread{Status: status("notLoaded")})
	f.server.closeConns()
	waitFor(t, func() bool { return f.threads.statusCount() >= 2 })
	f.threads.mu.Lock()
	coerced := f.threads.statuses[1]
	f.threads.mu.Unlock()
	if coerced.ProviderID != "t1" || coerced.Status != api.ThreadUnknown {
		t.Errorf("coercion = %+v, want t1 unknown", coerced)
	}

	// Reconnected and reconciled: t1 is back to its live state, t2 saw
	// session end.
	waitFor(t, func() bool { return f.threads.statusCount() == 3 && len(f.threads.deactivated()) == 1 })
	if got := f.threads.lastStatus(t); got.ProviderID != "t1" || got.Status != api.ThreadWaitingForPermission {
		t.Errorf("reconciled status = %+v", got)
	}
	if f.threads.deactivated()[0] != "term-bbbbb" || f.paired("t2") != "" {
		t.Errorf("deactivated = %v, t2 paired = %q", f.threads.deactivated(), f.paired("t2"))
	}
	if f.server.count("initialize") != 2 {
		t.Errorf("initialize count = %d, want 2", f.server.count("initialize"))
	}
}

// A prompt inside the grace flips the thread active before the pairing
// exists to hear it; the read right after binding catches it.
func TestPromptInsideGraceStillMints(t *testing.T) {
	f := newFixture(t, false)
	f.launch(t, "term-aaaaa", f.dir)
	f.server.setThread("t1", fakeThread{Preview: "quick", Status: status("active")})
	f.server.broadcast("thread/started", started("t1", f.dir))
	f.server.broadcast("thread/status/changed", changed("t1", status("active")))
	waitFor(t, func() bool { return f.threads.sessionCount() == 1 })
	if got := f.threads.lastSession(t); got.Status != api.ThreadWorking || got.Metadata.Title != "quick" {
		t.Errorf("session = %+v", got)
	}
}

// Evidence older on the wire than a reply already applied is skipped.
func TestStaleEvidenceSkipped(t *testing.T) {
	f := newFixture(t, false)
	f.mint(t, "term-aaaaa", "t1")
	f.observer.applyEvidence(f.ctx, "t1", evidence{status: api.ThreadIdle, seq: f.observer.seq.Load() + 10})
	f.observer.applyEvidence(f.ctx, "t1", evidence{status: api.ThreadWorking, seq: f.observer.seq.Load() + 5})
	if got := f.threads.lastStatus(t); got.Status != api.ThreadIdle {
		t.Errorf("stale evidence applied: %+v", got)
	}
}

// A transient failure at the session seam leaves the pairing unminted;
// the next live evidence retries.
func TestMintRetriesAfterSeamFailure(t *testing.T) {
	f := newFixture(t, false)
	f.bind(t, "term-aaaaa", "t1")
	f.threads.mu.Lock()
	f.threads.failSession = errors.New("database locked")
	f.threads.mu.Unlock()
	f.server.broadcast("thread/status/changed", changed("t1", status("active")))
	settle()
	f.threads.mu.Lock()
	f.threads.failSession = nil
	f.threads.mu.Unlock()
	f.server.broadcast("thread/status/changed", changed("t1", status("active", "waitingOnUserInput")))
	waitFor(t, func() bool { return f.threads.sessionCount() == 1 })
	if got := f.threads.lastSession(t); got.Status != api.ThreadWaitingForInput {
		t.Errorf("session = %+v", got)
	}
}

// A resume composes the exact resume, pairs the terminal at launch with
// no binding window, and establishes on its first evidence of any kind.
func TestResumePairsWithoutAWindow(t *testing.T) {
	f := newFixture(t, false)
	if _, err := f.app.PrepareLaunch(f.ctx, integrations.LaunchContext{Directory: f.dir, ResumeConversationID: "t9"}); err != nil {
		t.Fatal(err)
	}
	command, err := f.app.Command(f.ctx, integrations.LaunchContext{
		TerminalID: "term-aaaaa", Directory: f.dir, ResumeConversationID: "t9",
	})
	if err != nil {
		t.Fatal(err)
	}
	if command != "codex resume 't9'" {
		t.Fatalf("command = %q", command)
	}
	if f.pendingIn(f.dir) || f.paired("t9") != "term-aaaaa" {
		t.Fatalf("pending = %v, paired = %q", f.pendingIn(f.dir), f.paired("t9"))
	}
	f.server.broadcast("thread/status/changed", changed("t9", status("idle")))
	waitFor(t, func() bool { return f.threads.sessionCount() == 1 })
	if got := f.threads.lastSession(t); got.TerminalID != "term-aaaaa" || got.Status != api.ThreadIdle ||
		got.Metadata.Title != "" || got.Metadata.Cwd != "" {
		t.Errorf("session = %+v", got)
	}
	f.server.broadcast("thread/status/changed", changed("t9", status("active")))
	waitFor(t, func() bool { return f.threads.statusCount() == 1 })
	if f.server.count("thread/read") != 0 {
		t.Error("a resumed thread's title was re-read")
	}
}

// With no server answering, a launch starts one and waits for it; the
// observer never starts one on its own.
func TestColdStart(t *testing.T) {
	f := newFixture(t, true)
	time.Sleep(100 * time.Millisecond)
	if f.starts != 0 {
		t.Fatal("the observer started a server at boot")
	}
	f.launch(t, "term-aaaaa", f.dir)
	if f.starts != 1 || f.server.count("initialize") != 1 {
		t.Fatalf("starts = %d, initialize = %d", f.starts, f.server.count("initialize"))
	}
	// Prepared launches bind like any other.
	f.server.broadcast("thread/started", started("t1", f.dir))
	waitFor(t, func() bool { return f.paired("t1") == "term-aaaaa" })
}

// A server that cannot be started or reached fails the launch before
// any terminal exists, inside the start wait.
func TestLaunchFailsWithoutAServer(t *testing.T) {
	f := newFixture(t, true)
	f.observer.start = func(context.Context) error { return nil } // starts nothing
	f.observer.startWait = 200 * time.Millisecond
	begin := time.Now()
	_, err := f.app.PrepareLaunch(f.ctx, integrations.LaunchContext{Directory: f.dir})
	if err == nil {
		t.Fatal("launch prepared with no server")
	}
	if time.Since(begin) > 2*time.Second {
		t.Errorf("failure took %s; want inside the start wait", time.Since(begin))
	}
	f.observer.start = func(context.Context) error { return errors.New("no codex on PATH") }
	if _, err := f.app.PrepareLaunch(f.ctx, integrations.LaunchContext{Directory: f.dir}); err == nil {
		t.Fatal("launch prepared with a failed start")
	}
}

// Server-to-client requests are refused with method-not-found and the
// connection stays up.
func TestServerRequestsRefused(t *testing.T) {
	f := newFixture(t, false)
	f.mint(t, "term-aaaaa", "t1")
	f.server.request(7, "item/commandExecution/requestApproval")
	waitFor(t, func() bool {
		f.server.mu.Lock()
		defer f.server.mu.Unlock()
		for _, r := range f.server.requests {
			if r.Error != nil && r.Error.Code == -32601 && string(r.ID) == "7" {
				return true
			}
		}
		return false
	})
	f.server.broadcast("thread/status/changed", changed("t1", status("idle")))
	waitFor(t, func() bool { return f.threads.statusCount() == 1 })
	if f.server.connections() != 1 {
		t.Errorf("connections = %d", f.server.connections())
	}
}

// A prompt whose whole turn finishes inside the grace — the pairing
// existed for none of it — still mints: the live status heard for the
// candidate is remembered, and the read at binding lands the record.
func TestPromptAndTurnInsideGraceStillMint(t *testing.T) {
	f := newFixture(t, false)
	f.launch(t, "term-aaaaa", f.dir)
	f.server.setThread("t1", fakeThread{Preview: "quick one", Status: status("idle")})
	f.server.broadcast("thread/started", started("t1", f.dir))
	f.server.broadcast("thread/status/changed", changed("t1", status("active")))
	f.server.broadcast("thread/status/changed", changed("t1", status("idle")))
	waitFor(t, func() bool { return f.threads.sessionCount() == 1 })
	if got := f.threads.lastSession(t); got.Status != api.ThreadIdle || got.Metadata.Title != "quick one" {
		t.Errorf("session = %+v", got)
	}
}

// A server that answers but refuses the handshake is not a missing
// server: the launch fails and nothing is started.
func TestHandshakeRefusalDoesNotColdStart(t *testing.T) {
	f := newFixture(t, false)
	f.server.rejectInitialize()
	f.server.closeConns()
	waitFor(t, func() bool { return f.server.count("initialize") >= 2 })
	_, err := f.app.PrepareLaunch(f.ctx, integrations.LaunchContext{Directory: f.dir})
	if err == nil || f.starts != 0 {
		t.Fatalf("err = %v, starts = %d; want a refusal and no start", err, f.starts)
	}
}

// Command without its PrepareLaunch is an invariant violation, refused.
func TestCommandRequiresPreparation(t *testing.T) {
	f := newFixture(t, false)
	if _, err := f.app.Command(f.ctx, integrations.LaunchContext{TerminalID: "term-aaaaa", Directory: f.dir}); !errors.Is(err, errUnprepared) {
		t.Fatalf("err = %v, want errUnprepared", err)
	}
}

// A resumed pairing whose terminal record is not there yet (Command runs
// before the insert) drops the evidence but keeps the pairing.
func TestResumeSurvivesEvidenceBeforeTheRecord(t *testing.T) {
	f := newFixture(t, false)
	if _, err := f.app.PrepareLaunch(f.ctx, integrations.LaunchContext{Directory: f.dir, ResumeConversationID: "t9"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.app.Command(f.ctx, integrations.LaunchContext{TerminalID: "term-new", Directory: f.dir, ResumeConversationID: "t9"}); err != nil {
		t.Fatal(err)
	}
	f.server.broadcast("thread/status/changed", changed("t9", status("active")))
	settle()
	if f.threads.sessionCount() != 0 || f.paired("t9") != "term-new" {
		t.Fatalf("sessions = %d, paired = %q", f.threads.sessionCount(), f.paired("t9"))
	}
	f.terminals.set("term-new", api.TerminalRunning)
	f.server.broadcast("thread/status/changed", changed("t9", status("idle")))
	waitFor(t, func() bool { return f.threads.sessionCount() == 1 })
}
