package linear

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/store"
)

// Every delivery goes through the shared ingress: authenticity, freshness,
// workspace and app authorization, and a delivery identity decide
// acceptance; only an accepted delivery reaches the inbox, and a
// redelivery of one is acknowledged without a second row or session.
// Rejection summaries carry no secret or payload.
func TestDeliveriesThroughTheSharedIngress(t *testing.T) {
	f := newFixture(t)
	// T3 is slow for the whole test: acceptance and acknowledgement never
	// wait for it.
	release := make(chan struct{})
	f.starter.set(func(s *fakeCoordinator) { s.block = release })
	defer close(release)
	f.start()
	ingress := f.ingress()
	now := f.clock.Now()

	rec := deliver(ingress, signedRequest("dlv-1", createdEvent("sess-1", now), testSecret))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"status":"accepted"`) {
		t.Fatalf("valid delivery = %d %s, want 202 accepted", rec.Code, rec.Body)
	}
	f.waitSession("sess-1", "session recorded", func(s store.LinearSession) bool { return s.State == stateStarting })
	if start := f.submissions("sess-1"); len(start) != 1 || !strings.Contains(start[0].Text, "Linear issue ATC-302") || !strings.Contains(start[0].Text, "what does the webhook receiver do") {
		t.Errorf("start submission = %+v", start)
	}
	// Redelivery: acknowledged as a duplicate; nothing new.
	rec = deliver(ingress, signedRequest("dlv-1", createdEvent("sess-1", now), testSecret))
	if rec.Code != http.StatusAccepted || !strings.Contains(rec.Body.String(), `"duplicate":true`) {
		t.Fatalf("redelivery = %d %s, want 202 duplicate", rec.Code, rec.Body)
	}
	// A different delivery of the same session (Linear retried after a
	// timeout with a new delivery id) is accepted and changes nothing.
	rec = deliver(ingress, signedRequest("dlv-1b", createdEvent("sess-1", now), testSecret))
	if rec.Code != http.StatusAccepted {
		t.Fatalf("second delivery = %d", rec.Code)
	}
	waitFor(t, "inbox drained", func() bool {
		pending, err := f.store.Webhooks().Pending(context.Background())
		return err == nil && pending == 0
	})
	f.waitActivities("sess-1", 1)
	time.Sleep(30 * time.Millisecond)
	if acts := f.linear.activitiesOf("sess-1"); len(acts) != 1 || acts[0].Type != contentThought {
		t.Errorf("activities = %+v, want the one acknowledgement", acts)
	}
	if open, total, _ := f.store.Linear().CountSessions(context.Background()); total != 1 {
		t.Errorf("sessions = %d open of %d, want 1 total", open, total)
	}

	// An authenticated delivery of a category the Integration does not
	// act on is accepted and dropped, never a failure on Linear's side.
	other := []byte(`{"type":"Issue","action":"update","organizationId":"org-1","webhookTimestamp":` + itoa(now.UnixMilli()) + `,"data":{}}`)
	if rec := deliver(ingress, signedRequest("dlv-other", other, testSecret)); rec.Code != http.StatusAccepted {
		t.Errorf("other type = %d %s, want 202", rec.Code, rec.Body)
	}

	stale := createdEvent("sess-2", now, withTimestamp(now.Add(-2*time.Minute)))
	future := createdEvent("sess-2", now, withTimestamp(now.Add(2*time.Minute)))
	cases := map[string]struct {
		req  *http.Request
		want int
	}{
		"bad signature":     {signedRequest("dlv-2", createdEvent("sess-2", now), "wrong"), http.StatusUnauthorized},
		"no signature":      {signedRequest("dlv-2", createdEvent("sess-2", now), ""), http.StatusUnauthorized},
		"stale":             {signedRequest("dlv-2", stale, testSecret), http.StatusUnauthorized},
		"from the future":   {signedRequest("dlv-2", future, testSecret), http.StatusUnauthorized},
		"other workspace":   {signedRequest("dlv-2", createdEvent("sess-2", now, withOrg("org-2")), testSecret), http.StatusForbidden},
		"other app":         {signedRequest("dlv-2", createdEvent("sess-2", now, withClient("client-2")), testSecret), http.StatusForbidden},
		"no delivery id":    {signedRequest("", createdEvent("sess-2", now), testSecret), http.StatusBadRequest},
		"malformed":         {signedRequest("dlv-2", []byte(`{"type":`), testSecret), http.StatusBadRequest},
		"no timestamp":      {signedRequest("dlv-2", []byte(`{"type":"AgentSessionEvent","organizationId":"org-1","oauthClientId":"client-1"}`), testSecret), http.StatusBadRequest},
		"not a post":        {withMethod(signedRequest("dlv-2", createdEvent("sess-2", now), testSecret), http.MethodGet), http.StatusMethodNotAllowed},
		"tampered body":     {tamper(signedRequest("dlv-2", createdEvent("sess-2", now), testSecret)), http.StatusUnauthorized},
		"signature not hex": {withSignature(signedRequest("dlv-2", createdEvent("sess-2", now), testSecret), "zz"), http.StatusUnauthorized},
	}
	for name, tc := range cases {
		rec := deliver(ingress, tc.req)
		if rec.Code != tc.want {
			t.Errorf("%s: status = %d, want %d (%s)", name, rec.Code, tc.want, rec.Body)
		}
	}
	time.Sleep(30 * time.Millisecond)
	if _, err := f.store.Linear().GetSession(context.Background(), "sess-2"); err == nil {
		t.Error("a rejected delivery recorded a session")
	}
	status := ingress.Status(context.Background())
	if status.Rejected != len(cases) {
		t.Errorf("rejected = %d, want %d", status.Rejected, len(cases))
	}
	if strings.Contains(status.LastRejection, testSecret) || strings.Contains(status.LastRejection, "sess-2") {
		t.Errorf("rejection summary %q leaks request data", status.LastRejection)
	}
	if f.starter.count() != 1 {
		t.Errorf("starts = %d, want exactly one for the one accepted session", f.starter.count())
	}
}

// Without a setup file nothing can be verified: the sender is told to
// retry, nothing is stored, and the Integration says what to do.
func TestUnconfiguredIntegrationRefusesRetryably(t *testing.T) {
	f := newFixture(t)
	if err := removeFile(f.setupPath); err != nil {
		t.Fatal(err)
	}
	f.start()
	ingress := f.ingress()
	rec := deliver(ingress, signedRequest("dlv-1", createdEvent("sess-1", f.clock.Now()), testSecret))
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured = %d %s, want 503", rec.Code, rec.Body)
	}
	if pending, _ := f.store.Webhooks().Pending(context.Background()); pending != 0 {
		t.Errorf("pending = %d, want nothing stored", pending)
	}
	connection := f.service.Connection()
	if connection.State != "unavailable" || !strings.Contains(connection.Detail, "no setup file") {
		t.Errorf("connection = %+v", connection)
	}
}
