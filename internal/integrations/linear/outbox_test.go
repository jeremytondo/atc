package linear

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
)

// Outbound calls survive Linear's bad days: transient failures back off
// and land, an expired token is renewed and persisted, an answer lost on
// the wire is not posted twice, and Linear's final refusal is recorded on
// the row without blocking the others.
func TestOutboxRetriesRenewsAndRecordsRefusals(t *testing.T) {
	f := newFixture(t)
	f.linear.set(func(l *fakeLinear) { l.failNext = 2 })
	f.start()
	now := f.clock.Now()
	f.process(t, "dlv-1", createdEvent("sess-1", now))
	_, providerID := f.startedThread("sess-1")
	// Retried rows land in whatever order their backoff allows.
	acts := f.waitActivities("sess-1", 2)
	if bodies := acts[0].Body + acts[1].Body; !strings.Contains(bodies, "ATC accepted") || !strings.Contains(bodies, "conversation is running") {
		t.Errorf("activities after transient failures = %+v", acts)
	}
	if calls, _ := f.linear.counts(); calls < 4 {
		t.Errorf("calls = %d, want the failed attempts and their retries", calls)
	}
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "First."})
	f.waitActivities("sess-1", 3)

	// The token expires: the next call is refused, renewed once, and sent;
	// the setup file holds the renewed tokens.
	f.linear.set(func(l *fakeLinear) { l.expireToken = true })
	f.prompt(t, "dlv-2", "sess-1", "ping")
	acts = f.waitActivities("sess-1", 4)
	contains(t, acts[3].Body, "Sent to the agent")
	setup := f.readSetup()
	if setup.AccessToken != "token-1" || setup.RefreshToken != "refresh-1" || setup.AccessTokenExpiresAt.IsZero() || setup.ClientSecret != "secret-1" {
		t.Errorf("setup after renewal = %+v", setup)
	}
	if _, refreshes := f.linear.counts(); refreshes != 1 {
		t.Errorf("refreshes = %d, want 1", refreshes)
	}

	// An answer lost on the wire: the retry finds Linear already has the
	// activity id, and the row is done.
	f.linear.set(func(l *fakeLinear) { l.ambiguousNext = 1 })
	f.prompt(t, "dlv-3", "sess-unknown", "pong")
	f.waitActivities("sess-unknown", 1)
	waitFor(t, "ambiguous row sent", func() bool {
		pending, err := f.store.Linear().Pending(context.Background())
		return err == nil && pending == 0
	})
	time.Sleep(40 * time.Millisecond)
	if n := len(f.linear.activitiesOf("sess-unknown")); n != 1 {
		t.Errorf("activities = %d after an ambiguous send, want 1", n)
	}

	// Linear declining without an error is a refusal too.
	f.linear.set(func(l *fakeLinear) { l.declineNext = 1 })
	f.prompt(t, "dlv-3b", "sess-unknown", "declined?")
	waitFor(t, "declined row recorded", func() bool {
		pending, err := f.store.Linear().Pending(context.Background())
		return err == nil && pending == 0
	})
	contains(t, f.service.Connection().Detail, "success: false")

	// Linear refuses one body for good; the row records it and later rows
	// still flow.
	f.linear.set(func(l *fakeLinear) { l.refuseBodies = "not tracking a conversation" })
	f.prompt(t, "dlv-4", "sess-unknown", "refused?")
	// The ping's turn is still pending, so this one is refused — and the
	// refusal is a row that flows.
	f.prompt(t, "dlv-5", "sess-1", "still there?")
	acts = f.waitActivities("sess-1", 5)
	contains(t, acts[4].Body, "not sent")
	waitFor(t, "refusal recorded", func() bool {
		pending, err := f.store.Linear().Pending(context.Background())
		return err == nil && pending == 0
	})
	connection := f.service.Connection()
	if !strings.Contains(connection.Detail, "last Linear failure") || !strings.Contains(connection.Detail, "Argument Validation Error") {
		t.Errorf("connection detail = %q", connection.Detail)
	}
}

// A refresh Linear refuses is an authentication failure the operator must
// end: the state says so, and the owed calls wait instead of vanishing.
func TestAuthorizationFailurePreservesPendingWork(t *testing.T) {
	f := newFixture(t)
	f.start()
	waitFor(t, "connected", func() bool { return f.service.Connection().State == api.IntegrationConnected })
	f.linear.set(func(l *fakeLinear) { l.expireToken = true; l.refreshToken = "someone-else's" })
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	waitFor(t, "auth failed", func() bool { return f.service.Connection().State == api.IntegrationAuthFailed })
	connection := f.service.Connection()
	for _, want := range []string{"token refresh refused", "re-authorize the Linear app", f.setupPath, "Linear updates owed"} {
		contains(t, connection.Detail, want)
	}
	if strings.Contains(connection.Detail, "secret-1") || strings.Contains(connection.Detail, "token-0") || strings.Contains(connection.Detail, "refresh-0") {
		t.Errorf("detail leaks a credential: %q", connection.Detail)
	}
	pending, _ := f.store.Linear().Pending(context.Background())
	if pending == 0 {
		t.Error("pending calls were dropped on an auth failure")
	}
	// The operator re-authorizes: new tokens in the file, and everything
	// owed goes out.
	f.linear.set(func(l *fakeLinear) { l.expireToken = false; l.token = "token-new"; l.refreshToken = "refresh-new" })
	setup := f.readSetup()
	setup.AccessToken, setup.RefreshToken = "token-new", "refresh-new"
	f.writeSetup(setup)
	f.waitActivities("sess-1", 2)
	waitFor(t, "connected again", func() bool { return f.service.Connection().State == api.IntegrationConnected })
}

// The connection report tells the operator what stands between them and
// a working mention: the setup file, the credential, the workspace, the
// route.
func TestConnectionReportsSetupAndCredentialState(t *testing.T) {
	f := newFixture(t)
	f.linear.set(func(l *fakeLinear) { l.orgID = "org-other" })
	f.start()
	waitFor(t, "workspace mismatch", func() bool { return f.service.Connection().State == api.IntegrationAuthFailed })
	contains(t, f.service.Connection().Detail, "acts in workspace Eleven Ideas (org-other) but the setup file names organization org-1")

	f.linear.set(func(l *fakeLinear) { l.orgID = testOrg })
	connection := waitState(t, f.service, api.IntegrationConnected)
	for _, want := range []string{"acting as atc in workspace Eleven Ideas", "0 of 0 sessions open, 0 submissions in flight, 0 Linear updates owed", "webhook route https://node.ts.net/linear"} {
		contains(t, connection.Detail, want)
	}

	// The operator breaks the file; the report says what is missing, and
	// mends when it is fixed — no restart.
	setup := f.readSetup()
	setup.ProjectID = ""
	f.writeSetup(setup)
	connection = waitState(t, f.service, api.IntegrationUnavailable)
	contains(t, connection.Detail, "missing project_id")
	setup.ProjectID = testProject
	f.writeSetup(setup)
	waitState(t, f.service, api.IntegrationConnected)

	f.linear.srv.Close()
	connection = waitState(t, f.service, api.IntegrationConnecting)
	contains(t, connection.Detail, "verifying the Linear credential")
}

func waitState(t *testing.T, service *Service, state api.IntegrationConnectionState) api.IntegrationConnection {
	t.Helper()
	waitFor(t, "state "+string(state), func() bool { return service.Connection().State == state })
	return service.Connection()
}

// evaluateTurn's rules, one reading at a time.
func TestEvaluateTurn(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	sub := store.LinearSubmission{ID: "s/start", SessionID: "s", Kind: kindStart, State: subSent, TurnID: "turn-a"}
	links := &api.ThreadLinks{Web: "https://t3/x", App: "t3code://x"}
	thread := func(state api.TurnState, status api.ThreadStatus, response string) api.Thread {
		return api.Thread{ID: "thrd-a", Status: status, Links: links, LatestTurn: &api.ThreadTurn{ID: "turn-a", State: state, Response: response}}
	}
	seen := now.Add(-3 * time.Minute)
	cases := map[string]struct {
		sub         store.LinearSubmission
		thread      api.Thread
		wantState   string
		wantOutcome string
		wantNotice  bool
		wantChanged bool
	}{
		"running says nothing":        {sub, thread(api.TurnRunning, api.ThreadWorking, ""), subSent, "", false, false},
		"idle says nothing":           {sub, thread(api.TurnUnknown, api.ThreadIdle, ""), subSent, "", false, false},
		"waiting says nothing":        {sub, thread(api.TurnRunning, api.ThreadWaitingForInput, ""), subSent, "", false, false},
		"response delivered":          {sub, thread(api.TurnCompleted, api.ThreadIdle, "answer"), subDone, outcomeResponded, true, true},
		"completion without answer":   {sub, thread(api.TurnCompleted, api.ThreadIdle, ""), subSent, "", false, true},
		"answer still missing":        {withSeen(sub, now), thread(api.TurnCompleted, api.ThreadIdle, ""), subSent, "", false, false},
		"answer given up":             {withSeen(sub, seen), thread(api.TurnCompleted, api.ThreadIdle, ""), subDone, outcomeUnrecoverable, true, true},
		"failed":                      {sub, thread(api.TurnFailed, api.ThreadIdle, ""), subDone, outcomeFailed, true, true},
		"interrupted":                 {sub, thread(api.TurnInterrupted, api.ThreadIdle, ""), subDone, outcomeInterrupted, true, true},
		"newer turn":                  {sub, api.Thread{LatestTurn: &api.ThreadTurn{ID: "turn-b", State: api.TurnCompleted, Response: "other"}}, subDone, outcomeUnrecoverable, true, true},
		"no turn":                     {sub, api.Thread{}, subDone, outcomeUnrecoverable, true, true},
		"pending says nothing":        {sub, api.Thread{ID: "thrd-a", Status: api.ThreadWorking, PendingTurn: &api.PendingTurn{ID: "turn-a"}}, subSent, "", false, false},
		"pending on a dropped thread": {sub, withArchived(api.Thread{ID: "thrd-a", Status: api.ThreadUnknown, Links: links, PendingTurn: &api.PendingTurn{ID: "turn-a"}}), subDone, outcomeUnrecoverable, true, true},
		"pending is another's":        {sub, api.Thread{ID: "thrd-a", PendingTurn: &api.PendingTurn{ID: "turn-b"}, LatestTurn: &api.ThreadTurn{ID: "turn-a", State: api.TurnCompleted, Response: "answer"}}, subDone, outcomeResponded, true, true},
		"archived unfinished":         {sub, withArchived(thread(api.TurnUnknown, api.ThreadUnknown, "")), subDone, outcomeUnrecoverable, true, true},
		"archived after completion":   {sub, withArchived(thread(api.TurnCompleted, api.ThreadUnknown, "answer")), subDone, outcomeResponded, true, true},
	}
	s := &Service{responseGrace: 2 * time.Minute, now: func() time.Time { return now }}
	for name, tc := range cases {
		got, rows := s.evaluateTurn(tc.sub, tc.thread, now)
		changed := got != nil
		state, outcome := tc.sub.State, tc.sub.Outcome
		if got != nil {
			state, outcome = got.State, got.Outcome
		}
		if state != tc.wantState || outcome != tc.wantOutcome || changed != tc.wantChanged {
			t.Errorf("%s: state/outcome/changed = %s/%s/%t, want %s/%s/%t", name, state, outcome, changed, tc.wantState, tc.wantOutcome, tc.wantChanged)
		}
		switch {
		case !tc.wantNotice && len(rows) != 0:
			t.Errorf("%s: rows = %+v, want none", name, rows)
		case tc.wantNotice && (len(rows) != 1 || rows[0].ID != "s/turn/turn-a/result"):
			t.Errorf("%s: rows = %+v, want the result", name, rows)
		}
		if name == "newer turn" && strings.Contains(string(rows[0].Body), "other") {
			t.Errorf("%s delivered the newer turn's response", name)
		}
	}
}

func withSeen(s store.LinearSubmission, at time.Time) store.LinearSubmission {
	s.CompletedSeenAt = &at
	return s
}

func withArchived(t api.Thread) api.Thread {
	t.Archived = true
	return t
}
