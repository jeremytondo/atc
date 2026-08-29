package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

const (
	// connectTimeout bounds one dial + initialize handshake.
	connectTimeout = 5 * time.Second
	// reconnectInterval paces the observer's connection loop.
	reconnectInterval = time.Second
	// captureWindow bounds how long a launch waits for its thread/started
	// broadcast before the armed capture is abandoned.
	captureWindow = 2 * time.Minute
	// maxMessageBytes caps one server message.
	maxMessageBytes = 64 << 20
)

// ThreadObserver is the seam into the threads domain: neutral
// observations in, no provider vocabulary out. Codex needs no explicit
// deactivation — the threads sweep covers exited TUIs, and teardown
// speaks through status evidence. LookupIdentity gates status broadcasts:
// the shared server fans out every client's threads, and unmapped ones
// must never reach the seam.
type ThreadObserver interface {
	ObserveSession(ctx context.Context, o threads.SessionObservation) (string, error)
	ObserveStatus(ctx context.Context, o threads.StatusObservation) error
	LookupIdentity(agent, providerID string) (threadID, terminalID string, ok bool)
}

// TerminalReader resolves terminal records: the launch capture copies the
// observing terminal's project, and unarmed captures match running codex
// terminals by directory.
type TerminalReader interface {
	Get(id string) (api.Terminal, error)
	List(projectID string) []api.Terminal
}

// ObserverOptions wires an Observer.
type ObserverOptions struct {
	Supervisor *Supervisor
	Threads    ThreadObserver
	Terminals  TerminalReader
	Logger     *slog.Logger
	Now        func() time.Time
}

// Observer holds one passive connection to the shared app-server and
// turns its broadcasts into thread observations. It never originates a
// conversation method: status is observed by thread id, identity capture
// follows thread/started broadcasts, and a lost connection coerces every
// thread it was observing to unknown — honest ignorance rather than a
// stale busy while the provider moves on unheard.
type Observer struct {
	supervisor *Supervisor
	threads    ThreadObserver
	terminals  TerminalReader
	logger     *slog.Logger
	now        func() time.Time

	// connectMu serializes the connected-check with the dial+install, so
	// a launch racing the Run tick (or another launch) can never install
	// two connections — the loser would leak its goroutines and observe
	// every broadcast twice.
	connectMu sync.Mutex

	mu       sync.Mutex
	conn     *rpcConn
	captures []capture
	// observed tracks the last status this connection forwarded per
	// provider thread id: teardown coerces only the live ones — idle
	// persists when unobserved, matching the threads domain's rule.
	observed map[string]api.ThreadStatus
}

// capture is one armed launch: the next non-subagent thread/started whose
// cwd matches binds to this terminal.
type capture struct {
	terminalID string
	cwd        string
	armedAt    time.Time
}

func NewObserver(opts ObserverOptions) *Observer {
	if opts.Supervisor == nil || opts.Threads == nil || opts.Terminals == nil {
		panic("codex.NewObserver: Supervisor, Threads, and Terminals must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Observer{
		supervisor: opts.Supervisor,
		threads:    opts.Threads,
		terminals:  opts.Terminals,
		logger:     opts.Logger,
		now:        opts.Now,
		observed:   map[string]api.ThreadStatus{},
	}
}

// Prewarm adopts or starts the server and brings the observation
// connection up — the slow once-per-server work, run before the terminals
// commit lock so a cold start cannot block unrelated terminal mutations.
func (o *Observer) Prewarm(ctx context.Context) error {
	socket, err := o.supervisor.Ensure(ctx)
	if err != nil {
		return err
	}
	return o.ensureConnected(ctx, socket)
}

// EnsureForLaunch adopts or starts the server, brings observation up, and
// arms this launch's identity capture, returning the socket path for the
// --remote flag. The connection is established here, not left to the Run
// loop's next tick: the TUI starts immediately after, and a
// thread/started broadcast that beats the connection is permanently lost.
// A launch without observation would silently produce a thread-less TUI,
// so a failed connect refuses the launch. After Prewarm this is all fast
// path.
func (o *Observer) EnsureForLaunch(ctx context.Context, terminalID, directory string) (string, error) {
	socket, err := o.supervisor.Ensure(ctx)
	if err != nil {
		return "", err
	}
	if err := o.ensureConnected(ctx, socket); err != nil {
		return "", fmt.Errorf("codex app-server observation unavailable: %w", err)
	}
	now := o.now()
	o.mu.Lock()
	o.pruneCaptures(now)
	o.captures = append(o.captures, capture{terminalID: terminalID, cwd: directory, armedAt: now})
	o.mu.Unlock()
	return socket, nil
}

// AdoptAtBoot re-adopts a running server so live work returns to
// observation: only when a persisted identity or running codex terminals
// suggest there is something to observe, and never starting a server.
func (o *Observer) AdoptAtBoot(ctx context.Context) {
	if _, ok := o.supervisor.readIdentity(); !ok && !o.anyRunningCodexTerminal() {
		return
	}
	if _, ok := o.supervisor.Adopt(ctx); !ok {
		o.logger.Info("no codex app-server answering; observation resumes on demand")
	}
}

func (o *Observer) anyRunningCodexTerminal() bool {
	for _, terminal := range o.terminals.List("") {
		if terminal.Agent == "codex" && terminal.Status == api.TerminalRunning {
			return true
		}
	}
	return false
}

// Run maintains the held connection: whenever the supervisor knows a
// socket and no connection is up, connect; a dropped connection tears
// down to unknown and reconnects here. A dead server is deliberately not
// restarted from here — every attached TUI died with it, so the next
// launch's Ensure restarts on real demand.
func (o *Observer) Run(ctx context.Context) {
	ticker := time.NewTicker(reconnectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			o.mu.Lock()
			conn := o.conn
			o.mu.Unlock()
			if conn != nil {
				conn.close()
			}
			return
		case <-ticker.C:
		}
		socket := o.supervisor.Socket()
		if socket == "" {
			continue
		}
		if err := o.ensureConnected(ctx, socket); err != nil {
			o.logger.Debug("codex app-server connect failed", "error", err)
		}
	}
}

// ensureConnected is the one place a connection is decided: the
// connected-check and the dial+install run under connectMu, and an
// already-connected observer returns immediately.
func (o *Observer) ensureConnected(ctx context.Context, socketPath string) error {
	o.connectMu.Lock()
	defer o.connectMu.Unlock()
	o.mu.Lock()
	connected := o.conn != nil
	o.mu.Unlock()
	if connected {
		return nil
	}
	return o.connect(ctx, socketPath)
}

// connect dials, runs the initialize handshake, and installs the
// connection.
func (o *Observer) connect(ctx context.Context, socketPath string) error {
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	ws, err := dialSocket(dialCtx, socketPath)
	if err != nil {
		return err
	}
	conn := newRPCConn(ws, o.logger)
	conn.onNotification = func(method string, params json.RawMessage) {
		o.handleNotification(context.WithoutCancel(ctx), method, params)
	}
	conn.onClose = func(reason error) {
		o.teardown(context.WithoutCancel(ctx), conn, reason)
	}
	// Installed before the loops start: a connection that dies during the
	// handshake must find itself installed when its teardown runs, or Run
	// would believe a dead connection is live forever.
	o.mu.Lock()
	o.conn = conn
	o.observed = map[string]api.ThreadStatus{}
	o.mu.Unlock()
	conn.start()

	initCtx, cancelInit := context.WithTimeout(ctx, connectTimeout)
	defer cancelInit()
	if _, err := conn.call(initCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "atc", "title": "ATC", "version": "1"},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		conn.close()
		return fmt.Errorf("initialize: %w", err)
	}
	if err := conn.notify(initCtx, "initialized", map[string]any{}); err != nil {
		conn.close()
		return err
	}
	o.logger.Info("codex app-server observation connected")
	// A fresh connection knows nothing about work already in flight
	// (boot re-adoption, reconnect after a drop): walk the loaded threads
	// so live conversations return to observation instead of waiting for
	// their next broadcast. Async — a launch must not wait behind up to
	// fifty thread reads — and detached, like the callbacks above: the
	// walk must outlive the launch request whose connect started it.
	go o.reconcile(context.WithoutCancel(ctx), conn)
	return nil
}

// reconcileCap bounds the loaded-thread walk on one connection.
const reconcileCap = 50

// reconcile walks the server's loaded threads so work already in flight
// returns to observation. A root the identity mapping already knows goes
// straight back to its recorded terminal — the same seed check Claude
// runs after a restart — which disambiguates layouts cwd alone cannot
// (two terminals in one directory). Unmapped roots fall back to
// single-candidate cwd attribution, and when several unmapped roots share
// one cwd, none is picked: choosing an active conversation among them
// would be a guess. Armed launch captures are never consumed here — they
// belong to thread/started broadcasts. Best-effort: any failure leaves
// threads to recover on their next broadcast.
func (o *Observer) reconcile(ctx context.Context, conn *rpcConn) {
	callCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	result, err := conn.call(callCtx, "thread/loaded/list", map[string]any{})
	if err != nil {
		o.logger.Debug("codex loaded-thread walk unavailable", "error", err)
		return
	}
	var loaded struct {
		Data []string `json:"data"`
	}
	if err := json.Unmarshal(result, &loaded); err != nil || len(loaded.Data) == 0 {
		return
	}
	if len(loaded.Data) > reconcileCap {
		o.logger.Warn("codex loaded-thread walk truncated", "loaded", len(loaded.Data), "cap", reconcileCap)
		loaded.Data = loaded.Data[:reconcileCap]
	}

	byCwd := map[string][]loadedRoot{}
	for _, id := range loaded.Data {
		readCtx, cancelRead := context.WithTimeout(ctx, connectTimeout)
		result, err := conn.call(readCtx, "thread/read", map[string]any{"threadId": id})
		cancelRead()
		if err != nil {
			continue
		}
		var p struct {
			Thread struct {
				ID             string          `json:"id"`
				Cwd            string          `json:"cwd"`
				ParentThreadID json.RawMessage `json:"parentThreadId"`
				Status         json.RawMessage `json:"status"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(result, &p); err != nil || p.Thread.ID == "" || isJSONString(p.Thread.ParentThreadID) {
			continue
		}
		one := loadedRoot{id: p.Thread.ID, cwd: p.Thread.Cwd, status: p.Thread.Status}
		if o.reattachMapped(ctx, one) {
			continue
		}
		byCwd[p.Thread.Cwd] = append(byCwd[p.Thread.Cwd], one)
	}
	for cwd, roots := range byCwd {
		if len(roots) != 1 {
			o.logger.Debug("codex loaded threads ambiguous for directory", "cwd", cwd, "roots", len(roots))
			continue
		}
		o.bindThread(ctx, roots[0].id, roots[0].cwd, false)
		if len(roots[0].status) > 0 {
			o.observeStatus(ctx, roots[0].id, statusToThreadStatus(roots[0].status))
		}
	}
}

// loadedRoot is one non-subagent thread from the loaded-thread walk.
type loadedRoot struct {
	id, cwd string
	status  json.RawMessage
}

// reattachMapped re-activates a loaded root the identity mapping already
// ties to a terminal, reporting whether it was handled. The recorded
// terminal must still be holdable (not exited, missing, or deleted).
func (o *Observer) reattachMapped(ctx context.Context, one loadedRoot) bool {
	_, terminalID, known := o.threads.LookupIdentity("codex", one.id)
	if !known {
		return false
	}
	if terminalID != "" {
		terminal, err := o.terminals.Get(terminalID)
		if err == nil && terminalHoldable(terminal) {
			if _, err := o.threads.ObserveSession(ctx, threads.SessionObservation{
				Agent:      "codex",
				ProviderID: one.id,
				TerminalID: terminalID,
				ProjectID:  terminal.ProjectID,
				At:         o.now(),
				Metadata:   threads.Metadata{Cwd: one.cwd},
			}); err != nil {
				o.logger.Warn("recording codex session observation", "error", err)
				return true
			}
			o.mu.Lock()
			if _, tracked := o.observed[one.id]; !tracked {
				o.observed[one.id] = api.ThreadUnknown
			}
			o.mu.Unlock()
			if len(one.status) > 0 {
				o.observeStatus(ctx, one.id, statusToThreadStatus(one.status))
			}
		}
	}
	// Mapped but its terminal left: the record persists; nothing honest
	// to re-activate.
	return true
}

// teardown reacts to a lost connection: every thread this connection was
// observing coerces to unknown, armed captures fail closed, and the Run
// loop reconnects.
func (o *Observer) teardown(ctx context.Context, conn *rpcConn, reason error) {
	o.mu.Lock()
	if o.conn != conn {
		// A stale socket must not touch live state.
		o.mu.Unlock()
		return
	}
	o.conn = nil
	observed := o.observed
	o.observed = map[string]api.ThreadStatus{}
	o.captures = nil
	o.mu.Unlock()
	o.logger.Warn("codex app-server connection lost", "error", reason)
	for providerID, last := range observed {
		// Only unverifiable live states coerce; idle (and error) persist
		// when unobserved, the same rule the threads domain applies.
		if !liveStatus(last) {
			continue
		}
		if err := o.threads.ObserveStatus(ctx, threads.StatusObservation{
			Agent: "codex", ProviderID: providerID, At: o.now(), Status: api.ThreadUnknown,
		}); err != nil {
			o.logger.Warn("coercing codex thread on teardown", "error", err)
		}
	}
}

// terminalHoldable reports whether a terminal can still hold a
// conversation: anything but definitively gone. Mirrors the threads
// sweep — unreachable is unverifiable, not evidence of leaving.
func terminalHoldable(terminal api.Terminal) bool {
	return terminal.Status != api.TerminalExited && terminal.Status != api.TerminalMissing
}

func liveStatus(status api.ThreadStatus) bool {
	switch status {
	case api.ThreadWorking, api.ThreadWaitingForInput, api.ThreadWaitingForPermission:
		return true
	}
	return false
}

// handleNotification dispatches one server broadcast.
func (o *Observer) handleNotification(ctx context.Context, method string, params json.RawMessage) {
	switch method {
	case "thread/started":
		var p struct {
			Thread struct {
				ID             string          `json:"id"`
				Cwd            string          `json:"cwd"`
				ParentThreadID json.RawMessage `json:"parentThreadId"`
			} `json:"thread"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.Thread.ID == "" {
			return
		}
		// Subagent guard: a descendant's broadcast must never bind a
		// terminal.
		if isJSONString(p.Thread.ParentThreadID) {
			return
		}
		o.bindThread(ctx, p.Thread.ID, p.Thread.Cwd, true)
	case "thread/status/changed":
		var p struct {
			ThreadID string          `json:"threadId"`
			Status   json.RawMessage `json:"status"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.ThreadID == "" {
			return
		}
		o.observeStatus(ctx, p.ThreadID, statusToThreadStatus(p.Status))
	}
}

// captureThread binds a broadcast conversation to a terminal: the oldest
// armed capture with the exact cwd wins (a launch); with none armed, a
// single running codex terminal at that directory identifies an in-TUI
// switch (/new, /fork, /resume). Ambiguity drops the capture — the
// observer only follows evidence, never guesses between terminals.
//
// Accepted looseness: the shared server broadcasts every client's roots,
// so a non-ATC --remote client working in the same directory as exactly
// one ATC codex terminal would be attributed to it. Without a per-TUI
// relay (deliberately out of ATC-255's scope) cwd is the only correlation
// evidence, and dropping unarmed attribution entirely would forfeit
// in-TUI switch capture.
func (o *Observer) bindThread(ctx context.Context, providerID, cwd string, allowArmed bool) {
	now := o.now()
	terminalID := ""
	if allowArmed {
		o.mu.Lock()
		o.pruneCaptures(now)
		for i, armed := range o.captures {
			if armed.cwd == cwd {
				terminalID = armed.terminalID
				o.captures = append(o.captures[:i], o.captures[i+1:]...)
				break
			}
		}
		o.mu.Unlock()
	}

	if terminalID == "" {
		var candidates []string
		for _, terminal := range o.terminals.List("") {
			if terminal.Agent == "codex" && terminal.Status == api.TerminalRunning && terminal.Directory == cwd {
				candidates = append(candidates, terminal.ID)
			}
		}
		if len(candidates) != 1 {
			o.logger.Debug("codex thread not attributable", "candidates", len(candidates), "cwd", cwd)
			return
		}
		terminalID = candidates[0]
	}

	terminal, err := o.terminals.Get(terminalID)
	if err != nil || !terminalHoldable(terminal) {
		// A capture whose terminal definitively left (failed launch,
		// exited TUI, deleted record) must not bind a conversation to it.
		// A transiently unreachable terminal is no evidence of leaving —
		// the same rule the threads sweep applies.
		o.logger.Warn("codex capture for a terminal that has left dropped", "terminal", terminalID)
		return
	}
	metadata := threads.Metadata{Cwd: cwd, Title: o.threadPreview(ctx, providerID)}
	if _, err := o.threads.ObserveSession(ctx, threads.SessionObservation{
		Agent:      "codex",
		ProviderID: providerID,
		TerminalID: terminalID,
		ProjectID:  terminal.ProjectID,
		At:         now,
		Metadata:   metadata,
	}); err != nil {
		o.logger.Warn("recording codex session observation", "error", err)
		return
	}
	o.mu.Lock()
	if _, tracked := o.observed[providerID]; !tracked {
		o.observed[providerID] = api.ThreadUnknown
	}
	o.mu.Unlock()
}

func (o *Observer) observeStatus(ctx context.Context, providerID string, status api.ThreadStatus) {
	// The shared server broadcasts every client's threads; only mapped
	// conversations reach the seam or the teardown set.
	if _, _, known := o.threads.LookupIdentity("codex", providerID); !known {
		return
	}
	if err := o.threads.ObserveStatus(ctx, threads.StatusObservation{
		Agent: "codex", ProviderID: providerID, At: o.now(), Status: status,
	}); err != nil {
		o.logger.Warn("recording codex status observation", "error", err)
		return
	}
	o.mu.Lock()
	o.observed[providerID] = status
	o.mu.Unlock()
}

// threadPreview reads the thread's preview — the only first-prompt
// evidence a passive socket can reach — as the observed default title.
// Best-effort: any failure is an empty title.
func (o *Observer) threadPreview(ctx context.Context, providerID string) string {
	o.mu.Lock()
	conn := o.conn
	o.mu.Unlock()
	if conn == nil {
		return ""
	}
	callCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	result, err := conn.call(callCtx, "thread/read", map[string]any{"threadId": providerID})
	if err != nil {
		return ""
	}
	var p struct {
		Thread struct {
			Preview string `json:"preview"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &p); err != nil {
		return ""
	}
	return agents.CondenseTitle(p.Thread.Preview)
}

// pruneCaptures drops armed captures past the window. Callers hold mu.
func (o *Observer) pruneCaptures(now time.Time) {
	kept := o.captures[:0]
	for _, armed := range o.captures {
		if now.Sub(armed.armedAt) <= captureWindow {
			kept = append(kept, armed)
		}
	}
	o.captures = kept
}

// statusToThreadStatus maps the wire status shape to the thread
// vocabulary; anything unrecognized is honestly unknown.
func statusToThreadStatus(raw json.RawMessage) api.ThreadStatus {
	var status struct {
		Type        string   `json:"type"`
		ActiveFlags []string `json:"activeFlags"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return api.ThreadUnknown
	}
	switch status.Type {
	case "idle":
		return api.ThreadIdle
	case "active":
		for _, flag := range status.ActiveFlags {
			switch flag {
			case "waitingOnUserInput":
				return api.ThreadWaitingForInput
			case "waitingOnApproval":
				return api.ThreadWaitingForPermission
			}
		}
		return api.ThreadWorking
	case "systemError":
		// A faulted thread is error, not mere lack of evidence.
		return api.ThreadError
	}
	return api.ThreadUnknown
}

func isJSONString(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil
}

// probeSocket answers whether anything completes a WebSocket upgrade on
// the control socket — nothing more; the initialize handshake right after
// is what gates use.
func probeSocket(ctx context.Context, socketPath string) bool {
	probeCtx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	ws, err := dialSocket(probeCtx, socketPath)
	if err != nil {
		return false
	}
	_ = ws.Close(websocket.StatusNormalClosure, "probe")
	return true
}

// dialSocket opens the WebSocket over the unix control socket.
func dialSocket(ctx context.Context, socketPath string) (*websocket.Conn, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}}
	ws, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(maxMessageBytes)
	return ws, nil
}

// rpcConn is the JSON-RPC framing over one WebSocket: numbered requests
// matched to replies, notifications dispatched to the observer, and a
// close callback for teardown.
type rpcConn struct {
	ws     *websocket.Conn
	logger *slog.Logger

	onNotification func(method string, params json.RawMessage)
	onClose        func(reason error)
	// notifications decouples dispatch from the read loop: a handler may
	// issue RPCs (thread/read) whose replies the read loop must stay free
	// to receive, while one dispatcher goroutine preserves broadcast
	// order. Overflow fails the connection — the observer re-derives on
	// reconnect rather than silently losing transitions. done ends both
	// loops; the channel itself is never closed (the read loop must never
	// race a send against it).
	notifications chan notification
	done          chan struct{}
	// dispatcherDone closes when the dispatch loop has fully exited; fail
	// joins it before teardown, so no handler can apply evidence after
	// teardown coerced everything to unknown.
	dispatcherDone chan struct{}

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcReply
	closed  bool
}

type notification struct {
	method string
	params json.RawMessage
}

// notificationBuffer bounds the dispatch queue.
const notificationBuffer = 256

type rpcReply struct {
	result json.RawMessage
	err    error
}

type rpcMessage struct {
	ID     *int64          `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

func newRPCConn(ws *websocket.Conn, logger *slog.Logger) *rpcConn {
	return &rpcConn{
		ws: ws, logger: logger, nextID: 1,
		pending:        map[int64]chan rpcReply{},
		notifications:  make(chan notification, notificationBuffer),
		done:           make(chan struct{}),
		dispatcherDone: make(chan struct{}),
	}
}

func (c *rpcConn) start() {
	go c.readLoop()
	go c.dispatchLoop()
}

// dispatchLoop delivers notifications in arrival order until fail ends
// the connection.
func (c *rpcConn) dispatchLoop() {
	defer close(c.dispatcherDone)
	for {
		select {
		case <-c.done:
			return
		case n := <-c.notifications:
			if c.onNotification != nil {
				c.onNotification(n.method, n.params)
			}
		}
	}
}

func (c *rpcConn) readLoop() {
	ctx := context.Background()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.fail(err)
			return
		}
		var message rpcMessage
		if err := json.Unmarshal(data, &message); err != nil {
			c.fail(fmt.Errorf("invalid frame from codex app-server: %w", err))
			return
		}
		switch {
		case message.ID != nil && message.Method == "":
			c.mu.Lock()
			waiter, ok := c.pending[*message.ID]
			delete(c.pending, *message.ID)
			c.mu.Unlock()
			if ok {
				reply := rpcReply{result: message.Result}
				if message.Error != nil {
					reply.err = message.Error
				}
				waiter <- reply
			}
		case message.Method != "" && message.ID == nil:
			select {
			case c.notifications <- notification{method: message.Method, params: message.Params}:
			case <-c.done:
				return
			default:
				c.fail(errors.New("codex app-server notifications overflowed the dispatch queue"))
				return
			}
		default:
			// A server request (id + method). ATC is a passive observer
			// and answers nothing; codex does not direct requests at
			// observer connections.
			c.logger.Debug("unexpected codex server request ignored", "method", message.Method)
		}
	}
}

// fail ends the connection: every pending call errors and onClose fires
// exactly once.
func (c *rpcConn) fail(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = map[int64]chan rpcReply{}
	c.mu.Unlock()
	for _, waiter := range pending {
		waiter <- rpcReply{err: reason}
	}
	close(c.done)
	_ = c.ws.CloseNow()
	// Join the dispatcher before teardown: an in-flight handler's RPCs
	// fail fast (pending was just flushed), so this is bounded, and it
	// guarantees no evidence lands after teardown coerces to unknown.
	<-c.dispatcherDone
	if c.onClose != nil {
		c.onClose(reason)
	}
}

func (c *rpcConn) close() {
	c.fail(errors.New("connection closed"))
}

func (c *rpcConn) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, errors.New("connection closed")
	}
	id := c.nextID
	c.nextID++
	waiter := make(chan rpcReply, 1)
	c.pending[id] = waiter
	c.mu.Unlock()

	if err := c.write(ctx, rpcMessage{ID: &id, Method: method, Params: mustMarshal(params)}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, err
	}
	// The caller's context is the deadline; every caller bounds it.
	select {
	case reply := <-waiter:
		return reply.result, reply.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, ctx.Err()
	}
}

// notify sends one notification; the caller's context bounds the write —
// a stalled server must not block a launch on an unbounded send.
func (c *rpcConn) notify(ctx context.Context, method string, params any) error {
	return c.write(ctx, rpcMessage{Method: method, Params: mustMarshal(params)})
}

func (c *rpcConn) write(ctx context.Context, message rpcMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

func mustMarshal(params any) json.RawMessage {
	data, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return data
}
