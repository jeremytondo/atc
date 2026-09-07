package linear

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/store"
)

// Session states and outcomes, as stored. A session is accepted while
// its start is owed, starting while an attempt is recorded, bound once
// its Thread is known — for the rest of the conversation — and done only
// when nothing more can happen here: refused, a start that failed, a
// start stopped before it happened, or a Thread ATC lost.
const (
	stateAccepted = "accepted"
	stateStarting = "starting"
	stateBound    = "bound"
	stateDone     = "done"

	outcomeFailed        = "failed"
	outcomeRefused       = "refused"
	outcomeCancelled     = "cancelled"
	outcomeUnrecoverable = "unrecoverable"
)

// Submission kinds, states, and outcomes, as stored. A submission is
// pending until dispatched, sent while its outcome is watched for, and
// done once reported. Outcomes: for a start or a message, how the turn
// it directed ended — responded, failed, interrupted, unrecoverable; for
// a reply or a choice, resolved, superseded, or failed; for a decision,
// delivered; for a stop, stopped, finished, or failed; for any kind,
// refused when the shared capability would not take it, and cancelled
// when a stop ended it before it was sent.
const (
	kindStart    = "start"
	kindMessage  = "message"
	kindReply    = "reply"
	kindChoice   = "choice"
	kindDecision = "decision"
	kindStop     = "stop"

	subPending = "pending"
	subSent    = "sent"
	subDone    = "done"

	outcomeResponded   = "responded"
	outcomeInterrupted = "interrupted"
	outcomeResolved    = "resolved"
	outcomeSuperseded  = "superseded"
	outcomeDelivered   = "delivered"
	outcomeStopped     = "stopped"
	outcomeFinished    = "finished"
)

// Request kinds and states, as stored. A notice is an input request ATC
// cannot relay an answer to: shown once with its reason, never open to
// a choice or a reply, and closed without a word.
const (
	kindApproval = "approval"
	kindInput    = "input"
	kindNotice   = "notice"
	requestOpen  = "open"
	requestShut  = "closed"
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
	// Retries: retryBase doubling to retryMax for transient failures —
	// outbox calls, and dispatches the program could not take yet;
	// authRetry after an authentication failure, which only an operator
	// (or a renewed token) can end.
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

// Coordinator is the application coordinator as this Integration uses
// it: start one Thread with its first prompt, telling the caller the ids
// before anything is dispatched, and the four shared Thread capabilities
// a submission goes through.
type Coordinator interface {
	StartThread(ctx context.Context, params api.ThreadCreateParams, recorded func(threadID, turnID string) error) (api.Thread, error)
	SendMessage(ctx context.Context, threadID string, params api.ThreadMessageParams) (api.ThreadMessage, error)
	DecideApproval(ctx context.Context, threadID, approvalID string, params api.ApprovalDecisionParams) (api.ThreadApproval, error)
	AnswerInput(ctx context.Context, threadID, requestID string, params api.InputAnswerParams) (api.ThreadInputRequest, error)
	StopThread(ctx context.Context, threadID string, params api.ThreadStopParams) (api.ThreadStop, error)
}

// ThreadReader reads the normalized Thread and its operations
// (threads.Service in production).
type ThreadReader interface {
	Get(id string) (api.Thread, error)
	InputRequest(threadID, requestID string) (api.ThreadInputRequest, error)
	Stop(threadID, stopID string) (api.ThreadStop, error)
}

// Options wires a Service.
type Options struct {
	// SetupPath is the setup file (paths.LinearSetupFile).
	SetupPath   string
	Repository  *store.Linear
	Coordinator Coordinator
	Threads     ThreadReader
	Hub         *events.Hub
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
// process.go), the session loop that drives and watches sessions
// (session.go, observe.go), the outbox sender, and the credential probe
// behind the connection report. Construct with New; Run drives the
// loops.
type Service struct {
	setupPath   string
	repo        *store.Linear
	coordinator Coordinator
	threads     ThreadReader
	hub         *events.Hub
	ingress     func(ctx context.Context) api.Webhooks
	logger      *slog.Logger
	now         func() time.Time
	http        *http.Client
	apiURL      string

	// Production cadences; tests shrink them.
	probeRetry, probeInterval, sendPoll, sessionPoll, retryBase, retryMax, authRetry, responseGrace time.Duration

	sendKick    chan struct{}
	sessionKick chan struct{}
	// force makes the next reconcile ignore retry backoffs: the program
	// came back, which is what a deferred start or dispatch waits for.
	force atomic.Bool
	// workers tracks the in-flight session workers; Run joins them.
	workers sync.WaitGroup

	// renewMu serializes token renewals: the probe and the sender can
	// both meet an expired token, and one refresh must serve both.
	renewMu sync.Mutex

	mu         sync.Mutex
	setup      *Setup
	connection api.IntegrationConnection
	// lastFailure summarizes the most recent outbox failure for status.
	lastFailure string
	// working holds the sessions this process has a worker in flight for.
	working map[string]bool
}

// New wires the Service. It loads the setup file once so the connection
// report is honest before Run starts.
func New(opts Options) *Service {
	if opts.Repository == nil || opts.Coordinator == nil || opts.Threads == nil || opts.Hub == nil {
		panic("linear.New: Repository, Coordinator, Threads, and Hub must not be nil")
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
		coordinator:   opts.Coordinator,
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
		working:       map[string]bool{},
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
		inFlight, _ := s.repo.PendingSubmissions(ctx)
		detail += fmt.Sprintf("; %d of %d sessions open, %d submissions in flight, %d Linear updates owed", open, total, inFlight, pending)
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
// loop until ctx is cancelled, then joins every worker still in flight.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Go(func() { s.probeLoop(ctx) })
	wg.Go(func() { s.sendLoop(ctx) })
	s.sessionLoop(ctx)
	wg.Wait()
	s.workers.Wait()
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

// sessionLoop drives every open session: starts the accepted ones,
// resumes the recorded ones, dispatches the bound ones' submissions, and
// watches their Threads. It wakes on a thread change event for a
// watched Thread (a reason to refetch, never the state itself), on an
// Integration's connection change (the program coming back is what a
// deferred start or dispatch waits for, so backoffs are skipped once),
// on a kick from Process, and on the poll — so a dropped event stream, a
// lost backlog, or a provider reconnect all converge on the next tick at
// the latest.
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
				if change.Resource == resource && change.ID == profileIntegration {
					s.force.Store(true)
					break inner
				}
				if change.Resource == "thread" && watched[change.ID] {
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
	force := s.force.Swap(false)
	sessions, err := s.repo.OpenSessions(ctx)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("linear: reading open sessions", "error", err)
		}
		if force {
			s.force.Store(true)
		}
		return previous
	}
	// A session whose worker is still in flight cannot take the force
	// now; it stays set for the reconcile that worker's exit wakes.
	work := func(sessionID string) {
		if !s.work(ctx, sessionID, force) && force {
			s.force.Store(true)
		}
	}
	watched := make(map[string]bool, len(sessions))
	for _, session := range sessions {
		switch session.State {
		case stateAccepted:
			work(session.ID)
		case stateStarting:
			s.mu.Lock()
			inflight := s.working[session.ID]
			s.mu.Unlock()
			if inflight {
				continue
			}
			// The list was read before the flag was checked; a start that
			// finished in between left a newer row. Only the fresh row
			// says whether anything is owed.
			fresh, err := s.repo.GetSession(ctx, session.ID)
			if err != nil || fresh.State != stateStarting {
				if err == nil && fresh.State == stateBound {
					watched[fresh.ThreadID] = true
					s.observe(ctx, fresh)
					work(fresh.ID)
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
				work(session.ID)
				continue
			}
			session.State = stateBound
			session.UpdatedAt = s.now()
			if err := s.repo.Record(ctx, store.LinearChange{Session: &session, Rows: []store.LinearOutboxRow{s.activity(session.ID, "uncertain", contentThought, uncertain())}}); err != nil {
				s.logger.Error("linear: resuming a recorded start", "session", session.ID, "error", err)
				continue
			}
			s.wake(s.sendKick)
			watched[session.ThreadID] = true
			s.observe(ctx, session)
			work(session.ID)
		case stateBound:
			watched[session.ThreadID] = true
			s.observe(ctx, session)
			work(session.ID)
		}
	}
	return watched
}

// work drives a session on its own goroutine — a slow T3 must not hold
// up the acknowledgement or the watch of any other session — at most
// once per session per process; false reports a worker already in
// flight. The worker starts the Thread if owed and dispatches the
// submissions in order (session.go), then returns when nothing is due.
// A worker that did something wakes the loop, so what it changed is
// observed at once; one that found nothing due leaves the loop to its
// events and poll.
func (s *Service) work(ctx context.Context, sessionID string, force bool) bool {
	s.mu.Lock()
	if s.working[sessionID] {
		s.mu.Unlock()
		return false
	}
	s.working[sessionID] = true
	s.mu.Unlock()
	s.workers.Go(func() {
		progressed := s.drive(ctx, sessionID, force)
		s.mu.Lock()
		delete(s.working, sessionID)
		s.mu.Unlock()
		if progressed {
			s.wake(s.sessionKick)
		}
	})
	return true
}

// sessionLinks reads the links of the Thread a session is bound to, if
// any.
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

// backoff is the delay before the next attempt: retryBase doubling per
// failed attempt, capped at retryMax.
func (s *Service) backoff(attempts int) time.Duration {
	delay := s.retryBase
	for i := 1; i < attempts && delay < s.retryMax; i++ {
		delay *= 2
	}
	return min(delay, s.retryMax)
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
