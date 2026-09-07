package linear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
)

// Session states and outcomes, as stored.
const (
	stateAccepted = "accepted"
	stateStarting = "starting"
	stateStarted  = "started"
	stateDone     = "done"

	outcomeResponded     = "responded"
	outcomeFailed        = "failed"
	outcomeInterrupted   = "interrupted"
	outcomeRefused       = "refused"
	outcomeUnrecoverable = "unrecoverable"
)

const (
	// probeRetry paces setup and token checks while they fail;
	// probeInterval re-checks a working credential (and picks up a
	// rewritten setup file).
	probeRetry    = 30 * time.Second
	probeInterval = 30 * time.Minute
	// sendPoll and sessionPoll are the idle wake intervals of the two
	// loops; both also wake on demand. The session poll doubles as the
	// periodic refetch that a lost or dropped event stream needs.
	sendPoll    = 5 * time.Second
	sessionPoll = 15 * time.Second
	// Outbox retries: retryBase doubling to retryMax for transient
	// failures; authRetry after an authentication failure, which only an
	// operator (or a renewed token) can end.
	retryBase = 2 * time.Second
	retryMax  = 5 * time.Minute
	authRetry = 5 * time.Minute
	// responseGrace is how long a completed Turn may lack its response
	// before ATC reports that it could not be recovered: T3's own
	// recovery reads span a few seconds; this leaves ample room.
	responseGrace = 2 * time.Minute
	// tokenSlack renews a token this long before its recorded expiry.
	tokenSlack = 5 * time.Minute
	// sendBatch bounds one pass over the outbox; receiptRetention bounds
	// how long sent rows (the deduplication receipts) are kept.
	sendBatch        = 16
	receiptRetention = 30 * 24 * time.Hour
	pruneInterval    = time.Hour
	httpTimeout      = 15 * time.Second

	resource = "integration"
)

// ThreadStarter is the application coordinator's creation seam as this
// Integration uses it: start one Thread with its first prompt, telling
// the caller the ids before anything is dispatched.
type ThreadStarter interface {
	StartThread(ctx context.Context, params api.ThreadCreateParams, recorded func(threadID, turnID string) error) (api.Thread, error)
}

// ThreadReader reads the normalized Thread (threads.Service in
// production).
type ThreadReader interface {
	Get(id string) (api.Thread, error)
}

// Options wires a Service.
type Options struct {
	// SetupPath is the setup file (paths.LinearSetupFile).
	SetupPath  string
	Repository *store.Linear
	Starter    ThreadStarter
	Threads    ThreadReader
	Hub        *events.Hub
	// Ingress reports the webhook ingress state for the connection detail
	// (webhooks.Service.Status in production); nil reports nothing.
	Ingress func(ctx context.Context) api.Webhooks
	Logger  *slog.Logger
	Now     func() time.Time
	// HTTPClient talks to Linear; nil selects one with httpTimeout. APIURL
	// replaces Linear's API origin in tests.
	HTTPClient *http.Client
	APIURL     string
}

// Service is the Integration: the webhook handler (verify.go,
// process.go), the session loop that starts and watches Threads, the
// outbox sender, and the credential probe behind the connection report.
// Construct with New; Run drives the loops.
type Service struct {
	setupPath string
	repo      *store.Linear
	starter   ThreadStarter
	threads   ThreadReader
	hub       *events.Hub
	ingress   func(ctx context.Context) api.Webhooks
	logger    *slog.Logger
	now       func() time.Time
	http      *http.Client
	apiURL    string

	// Production cadences; tests shrink them.
	probeRetry, probeInterval, sendPoll, sessionPoll, retryBase, retryMax, authRetry, responseGrace time.Duration

	sendKick    chan struct{}
	sessionKick chan struct{}
	// starts tracks the in-flight start goroutines; Run joins them.
	starts sync.WaitGroup

	// renewMu serializes token renewals: the probe and the sender can
	// both meet an expired token, and one refresh must serve both.
	renewMu sync.Mutex

	mu         sync.Mutex
	setup      *Setup
	connection api.IntegrationConnection
	// lastFailure summarizes the most recent outbox failure for status.
	lastFailure string
	// starting holds the sessions this process has a start in flight for.
	starting map[string]bool
}

// New wires the Service. It loads the setup file once so the connection
// report is honest before Run starts.
func New(opts Options) *Service {
	if opts.Repository == nil || opts.Starter == nil || opts.Threads == nil || opts.Hub == nil {
		panic("linear.New: Repository, Starter, Threads, and Hub must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.HTTPClient == nil {
		opts.HTTPClient = &http.Client{Timeout: httpTimeout}
	}
	if opts.APIURL == "" {
		opts.APIURL = defaultAPIURL
	}
	s := &Service{
		setupPath:     opts.SetupPath,
		repo:          opts.Repository,
		starter:       opts.Starter,
		threads:       opts.Threads,
		hub:           opts.Hub,
		ingress:       opts.Ingress,
		logger:        opts.Logger,
		now:           opts.Now,
		http:          opts.HTTPClient,
		apiURL:        opts.APIURL,
		probeRetry:    probeRetry,
		probeInterval: probeInterval,
		sendPoll:      sendPoll,
		sessionPoll:   sessionPoll,
		retryBase:     retryBase,
		retryMax:      retryMax,
		authRetry:     authRetry,
		responseGrace: responseGrace,
		sendKick:      make(chan struct{}, 1),
		sessionKick:   make(chan struct{}, 1),
		starting:      map[string]bool{},
	}
	s.connection = api.IntegrationConnection{State: api.IntegrationConnecting, Since: s.now(), Detail: "starting"}
	s.reloadSetup()
	return s
}

// reloadSetup reads the setup file into memory, reporting unavailable
// with the operator action when it cannot be used. Reports whether a
// setup is loaded.
func (s *Service) reloadSetup() bool {
	setup, err := loadSetup(s.setupPath)
	if err != nil {
		s.setSetup(nil)
		s.setState(api.IntegrationUnavailable, err.Error())
		return false
	}
	s.setSetup(&setup)
	return true
}

func (s *Service) setSetup(setup *Setup) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.setup = setup
}

// currentSetup returns a copy of the loaded setup.
func (s *Service) currentSetup() (Setup, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.setup == nil {
		return Setup{}, false
	}
	return *s.setup, true
}

// setState records the connection state, publishing integration.updated
// on a transition only.
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
		s.logger.Info("linear connected", "detail", detail)
	case api.IntegrationAuthFailed, api.IntegrationUnavailable:
		s.logger.Warn("linear "+string(state), "detail", detail)
	default:
		s.logger.Debug("linear state", "state", state, "detail", detail)
	}
	s.hub.Publish(api.EventIntegrationUpdated, resource, ID)
}

// Connection reports the Integration's state: the credential probe's
// verdict, what it tracks and owes, the last outbound failure, and the
// webhook route's readiness — everything an operator needs to see why a
// mention did or did not work, and nothing secret.
func (s *Service) Connection() api.IntegrationConnection {
	s.mu.Lock()
	connection := s.connection
	failure := s.lastFailure
	s.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	detail := connection.Detail
	if open, total, err := s.repo.CountSessions(ctx); err == nil {
		pending, _ := s.repo.Pending(ctx)
		detail += fmt.Sprintf("; %d of %d sessions open, %d Linear updates owed", open, total, pending)
	}
	if failure != "" {
		detail += "; last Linear failure: " + failure
	}
	if s.ingress != nil {
		status := s.ingress(ctx)
		switch {
		case status.State == api.WebhooksReady:
			detail += "; webhook route " + status.URL + RoutePath
		case status.URL != "":
			detail += fmt.Sprintf("; webhook route %s%s (%s)", status.URL, RoutePath, status.State)
		default:
			detail += "; webhook intake " + string(status.State)
		}
	}
	connection.Detail = detail
	return connection
}

// Run drives the credential probe, the outbox sender, and the session
// loop until ctx is cancelled, then joins every start still in flight.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { s.probeLoop(ctx) })
	wg.Go(func() { s.sendLoop(ctx) })
	s.sessionLoop(ctx)
	wg.Wait()
	s.starts.Wait()
}

// wake kicks a loop without blocking.
func (s *Service) wake(kick chan struct{}) {
	select {
	case kick <- struct{}{}:
	default:
	}
}

// probeLoop keeps the connection report honest: the setup file reloads
// every probeRetry, so an operator's edit lands without a restart, and
// Linear is asked whose token it is and in which workspace — after every
// change to the file, while the report is anything but connected, and
// every probeInterval otherwise.
func (s *Service) probeLoop(ctx context.Context) {
	var last Setup
	var probed time.Time
	for {
		setup, ok := Setup{}, s.reloadSetup()
		if ok {
			setup, _ = s.currentSetup()
		}
		s.mu.Lock()
		state := s.connection.State
		s.mu.Unlock()
		if ok && (setup != last || state != api.IntegrationConnected || s.now().Sub(probed) >= s.probeInterval) {
			s.probe(ctx, setup)
			probed = s.now()
		}
		last = setup
		if !wait(ctx, s.probeRetry) {
			return
		}
	}
}

// probe asks Linear whose token the setup holds and records the verdict.
func (s *Service) probe(ctx context.Context, setup Setup) {
	token, err := s.token(ctx)
	if err != nil {
		s.reportAPIFailure(err, "verifying the Linear credential")
		return
	}
	who, err := fetchViewer(ctx, s.http, s.apiURL, token)
	if classify(err) == failureAuth {
		if renewErr := s.renew(ctx, token); renewErr != nil {
			err = renewErr
		} else if token, err = s.token(ctx); err == nil {
			who, err = fetchViewer(ctx, s.http, s.apiURL, token)
		}
	}
	if err != nil {
		s.reportAPIFailure(err, "verifying the Linear credential")
		return
	}
	if who.Organization.ID != setup.OrganizationID {
		s.setState(api.IntegrationAuthFailed, fmt.Sprintf("the token acts in workspace %s (%s) but the setup file names organization %s; fix organization_id or re-authorize", who.Organization.Name, who.Organization.ID, setup.OrganizationID))
		return
	}
	s.setState(api.IntegrationConnected, fmt.Sprintf("acting as %s in workspace %s", who.Name, who.Organization.Name))
}

// reportAPIFailure turns a failed Linear call into the connection state:
// an authentication failure names the operator action, anything else is
// a reconnect in progress.
func (s *Service) reportAPIFailure(err error, doing string) {
	switch classify(err) {
	case failureAuth:
		s.setState(api.IntegrationAuthFailed, fmt.Sprintf("%s: %v; re-authorize the Linear app and update access_token and refresh_token in %s", doing, err, s.setupPath))
	default:
		s.setState(api.IntegrationConnecting, fmt.Sprintf("%s: %v", doing, err))
	}
}

// token returns an access token to call Linear with, renewing it first
// when its recorded expiry is near.
func (s *Service) token(ctx context.Context) (string, error) {
	setup, ok := s.currentSetup()
	if !ok {
		return "", ErrNotConfigured
	}
	if !setup.AccessTokenExpiresAt.IsZero() && s.now().After(setup.AccessTokenExpiresAt.Add(-tokenSlack)) {
		if err := s.renew(ctx, setup.AccessToken); err != nil {
			return "", err
		}
		setup, _ = s.currentSetup()
	}
	return setup.AccessToken, nil
}

// renew refreshes the access token, persists it to the setup file, and
// loads the result. Serialized, and a caller whose token was already
// replaced by another renewal gets that result instead of a second
// refresh.
func (s *Service) renew(ctx context.Context, seen string) error {
	s.renewMu.Lock()
	defer s.renewMu.Unlock()
	setup, ok := s.currentSetup()
	if !ok {
		return ErrNotConfigured
	}
	if seen != "" && setup.AccessToken != seen {
		return nil
	}
	renewed, err := refresh(ctx, s.http, s.apiURL, setup, s.now())
	if err != nil {
		return fmt.Errorf("renewing the Linear token: %w", err)
	}
	if err := saveTokens(s.setupPath, renewed.AccessToken, renewed.RefreshToken, renewed.ExpiresAt); err != nil {
		return fmt.Errorf("persisting the renewed Linear token: %w", err)
	}
	s.reloadSetup()
	s.logger.Info("linear token renewed")
	return nil
}

// sessionLoop starts accepted sessions, resumes recorded ones, and
// watches started ones. It wakes on a thread change event for a watched
// Thread (a reason to refetch, never the state itself), on a kick from
// Process, and on the poll — so a dropped event stream, a lost backlog,
// or a provider reconnect all converge on the next tick at the latest.
func (s *Service) sessionLoop(ctx context.Context) {
	sub := s.hub.Subscribe(0, false)
	defer func() { sub.Close() }()
	var watched map[string]bool
	for {
		watched = s.reconcile(ctx, watched)
		timer := time.NewTimer(s.sessionPoll)
	inner:
		for {
			select {
			case <-ctx.Done():
				timer.Stop()
				return
			case <-s.sessionKick:
				break inner
			case change, ok := <-sub.C:
				if !ok {
					// Dropped for falling behind: resubscribe and refetch.
					sub = s.hub.Subscribe(0, false)
					break inner
				}
				if change.Resource == "thread" && watched[change.ID] || change.Resource == resource {
					// A watched Thread changed, or an Integration's
					// connection did — T3 coming back is what an owed
					// start waits for.
					break inner
				}
			case <-timer.C:
				break inner
			}
		}
		timer.Stop()
	}
}

// reconcile acts on every open session and returns the Threads being
// watched. Reads that fail leave the previous set standing.
func (s *Service) reconcile(ctx context.Context, previous map[string]bool) map[string]bool {
	sessions, err := s.repo.OpenSessions(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("linear: reading open sessions", "error", err)
		}
		return previous
	}
	watched := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		switch session.State {
		case stateAccepted:
			s.launch(ctx, session)
		case stateStarting:
			s.mu.Lock()
			inflight := s.starting[session.ID]
			s.mu.Unlock()
			if inflight {
				continue
			}
			// The list was read before the flag was checked; a start that
			// finished in between left a newer row. Only the fresh row
			// says whether anything is owed.
			fresh, err := s.repo.GetSession(ctx, session.ID)
			if err != nil || fresh.State != stateStarting {
				if err == nil && fresh.State == stateStarted {
					watched[fresh.ThreadID] = true
					s.observe(ctx, fresh)
				}
				continue
			}
			session = fresh
			// Recorded by an earlier process that did not finish. Without
			// a thread id nothing was dispatched (the record comes before
			// the command), so the start is owed again; with one, T3 may
			// hold the conversation — never start another, say so, and
			// watch what was recorded.
			if session.ThreadID == "" {
				s.launch(ctx, session)
				continue
			}
			session.State = stateStarted
			session.Prompt = ""
			session.UpdatedAt = s.now()
			if _, err := s.repo.UpdateSession(ctx, session, s.activity(session.ID, "uncertain", contentThought, uncertain())); err != nil {
				s.logger.Error("linear: resuming a recorded start", "session", session.ID, "error", err)
				continue
			}
			s.wake(s.sendKick)
			watched[session.ThreadID] = true
			s.observe(ctx, session)
		case stateStarted:
			watched[session.ThreadID] = true
			s.observe(ctx, session)
		}
	}
	return watched
}

// launch starts a session's Thread on its own goroutine — a slow T3 must
// not hold up the acknowledgement or the watch of any other session — at
// most once per session per process.
func (s *Service) launch(ctx context.Context, session store.LinearSession) {
	s.mu.Lock()
	if s.starting[session.ID] {
		s.mu.Unlock()
		return
	}
	s.starting[session.ID] = true
	s.mu.Unlock()
	s.starts.Go(func() {
		defer func() {
			s.mu.Lock()
			delete(s.starting, session.ID)
			s.mu.Unlock()
			s.wake(s.sessionKick)
		}()
		s.start(ctx, session)
	})
}

// start records the attempt, asks the coordinator for the Thread with
// the fixed profile, and records how it went: started (owed the links),
// or done — refused for a definite reason, or uncertain when T3 may have
// the conversation. The ids are persisted before dispatch, so a crash in
// between is recognized at the next boot as a start that may have been
// submitted.
func (s *Service) start(ctx context.Context, session store.LinearSession) {
	setup, ok := s.currentSetup()
	if !ok {
		// Nothing to start against; the row stays accepted until the
		// operator finishes setup.
		return
	}
	// The row the loop read may predate a start that has since finished;
	// with this session's flag held, the fresh row is the truth about
	// whether a start is still owed.
	fresh, err := s.repo.GetSession(ctx, session.ID)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("linear: reading a session before starting it", "session", session.ID, "error", err)
		}
		return
	}
	if fresh.State != stateAccepted && (fresh.State != stateStarting || fresh.ThreadID != "") {
		return
	}
	session = fresh
	session.State = stateStarting
	session.ThreadID, session.TurnID = "", ""
	session.UpdatedAt = s.now()
	if ok, err := s.repo.UpdateSession(ctx, session); err != nil || !ok {
		s.logger.Error("linear: recording a start attempt", "session", session.ID, "error", err)
		return
	}
	params := api.ThreadCreateParams{
		IntegrationID: profileIntegration,
		Agent:         profileAgent,
		ProjectID:     setup.ProjectID,
		Prompt:        session.Prompt,
		Model:         profileModel,
		Options:       []api.ThreadOption{{ID: "reasoningEffort", Value: profileEffort}},
	}
	thread, err := s.starter.StartThread(ctx, params, func(threadID, turnID string) error {
		session.ThreadID, session.TurnID = threadID, turnID
		session.UpdatedAt = s.now()
		updated, err := s.repo.UpdateSession(ctx, session)
		if err != nil {
			return err
		}
		if !updated {
			return errors.New("the session row is gone")
		}
		return nil
	})
	if err != nil && ctx.Err() != nil {
		// Shutdown mid-start: no outcome is known. The row stays as
		// recorded — owed again if nothing was dispatched, uncertain if
		// the thread was — for the next boot to pick up.
		return
	}
	session.UpdatedAt = s.now()
	var owed []store.LinearOutboxRow
	switch {
	case err == nil:
		session.State, session.Prompt = stateStarted, ""
		owed = append(owed, s.activity(session.ID, "started", contentThought, started(thread.Links)))
		if thread.Links != nil {
			owed = append(owed, s.links(session.ID, *thread.Links))
		}
		s.logger.Info("linear: thread started", "session", session.ID, "thread", thread.ID, "turn", session.TurnID)
	case errors.Is(err, integrations.ErrThreadCreationUncertain):
		// T3 may hold the conversation; the coordinator kept the record,
		// and T3's report of the thread will find it. Watch it, and say
		// what is known.
		session.State, session.Prompt = stateStarted, ""
		owed = append(owed, s.activity(session.ID, "uncertain", contentThought, uncertain()))
		s.logger.Warn("linear: thread start uncertain", "session", session.ID, "thread", session.ThreadID, "error", err)
	case errors.Is(err, integrations.ErrNotConnected):
		// Nothing was dispatched: the start is owed again once T3 is
		// back, and the user hears why nothing has happened yet, once.
		session.State, session.ThreadID, session.TurnID = stateAccepted, "", ""
		owed = append(owed, s.activity(session.ID, "waiting-t3", contentThought, waitingForT3()))
		s.logger.Info("linear: thread start waits for T3 Code", "session", session.ID, "error", err)
	default:
		session.State, session.Prompt, session.Outcome = stateDone, "", outcomeFailed
		owed = append(owed, s.activity(session.ID, "failed", contentError, startFailed(err)))
		s.logger.Warn("linear: thread start failed", "session", session.ID, "error", err)
	}
	if _, err := s.repo.UpdateSession(ctx, session, owed...); err != nil {
		s.logger.Error("linear: recording a start outcome", "session", session.ID, "error", err)
	}
	s.wake(s.sendKick)
}

// observe refetches a watched Thread and applies what it shows.
func (s *Service) observe(ctx context.Context, session store.LinearSession) {
	thread, err := s.threads.Get(session.ThreadID)
	if err != nil && !errors.Is(err, threads.ErrNotFound) {
		s.logger.Warn("linear: reading a watched thread", "session", session.ID, "thread", session.ThreadID, "error", err)
		return
	}
	updated, owed, changed := evaluate(session, thread, err, s.now(), s.responseGrace)
	if !changed {
		return
	}
	rows := make([]store.LinearOutboxRow, 0, len(owed))
	for _, notice := range owed {
		rows = append(rows, s.activity(session.ID, notice.purpose, notice.kind, notice.body))
	}
	if _, err := s.repo.UpdateSession(ctx, updated, rows...); err != nil {
		s.logger.Error("linear: recording an observation", "session", session.ID, "error", err)
		return
	}
	if len(rows) > 0 {
		s.wake(s.sendKick)
	}
}

// notice is one activity an observation owes.
type notice struct {
	purpose string
	kind    string
	body    string
}

// evaluate decides what one reading of the Thread means for the session:
// the outcome to report, a waiting state to announce once, or nothing
// yet. Only the exact Turn counts — a newer one replacing it, the Thread
// vanishing, or T3 dropping the Thread are established limitations, never
// substitutes. A Thread at rest or unknown says nothing about the Turn's
// end; a completed Turn without its response is waited on for grace
// before the recovery is given up. Pure, for tests.
func evaluate(session store.LinearSession, thread api.Thread, readErr error, now time.Time, grace time.Duration) (store.LinearSession, []notice, bool) {
	finish := func(outcome string, kind string, body string) (store.LinearSession, []notice, bool) {
		session.State, session.Outcome = stateDone, outcome
		session.UpdatedAt = now
		return session, []notice{{purpose: "result", kind: kind, body: body}}, true
	}
	if errors.Is(readErr, threads.ErrNotFound) {
		return finish(outcomeUnrecoverable, contentError, threadGone())
	}
	links := thread.Links
	if pending := thread.PendingTurn; pending != nil && pending.ID == session.TurnID {
		// T3 has not started the Turn yet. A Thread T3 no longer reports
		// never will; otherwise there is nothing to say.
		if thread.Archived {
			return finish(outcomeUnrecoverable, contentError, threadDropped(links))
		}
		return session, nil, false
	}
	turn := thread.LatestTurn
	if turn == nil || turn.ID != session.TurnID {
		return finish(outcomeUnrecoverable, contentError, turnReplaced(links))
	}
	switch turn.State {
	case api.TurnCompleted:
		if turn.Response != "" {
			return finish(outcomeResponded, contentResponse, turn.Response)
		}
		if session.CompletedSeenAt == nil {
			seen := now
			session.CompletedSeenAt = &seen
			session.UpdatedAt = now
			return session, nil, true
		}
		if now.Sub(*session.CompletedSeenAt) >= grace {
			return finish(outcomeUnrecoverable, contentError, responseMissing(links))
		}
		return session, nil, false
	case api.TurnFailed:
		return finish(outcomeFailed, contentError, turnFailed(turn.Error, links))
	case api.TurnInterrupted:
		return finish(outcomeInterrupted, contentError, turnInterrupted(links))
	}
	// Running or unknown: the Turn is not over. A Thread T3 no longer
	// reports cannot finish one.
	if thread.Archived {
		return finish(outcomeUnrecoverable, contentError, threadDropped(links))
	}
	switch status := thread.Status; status {
	case api.ThreadWaitingForInput, api.ThreadWaitingForPermission:
		if session.NoticedStatus == string(status) {
			return session, nil, false
		}
		session.NoticedStatus = string(status)
		session.UpdatedAt = now
		return session, []notice{{purpose: "notice/" + string(status), kind: contentThought, body: waiting(status, links)}}, true
	}
	return session, nil, false
}

// sessionLinks reads the links of the Thread a session started, if any.
func (s *Service) sessionLinks(ctx context.Context, sessionID string) *api.ThreadLinks {
	session, err := s.repo.GetSession(ctx, sessionID)
	if err != nil || session.ThreadID == "" {
		return nil
	}
	thread, err := s.threads.Get(session.ThreadID)
	if err != nil {
		return nil
	}
	return thread.Links
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
