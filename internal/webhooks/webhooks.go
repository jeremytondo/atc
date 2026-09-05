// Package webhooks is ATC's built-in webhook ingress (ATC-306): the shared
// contract through which built-in Integrations receive deliveries from
// external systems without running listeners, managing exposure, or
// storing anything themselves.
//
// The trust boundary is two processes. A restricted receiver (package
// receiver, a child of the server sandboxed with Landlock) terminates the
// public traffic Tailscale Funnel forwards to it and relays each request,
// bounded, over a loopback channel to this package inside trusted Core.
// Core treats everything arriving on the channel as untrusted public
// input: the route's Integration verifies and authorizes it here, Core
// enforces its own limits again, and only an authorized delivery enters
// the durable inbox — which is when, and only when, the sender is told it
// was accepted. Acceptance means ATC owns the delivery's handling, not
// that any action completed.
//
// The inbox is a table in ATC's own store. Acceptance and deduplication
// are one atomic write keyed on the Integration's delivery identity, so a
// redelivery — including one racing its original — never becomes a second
// entry. A worker processes pending deliveries and resumes them after a
// restart; processing may run more than once, and the consuming
// Integration owns duplicate-action prevention. Completion drops the
// payload and keeps a receipt for deduplication.
//
// Concrete bounds (each also enforced by the receiver, so a compromised
// receiver cannot widen them): request bodies MaxBodyBytes, headers
// MaxHeaderBytes, channelConcurrency deliveries in flight, verifyTimeout
// per verification, processTimeout per processing attempt, DefaultCapacity
// pending deliveries (a full inbox refuses with a retryable 503 and never
// evicts accepted work), receipts kept receiptRetention or receiptKeep
// newest whichever bounds first, retries at retryBase doubling to
// retryMax. Rejections and processing failures are summarized by route,
// status, and the Integration's reason — never headers, signatures, or
// payloads.
package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/store"
)

const (
	// MaxBodyBytes bounds one delivery's body, at the receiver and again
	// at the channel.
	MaxBodyBytes = 1 << 20
	// MaxHeaderBytes bounds one delivery's headers, at both hops.
	MaxHeaderBytes = 32 << 10
	// DefaultCapacity is the pending-delivery bound when Options.Capacity
	// is zero.
	DefaultCapacity = 1000

	channelConcurrency = 32
	verifyTimeout      = 10 * time.Second
	processTimeout     = 5 * time.Minute
	processBatch       = 16
	pollInterval       = 5 * time.Second
	pruneInterval      = time.Hour
	receiptRetention   = 30 * 24 * time.Hour
	receiptKeep        = 10000
	retryBase          = 5 * time.Second
	retryMax           = time.Hour
	// summaryLimit bounds the failure text kept for status.
	summaryLimit = 200
)

// Request is one delivery as the public endpoint received it: the
// protocol information an Integration needs to verify it, with the body
// preserved byte for byte. Nothing in it is trusted.
type Request struct {
	Method string
	// Path is the route path the delivery arrived on.
	Path     string
	RawQuery string
	Header   http.Header
	Body     []byte
}

// Delivery is what an Integration's Verify returns for an authenticated,
// authorized request: the protocol's delivery identity, and the data to
// keep for processing.
type Delivery struct {
	// ID is the Integration's delivery identity — whatever its protocol
	// supplies for redelivery detection. Deduplication is scoped to the
	// Integration; a redelivery of an ID already accepted is acknowledged
	// without a new inbox entry, for as long as its receipt is retained.
	ID string
	// Payload is stored with the delivery until processing completes;
	// typically the body, or the parts of it processing needs.
	Payload []byte
}

// Rejection is the error Verify returns to refuse a request: the HTTP
// status to answer with and a reason safe to show in status output
// (never a secret, signature, or payload).
type Rejection struct {
	Status int
	Reason string
}

func (r *Rejection) Error() string { return r.Reason }

// Reject builds a Rejection.
func Reject(status int, reason string) error {
	return &Rejection{Status: status, Reason: reason}
}

// Accepted is a durably stored delivery handed to Process.
type Accepted struct {
	// ID is the stable ATC identity.
	ID            string
	IntegrationID string
	Route         string
	// DeliveryID is the Integration's own identity for it.
	DeliveryID string
	Payload    []byte
	// Attempts counts earlier failed processing attempts.
	Attempts int
}

// Handler is what an Integration implements for one route.
type Handler interface {
	// Verify authenticates and authorizes one untrusted request and
	// decides what to keep: signature and freshness checks, sender and
	// event authorization, and payload interpretation are the
	// Integration's. It returns a *Rejection to refuse; any other error is
	// an internal failure answered with 500 and never stored.
	Verify(ctx context.Context, req Request) (Delivery, error)
	// Process handles a durably accepted delivery. It may be called more
	// than once for the same delivery (a crash between processing and
	// completion, a timeout); preventing duplicate actions is the
	// Integration's and its domains' responsibility. An error schedules a
	// retry with backoff.
	Process(ctx context.Context, delivery Accepted) error
}

// Route is one Integration's registration: the public path it listens on
// and the handler that owns it.
type Route struct {
	IntegrationID string
	// Path is the route under the public base URL, e.g. "/linear".
	Path    string
	Handler Handler
}

// Inbox is the durable store the Service needs; *store.Webhooks
// implements it.
type Inbox interface {
	Accept(ctx context.Context, record store.WebhookDelivery, capacity int) (bool, error)
	Due(ctx context.Context, now time.Time, limit int) ([]store.WebhookDelivery, error)
	Complete(ctx context.Context, id string, at time.Time) (bool, error)
	Fail(ctx context.Context, id string, attempts int, next time.Time, reason string) (bool, error)
	Pending(ctx context.Context) (int, error)
	Prune(ctx context.Context, cutoff time.Time, keep int) error
}

// Options wires a Service.
type Options struct {
	Inbox Inbox
	// Routes are the built-in Integrations' registrations. Paths must be
	// unique and absolute.
	Routes []Route
	// Capacity bounds pending deliveries; zero means DefaultCapacity.
	Capacity int
	// Ingress enables intake: the restricted receiver and its Funnel
	// exposure. Nil leaves intake disabled while accepted deliveries keep
	// being processed.
	Ingress *IngressOptions
	Logger  *slog.Logger
	Now     func() time.Time
}

// Service owns the routes, the acceptance path, the processing worker,
// and — when intake is enabled — the ingress lifecycle. Construct with
// New; Run drives it.
type Service struct {
	inbox    Inbox
	routes   map[string]Route
	ordered  []Route
	capacity int
	logger   *slog.Logger
	now      func() time.Time
	ingress  *ingress

	// inflight bounds concurrent channel deliveries.
	inflight chan struct{}
	// kick wakes the worker after an acceptance.
	kick chan struct{}
	// poll is the worker's idle wake interval; tests shrink it.
	poll time.Duration

	mu             sync.Mutex
	rejected       int
	lastRejection  string
	failures       int
	lastFailure    string
	storageFailing bool
	exposure       exposureState
}

// New validates the registrations and wires the Service.
func New(opts Options) (*Service, error) {
	if opts.Inbox == nil {
		panic("webhooks.New: Inbox must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Capacity == 0 {
		opts.Capacity = DefaultCapacity
	}
	s := &Service{
		inbox:    opts.Inbox,
		routes:   make(map[string]Route, len(opts.Routes)),
		capacity: opts.Capacity,
		logger:   opts.Logger,
		now:      opts.Now,
		inflight: make(chan struct{}, channelConcurrency),
		kick:     make(chan struct{}, 1),
		poll:     pollInterval,
	}
	for _, route := range opts.Routes {
		switch {
		case route.IntegrationID == "":
			return nil, fmt.Errorf("webhook route %q has no integration", route.Path)
		case !strings.HasPrefix(route.Path, "/") || len(route.Path) < 2 || strings.HasSuffix(route.Path, "/"):
			return nil, fmt.Errorf("webhook route %q for %s: paths are absolute, non-root, without a trailing slash", route.Path, route.IntegrationID)
		case route.Handler == nil:
			return nil, fmt.Errorf("webhook route %q for %s has no handler", route.Path, route.IntegrationID)
		}
		if _, taken := s.routes[route.Path]; taken {
			return nil, fmt.Errorf("webhook route %q registered twice", route.Path)
		}
		s.routes[route.Path] = route
		s.ordered = append(s.ordered, route)
	}
	if opts.Ingress != nil {
		s.ingress = newIngress(*opts.Ingress, s)
	} else {
		s.exposure = exposureState{state: api.WebhooksDisabled}
	}
	return s, nil
}

// Run drives processing, and intake when enabled, until ctx is cancelled.
// It returns once the worker has stopped and every ingress child is gone.
func (s *Service) Run(ctx context.Context) {
	var wg sync.WaitGroup
	if s.ingress != nil {
		wg.Go(func() { s.ingress.run(ctx) })
	}
	s.work(ctx)
	wg.Wait()
}

// ChannelHandler is the handler the receiver's channel targets: the
// registered routes and nothing else — no API, no docs, no hook routes.
// Exposed for tests; production reaches it only through the ingress
// channel listener.
func (s *Service) ChannelHandler() http.Handler {
	return http.HandlerFunc(s.serveDelivery)
}

// serveDelivery runs one untrusted delivery through the acceptance path:
// route lookup, admission, the Integration's verification, durable
// acceptance, acknowledgement.
func (s *Service) serveDelivery(w http.ResponseWriter, r *http.Request) {
	route, ok := s.routes[r.URL.Path]
	if !ok {
		writeProblem(w, http.StatusNotFound, "webhook_route_not_found", "no such webhook route")
		return
	}
	select {
	case s.inflight <- struct{}{}:
		defer func() { <-s.inflight }()
	default:
		w.Header().Set("Retry-After", "5")
		writeProblem(w, http.StatusServiceUnavailable, "webhook_busy", "too many deliveries in flight; retry")
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			s.recordRejection(route, http.StatusRequestEntityTooLarge, "body exceeds limit")
			writeProblem(w, http.StatusRequestEntityTooLarge, "webhook_body_too_large", fmt.Sprintf("body exceeds %d bytes", MaxBodyBytes))
			return
		}
		writeProblem(w, http.StatusBadRequest, "webhook_body_unreadable", "request body could not be read")
		return
	}

	verifyCtx, cancel := context.WithTimeout(r.Context(), verifyTimeout)
	defer cancel()
	delivery, err := route.Handler.Verify(verifyCtx, Request{
		Method: r.Method, Path: r.URL.Path, RawQuery: r.URL.RawQuery, Header: r.Header, Body: body,
	})
	if err != nil {
		var rejection *Rejection
		if errors.As(err, &rejection) {
			s.recordRejection(route, rejection.Status, rejection.Reason)
			writeProblem(w, rejection.Status, "webhook_rejected", rejection.Reason)
			return
		}
		s.recordRejection(route, http.StatusInternalServerError, "verification failed")
		s.logger.Error("webhook verification failed", "route", route.Path, "integration", route.IntegrationID, "error", err)
		writeProblem(w, http.StatusInternalServerError, "webhook_verification_failed", "verification failed")
		return
	}
	if delivery.ID == "" {
		s.recordRejection(route, http.StatusInternalServerError, "integration supplied no delivery identity")
		s.logger.Error("webhook integration supplied no delivery identity", "route", route.Path, "integration", route.IntegrationID)
		writeProblem(w, http.StatusInternalServerError, "webhook_verification_failed", "verification failed")
		return
	}

	now := s.now()
	record := store.WebhookDelivery{
		ID:            ids.NewLong("whk-"),
		IntegrationID: route.IntegrationID,
		Route:         route.Path,
		DeliveryID:    delivery.ID,
		Payload:       delivery.Payload,
		NextAttemptAt: now,
		AcceptedAt:    now,
	}
	accepted, err := s.inbox.Accept(r.Context(), record, s.capacity)
	if err != nil {
		w.Header().Set("Retry-After", "60")
		if errors.Is(err, store.ErrInboxFull) {
			writeProblem(w, http.StatusServiceUnavailable, "webhook_inbox_full", "the inbox is at capacity; retry later")
			return
		}
		s.setStorageFailing(true)
		s.logger.Error("webhook acceptance could not be stored", "route", route.Path, "error", err)
		writeProblem(w, http.StatusServiceUnavailable, "webhook_storage_failed", "the delivery could not be stored; retry later")
		return
	}
	s.setStorageFailing(false)
	response := map[string]any{"status": "accepted"}
	if accepted {
		response["id"] = record.ID
		select {
		case s.kick <- struct{}{}:
		default:
		}
	} else {
		response["duplicate"] = true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(response)
}

func writeProblem(w http.ResponseWriter, status int, code, detail string) {
	p := api.Problem{Title: http.StatusText(status), Status: status, Code: code, Detail: detail}
	w.Header().Set("Content-Type", p.ContentType(""))
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(p)
}

func (s *Service) recordRejection(route Route, status int, reason string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rejected++
	s.lastRejection = truncate(fmt.Sprintf("%s: %d %s", route.Path, status, reason))
}

func (s *Service) recordFailure(route string, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failures++
	s.lastFailure = truncate(fmt.Sprintf("%s: %v", route, err))
}

func (s *Service) setStorageFailing(failing bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.storageFailing = failing
}

func truncate(text string) string {
	if len(text) > summaryLimit {
		return text[:summaryLimit-1] + "…"
	}
	return text
}

// work is the processing loop: due deliveries in batches, each attempt
// bounded, failures rescheduled with backoff; receipts pruned hourly. It
// runs whether or not intake is enabled, so deliveries accepted before a
// restart or before intake was disabled still complete.
func (s *Service) work(ctx context.Context) {
	s.prune(ctx)
	lastPrune := s.now()
	for {
		if ctx.Err() != nil {
			return
		}
		busy := s.processDue(ctx)
		if s.now().Sub(lastPrune) >= pruneInterval {
			s.prune(ctx)
			lastPrune = s.now()
		}
		if busy {
			// A full batch means more may be due; go straight back.
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		case <-time.After(s.poll):
		}
	}
}

// processDue handles one batch and reports whether it was full.
func (s *Service) processDue(ctx context.Context) bool {
	due, err := s.inbox.Due(ctx, s.now(), processBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.setStorageFailing(true)
			s.logger.Error("webhook inbox could not be read", "error", err)
		}
		return false
	}
	s.setStorageFailing(false)
	for _, record := range due {
		if ctx.Err() != nil {
			return false
		}
		s.process(ctx, record)
	}
	return len(due) == processBatch
}

func (s *Service) process(ctx context.Context, record store.WebhookDelivery) {
	route, ok := s.routes[record.Route]
	var err error
	if !ok {
		err = fmt.Errorf("no handler registered for route %s", record.Route)
	} else {
		processCtx, cancel := context.WithTimeout(ctx, processTimeout)
		err = route.Handler.Process(processCtx, Accepted{
			ID: record.ID, IntegrationID: record.IntegrationID, Route: record.Route,
			DeliveryID: record.DeliveryID, Payload: record.Payload, Attempts: record.Attempts,
		})
		cancel()
	}
	if ctx.Err() != nil {
		// Shutdown mid-attempt: leave the row for the next start.
		return
	}
	now := s.now()
	if err != nil {
		attempts := record.Attempts + 1
		s.recordFailure(record.Route, err)
		s.logger.Warn("webhook processing failed", "id", record.ID, "route", record.Route, "attempt", attempts, "error", err)
		if _, failErr := s.inbox.Fail(ctx, record.ID, attempts, now.Add(backoff(attempts)), truncate(err.Error())); failErr != nil {
			s.setStorageFailing(true)
			s.logger.Error("webhook failure could not be recorded", "id", record.ID, "error", failErr)
		}
		return
	}
	if _, err := s.inbox.Complete(ctx, record.ID, now); err != nil {
		s.setStorageFailing(true)
		s.logger.Error("webhook completion could not be recorded", "id", record.ID, "error", err)
	}
}

// backoff is the delay before the next attempt: retryBase doubling per
// failed attempt, capped at retryMax.
func backoff(attempts int) time.Duration {
	delay := retryBase
	for i := 1; i < attempts && delay < retryMax; i++ {
		delay *= 2
	}
	return min(delay, retryMax)
}

func (s *Service) prune(ctx context.Context) {
	if err := s.inbox.Prune(ctx, s.now().Add(-receiptRetention), receiptKeep); err != nil && ctx.Err() == nil {
		s.logger.Warn("webhook receipts could not be pruned", "error", err)
	}
}

// exposureState is the ingress lifecycle's view for status.
type exposureState struct {
	state  api.WebhookState
	url    string
	action string
	reason string
}

func (s *Service) setExposure(state exposureState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exposure = state
	if state.state == api.WebhooksReady {
		s.logger.Info("webhook ingress ready", "url", state.url)
	} else {
		s.logger.Info("webhook ingress "+string(state.state), "url", state.url, "reason", state.reason, "action", strings.Join(strings.Fields(state.action), " "))
	}
}

// Status reports the ingress and inbox state for the API and the CLI.
func (s *Service) Status(ctx context.Context) api.Webhooks {
	pending, err := s.inbox.Pending(ctx)
	if err != nil {
		s.setStorageFailing(true)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	status := api.Webhooks{
		State:                 s.exposure.state,
		URL:                   s.exposure.url,
		Routes:                make([]api.WebhookRoute, 0, len(s.ordered)),
		Action:                s.exposure.action,
		Reason:                s.exposure.reason,
		Pending:               pending,
		IntakeBlocked:         s.storageFailing || pending >= s.capacity,
		Rejected:              s.rejected,
		LastRejection:         s.lastRejection,
		ProcessingFailures:    s.failures,
		LastProcessingFailure: s.lastFailure,
	}
	for _, route := range s.ordered {
		status.Routes = append(status.Routes, api.WebhookRoute{IntegrationID: route.IntegrationID, Path: route.Path})
	}
	return status
}
