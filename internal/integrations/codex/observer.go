package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/threads"
)

const (
	// connectTimeout bounds one dial + initialize handshake.
	connectTimeout = 5 * time.Second
	// callTimeout bounds one request while observing (thread/read).
	callTimeout = 5 * time.Second
	// startWait is how long a launch waits for a server it just started
	// to answer before failing.
	startWait = 5 * time.Second
	// startPoll paces connection attempts while waiting on a start.
	startPoll = 200 * time.Millisecond
	// backoffMin and backoffMax bound the reconnect loop's pacing.
	backoffMin = time.Second
	backoffMax = 30 * time.Second
	// titleReads bounds the thread/read calls spent on a minted thread's
	// title: the preview lands with the first prompt, so the read at
	// minting usually has it and one later retry covers a late one.
	titleReads = 2
)

// errNoServer marks a connect that found nothing answering on the
// control socket — the one failure a launch may answer by starting a
// server. A server that answers but refuses the handshake is not it.
var errNoServer = errors.New("no codex app-server answering")

// ThreadObserver is the seam into the threads domain: neutral
// observations in, no provider vocabulary out.
type ThreadObserver interface {
	ObserveSession(ctx context.Context, o threads.SessionObservation) (string, error)
	ObserveStatus(ctx context.Context, o threads.StatusObservation) error
	Deactivate(ctx context.Context, terminalID string)
}

// TerminalReader resolves a terminal's record: evidence applies only
// while the terminal is live, and its directory is the origin of a
// conversation whose announcement carried no cwd.
type TerminalReader interface {
	Get(id string) (api.Terminal, error)
}

// ObserverOptions wires an Observer.
type ObserverOptions struct {
	// CodexHome locates the shared server's control socket and log file
	// (CodexHome()).
	CodexHome string
	// ClientVersion is reported in the initialize handshake.
	ClientVersion string
	Threads       ThreadObserver
	Terminals     TerminalReader
	Logger        *slog.Logger
	Now           func() time.Time
	// Start launches the shared server when a launch finds none
	// answering; nil selects the real detached spawn. A test seam.
	Start func(ctx context.Context) error
}

// Observer is ATC's one client connection to the shared Codex app-server
// and everything it derives from it: pending launches waiting for their
// thread announcement, the private pairings of terminals to Codex
// threads, and the translation of thread statuses into thread
// observations. It never originates a conversation method.
//
// Locks, outermost first; no IO under mu:
//
//	connectMu serializes the connected-check with the dial+install, so a
//	          launch racing the Run loop can never install two
//	          connections.
//	handleMu  serializes evidence application — a notification, a
//	          reconcile read, a teardown, a Forget — so pairing state and
//	          the observations it produces move as one unit. IO (a
//	          thread/read, the threads seam) happens under it.
//	mu        guards the connection pointer, pending launches, directory
//	          reservations, and pairings.
type Observer struct {
	socket             string
	tuiCapabilitiesDir string
	clientVersion      string
	threads            ThreadObserver
	terminals          TerminalReader
	logger             *slog.Logger
	now                func() time.Time
	start              func(ctx context.Context) error

	// Production cadences; tests shrink them.
	window, grace          time.Duration
	startWait, startPoll   time.Duration
	backoffMin, backoffMax time.Duration
	callTimeout            time.Duration

	seq atomic.Uint64
	// reads tracks the detached read goroutines (a connection's reconcile,
	// a binding's follow-up read). Each is added under mu while stopped is
	// false, and Run sets stopped before joining them — the ordering
	// WaitGroup requires between an Add from zero and a Wait.
	reads sync.WaitGroup

	connectMu sync.Mutex
	handleMu  sync.Mutex

	mu      sync.Mutex
	conn    *rpcConn
	stopped bool
	// slots holds each canonical directory with a launch in progress —
	// reserved at preparation, armed at Command, released when the
	// window closes. Same-directory launches queue on the slot.
	slots map[string]*launchSlot
	// held maps Codex thread id → the terminal paired with it.
	held map[string]*pairing
}

// pairing is one terminal ↔ Codex thread association, private to the
// observer until minted.
type pairing struct {
	terminalID string
	threadID   string
	// cwd and title come from the thread announcement (fresh launches).
	cwd   string
	title string
	// resumed marks a thread already minted by an open: it establishes on
	// its first evidence of any kind, not on a first prompt.
	resumed bool
	// established records that the threads domain has accepted the
	// session observation for this terminal — for a fresh launch, that
	// the thread is minted.
	established bool
	// promptSeen records a live status heard while the launch window was
	// still open: the thread had its prompt, so the next status mints it
	// even when the turn already finished.
	promptSeen bool
	// reads counts thread/read attempts at a title; titled stops them.
	reads  int
	titled bool
	// last is the last status forwarded; teardown coerces only live
	// ones.
	last api.ThreadStatus
	// seq is the wire sequence of the last evidence applied; older
	// evidence that crossed a reply is skipped.
	seq uint64
}

func NewObserver(opts ObserverOptions) *Observer {
	if opts.Threads == nil || opts.Terminals == nil {
		panic("codex.NewObserver: Threads and Terminals must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.ClientVersion == "" {
		opts.ClientVersion = "dev"
	}
	o := &Observer{
		socket:             ControlSocketPath(opts.CodexHome),
		tuiCapabilitiesDir: tuiCapabilitiesDir(opts.CodexHome),
		clientVersion:      opts.ClientVersion,
		threads:            opts.Threads,
		terminals:          opts.Terminals,
		logger:             opts.Logger,
		now:                opts.Now,
		start:              opts.Start,
		window:             launchWindow,
		grace:              launchGrace,
		startWait:          startWait,
		startPoll:          startPoll,
		backoffMin:         backoffMin,
		backoffMax:         backoffMax,
		callTimeout:        callTimeout,
		slots:              map[string]*launchSlot{},
		held:               map[string]*pairing{},
	}
	if o.start == nil {
		o.start = func(context.Context) error { return startServer(opts.CodexHome) }
	}
	return o
}

// Run keeps the connection for the life of the ATC server: connect,
// reconcile, consume until the connection drops, coerce, reconnect with
// backoff. It never starts a server — with none answering it retries
// quietly until a launch starts one or another client does.
func (o *Observer) Run(ctx context.Context) {
	defer o.stop()
	backoff := o.backoffMin
	for {
		conn, err := o.ensureConnected(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			o.logger.Debug("codex app-server not reachable", "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, o.backoffMax)
			continue
		}
		connectedAt := o.now()
		select {
		case <-ctx.Done():
			return
		case <-conn.done:
		}
		if o.now().Sub(connectedAt) >= o.backoffMin {
			// The connection did real work; retry at once — a restarted
			// server is usually already back — and start the backoff
			// fresh.
			backoff = o.backoffMin
			continue
		}
		// A connection that died at once is a server flapping: pace the
		// successor instead of spinning through dial, handshake, and
		// reconcile.
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, o.backoffMax)
	}
}

// stop ends observation: no further connection or read starts, the live
// connection closes, and every read goroutine is joined. connectMu
// serializes it with a connect in flight — a launch dialing during
// shutdown must not install a connection nobody will close.
func (o *Observer) stop() {
	o.connectMu.Lock()
	o.mu.Lock()
	o.stopped = true
	conn := o.conn
	o.mu.Unlock()
	o.connectMu.Unlock()
	if conn != nil {
		conn.close()
		<-conn.finished
	}
	o.reads.Wait()
}

// spawnRead starts a detached read goroutine, unless observation has
// stopped. Caller holds mu.
func (o *Observer) spawnRead(read func()) {
	if o.stopped {
		return
	}
	o.reads.Add(1)
	go func() {
		defer o.reads.Done()
		read()
	}()
}

// ensureConnected returns the live connection, dialing one when none is
// up. The connected-check and the dial+install run under connectMu. A
// connection that has failed but not finished retiring (its teardown is
// coercing statuses) is waited out first, so no caller ever proceeds on
// a dead connection and no successor is installed under it.
func (o *Observer) ensureConnected(ctx context.Context) (*rpcConn, error) {
	o.connectMu.Lock()
	defer o.connectMu.Unlock()
	o.mu.Lock()
	conn, stopped := o.conn, o.stopped
	o.mu.Unlock()
	if stopped {
		return nil, errors.New("codex observation stopped")
	}
	if conn != nil && !conn.isClosed() {
		return conn, nil
	}
	if conn != nil {
		select {
		case <-conn.finished:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return o.connect(ctx)
}

// connect dials, runs the initialize handshake, installs the connection,
// and starts its reconcile. Caller holds connectMu.
func (o *Observer) connect(ctx context.Context) (*rpcConn, error) {
	dialCtx, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	ws, err := dialSocket(dialCtx, o.socket)
	if err != nil {
		return nil, fmt.Errorf("%w on %s: %w", errNoServer, o.socket, err)
	}
	conn := newRPCConn(ws, &o.seq, o.now, o.logger)
	// Callbacks run detached from whichever request's context connected:
	// they must outlive it.
	background := context.WithoutCancel(ctx)
	conn.onAnnouncement = o.handleAnnouncement
	conn.onNotification = func(n notification) { o.handleNotification(background, n) }
	conn.onClose = func(reason error) { o.teardown(background, conn, reason) }
	// Installed before the loops start: a connection that dies during the
	// handshake must find itself installed when its teardown runs.
	o.mu.Lock()
	o.conn = conn
	o.mu.Unlock()
	conn.start()

	if _, _, err := conn.call(dialCtx, "initialize", map[string]any{
		"clientInfo":   map[string]any{"name": "atc", "title": "ATC", "version": o.clientVersion},
		"capabilities": map[string]any{"experimentalApi": true},
	}); err != nil {
		conn.close()
		return nil, fmt.Errorf("initialize: %w", err)
	}
	if err := conn.notify(dialCtx, "initialized", map[string]any{}); err != nil {
		conn.close()
		return nil, err
	}
	o.logger.Info("codex app-server connected")
	// Threads paired before this connection have moved unheard: read each
	// so live work returns to observation. Async — a launch must not wait
	// behind the reads.
	o.mu.Lock()
	o.spawnRead(func() { o.reconcile(background, conn) })
	o.mu.Unlock()
	return conn, nil
}

// teardown reacts to a lost connection: every established pairing in a
// live state coerces to unknown until a reconcile restores it. Pairings
// and pending launches survive — they are facts about terminals, not
// about the connection; a pending launch whose announcement the gap
// swallowed simply closes with no candidate.
func (o *Observer) teardown(ctx context.Context, conn *rpcConn, reason error) {
	o.handleMu.Lock()
	defer o.handleMu.Unlock()
	o.mu.Lock()
	if o.conn != conn {
		o.mu.Unlock()
		return
	}
	o.conn = nil
	var live []*pairing
	for _, p := range o.held {
		if p.established && isLive(p.last) {
			live = append(live, p)
		}
	}
	o.mu.Unlock()
	if !errors.Is(reason, errConnClosed) {
		o.logger.Warn("codex app-server connection lost", "error", reason)
	}
	for _, p := range live {
		if _, ok := o.terminalLive(ctx, p); !ok {
			continue
		}
		if err := o.threads.ObserveStatus(ctx, threads.StatusObservation{
			IntegrationID: ID, ProviderID: p.threadID, At: o.now(), Status: api.ThreadUnknown,
		}); err != nil {
			o.logger.Warn("coercing codex thread on disconnect", "error", err)
			continue
		}
		o.mu.Lock()
		p.last = api.ThreadUnknown
		o.mu.Unlock()
	}
}

// terminalLive is the liveness gate every application of evidence runs:
// the pairing's terminal must exist and not have definitively left. A
// terminal that is gone ends the pairing — deactivated if it was
// established — so no later evidence (an iOS-driven turn) lands against
// a closed terminal. A record not there yet (a resume paired at Command,
// before its insert) only drops this evidence. Caller holds handleMu.
func (o *Observer) terminalLive(ctx context.Context, p *pairing) (api.Terminal, bool) {
	terminal, err := o.terminals.Get(p.terminalID)
	if err != nil {
		if p.established {
			o.forget(p.threadID, p)
		}
		return api.Terminal{}, false
	}
	if terminal.Status == api.TerminalExited || terminal.Status == api.TerminalMissing {
		if p.established {
			o.threads.Deactivate(ctx, p.terminalID)
		}
		o.forget(p.threadID, p)
		return api.Terminal{}, false
	}
	return terminal, true
}

// reconcile reads every paired thread on a fresh connection and applies
// what it finds, in wire order against any notification that crosses
// the read. Best-effort: a transport failure ends the pass, and the
// connection's own failure path takes over.
func (o *Observer) reconcile(ctx context.Context, conn *rpcConn) {
	o.mu.Lock()
	ids := make([]string, 0, len(o.held))
	for id := range o.held {
		ids = append(ids, id)
	}
	o.mu.Unlock()
	for _, id := range ids {
		o.readAndApply(ctx, conn, id)
	}
}

// readAndApply reads one thread's status and applies it as evidence. A
// failed read — refused by the server or lost in transport — is no
// evidence: nothing is applied, and the thread's next notification, the
// next reconcile, or the terminal sweep covers it.
func (o *Observer) readAndApply(ctx context.Context, conn *rpcConn, threadID string) {
	callCtx, cancel := context.WithTimeout(ctx, o.callTimeout)
	defer cancel()
	result, seq, err := conn.call(callCtx, "thread/read", map[string]any{"threadId": threadID})
	if err != nil {
		o.logger.Debug("codex thread read failed", "thread", threadID, "error", err)
		return
	}
	var p struct {
		Thread struct {
			Status  json.RawMessage `json:"status"`
			Name    string          `json:"name"`
			Preview string          `json:"preview"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &p); err != nil {
		return
	}
	e := evidenceFrom(p.Thread.Status, seq)
	e.conn = conn
	e.title = titleFrom(p.Thread.Name, p.Thread.Preview)
	o.applyEvidence(ctx, threadID, e)
}

// titleFrom is the observed default title: the thread's name, else its
// condensed preview (the first prompt).
func titleFrom(name, preview string) string {
	if name != "" {
		return name
	}
	return integrations.CondenseTitle(preview)
}

// handleAnnouncement runs on the read loop: thread/started is stamped and
// matched at receipt.
func (o *Observer) handleAnnouncement(n notification) {
	var p struct {
		Thread struct {
			ID      string          `json:"id"`
			Cwd     string          `json:"cwd"`
			Source  json.RawMessage `json:"source"`
			Name    string          `json:"name"`
			Preview string          `json:"preview"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(n.params, &p); err != nil || p.Thread.ID == "" {
		return
	}
	o.announced(candidate{
		threadID: p.Thread.ID, cwd: p.Thread.Cwd, title: titleFrom(p.Thread.Name, p.Thread.Preview),
		source: sourceKind(p.Thread.Source), at: n.at, seq: n.seq,
	})
}

// handleNotification dispatches one server broadcast (thread/started
// never reaches here — the read loop handles it at receipt).
func (o *Observer) handleNotification(ctx context.Context, n notification) {
	switch n.method {
	case "thread/status/changed":
		var p struct {
			ThreadID string          `json:"threadId"`
			Status   json.RawMessage `json:"status"`
		}
		if err := json.Unmarshal(n.params, &p); err != nil || p.ThreadID == "" {
			return
		}
		o.applyEvidence(ctx, p.ThreadID, evidenceFrom(p.Status, n.seq))
	case "thread/closed":
		var p struct {
			ThreadID string `json:"threadId"`
		}
		if err := json.Unmarshal(n.params, &p); err != nil || p.ThreadID == "" {
			return
		}
		o.applyEvidence(ctx, p.ThreadID, evidence{kind: evidenceClosed, seq: n.seq})
	}
}

// sourceKind reduces a SessionSource — a plain string for first-party
// clients ("cli", "vscode", ...), an object for subagents and custom
// sources — to the string kind, empty for anything that is not one.
func sourceKind(raw json.RawMessage) string {
	var kind string
	if json.Unmarshal(raw, &kind) == nil {
		return kind
	}
	return ""
}

// evidence is one status fact about a Codex thread.
type evidence struct {
	kind   evidenceKind
	status api.ThreadStatus
	seq    uint64
	// conn is the connection a read came over; a read that outlived its
	// connection's teardown is stale. Notifications carry none: their
	// dispatcher is joined before teardown runs.
	conn *rpcConn
	// title rides along on reads (name, else condensed preview).
	title string
}

type evidenceKind int

const (
	// evidenceStatus carries a live or settled status.
	evidenceStatus evidenceKind = iota
	// evidenceClosed is thread/closed: the conversation left the server.
	evidenceClosed
	// evidenceNotLoaded is a reconcile finding the thread not loaded.
	evidenceNotLoaded
)

// evidenceFrom maps the wire status shape to the thread vocabulary:
//
//	idle                          → idle
//	active, no flags              → working
//	active, waitingOnApproval     → waiting_for_permission
//	active, waitingOnUserInput    → waiting_for_input
//	systemError                   → error
//	notLoaded                     → (session end)
//
// Anything unrecognized is honestly unknown.
func evidenceFrom(raw json.RawMessage, seq uint64) evidence {
	var status struct {
		Type        string   `json:"type"`
		ActiveFlags []string `json:"activeFlags"`
	}
	if err := json.Unmarshal(raw, &status); err != nil {
		return evidence{kind: evidenceStatus, status: api.ThreadUnknown, seq: seq}
	}
	switch status.Type {
	case "idle":
		return evidence{kind: evidenceStatus, status: api.ThreadIdle, seq: seq}
	case "active":
		// The flags are a set; should both ever appear, a question to the
		// user outranks a permission prompt (the threads domain's own
		// ranking for Claude).
		mapped := api.ThreadWorking
		for _, flag := range status.ActiveFlags {
			switch flag {
			case "waitingOnUserInput":
				mapped = api.ThreadWaitingForInput
			case "waitingOnApproval":
				if mapped != api.ThreadWaitingForInput {
					mapped = api.ThreadWaitingForPermission
				}
			}
		}
		return evidence{kind: evidenceStatus, status: mapped, seq: seq}
	case "systemError":
		return evidence{kind: evidenceStatus, status: api.ThreadError, seq: seq}
	case "notLoaded":
		return evidence{kind: evidenceNotLoaded, seq: seq}
	}
	return evidence{kind: evidenceStatus, status: api.ThreadUnknown, seq: seq}
}

// applyEvidence is the one path evidence takes into the threads domain.
// Only a thread paired with a live terminal gets through; everything
// else — other clients' threads, threads whose terminal has left — is
// dropped entirely, no status, metadata, or last-evidence update. A
// fresh pairing mints its thread at its first live status; a resumed one
// establishes on any status; an established one forwards. Session end
// (closed, or not loaded on reconcile) deactivates the terminal.
func (o *Observer) applyEvidence(ctx context.Context, threadID string, e evidence) {
	o.handleMu.Lock()
	defer o.handleMu.Unlock()

	o.mu.Lock()
	p, ok := o.held[threadID]
	if !ok && e.kind == evidenceStatus && isLive(e.status) {
		o.candidateHeardLive(threadID)
	}
	stale := ok && ((e.seq != 0 && e.seq < p.seq) || (e.conn != nil && e.conn != o.conn))
	if ok && !stale {
		p.seq = max(p.seq, e.seq)
	}
	o.mu.Unlock()
	if !ok {
		o.logger.Debug("codex evidence for an unpaired thread dropped", "thread", threadID)
		return
	}
	if stale {
		// Older on the wire than evidence already applied (a notification
		// that crossed a reconcile read), or a read whose connection has
		// since been torn down and its statuses coerced.
		o.logger.Debug("stale codex evidence dropped", "thread", threadID)
		return
	}
	terminal, ok := o.terminalLive(ctx, p)
	if !ok {
		return
	}

	switch e.kind {
	case evidenceClosed, evidenceNotLoaded:
		if !p.established {
			if e.kind == evidenceNotLoaded {
				// A resume whose TUI is still loading, or a fresh launch
				// whose thread already went — nothing to record either way;
				// the pairing waits for a definitive signal.
				return
			}
			// Exit before the first prompt: no record, by design.
			o.forget(threadID, p)
			return
		}
		o.threads.Deactivate(ctx, p.terminalID)
		o.forget(threadID, p)
		return
	}

	if !p.established && !p.resumed && !isLive(e.status) && !p.promptSeen {
		// Held, not yet minted: only the first prompt mints.
		return
	}
	metadata := threads.Metadata{}
	if !p.established && !p.resumed {
		metadata.Cwd = p.cwd
	}
	if !p.resumed && !p.titled {
		metadata.Title = o.resolveTitle(ctx, p, e.title)
	}
	if !p.established {
		// The announcement's cwd is the conversation's origin; a resume
		// pairing carries none and the threads domain ignores origin for
		// a known conversation anyway.
		origin := p.cwd
		if origin == "" {
			origin = terminal.Directory
		}
		if _, err := o.threads.ObserveSession(ctx, threads.SessionObservation{
			IntegrationID: ID, AppID: AppID, AgentID: AgentID, ProviderID: threadID, TerminalID: p.terminalID, InitialDirectory: origin,
			At: o.now(), Status: e.status, Metadata: metadata,
		}); err != nil {
			// A transient failure leaves the pairing unestablished; the
			// next evidence retries rather than silencing the thread.
			o.logger.Warn("recording codex session observation", "terminal", p.terminalID, "error", err)
			return
		}
	} else if err := o.threads.ObserveStatus(ctx, threads.StatusObservation{
		IntegrationID: ID, ProviderID: threadID, At: o.now(), Status: e.status, Metadata: metadata,
	}); err != nil {
		o.logger.Warn("recording codex status observation", "error", err)
		return
	}
	o.mu.Lock()
	p.established = true
	p.last = e.status
	p.titled = p.titled || metadata.Title != ""
	o.mu.Unlock()
}

// resolveTitle returns the title a fresh pairing's thread should carry:
// the announcement's, else what the evidence's own read returned, else
// one bounded read of the server's current name or preview — the
// preview lands with the first prompt, so it is usually there by the
// first live status. An observed title only ever fills an untitled
// thread, so a retry on later evidence is safe. Caller holds handleMu.
func (o *Observer) resolveTitle(ctx context.Context, p *pairing, fromRead string) string {
	if p.title != "" {
		return p.title
	}
	if fromRead != "" {
		return fromRead
	}
	o.mu.Lock()
	conn := o.conn
	attempts := p.reads
	p.reads++
	o.mu.Unlock()
	if conn == nil || attempts >= titleReads {
		return ""
	}
	callCtx, cancel := context.WithTimeout(ctx, o.callTimeout)
	defer cancel()
	result, _, err := conn.call(callCtx, "thread/read", map[string]any{"threadId": p.threadID})
	if err != nil {
		return ""
	}
	var read struct {
		Thread struct {
			Name    string `json:"name"`
			Preview string `json:"preview"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(result, &read); err != nil {
		return ""
	}
	return titleFrom(read.Thread.Name, read.Thread.Preview)
}

// forget drops a pairing. Caller holds handleMu.
func (o *Observer) forget(threadID string, p *pairing) {
	o.mu.Lock()
	if o.held[threadID] == p {
		delete(o.held, threadID)
	}
	o.mu.Unlock()
}

// Forget drops everything held for a deleted terminal — its pairing and
// any pending launch — wired by the composition root to the terminal
// delete. It is a barrier: it returns only after any in-flight evidence
// application has finished, so no late observation lands over the
// delete's convergence.
func (o *Observer) Forget(terminalID string) {
	o.handleMu.Lock()
	defer o.handleMu.Unlock()
	o.mu.Lock()
	for threadID, p := range o.held {
		if p.terminalID == terminalID {
			delete(o.held, threadID)
		}
	}
	var abandoned *pendingLaunch
	for _, slot := range o.slots {
		if slot.launch != nil && slot.launch.terminalID == terminalID {
			abandoned = slot.launch
		}
	}
	o.mu.Unlock()
	if abandoned != nil {
		o.closeLaunch(abandoned, "terminal deleted")
	}
}

// holdResume pairs a resumed terminal with the thread it reopens
// (Command time, under the terminals commit lock — no IO). The thread is
// already minted; the pairing establishes on its first evidence.
func (o *Observer) holdResume(terminalID, threadID string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.held[threadID] = &pairing{terminalID: terminalID, threadID: threadID, resumed: true, titled: true}
}

// forgetThread drops the pairing for a thread — a resume whose create
// failed after Command.
func (o *Observer) forgetThread(threadID string) {
	o.handleMu.Lock()
	defer o.handleMu.Unlock()
	o.mu.Lock()
	delete(o.held, threadID)
	o.mu.Unlock()
}

// prepareLaunch is the launch's pre-lock work: a live connection to the
// shared server — started when nothing answers — and, for a fresh
// launch, the directory reservation. The returned abort undoes the
// preparation if the create fails.
func (o *Observer) prepareLaunch(ctx context.Context, directory, resumeID string) (func(), error) {
	if _, err := o.ensureConnected(ctx); err != nil {
		if ctx.Err() != nil || !errors.Is(err, errNoServer) {
			// The caller gave up, or something answers but refuses us:
			// neither is a missing server, and starting one would only
			// compete with it.
			return nil, err
		}
		o.logger.Info("codex app-server not answering; starting one", "error", err)
		if err := o.coldStart(ctx); err != nil {
			return nil, err
		}
	}
	if resumeID != "" {
		return func() { o.forgetThread(resumeID) }, nil
	}
	dir := canonical(directory)
	slot, err := o.reserve(ctx, dir)
	if err != nil {
		return nil, err
	}
	return func() { o.abandon(dir, slot) }, nil
}

// coldStart starts the shared server and waits a bounded time for it to
// answer. Codex's own startup lock and stale-socket cleanup handle a race
// with Desktop; a losing starter exits harmlessly and the winner answers.
func (o *Observer) coldStart(ctx context.Context) error {
	if err := o.start(ctx); err != nil {
		return fmt.Errorf("starting codex app-server: %w", err)
	}
	deadline := time.Now().Add(o.startWait)
	for {
		_, err := o.ensureConnected(ctx)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("codex app-server did not answer on %s within %s: %w", o.socket, o.startWait, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(o.startPoll):
		}
	}
}

func isLive(status api.ThreadStatus) bool {
	switch status {
	case api.ThreadWorking, api.ThreadWaitingForInput, api.ThreadWaitingForPermission:
		return true
	}
	return false
}
