package t3code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/threads"
)

const (
	// pollInterval paces the check for a T3 runtime file while none is
	// present — the quiet "not installed / not running" state.
	pollInterval = 5 * time.Second
	// backoffMin and backoffMax bound reconnects after a dropped
	// subscription.
	backoffMin = time.Second
	backoffMax = 30 * time.Second
	// authRetry is how long an auth failure waits before pairing is tried
	// again — never a tight loop, and never a CLI spawn per tick.
	authRetry = 5 * time.Minute
	// httpTimeout bounds each HTTP exchange with T3 and the WebSocket
	// handshake; the subscription itself is unbounded.
	httpTimeout = 10 * time.Second

	resource = "integration"
)

// ThreadObserver is the seam into the threads domain: neutral
// observations in, no T3 vocabulary out.
type ThreadObserver interface {
	ObserveExternal(ctx context.Context, o threads.ExternalObservation) (string, error)
	ArchiveExternalThread(ctx context.Context, integrationID, providerID string) error
	ReleaseIntegration(ctx context.Context, integrationID string)
	UnarchivedProviderIDs(integrationID string) []string
}

// Options wires an Observer.
type Options struct {
	// Home is the T3 home (Home()); SessionPath the 0600 session file.
	Home        string
	SessionPath string
	Threads     ThreadObserver
	Hub         *events.Hub
	Logger      *slog.Logger
	Now         func() time.Time
	// RunCLI runs T3's CLI for pairing; nil selects node against the
	// versioned entrypoint under Home. A test seam.
	RunCLI func(ctx context.Context, args ...string) ([]byte, error)
	// HTTPClient talks to T3; nil selects one with httpTimeout.
	HTTPClient *http.Client
	// ProcessAlive reports whether the runtime file's pid is live; nil
	// selects a signal-0 probe. A test seam.
	ProcessAlive func(pid int) bool
}

// Observer is the Integration's one long-lived connection to the local
// T3 environment and everything it mirrors from it. Run owns discovery,
// pairing, the subscription, and every application of shell state; the
// only state read from other goroutines — the connection report and the
// link inputs — sits under mu.
type Observer struct {
	home        string
	sessionPath string
	threads     ThreadObserver
	hub         *events.Hub
	logger      *slog.Logger
	now         func() time.Time
	runCLI      func(ctx context.Context, args ...string) ([]byte, error)
	httpClient  *http.Client
	alive       func(pid int) bool

	// Production cadences; tests shrink them.
	pollInterval, backoffMin, backoffMax, authRetry time.Duration

	// Run-goroutine state.
	session *session
	// retryAt gates pairing after an auth failure; a T3 restart (a
	// different runtime than last seen) clears it.
	retryAt time.Time
	last    runtime
	shell   shellState
	// skipped holds the T3 threads whose workspace has no local directory,
	// by id, with the reason; settled holds the ones T3 has settled. Both
	// are counted in the connection detail, neither is mirrored.
	skipped map[string]string
	settled map[string]bool

	mu                    sync.Mutex
	connection            api.IntegrationConnection
	origin, environmentID string
}

// shellState is ATC's copy of T3's shell projection, kept so a dropped
// subscription resumes from the last applied sequence and a replay can
// re-establish every hold.
type shellState struct {
	initialized bool
	sequence    uint64
	projects    map[string]projectShell
	threads     map[string]threadShell
}

func New(opts Options) *Observer {
	if opts.Threads == nil || opts.Hub == nil {
		panic("t3code.New: Threads and Hub must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.RunCLI == nil {
		opts.RunCLI = runNodeCLI(opts.Home)
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	if opts.ProcessAlive == nil {
		opts.ProcessAlive = processAlive
	}
	o := &Observer{
		home:         opts.Home,
		sessionPath:  opts.SessionPath,
		threads:      opts.Threads,
		hub:          opts.Hub,
		logger:       opts.Logger,
		now:          opts.Now,
		runCLI:       opts.RunCLI,
		httpClient:   opts.HTTPClient,
		alive:        opts.ProcessAlive,
		pollInterval: pollInterval,
		backoffMin:   backoffMin,
		backoffMax:   backoffMax,
		authRetry:    authRetry,
		skipped:      map[string]string{},
		settled:      map[string]bool{},
	}
	// The report is honest before Run starts: one discovery decides
	// between "not running" and "about to connect".
	if _, err := discover(o.home, o.alive); err != nil {
		o.connection = api.IntegrationConnection{State: api.IntegrationUnavailable, Since: o.now(), Detail: err.Error()}
	} else {
		o.connection = api.IntegrationConnection{State: api.IntegrationConnecting, Since: o.now(), Detail: "T3 Code is running; connecting"}
	}
	return o
}

// Connection reports the Integration's live state.
func (o *Observer) Connection() api.IntegrationConnection {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.connection
}

// Links derives a T3 thread's deep links from the last connection's
// origin and environment; nil until the Integration has connected once.
func (o *Observer) Links(providerID string) *api.ThreadLinks {
	o.mu.Lock()
	defer o.mu.Unlock()
	return links(o.origin, o.environmentID, providerID)
}

// setState records the connection state, publishing integration.updated
// on a transition only; a detail change within one state is silent.
func (o *Observer) setState(state api.IntegrationConnectionState, detail string) {
	o.mu.Lock()
	changed := o.connection.State != state
	if changed {
		o.connection.State = state
		o.connection.Since = o.now()
	}
	o.connection.Detail = detail
	o.mu.Unlock()
	if !changed {
		return
	}
	switch state {
	case api.IntegrationConnected:
		o.logger.Info("t3code connected", "detail", detail)
	case api.IntegrationAuthFailed:
		o.logger.Warn("t3code pairing failed", "detail", detail)
	default:
		o.logger.Debug("t3code state", "state", state, "detail", detail)
	}
	o.hub.Publish(api.EventIntegrationUpdated, resource, ID)
}

// Run keeps the mirror for the life of the ATC server: discover T3, pair
// when needed, subscribe, apply until the subscription drops, release
// every hold, and reconnect with backoff — quietly polling for a runtime
// file while T3 is not running.
func (o *Observer) Run(ctx context.Context) {
	backoff := o.backoffMin
	for {
		state, err := discover(o.home, o.alive)
		if err != nil {
			o.setState(api.IntegrationUnavailable, err.Error())
			o.release(ctx)
			if !wait(ctx, o.pollInterval) {
				return
			}
			continue
		}
		if state != o.last {
			o.last = state
			o.retryAt = time.Time{}
		}
		if o.now().Before(o.retryAt) {
			if !wait(ctx, o.pollInterval) {
				return
			}
			continue
		}
		started := o.now()
		err = o.serve(ctx, state)
		if ctx.Err() != nil {
			return
		}
		o.release(ctx)
		var auth *authError
		var schema *schemaError
		var protocol *protocolError
		switch {
		case errors.As(err, &auth):
			o.setState(api.IntegrationAuthFailed, err.Error())
			o.retryAt = o.now().Add(o.authRetry)
			continue
		case errors.As(err, &schema), errors.As(err, &protocol):
			// The local copy may be inconsistent with what T3 sent; the
			// next connection takes a fresh snapshot.
			o.shell = shellState{}
			o.setState(api.IntegrationUnavailable, err.Error())
		default:
			o.setState(api.IntegrationConnecting, "reconnecting: "+err.Error())
		}
		if o.now().Sub(started) >= o.backoffMin {
			// The subscription did real work; retry promptly and start the
			// backoff fresh.
			backoff = o.backoffMin
		}
		if !wait(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, o.backoffMax)
	}
}

// release ends this connection's holds: every live status the
// Integration vouched for coerces to unknown until the next subscription
// re-observes it.
func (o *Observer) release(ctx context.Context) {
	o.threads.ReleaseIntegration(context.WithoutCancel(ctx), ID)
}

// serve runs one connection: the environment descriptor, a session
// (paired when absent, re-paired once when rejected), a ticket, and the
// subscription until it ends.
func (o *Observer) serve(ctx context.Context, state runtime) error {
	origin := state.Origin
	environmentID, err := o.describe(ctx, origin)
	if err != nil {
		return err
	}
	if o.session == nil {
		if o.session, err = loadSession(o.sessionPath); err != nil {
			// A file that is not a session (a restored fragment, a hand
			// edit) is not a credential: pair afresh over it. Nothing can
			// be revoked for it.
			o.logger.Warn("t3code: ignoring an unreadable session file", "path", o.sessionPath, "error", err)
		}
	}
	repaired := false
	var ticket string
	for {
		if o.session == nil {
			s, err := o.pair(ctx, origin)
			if err != nil {
				return err
			}
			if err := saveSession(o.sessionPath, s); err != nil {
				// A session that only lives in memory would be paired over
				// at every restart, leaking sessions in T3: retire it and
				// report the persistence failure as the thing to fix.
				o.revoke(ctx, s)
				return authErrorf("persisting the T3 Code session: %w", err)
			}
			o.session = s
			repaired = true
		}
		ticket, err = websocketTicket(ctx, o.httpClient, origin, o.session.Token)
		if err == nil {
			break
		}
		var status *httpError
		if !errors.As(err, &status) {
			return fmt.Errorf("websocket ticket: %w", err)
		}
		switch status.status {
		case http.StatusUnauthorized:
			if repaired {
				return authErrorf("T3 Code rejected the session it just issued: %w", err)
			}
			// The stored session was revoked or expired: pair once more,
			// retiring the old session first so they do not accumulate.
			old := o.session
			o.session = nil
			o.revoke(ctx, old)
		case http.StatusForbidden:
			return o.scopeRefused(ctx, err)
		default:
			return fmt.Errorf("websocket ticket: %w", err)
		}
	}

	o.mu.Lock()
	o.origin, o.environmentID = origin, environmentID
	o.mu.Unlock()

	socketURL, err := websocketURL(origin, ticket)
	if err != nil {
		return err
	}
	var after *uint64
	if o.shell.initialized {
		sequence := o.shell.sequence
		after = &sequence
	}
	// After a replay (no snapshot in this subscription) every hold has to
	// be re-established from the local copy once the marker arrives; the
	// Integration is connected — statuses current, holds in place — only
	// from then, or from a snapshot.
	replaying := after != nil
	established := false
	err = subscribeShell(ctx, o.httpClient, socketURL, after, func(item json.RawMessage) error {
		event, err := decodeEvent(item)
		if err != nil {
			return err
		}
		switch event.Kind {
		case "snapshot":
			if err := o.applySnapshot(ctx, *event.Snapshot); err != nil {
				return err
			}
			replaying, established = false, true
		case "synchronized":
			if !o.shell.initialized {
				return &protocolError{err: errors.New("synchronized before any snapshot")}
			}
			if replaying {
				o.reconcile(ctx)
				replaying = false
			}
			established = true
		default:
			if err := o.applyEvent(ctx, event); err != nil {
				return err
			}
		}
		if established {
			o.setState(api.IntegrationConnected, o.detail(origin))
		}
		return nil
	})
	var status *httpError
	if errors.As(err, &status) && status.status == http.StatusForbidden {
		return o.scopeRefused(ctx, err)
	}
	return err
}

// scopeRefused handles T3 refusing the session's scope, at the ticket or
// inside the subscription: the session is retired, so the slow retry
// pairs afresh instead of presenting the same credential.
func (o *Observer) scopeRefused(ctx context.Context, err error) error {
	o.revoke(ctx, o.session)
	o.session = nil
	return authErrorf("T3 Code refused the %s scope: %w", scope, err)
}

func (o *Observer) describe(ctx context.Context, origin string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/.well-known/t3/environment", nil)
	if err != nil {
		return "", err
	}
	var descriptor struct {
		EnvironmentID string `json:"environmentId"`
	}
	if err := doJSON(o.httpClient, req, &descriptor); err != nil {
		return "", fmt.Errorf("environment descriptor: %w", err)
	}
	if descriptor.EnvironmentID == "" {
		return "", &protocolError{err: errors.New("environment descriptor omitted environmentId")}
	}
	return descriptor.EnvironmentID, nil
}

// applySnapshot replaces the local copy and diff-applies it: every
// reported thread is observed, and every unarchived T3 thread ATC holds
// that the snapshot lacks is archived — including ones T3 dropped while
// ATC was not listening.
func (o *Observer) applySnapshot(ctx context.Context, snapshot shellSnapshot) error {
	projects := make(map[string]projectShell, len(*snapshot.Projects))
	for _, project := range *snapshot.Projects {
		projects[project.ID] = project
	}
	threadsByID := make(map[string]threadShell, len(*snapshot.Threads))
	for _, thread := range *snapshot.Threads {
		if _, ok := projects[thread.ProjectID]; !ok {
			return schemaErrorf("thread %s: %w %s", thread.ID, errUnknownProject, thread.ProjectID)
		}
		threadsByID[thread.ID] = thread
	}
	o.shell = shellState{initialized: true, sequence: *snapshot.Sequence, projects: projects, threads: threadsByID}
	o.skipped = map[string]string{}
	o.settled = map[string]bool{}
	o.reconcile(ctx)
	for _, id := range o.threads.UnarchivedProviderIDs(ID) {
		if _, present := threadsByID[id]; !present {
			o.forget(ctx, id)
		}
	}
	return nil
}

func (o *Observer) reconcile(ctx context.Context) {
	ids := make([]string, 0, len(o.shell.threads))
	for id := range o.shell.threads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		thread := o.shell.threads[id]
		o.observe(ctx, thread, o.shell.projects[thread.ProjectID])
	}
}

// applyEvent applies one ordered event; replayed sequences at or below
// the last applied are ignored.
func (o *Observer) applyEvent(ctx context.Context, event shellEvent) error {
	if !o.shell.initialized {
		return &protocolError{err: fmt.Errorf("%s before any snapshot", event.Kind)}
	}
	if *event.Sequence <= o.shell.sequence {
		return nil
	}
	switch event.Kind {
	case "project-upserted":
		o.shell.projects[event.Project.ID] = *event.Project
	case "project-removed":
		// T3 forgetting a project says nothing about ATC's: the record is
		// the user's now.
		delete(o.shell.projects, event.ProjectID)
	case "thread-upserted":
		project, ok := o.shell.projects[event.Thread.ProjectID]
		if !ok {
			return schemaErrorf("thread %s: %w %s", event.Thread.ID, errUnknownProject, event.Thread.ProjectID)
		}
		o.shell.threads[event.Thread.ID] = *event.Thread
		o.observe(ctx, *event.Thread, project)
	case "thread-removed":
		delete(o.shell.threads, event.ThreadID)
		delete(o.settled, event.ThreadID)
		o.forget(ctx, event.ThreadID)
	}
	o.shell.sequence = *event.Sequence
	return nil
}

// observe feeds one T3 thread to the threads domain as an external
// observation. A thread T3 has settled (ATC-292) is treated as archived:
// a known record archives, an unknown one is never minted, and the same
// record returns when T3 unsettles it. The workspace root is the
// conversation's origin evidence; the domain classifies it into a
// project (T3 never resolves or creates one) and refuses to mint a
// thread whose workspace has no local directory — skipped and counted
// here, not recorded.
func (o *Observer) observe(ctx context.Context, thread threadShell, project projectShell) {
	if thread.settled() {
		o.settled[thread.ID] = true
		o.forget(ctx, thread.ID)
		return
	}
	delete(o.settled, thread.ID)
	cwd := thread.WorktreePath.Value
	if cwd == "" {
		cwd = project.WorkspaceRoot
	}
	observation := threads.ExternalObservation{
		IntegrationID:    ID,
		ProviderID:       thread.ID,
		InitialDirectory: project.WorkspaceRoot,
		At:               o.now(),
		Status:           projectStatus(thread),
		Title:            thread.Title,
		Metadata:         threads.Metadata{Model: thread.ModelSelection.Model, Cwd: cwd},
	}
	if thread.Session != nil {
		if thread.Session.ProviderName != nil {
			// T3's provider kind is the agent id, as reported: opaque and
			// scoped to this Integration.
			observation.AgentID = *thread.Session.ProviderName
		}
		if thread.Session.LastError != nil {
			observation.LastError = *thread.Session.LastError
		}
	}
	_, err := o.threads.ObserveExternal(ctx, observation)
	if errors.Is(err, threads.ErrNoLocalDirectory) {
		o.skipped[thread.ID] = fmt.Sprintf("workspace %s: %v", project.WorkspaceRoot, err)
		return
	}
	if err != nil {
		o.logger.Warn("t3code: recording thread observation", "thread", thread.ID, "error", err)
		return
	}
	delete(o.skipped, thread.ID)
}

// forget mirrors a thread T3 no longer reports as active — removed or
// settled: archived, never deleted.
func (o *Observer) forget(ctx context.Context, threadID string) {
	delete(o.skipped, threadID)
	if err := o.threads.ArchiveExternalThread(ctx, ID, threadID); err != nil {
		o.logger.Warn("t3code: archiving a removed thread", "thread", threadID, "error", err)
	}
}

// detail is the connected state's human-readable summary.
func (o *Observer) detail(origin string) string {
	detail := fmt.Sprintf("subscribed to %s; %d threads mirrored", origin, len(o.shell.threads)-len(o.skipped)-len(o.settled))
	if len(o.settled) > 0 {
		detail += fmt.Sprintf(", %d settled", len(o.settled))
	}
	if len(o.skipped) == 0 {
		return detail
	}
	ids := make([]string, 0, len(o.skipped))
	for id := range o.skipped {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	reasons := make([]string, 0, len(ids))
	for i, id := range ids {
		if i == 3 {
			reasons = append(reasons, "…")
			break
		}
		reasons = append(reasons, id+": "+o.skipped[id])
	}
	return fmt.Sprintf("%s; %d skipped (%s)", detail, len(o.skipped), strings.Join(reasons, "; "))
}

func wait(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
