package linear

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
	"github.com/jeremytondo/atc/internal/webhooks"
)

const (
	testOrg      = "org-1"
	testClient   = "client-1"
	testSecret   = "whsec-1"
	testProject  = "proj-fixtr"
	providerT3   = "t3code"
	testPollFast = 20 * time.Millisecond
)

// noTerminals is the threads domain's terminal seam: nothing here ever
// runs in an ATC terminal.
type noTerminals struct{}

func (noTerminals) Get(string) (api.Terminal, error) {
	return api.Terminal{}, errors.New("no terminals")
}

// fakeClock advances only when told to, so grace windows are decided by
// the test. Every reading moves a millisecond so timestamps stay ordered.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// fakeStarter is the application coordinator's creation seam: it records
// the thread and its provisional turn in the real threads domain, exactly
// as the coordinator does, then answers as the test decided — after a
// block the test controls, so a slow T3 is one channel close away.
type fakeStarter struct {
	threads *threads.Service
	dir     string

	mu       sync.Mutex
	block    chan struct{}
	prepare  error
	dispatch error
	recorded error
	calls    []api.ThreadCreateParams
	// created maps each created ATC thread id to its provider id.
	created map[string]string
	nextID  int
}

func (f *fakeStarter) StartThread(ctx context.Context, params api.ThreadCreateParams, recorded func(threadID, turnID string) error) (api.Thread, error) {
	f.mu.Lock()
	f.calls = append(f.calls, params)
	block, prepareErr, dispatchErr, recordedErr := f.block, f.prepare, f.dispatch, f.recorded
	f.nextID++
	providerID := fmt.Sprintf("t3-thread-%d", f.nextID)
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return api.Thread{}, ctx.Err()
		}
	}
	// Preparation failures happen before the coordinator records anything.
	// Dispatch failures happen after the record and durability callback.
	if prepareErr != nil {
		return api.Thread{}, prepareErr
	}
	id, err := f.threads.ObserveExternal(ctx, threads.ExternalObservation{
		IntegrationID: providerT3, ProviderID: providerID, InitialDirectory: f.dir, AgentID: params.Agent, Title: "T",
		Metadata: threads.Metadata{Model: params.Model, Cwd: f.dir},
	})
	if err != nil {
		return api.Thread{}, err
	}
	turnID, err := f.threads.SubmitTurn(ctx, id)
	if err != nil {
		return api.Thread{}, err
	}
	discard := func() { _ = f.threads.DiscardExternal(context.WithoutCancel(ctx), providerT3, providerID) }
	if recordedErr != nil {
		discard()
		return api.Thread{}, fmt.Errorf("recording the thread before dispatch: %w", recordedErr)
	}
	if err := recorded(id, turnID); err != nil {
		discard()
		return api.Thread{}, fmt.Errorf("recording the thread before dispatch: %w", err)
	}
	f.mu.Lock()
	f.created[id] = providerID
	f.mu.Unlock()
	if dispatchErr != nil {
		// As the coordinator does: a recorded create whose dispatch went
		// unanswered keeps its record; any other failure discards it.
		if !errors.Is(dispatchErr, integrations.ErrThreadCreationUncertain) {
			discard()
		}
		return api.Thread{}, dispatchErr
	}
	return f.threads.Get(id)
}

func (f *fakeStarter) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// params copies the create parameters seen so far.
func (f *fakeStarter) params() []api.ThreadCreateParams {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]api.ThreadCreateParams(nil), f.calls...)
}

func (f *fakeStarter) set(change func(*fakeStarter)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(f)
}

// providerOf is the provider id of a created thread.
func (f *fakeStarter) providerOf(threadID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.created[threadID]
}

// fixture is the Integration over the real store, threads domain, and
// event hub, a fake Linear API, and a fake starter.
type fixture struct {
	t         *testing.T
	store     *store.Store
	hub       *events.Hub
	threads   *threads.Service
	linear    *fakeLinear
	starter   *fakeStarter
	clock     *fakeClock
	setupPath string
	dir       string
	service   *Service
	cancel    context.CancelFunc
	done      chan struct{}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	hub := events.NewHubAt(256, 1)
	clock := &fakeClock{now: time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)}
	threadService := threads.NewService(threads.Options{Repository: db.Threads(), Terminals: noTerminals{}, Projects: db.Projects(), Hub: hub, Now: clock.Now})
	if err := threadService.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if ok, err := db.Projects().Insert(context.Background(), store.ProjectRecord{ID: testProject, Name: "atc", Directory: dir, CreatedAt: clock.Now(), UpdatedAt: clock.Now()}); err != nil || !ok {
		t.Fatalf("planting project = %v, %v", ok, err)
	}
	// Links derive from the provider id, as T3 Code's do.
	threadService.SetLinker(providerT3, func(providerID string) *api.ThreadLinks {
		return &api.ThreadLinks{Web: "https://t3.test/env-1/" + providerID, App: "t3code://threads/env-1/" + providerID}
	})
	f := &fixture{
		t: t, store: db, hub: hub, threads: threadService, linear: newFakeLinear(t), clock: clock,
		starter:   &fakeStarter{threads: threadService, dir: dir, created: map[string]string{}},
		setupPath: filepath.Join(t.TempDir(), "linear.json"), dir: dir,
	}
	f.writeSetup(Setup{
		OrganizationID: testOrg, ClientID: testClient, ClientSecret: "secret-1", WebhookSigningSecret: testSecret,
		AccessToken: "token-0", RefreshToken: "refresh-0", ProjectID: testProject,
	})
	return f
}

func (f *fixture) writeSetup(setup Setup) {
	f.t.Helper()
	data, err := json.Marshal(setup)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(f.setupPath, data, 0o600); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) readSetup() Setup {
	f.t.Helper()
	setup, err := loadSetup(f.setupPath)
	if err != nil {
		f.t.Fatal(err)
	}
	return setup
}

// start builds the service with shrunk cadences and runs it until stop or
// the test's end.
func (f *fixture) start() *Service {
	f.t.Helper()
	f.service = New(Options{
		SetupPath: f.setupPath, Repository: f.store.Linear(), Starter: f.starter, Threads: f.threads, Hub: f.hub,
		Now: f.clock.Now, APIURL: f.linear.srv.URL, HTTPClient: &http.Client{Timeout: time.Second},
		Ingress: func(context.Context) api.Webhooks {
			return api.Webhooks{State: api.WebhooksReady, URL: "https://node.ts.net"}
		},
	})
	f.service.probeRetry = testPollFast
	f.service.probeInterval = 0 // the fake clock barely moves; probe on every tick
	f.service.sendPoll = testPollFast
	f.service.sessionPoll = testPollFast
	f.service.retryBase = testPollFast
	f.service.retryMax = 100 * time.Millisecond
	f.service.authRetry = 50 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	f.cancel = cancel
	f.done = make(chan struct{})
	service := f.service
	go func() {
		defer close(f.done)
		service.Run(ctx)
	}()
	f.t.Cleanup(f.stop)
	return service
}

// stop ends the running service and waits for it — a server shutdown.
func (f *fixture) stop() {
	if f.cancel == nil {
		return
	}
	f.cancel()
	select {
	case <-f.done:
	case <-time.After(5 * time.Second):
		f.t.Error("Run did not return after cancellation")
	}
	f.cancel = nil
}

// restart is a server restart from the Integration's point of view: the
// same store, a fresh service.
func (f *fixture) restart() *Service {
	f.t.Helper()
	f.stop()
	return f.start()
}

func (f *fixture) session(id string) store.LinearSession {
	f.t.Helper()
	session, err := f.store.Linear().GetSession(context.Background(), id)
	if err != nil {
		f.t.Fatalf("session %s: %v", id, err)
	}
	return session
}

// waitSession waits until a session satisfies cond and returns it.
func (f *fixture) waitSession(id string, what string, cond func(store.LinearSession) bool) store.LinearSession {
	f.t.Helper()
	var session store.LinearSession
	waitFor(f.t, what, func() bool {
		got, err := f.store.Linear().GetSession(context.Background(), id)
		if err != nil {
			return false
		}
		session = got
		return cond(got)
	})
	return session
}

// waitActivities waits for n activities on a session and returns them.
func (f *fixture) waitActivities(sessionID string, n int) []recordedActivity {
	f.t.Helper()
	var got []recordedActivity
	waitFor(f.t, fmt.Sprintf("%d activities on %s", n, sessionID), func() bool {
		got = f.linear.activitiesOf(sessionID)
		return len(got) >= n
	})
	return got
}

// startedThread waits until the session is started and returns its
// thread id and provider id.
func (f *fixture) startedThread(sessionID string) (threadID, providerID string) {
	f.t.Helper()
	session := f.waitSession(sessionID, "session started", func(s store.LinearSession) bool { return s.State == stateStarted })
	return session.ThreadID, f.starter.providerOf(session.ThreadID)
}

// report feeds T3's view of the thread's turn to the threads domain, as
// the T3 Code Integration would.
func (f *fixture) report(providerID string, status api.ThreadStatus, turn *threads.TurnObservation) {
	f.t.Helper()
	if _, err := f.threads.ObserveExternal(context.Background(), threads.ExternalObservation{
		IntegrationID: providerT3, ProviderID: providerID, InitialDirectory: f.dir, AgentID: "codex", Title: "T", Status: status, Turn: turn,
	}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) process(t *testing.T, deliveryID string, body []byte) {
	t.Helper()
	if err := f.service.Process(context.Background(), webhooks.Accepted{ID: "whk-" + deliveryID, DeliveryID: deliveryID, Payload: body}); err != nil {
		t.Fatalf("Process(%s) = %v", deliveryID, err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// Linear payload fixtures.

type eventOpt func(map[string]any)

func withPromptContext(context string) eventOpt {
	return func(m map[string]any) { m["promptContext"] = context }
}

func withOrg(org string) eventOpt {
	return func(m map[string]any) { m["organizationId"] = org }
}

func withClient(client string) eventOpt {
	return func(m map[string]any) { m["oauthClientId"] = client }
}

func withTimestamp(at time.Time) eventOpt {
	return func(m map[string]any) { m["webhookTimestamp"] = at.UnixMilli() }
}

// createdEvent is a `created` AgentSessionEvent for an explicit mention
// with context, adjusted by opts.
func createdEvent(sessionID string, at time.Time, opts ...eventOpt) []byte {
	m := map[string]any{
		"type": "AgentSessionEvent", "action": "created", "createdAt": at.UTC().Format(time.RFC3339Nano),
		"organizationId": testOrg, "oauthClientId": testClient, "appUserId": "app-user-1",
		"webhookTimestamp": at.UnixMilli(), "webhookId": "wh-1",
		"agentSession": map[string]any{
			"id": sessionID, "status": "pending", "type": "commentThread", "appUserId": "app-user-1", "organizationId": testOrg,
			"url":     "https://linear.app/x/issue/ATC-302#agent-session",
			"issue":   map[string]any{"id": "iss-1", "identifier": "ATC-302", "title": "Prototype", "url": "https://linear.app/x/issue/ATC-302"},
			"comment": map[string]any{"id": "cmt-1", "body": "@atc what does the webhook receiver do?"},
			"creator": map[string]any{"id": "usr-1", "name": "Jeremy"},
		},
		"promptContext": "<issue identifier=\"ATC-302\"><title>Prototype</title></issue>\n<comment>@atc what does the webhook receiver do?</comment>",
	}
	for _, opt := range opts {
		opt(m)
	}
	body, _ := json.Marshal(m)
	return body
}

// promptedEvent is a `prompted` AgentSessionEvent: a user message, or a
// stop signal when signal is "stop".
func promptedEvent(sessionID string, at time.Time, text, signal string) []byte {
	activity := map[string]any{"id": "act-1", "agentSessionId": sessionID, "content": map[string]any{"type": "prompt", "body": text}}
	if signal != "" {
		activity["signal"] = signal
	}
	m := map[string]any{
		"type": "AgentSessionEvent", "action": "prompted", "createdAt": at.UTC().Format(time.RFC3339Nano),
		"organizationId": testOrg, "oauthClientId": testClient, "appUserId": "app-user-1",
		"webhookTimestamp": at.UnixMilli(), "webhookId": "wh-1",
		"agentSession":  map[string]any{"id": sessionID, "status": "active", "type": "commentThread", "appUserId": "app-user-1", "organizationId": testOrg},
		"agentActivity": activity,
	}
	body, _ := json.Marshal(m)
	return body
}

func sign(body []byte, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

// signedRequest is a delivery as the receiver would relay it.
func signedRequest(deliveryID string, body []byte, secret string) *http.Request {
	req := httptest.NewRequest(http.MethodPost, RoutePath, bytes.NewReader(body))
	if secret != "" {
		req.Header.Set(signatureHeader, sign(body, secret))
	}
	if deliveryID != "" {
		req.Header.Set(deliveryHeader, deliveryID)
	}
	return req
}

// ingress is the shared webhook Service over the real inbox with the
// Linear route, running; deliveries go through its channel handler.
func (f *fixture) ingress() *webhooks.Service {
	f.t.Helper()
	service, err := webhooks.New(webhooks.Options{
		Repository: f.store.Webhooks(),
		Routes:     []webhooks.Route{{IntegrationID: ID, Path: RoutePath, Handler: f.service}},
		Now:        f.clock.Now,
	})
	if err != nil {
		f.t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	f.t.Cleanup(func() {
		cancel()
		<-done
	})
	return service
}

func deliver(service *webhooks.Service, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	service.Handler().ServeHTTP(rec, req)
	return rec
}

func contains(t *testing.T, got, want string) {
	t.Helper()
	if !strings.Contains(got, want) {
		t.Errorf("%q does not contain %q", got, want)
	}
}

var errT3Refused = fmt.Errorf("%w: T3 Code rejected the command: no such project", integrations.ErrThreadCreationFailed)
var errT3Silent = fmt.Errorf("%w: %w: T3 Code did not answer: timeout", integrations.ErrThreadCreationFailed, integrations.ErrThreadCreationUncertain)
