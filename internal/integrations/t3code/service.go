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

// Options wires a Service.
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

// Service is the Integration's one long-lived connection to the local
// T3 environment: everything it mirrors from it, and the seam through
// which ATC starts threads there (ATC-289). Run owns discovery, pairing,
// the subscription, and every application of shell state; the state
// read from other goroutines — the connection report, the link inputs,
// the live client, and the project list a create resolves against —
// sits under mu, which the Run goroutine also takes for every write to
// the shell copy (its own reads of the copy need no lock: it is the only
// writer).
type Service struct {
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
	// client is the live connection's RPC client, for dispatching
	// commands; nil between connections.
	client *rpcClient
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

func New(opts Options) *Service {
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
	s := &Service{
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
	if _, err := discover(s.home, s.alive); err != nil {
		s.connection = api.IntegrationConnection{State: api.IntegrationUnavailable, Since: s.now(), Detail: err.Error()}
	} else {
		s.connection = api.IntegrationConnection{State: api.IntegrationConnecting, Since: s.now(), Detail: "T3 Code is running; connecting"}
	}
	return s
}

// Connection reports the Integration's live state.
func (s *Service) Connection() api.IntegrationConnection {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connection
}

// Links derives a T3 thread's deep links from the last connection's
// origin and environment; nil until the Integration has connected once.
func (s *Service) Links(providerID string) *api.ThreadLinks {
	s.mu.Lock()
	defer s.mu.Unlock()
	return links(s.origin, s.environmentID, providerID)
}

// setState records the connection state, publishing integration.updated
// on a transition only; a detail change within one state is silent.
func (s *Service) setState(state api.IntegrationConnectionState, detail string) {
	s.mu.Lock()
	changed := s.connection.State != state
	if changed {
		s.connection.State = state
		s.connection.Since = s.now()
	}
	s.connection.Detail = detail
	s.mu.Unlock()
	if !changed {
		return
	}
	switch state {
	case api.IntegrationConnected:
		s.logger.Info("t3code connected", "detail", detail)
	case api.IntegrationAuthFailed:
		s.logger.Warn("t3code pairing failed", "detail", detail)
	default:
		s.logger.Debug("t3code state", "state", state, "detail", detail)
	}
	s.hub.Publish(api.EventIntegrationUpdated, resource, ID)
}

// Run keeps the mirror for the life of the ATC server: discover T3, pair
// when needed, subscribe, apply until the subscription drops, release
// every hold, and reconnect with backoff — quietly polling for a runtime
// file while T3 is not running.
func (s *Service) Run(ctx context.Context) {
	backoff := s.backoffMin
	for {
		state, err := discover(s.home, s.alive)
		if err != nil {
			s.setState(api.IntegrationUnavailable, err.Error())
			s.release(ctx)
			if !wait(ctx, s.pollInterval) {
				return
			}
			continue
		}
		if state != s.last {
			s.last = state
			s.retryAt = time.Time{}
		}
		if s.now().Before(s.retryAt) {
			if !wait(ctx, s.pollInterval) {
				return
			}
			continue
		}
		started := s.now()
		err = s.serve(ctx, state)
		if ctx.Err() != nil {
			return
		}
		s.release(ctx)
		var auth *authError
		var schema *schemaError
		var protocol *protocolError
		switch {
		case errors.As(err, &auth):
			s.setState(api.IntegrationAuthFailed, err.Error())
			s.retryAt = s.now().Add(s.authRetry)
			continue
		case errors.As(err, &schema), errors.As(err, &protocol):
			// The local copy may be inconsistent with what T3 sent; the
			// next connection takes a fresh snapshot.
			s.mu.Lock()
			s.shell = shellState{}
			s.mu.Unlock()
			s.setState(api.IntegrationUnavailable, err.Error())
		default:
			s.setState(api.IntegrationConnecting, "reconnecting: "+err.Error())
		}
		if s.now().Sub(started) >= s.backoffMin {
			// The subscription did real work; retry promptly and start the
			// backoff fresh.
			backoff = s.backoffMin
		}
		if !wait(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, s.backoffMax)
	}
}

// release ends this connection's holds: every live status the
// Integration vouched for coerces to unknown until the next subscription
// re-observes it.
func (s *Service) release(ctx context.Context) {
	s.threads.ReleaseIntegration(context.WithoutCancel(ctx), ID)
}

// serve runs one connection: the environment descriptor, a session
// (paired when absent, re-paired once when rejected), a ticket, and the
// subscription until it ends.
func (s *Service) serve(ctx context.Context, state runtime) error {
	origin := state.Origin
	environmentID, err := s.describe(ctx, origin)
	if err != nil {
		return err
	}
	if s.session == nil {
		if s.session, err = loadSession(s.sessionPath); err != nil {
			// A file that is not a session (a restored fragment, a hand
			// edit) is not a credential: pair afresh over it. Nothing can
			// be revoked for it.
			s.logger.Warn("t3code: ignoring an unreadable session file", "path", s.sessionPath, "error", err)
		}
	}
	if s.session != nil && !sameScopes(s.session.Scope, scope) {
		// A session from before the scope set changed cannot dispatch:
		// retire it and pair over it, once, exactly as a rejection does.
		s.logger.Info("t3code: re-pairing a session with a stale scope set", "scope", s.session.Scope)
		old := s.session
		s.session = nil
		s.revoke(ctx, old)
	}
	repaired := false
	var ticket string
	for {
		if s.session == nil {
			sess, err := s.pair(ctx, origin)
			if err != nil {
				return err
			}
			if err := saveSession(s.sessionPath, sess); err != nil {
				// A session that only lives in memory would be paired over
				// at every restart, leaking sessions in T3: retire it and
				// report the persistence failure as the thing to fix.
				s.revoke(ctx, sess)
				return authErrorf("persisting the T3 Code session: %w", err)
			}
			s.session = sess
			repaired = true
		}
		ticket, err = websocketTicket(ctx, s.httpClient, origin, s.session.Token)
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
			old := s.session
			s.session = nil
			s.revoke(ctx, old)
		case http.StatusForbidden:
			return s.scopeRefused(ctx, err)
		default:
			return fmt.Errorf("websocket ticket: %w", err)
		}
	}

	s.mu.Lock()
	s.origin, s.environmentID = origin, environmentID
	s.mu.Unlock()

	socketURL, err := websocketURL(origin, ticket)
	if err != nil {
		return err
	}
	client, err := dialRPC(ctx, s.httpClient, socketURL)
	if err != nil {
		return err
	}
	s.mu.Lock()
	s.client = client
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.client = nil
		s.mu.Unlock()
		client.close()
	}()
	payload := map[string]any{"requestCompletionMarker": true}
	if s.shell.initialized {
		payload["afterSequence"] = s.shell.sequence
	}
	// After a replay (no snapshot in this subscription) every hold has to
	// be re-established from the local copy once the marker arrives; the
	// Integration is connected — statuses current, holds in place — only
	// from then, or from a snapshot.
	replaying := s.shell.initialized
	established := false
	err = client.subscribe(ctx, shellMethod, payload, func(item json.RawMessage) error {
		event, err := decodeEvent(item)
		if err != nil {
			return err
		}
		switch event.Kind {
		case "snapshot":
			if err := s.applySnapshot(ctx, *event.Snapshot); err != nil {
				return err
			}
			replaying, established = false, true
		case "synchronized":
			if !s.shell.initialized {
				return &protocolError{err: errors.New("synchronized before any snapshot")}
			}
			if replaying {
				s.reconcile(ctx)
				replaying = false
			}
			established = true
		default:
			if err := s.applyEvent(ctx, event); err != nil {
				return err
			}
		}
		if established {
			s.setState(api.IntegrationConnected, s.detail(origin))
		}
		return nil
	})
	var failure *rpcFailure
	if errors.As(err, &failure) {
		// The scope check for the subscription itself happens here, not at
		// the ticket: a session without it is refused inside the stream.
		if failure.tag == "EnvironmentAuthorizationError" {
			return s.scopeRefused(ctx, fmt.Errorf("subscription refused: %w", err))
		}
		return &protocolError{err: fmt.Errorf("subscription failed: %w", err)}
	}
	return err
}

// scopeRefused handles T3 refusing the session's scope, at the ticket or
// inside the subscription: the session is retired, so the slow retry
// pairs afresh instead of presenting the same credential.
func (s *Service) scopeRefused(ctx context.Context, err error) error {
	s.revoke(ctx, s.session)
	s.session = nil
	return authErrorf("T3 Code refused the scope %q: %w", scope, err)
}

func (s *Service) describe(ctx context.Context, origin string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/.well-known/t3/environment", nil)
	if err != nil {
		return "", err
	}
	var descriptor struct {
		EnvironmentID string `json:"environmentId"`
	}
	if err := doJSON(s.httpClient, req, &descriptor); err != nil {
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
func (s *Service) applySnapshot(ctx context.Context, snapshot shellSnapshot) error {
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
	s.mu.Lock()
	s.shell = shellState{initialized: true, sequence: *snapshot.Sequence, projects: projects, threads: threadsByID}
	s.mu.Unlock()
	s.skipped = map[string]string{}
	s.settled = map[string]bool{}
	s.reconcile(ctx)
	for _, id := range s.threads.UnarchivedProviderIDs(ID) {
		if _, present := threadsByID[id]; !present {
			s.forget(ctx, id)
		}
	}
	return nil
}

func (s *Service) reconcile(ctx context.Context) {
	ids := make([]string, 0, len(s.shell.threads))
	for id := range s.shell.threads {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		thread := s.shell.threads[id]
		s.observe(ctx, thread, s.shell.projects[thread.ProjectID])
	}
}

// applyEvent applies one ordered event; replayed sequences at or below
// the last applied are ignored.
func (s *Service) applyEvent(ctx context.Context, event shellEvent) error {
	if !s.shell.initialized {
		return &protocolError{err: fmt.Errorf("%s before any snapshot", event.Kind)}
	}
	if *event.Sequence <= s.shell.sequence {
		return nil
	}
	switch event.Kind {
	case "project-upserted":
		s.mu.Lock()
		s.shell.projects[event.Project.ID] = *event.Project
		s.mu.Unlock()
	case "project-removed":
		// T3 forgetting a project says nothing about ATC's: the record is
		// the user's now.
		s.mu.Lock()
		delete(s.shell.projects, event.ProjectID)
		s.mu.Unlock()
	case "thread-upserted":
		project, ok := s.shell.projects[event.Thread.ProjectID]
		if !ok {
			return schemaErrorf("thread %s: %w %s", event.Thread.ID, errUnknownProject, event.Thread.ProjectID)
		}
		s.mu.Lock()
		s.shell.threads[event.Thread.ID] = *event.Thread
		s.mu.Unlock()
		s.observe(ctx, *event.Thread, project)
	case "thread-removed":
		s.mu.Lock()
		delete(s.shell.threads, event.ThreadID)
		s.mu.Unlock()
		delete(s.settled, event.ThreadID)
		s.forget(ctx, event.ThreadID)
	}
	s.shell.sequence = *event.Sequence
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
func (s *Service) observe(ctx context.Context, thread threadShell, project projectShell) {
	if thread.settled() {
		s.settled[thread.ID] = true
		s.forget(ctx, thread.ID)
		return
	}
	delete(s.settled, thread.ID)
	cwd := thread.WorktreePath.Value
	if cwd == "" {
		cwd = project.WorkspaceRoot
	}
	observation := threads.ExternalObservation{
		IntegrationID:    ID,
		ProviderID:       thread.ID,
		InitialDirectory: project.WorkspaceRoot,
		At:               s.now(),
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
			// The session's error text explains a faulted session and a
			// failed turn alike; the domain records it where it applies.
			observation.StatusDetail = *thread.Session.LastError
		}
	}
	observation.Turn = turnObservation(thread.LatestTurn, observation.StatusDetail)
	_, err := s.threads.ObserveExternal(ctx, observation)
	if errors.Is(err, threads.ErrNoLocalDirectory) {
		s.skipped[thread.ID] = fmt.Sprintf("workspace %s: %v", project.WorkspaceRoot, err)
		return
	}
	if err != nil {
		s.logger.Warn("t3code: recording thread observation", "thread", thread.ID, "error", err)
		return
	}
	delete(s.skipped, thread.ID)
}

// forget mirrors a thread T3 no longer reports as active — removed or
// settled: archived, never deleted.
func (s *Service) forget(ctx context.Context, threadID string) {
	delete(s.skipped, threadID)
	if err := s.threads.ArchiveExternalThread(ctx, ID, threadID); err != nil {
		s.logger.Warn("t3code: archiving a removed thread", "thread", threadID, "error", err)
	}
}

// detail is the connected state's human-readable summary.
func (s *Service) detail(origin string) string {
	detail := fmt.Sprintf("subscribed to %s; %d threads mirrored", origin, len(s.shell.threads)-len(s.skipped)-len(s.settled))
	if len(s.settled) > 0 {
		detail += fmt.Sprintf(", %d settled", len(s.settled))
	}
	if len(s.skipped) == 0 {
		return detail
	}
	ids := make([]string, 0, len(s.skipped))
	for id := range s.skipped {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	reasons := make([]string, 0, len(ids))
	for i, id := range ids {
		if i == 3 {
			reasons = append(reasons, "…")
			break
		}
		reasons = append(reasons, id+": "+s.skipped[id])
	}
	return fmt.Sprintf("%s; %d skipped (%s)", detail, len(s.skipped), strings.Join(reasons, "; "))
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
