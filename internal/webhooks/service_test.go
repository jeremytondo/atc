package webhooks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/webhooks/receiver"
)

// probeHandler is the test-only Integration: a shared-secret header is
// its authentication, a delivery header its protocol identity, a JSON
// object its payload contract. It records what it processed and can be
// told to fail or block.
type probeHandler struct {
	mu        sync.Mutex
	processed []Accepted
	failUntil map[string]int // delivery id → attempts that must fail
	block     chan struct{}  // Process waits on it while non-nil
	failAll   bool
}

const probeSecret = "s3cret-header-value"

func (h *probeHandler) Verify(_ context.Context, req Request) (Delivery, error) {
	if req.Header.Get("X-Probe-Secret") != probeSecret {
		return Delivery{}, Reject(http.StatusUnauthorized, "bad signature")
	}
	id := req.Header.Get("X-Probe-Delivery")
	if id == "" {
		return Delivery{}, Reject(http.StatusBadRequest, "missing delivery id")
	}
	var payload map[string]any
	if err := json.Unmarshal(req.Body, &payload); err != nil {
		return Delivery{}, Reject(http.StatusBadRequest, "malformed body")
	}
	if payload["forbidden"] == true {
		return Delivery{}, Reject(http.StatusForbidden, "sender not authorized")
	}
	return Delivery{ID: id, Payload: req.Body}, nil
}

func (h *probeHandler) Process(ctx context.Context, d Accepted) error {
	h.mu.Lock()
	block := h.block
	fails := h.failUntil[d.DeliveryID]
	failAll := h.failAll
	h.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if failAll || d.Attempts < fails {
		return fmt.Errorf("provider unreachable (attempt %d)", d.Attempts+1)
	}
	h.mu.Lock()
	h.processed = append(h.processed, d)
	h.mu.Unlock()
	return nil
}

func (h *probeHandler) count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.processed)
}

func openInbox(t *testing.T) (*store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "atc.db")
	db, err := store.Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db, path
}

// fixture is a Service over a real store with the probe route, running.
type fixture struct {
	service *Service
	handler *probeHandler
	inbox   Repository
	stop    func()
}

// fastClock advances a minute per reading, so a retry scheduled seconds
// out is due at the next look.
func fastClock() func() time.Time {
	var mu sync.Mutex
	now := time.Now()
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		now = now.Add(time.Minute)
		return now
	}
}

func newFixture(t *testing.T, inbox Repository, capacity int, now func() time.Time) *fixture {
	t.Helper()
	handler := &probeHandler{failUntil: map[string]int{}}
	service, err := New(Options{
		Repository: inbox,
		Routes:     []Route{{IntegrationID: "probe", Path: "/probe", Handler: handler}},
		Capacity:   capacity,
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Now:        now,
	})
	if err != nil {
		t.Fatal(err)
	}
	service.poll = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	stop := func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	}
	t.Cleanup(stop)
	return &fixture{service: service, handler: handler, inbox: inbox, stop: stop}
}

// deliver posts one request to the channel handler as the receiver would.
func (f *fixture) deliver(method, path, secret, deliveryID, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if secret != "" {
		req.Header.Set("X-Probe-Secret", secret)
	}
	if deliveryID != "" {
		req.Header.Set("X-Probe-Delivery", deliveryID)
	}
	rec := httptest.NewRecorder()
	f.service.serveDelivery(rec, req)
	return rec
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

func TestAcceptedDeliveryIsStoredThenProcessedAndDeduplicated(t *testing.T) {
	db, _ := openInbox(t)
	f := newFixture(t, db.Webhooks(), 10, nil)

	rec := f.deliver(http.MethodPost, "/probe", probeSecret, "evt-1", `{"n":1}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("valid delivery = %d %s, want 202", rec.Code, rec.Body)
	}
	var ack struct {
		Status string `json:"status"`
		ID     string `json:"id"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &ack); err != nil || ack.Status != "accepted" || !strings.HasPrefix(ack.ID, "whk-") {
		t.Fatalf("ack = %s, want accepted with an ATC id", rec.Body)
	}
	waitFor(t, "processing", func() bool { return f.handler.count() == 1 })
	got := f.handler.processed[0]
	if got.ID != ack.ID || got.DeliveryID != "evt-1" || string(got.Payload) != `{"n":1}` {
		t.Errorf("processed = %+v, want the acknowledged delivery", got)
	}
	waitFor(t, "completion", func() bool {
		pending, err := f.inbox.Pending(context.Background())
		return err == nil && pending == 0
	})

	// The redelivery is acknowledged without a new entry or a second
	// processing, whether it races or arrives after completion.
	rec = f.deliver(http.MethodPost, "/probe", probeSecret, "evt-1", `{"n":1}`)
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("redelivery = %d %s, want 202 duplicate", rec.Code, rec.Body)
	}
	time.Sleep(50 * time.Millisecond)
	if f.handler.count() != 1 {
		t.Errorf("redelivery was processed again (%d)", f.handler.count())
	}
	status := f.service.Status(context.Background())
	want := api.Webhooks{State: api.WebhooksDisabled, Routes: []api.WebhookRoute{{IntegrationID: "probe", Path: "/probe"}}}
	if status.State != want.State || status.Pending != 0 || status.IntakeBlocked || status.Rejected != 0 || len(status.Routes) != 1 || status.Routes[0] != want.Routes[0] {
		t.Errorf("status = %+v, want disabled intake, empty inbox, one route", status)
	}
}

// Nothing rejected is stored, and the summaries carry no request data.
func TestRejectedDeliveriesAreNeverStored(t *testing.T) {
	db, _ := openInbox(t)
	f := newFixture(t, db.Webhooks(), 10, nil)
	oversized := strings.Repeat("x", receiver.MaxBodyBytes+1)
	for name, tc := range map[string]struct {
		method, path, secret, id, body string
		want                           int
	}{
		"bad signature":   {http.MethodPost, "/probe", "wrong", "evt-2", `{}`, http.StatusUnauthorized},
		"no signature":    {http.MethodPost, "/probe", "", "evt-2", `{}`, http.StatusUnauthorized},
		"unauthorized":    {http.MethodPost, "/probe", probeSecret, "evt-2", `{"forbidden":true}`, http.StatusForbidden},
		"malformed":       {http.MethodPost, "/probe", probeSecret, "evt-2", `not json`, http.StatusBadRequest},
		"no delivery id":  {http.MethodPost, "/probe", probeSecret, "", `{}`, http.StatusBadRequest},
		"oversized":       {http.MethodPost, "/probe", probeSecret, "evt-2", oversized, http.StatusRequestEntityTooLarge},
		"unknown route":   {http.MethodPost, "/nope", probeSecret, "evt-2", `{}`, http.StatusNotFound},
		"api route":       {http.MethodGet, "/v1/health", probeSecret, "evt-2", ``, http.StatusNotFound},
		"hook route":      {http.MethodPost, "/internal/claude/hooks", probeSecret, "evt-2", `{}`, http.StatusNotFound},
		"docs":            {http.MethodGet, "/docs", probeSecret, "evt-2", ``, http.StatusNotFound},
		"trailing slash":  {http.MethodPost, "/probe/", probeSecret, "evt-2", `{}`, http.StatusNotFound},
		"route as prefix": {http.MethodPost, "/probe/extra", probeSecret, "evt-2", `{}`, http.StatusNotFound},
	} {
		rec := f.deliver(tc.method, tc.path, tc.secret, tc.id, tc.body)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, tc.want, rec.Body)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: Content-Type = %q, want problem+json", name, ct)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if pending, _ := f.inbox.Pending(context.Background()); pending != 0 || f.handler.count() != 0 {
		t.Errorf("rejected deliveries reached the inbox: pending=%d processed=%d", pending, f.handler.count())
	}
	status := f.service.Status(context.Background())
	if status.Rejected != 6 {
		t.Errorf("rejected = %d, want 6 (route misses are not rejections)", status.Rejected)
	}
	if strings.Contains(status.LastRejection, probeSecret) || strings.Contains(status.LastRejection, "wrong") {
		t.Errorf("last rejection %q leaks request data", status.LastRejection)
	}
	if !strings.HasPrefix(status.LastRejection, "/probe: ") {
		t.Errorf("last rejection %q does not name the route and status", status.LastRejection)
	}
}

// Concurrent redeliveries of one delivery yield one inbox entry and one
// processing, every sender acknowledged.
func TestConcurrentRedeliveriesAcceptOnce(t *testing.T) {
	db, _ := openInbox(t)
	f := newFixture(t, db.Webhooks(), 100, nil)
	var wg sync.WaitGroup
	codes := make([]int, 24)
	for i := range codes {
		wg.Go(func() {
			codes[i] = f.deliver(http.MethodPost, "/probe", probeSecret, "evt-race", `{"race":true}`).Code
		})
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusAccepted {
			t.Errorf("sender %d got %d, want 202", i, code)
		}
	}
	waitFor(t, "processing", func() bool { return f.handler.count() >= 1 })
	time.Sleep(50 * time.Millisecond)
	if f.handler.count() != 1 {
		t.Errorf("processed %d times, want 1", f.handler.count())
	}
}

// A full inbox refuses with a retryable failure and reports intake
// blocked; accepted work is untouched, and intake recovers as the
// backlog drains. Storage failures behave the same way.
func TestCapacityAndStorageFailuresBlockIntakeWithoutLoss(t *testing.T) {
	db, _ := openInbox(t)
	failing := &flakyInbox{Repository: db.Webhooks()}
	f := newFixture(t, failing, 2, nil)
	release := make(chan struct{})
	f.handler.mu.Lock()
	f.handler.block = release
	f.handler.mu.Unlock()

	for _, id := range []string{"evt-a", "evt-b"} {
		if rec := f.deliver(http.MethodPost, "/probe", probeSecret, id, `{}`); rec.Code != http.StatusAccepted {
			t.Fatalf("%s = %d, want 202", id, rec.Code)
		}
	}
	rec := f.deliver(http.MethodPost, "/probe", probeSecret, "evt-c", `{}`)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("over capacity = %d (Retry-After %q), want 503 with Retry-After", rec.Code, rec.Header().Get("Retry-After"))
	}
	if rec := f.deliver(http.MethodPost, "/probe", probeSecret, "evt-a", `{}`); rec.Code != http.StatusAccepted {
		t.Errorf("redelivery at capacity = %d, want 202 (nothing new to store)", rec.Code)
	}
	status := f.service.Status(context.Background())
	if !status.IntakeBlocked || status.Pending != 2 {
		t.Errorf("status = %+v, want intake blocked with 2 pending", status)
	}

	close(release)
	waitFor(t, "backlog drained", func() bool { return f.handler.count() == 2 })
	waitFor(t, "intake unblocked", func() bool { return !f.service.Status(context.Background()).IntakeBlocked })
	if rec := f.deliver(http.MethodPost, "/probe", probeSecret, "evt-c", `{}`); rec.Code != http.StatusAccepted {
		t.Errorf("after drain = %d, want 202", rec.Code)
	}

	failing.fail.Store(true)
	rec = f.deliver(http.MethodPost, "/probe", probeSecret, "evt-d", `{}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("storage failure = %d, want 503", rec.Code)
	}
	if !f.service.Status(context.Background()).IntakeBlocked {
		t.Error("storage failure did not report intake blocked")
	}
	failing.fail.Store(false)
	if rec := f.deliver(http.MethodPost, "/probe", probeSecret, "evt-d", `{}`); rec.Code != http.StatusAccepted {
		t.Errorf("after storage recovered = %d, want 202", rec.Code)
	}
	waitFor(t, "intake unblocked after storage recovery", func() bool { return !f.service.Status(context.Background()).IntakeBlocked })
}

// An acknowledged delivery whose processing did not finish before the
// server stopped is processed by the next launch under the same ATC id —
// with intake disabled there, and possibly again if the earlier attempt
// had in fact run.
func TestUnfinishedDeliveriesResumeAfterRestart(t *testing.T) {
	db, _ := openInbox(t)
	first := newFixture(t, db.Webhooks(), 10, nil)
	first.handler.mu.Lock()
	first.handler.failAll = true
	first.handler.mu.Unlock()
	rec := first.deliver(http.MethodPost, "/probe", probeSecret, "evt-r", `{"resume":true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatal(rec.Code)
	}
	var ack struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &ack)
	waitFor(t, "a failed attempt", func() bool { return first.service.Status(context.Background()).ProcessingFailures >= 1 })
	status := first.service.Status(context.Background())
	if !strings.Contains(status.LastProcessingFailure, "/probe: provider unreachable") {
		t.Errorf("last failure = %q, want the route and reason", status.LastProcessingFailure)
	}
	first.stop()

	// The next launch: failures are due after backoff, so the clock runs
	// ahead of the wall.
	second := newFixture(t, db.Webhooks(), 10, fastClock())
	waitFor(t, "resumed processing", func() bool { return second.handler.count() == 1 })
	got := second.handler.processed[0]
	if got.ID != ack.ID || got.DeliveryID != "evt-r" || string(got.Payload) != `{"resume":true}` || got.Attempts != 1 {
		t.Errorf("resumed = %+v, want the same identity and payload after one failed attempt", got)
	}
}

// A processing failure is retried with backoff; the second attempt may
// repeat work, which the contract permits.
func TestProcessingRetriesAfterFailure(t *testing.T) {
	db, _ := openInbox(t)
	f := newFixture(t, db.Webhooks(), 10, fastClock())
	f.handler.mu.Lock()
	f.handler.failUntil["evt-f"] = 1
	f.handler.mu.Unlock()
	if rec := f.deliver(http.MethodPost, "/probe", probeSecret, "evt-f", `{}`); rec.Code != http.StatusAccepted {
		t.Fatal(rec.Code)
	}
	waitFor(t, "retry success", func() bool { return f.handler.count() == 1 })
	if f.handler.processed[0].Attempts != 1 {
		t.Errorf("attempts = %d, want 1 earlier failure", f.handler.processed[0].Attempts)
	}
	if got := backoff(1); got != retryBase {
		t.Errorf("backoff(1) = %s, want %s", got, retryBase)
	}
	if got := backoff(30); got != retryMax {
		t.Errorf("backoff(30) = %s, want the cap %s", got, retryMax)
	}
}

func TestNewValidatesRoutes(t *testing.T) {
	db, _ := openInbox(t)
	handler := &probeHandler{}
	for name, routes := range map[string][]Route{
		"relative":       {{IntegrationID: "a", Path: "a", Handler: handler}},
		"root":           {{IntegrationID: "a", Path: "/", Handler: handler}},
		"trailing slash": {{IntegrationID: "a", Path: "/a/", Handler: handler}},
		"no integration": {{Path: "/a", Handler: handler}},
		"no handler":     {{IntegrationID: "a", Path: "/a"}},
		"duplicate":      {{IntegrationID: "a", Path: "/a", Handler: handler}, {IntegrationID: "b", Path: "/a", Handler: handler}},
	} {
		if _, err := New(Options{Repository: db.Webhooks(), Routes: routes}); err == nil {
			t.Errorf("%s: New accepted invalid routes", name)
		}
	}
}

// flakyInbox fails every acceptance while fail is set.
type flakyInbox struct {
	Repository
	fail atomic.Bool
}

func (f *flakyInbox) Accept(ctx context.Context, record store.WebhookDelivery, capacity int) (bool, error) {
	if f.fail.Load() {
		return false, errors.New("disk full")
	}
	return f.Repository.Accept(ctx, record, capacity)
}
