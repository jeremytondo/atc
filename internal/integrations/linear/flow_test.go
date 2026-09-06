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

// The whole interaction: a mention is acknowledged, starts exactly one
// Thread under the fixed profile, gets the Thread's links, and receives
// the exact Turn's final response. Repeating the inbox delivery afterwards
// starts nothing and posts nothing.
func TestMentionStartsOneThreadAndReturnsItsAnswer(t *testing.T) {
	f := newFixture(t)
	f.start()
	now := f.clock.Now()
	body := createdEvent("sess-1", now)
	f.process(t, "dlv-1", body)

	threadID, providerID := f.startedThread("sess-1")
	acts := f.waitActivities("sess-1", 2)
	if acts[0].Type != contentThought || !strings.Contains(acts[0].Body, "ATC accepted") {
		t.Errorf("first activity = %+v, want the acknowledgement", acts[0])
	}
	contains(t, acts[1].Body, "https://t3.test/env-1/"+providerID)
	waitFor(t, "links", func() bool { return len(f.linear.linksOf("sess-1")) == 2 })
	if links := f.linear.linksOf("sess-1"); links[0].URL != "https://t3.test/env-1/"+providerID || links[1].URL != "t3code://threads/env-1/"+providerID {
		t.Errorf("links = %+v", links)
	}
	if calls := f.starter.params(); len(calls) != 1 || calls[0].IntegrationID != "t3code" || calls[0].Agent != "codex" || calls[0].Model != "gpt-5.6-sol" ||
		calls[0].ProjectID != testProject || len(calls[0].Options) != 1 || calls[0].Options[0] != (api.ThreadOption{ID: "reasoningEffort", Value: "high"}) ||
		!strings.Contains(calls[0].Prompt, "mention of @atc in Linear issue ATC-302 (https://linear.app/x/issue/ATC-302)") || !strings.HasSuffix(calls[0].Prompt, "<comment>@atc what does the webhook receiver do?</comment>") {
		t.Errorf("create params = %+v", calls)
	}

	// T3 reports the turn running, then completed with its response.
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	time.Sleep(30 * time.Millisecond)
	if acts := f.linear.activitiesOf("sess-1"); len(acts) != 2 {
		t.Fatalf("a running turn changed the activities: %+v", acts)
	}
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "The receiver relays public traffic to Core."})
	acts = f.waitActivities("sess-1", 3)
	if acts[2].Type != contentResponse || acts[2].Body != "The receiver relays public traffic to Core." {
		t.Errorf("result = %+v", acts[2])
	}
	session := f.waitSession("sess-1", "done", func(s store.LinearSession) bool { return s.State == stateDone })
	if session.Outcome != outcomeResponded || session.ThreadID != threadID || session.Prompt != "" {
		t.Errorf("session = %+v", session)
	}

	// The inbox processes the delivery again (a crash before completion).
	f.process(t, "dlv-1", body)
	f.service.wake(f.service.sessionKick)
	time.Sleep(50 * time.Millisecond)
	if f.starter.count() != 1 || len(f.linear.activitiesOf("sess-1")) != 3 {
		t.Errorf("reprocessing started %d threads and left %d activities", f.starter.count(), len(f.linear.activitiesOf("sess-1")))
	}
	if open, total, _ := f.store.Linear().CountSessions(context.Background()); open != 0 || total != 1 {
		t.Errorf("sessions open/total = %d/%d", open, total)
	}
}

// A T3 that never answers holds up neither the acknowledgement of its own
// session nor another session's; each session is its own start.
func TestSlowStartDoesNotDelayAcknowledgements(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.starter.set(func(s *fakeStarter) { s.block = release })
	f.start()
	now := f.clock.Now()
	f.process(t, "dlv-1", createdEvent("sess-1", now))
	f.waitActivities("sess-1", 1)
	f.process(t, "dlv-2", createdEvent("sess-2", now))
	f.waitActivities("sess-2", 1)
	waitFor(t, "both starts in flight", func() bool { return f.starter.count() == 2 })
	for _, id := range []string{"sess-1", "sess-2"} {
		if session := f.session(id); session.State != stateStarting {
			t.Errorf("%s = %s, want starting while T3 is silent", id, session.State)
		}
	}
	close(release)
	f.startedThread("sess-1")
	f.startedThread("sess-2")
	if f.starter.count() != 2 {
		t.Errorf("starts = %d", f.starter.count())
	}
}

// Waits are announced once per kind and point to T3; follow-ups and stop
// signals get the fixed explanation, submit nothing, and end nothing —
// the Turn's outcome still arrives.
func TestWaitsFollowUpsAndStopsPointToT3(t *testing.T) {
	f := newFixture(t)
	f.start()
	now := f.clock.Now()
	f.process(t, "dlv-1", createdEvent("sess-1", now))
	_, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)

	f.report(providerID, api.ThreadWaitingForInput, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	acts := f.waitActivities("sess-1", 3)
	contains(t, acts[2].Body, "waiting for input")
	contains(t, acts[2].Body, "https://t3.test/env-1/"+providerID)
	// The same wait, observed again and again, is announced once.
	for range 3 {
		f.report(providerID, api.ThreadWaitingForInput, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
		f.service.wake(f.service.sessionKick)
	}
	time.Sleep(60 * time.Millisecond)
	if len(f.linear.activitiesOf("sess-1")) != 3 {
		t.Fatalf("repeated wait changed the activities: %+v", f.linear.activitiesOf("sess-1"))
	}
	// A different kind of wait is announced.
	f.report(providerID, api.ThreadWaitingForPermission, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	acts = f.waitActivities("sess-1", 4)
	contains(t, acts[3].Body, "waiting for an approval")

	f.process(t, "dlv-2", promptedEvent("sess-1", now, "also check the tests", ""))
	acts = f.waitActivities("sess-1", 5)
	contains(t, acts[4].Body, "Follow-up messages from Linear are not supported")
	contains(t, acts[4].Body, "https://t3.test/env-1/"+providerID)
	f.process(t, "dlv-3", promptedEvent("sess-1", now, "", "stop"))
	acts = f.waitActivities("sess-1", 6)
	contains(t, acts[5].Body, "has not been stopped")
	// The same prompted delivery processed twice replies once.
	f.process(t, "dlv-3", promptedEvent("sess-1", now, "", "stop"))
	time.Sleep(40 * time.Millisecond)
	if n := len(f.linear.activitiesOf("sess-1")); n != 6 {
		t.Errorf("activities = %d after a repeated delivery, want 6", n)
	}
	if f.starter.count() != 1 {
		t.Errorf("starts = %d; replies must submit nothing", f.starter.count())
	}
	if session := f.session("sess-1"); session.State != stateStarted {
		t.Errorf("session = %s, want still started", session.State)
	}

	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "Done."})
	acts = f.waitActivities("sess-1", 7)
	if acts[6].Type != contentResponse || acts[6].Body != "Done." {
		t.Errorf("result = %+v", acts[6])
	}
}

// Sessions with nothing to act on get an explanation and no Thread:
// delegation (no comment), a mention Linear supplied no context for, and
// a message into a session ATC never recorded.
func TestSessionsWithoutAPromptAreRefused(t *testing.T) {
	f := newFixture(t)
	f.start()
	now := f.clock.Now()
	delegated := createdEvent("sess-del", now, func(m map[string]any) { delete(m["agentSession"].(map[string]any), "comment") })
	f.process(t, "dlv-1", delegated)
	acts := f.waitActivities("sess-del", 1)
	if acts[0].Type != contentError || !strings.Contains(acts[0].Body, "Delegating the issue does not start a run") {
		t.Errorf("delegation reply = %+v", acts[0])
	}
	f.process(t, "dlv-2", createdEvent("sess-ctx", now, withPromptContext("")))
	acts = f.waitActivities("sess-ctx", 1)
	if acts[0].Type != contentError || !strings.Contains(acts[0].Body, "no context") {
		t.Errorf("no-context reply = %+v", acts[0])
	}
	f.process(t, "dlv-3", promptedEvent("sess-unknown", now, "hello?", ""))
	acts = f.waitActivities("sess-unknown", 1)
	contains(t, acts[0].Body, "not tracking a run for this session")
	time.Sleep(30 * time.Millisecond)
	if f.starter.count() != 0 {
		t.Errorf("starts = %d, want none", f.starter.count())
	}
	for _, id := range []string{"sess-del", "sess-ctx"} {
		if session := f.session(id); session.State != stateDone || session.Outcome != outcomeRefused {
			t.Errorf("%s = %s/%s", id, session.State, session.Outcome)
		}
	}
	// The refused delivery, processed again, replies once.
	f.process(t, "dlv-1", delegated)
	time.Sleep(30 * time.Millisecond)
	if n := len(f.linear.activitiesOf("sess-del")); n != 1 {
		t.Errorf("refusal posted %d times", n)
	}
}

// A refused or failed start is a clear error, nothing is retried, and no
// Thread remains.
func TestStartFailuresAreReportedHonestly(t *testing.T) {
	cases := map[string]struct {
		fail, recorded error
		want           string
	}{
		"T3 refused":      {fail: errT3Refused, want: "could not start the T3 Code conversation: thread creation failed: T3 Code rejected the command: no such project"},
		"record failed":   {recorded: errStorage, want: "recording the thread before dispatch"},
		"project unknown": {fail: errProjectUnknown, want: "project not found"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.starter.set(func(s *fakeStarter) { s.fail, s.recorded = tc.fail, tc.recorded })
			f.start()
			f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
			session := f.waitSession("sess-1", "done", func(s store.LinearSession) bool { return s.State == stateDone })
			if session.Outcome != outcomeFailed {
				t.Errorf("outcome = %s, want %s", session.Outcome, outcomeFailed)
			}
			acts := f.waitActivities("sess-1", 2)
			if acts[1].Type != contentError || !strings.Contains(acts[1].Body, tc.want) {
				t.Errorf("activity = %+v, want an error containing %q", acts[1], tc.want)
			}
			time.Sleep(50 * time.Millisecond)
			if f.starter.count() != 1 {
				t.Errorf("starts = %d, want no retry", f.starter.count())
			}
			if threads := f.threads.List("", "", true); len(threads) != 0 {
				t.Errorf("a failed start left threads %+v", threads)
			}
		})
	}
}

// A dispatch T3 never answered is not a failure: the recorded Thread is
// kept and watched, Linear hears the start is uncertain, nothing is
// started again, and T3's later report of the Thread still delivers the
// answer.
func TestUncertainStartKeepsWatching(t *testing.T) {
	f := newFixture(t)
	f.starter.set(func(s *fakeStarter) { s.fail = errT3Silent })
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	threadID, providerID := f.startedThread("sess-1")
	acts := f.waitActivities("sess-1", 2)
	if acts[1].Type != contentThought || !strings.Contains(acts[1].Body, "could not confirm whether T3 Code accepted") {
		t.Errorf("activity = %+v, want the uncertainty notice", acts[1])
	}
	if threads := f.threads.List("", "", true); len(threads) != 1 || threads[0].ID != threadID {
		t.Fatalf("threads = %+v, want the recorded one kept", threads)
	}
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "It did start."})
	acts = f.waitActivities("sess-1", 3)
	if acts[2].Type != contentResponse || acts[2].Body != "It did start." {
		t.Errorf("result = %+v", acts[2])
	}
	if f.starter.count() != 1 {
		t.Errorf("starts = %d, want no retry", f.starter.count())
	}
}

// T3 not being connected defers the start rather than failing it: the
// user hears once, the start is owed, and it happens when T3 is back —
// woken by the Integration's connection change, not just the poll.
func TestStartWaitsForT3(t *testing.T) {
	f := newFixture(t)
	f.starter.set(func(s *fakeStarter) { s.fail = errNotConnected })
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	acts := f.waitActivities("sess-1", 2)
	contains(t, acts[1].Body, "T3 Code is not connected")
	waitFor(t, "start owed again", func() bool { return f.session("sess-1").State == stateAccepted })
	time.Sleep(60 * time.Millisecond)
	if n := len(f.linear.activitiesOf("sess-1")); n != 2 {
		t.Errorf("retries posted %d activities, want the notice once", n)
	}
	if threads := f.threads.List("", "", true); len(threads) != 0 {
		t.Errorf("a deferred start left threads %+v", threads)
	}
	f.starter.set(func(s *fakeStarter) { s.fail = nil })
	f.hub.Publish(api.EventIntegrationUpdated, "integration", "t3code")
	f.startedThread("sess-1")
	acts = f.waitActivities("sess-1", 3)
	contains(t, acts[2].Body, "conversation is running")
}

// How the exact Turn ends is what Linear hears: a failure with its detail,
// a cancellation, a newer Turn replacing it, the Thread dropped by T3 or
// gone from ATC — each an honest error with the links, never a substitute
// answer. A Thread at rest or unknown with the Turn unfinished is not an
// outcome.
func TestTurnOutcomesAreReportedExactly(t *testing.T) {
	cases := map[string]struct {
		drive   func(f *fixture, threadID, providerID string)
		outcome string
		want    string
	}{
		"failed": {
			drive: func(f *fixture, _, providerID string) {
				f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnFailed, Error: "context window exceeded"})
			},
			outcome: outcomeFailed, want: "The run failed in T3 Code: context window exceeded",
		},
		"interrupted": {
			drive: func(f *fixture, _, providerID string) {
				f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnInterrupted})
			},
			outcome: outcomeInterrupted, want: "stopped in T3 Code before it finished",
		},
		"newer turn": {
			drive: func(f *fixture, _, providerID string) {
				// The user prompted again in T3 before the first turn's
				// response reached ATC: T3's latest turn is another one.
				f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnRunning})
				f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnCompleted, Response: "the second answer"})
			},
			outcome: outcomeUnrecoverable, want: "A newer turn replaced the one started from this mention",
		},
		"dropped by T3": {
			drive: func(f *fixture, _, providerID string) {
				if err := f.threads.ArchiveExternalThread(context.Background(), providerT3, providerID); err != nil {
					f.t.Fatal(err)
				}
			},
			outcome: outcomeUnrecoverable, want: "T3 Code no longer reports the conversation",
		},
		"gone from ATC": {
			drive: func(f *fixture, _, providerID string) {
				if err := f.threads.DiscardExternal(context.Background(), providerT3, providerID); err != nil {
					f.t.Fatal(err)
				}
			},
			outcome: outcomeUnrecoverable, want: "no longer exists in ATC",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			f.start()
			f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
			threadID, providerID := f.startedThread("sess-1")
			f.waitActivities("sess-1", 2)
			f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
			// Rest and ignorance are not outcomes.
			f.report(providerID, api.ThreadIdle, nil)
			f.report(providerID, api.ThreadUnknown, nil)
			f.service.wake(f.service.sessionKick)
			time.Sleep(40 * time.Millisecond)
			if acts := f.linear.activitiesOf("sess-1"); len(acts) != 2 {
				t.Fatalf("an idle or unknown thread changed the activities: %+v", acts)
			}
			tc.drive(f, threadID, providerID)
			acts := f.waitActivities("sess-1", 3)
			if acts[2].Type != contentError || !strings.Contains(acts[2].Body, tc.want) {
				t.Errorf("result = %+v, want an error containing %q", acts[2], tc.want)
			}
			if strings.Contains(acts[2].Body, "the second answer") {
				t.Error("a newer turn's response was delivered")
			}
			if session := f.session("sess-1"); session.State != stateDone || session.Outcome != tc.outcome {
				t.Errorf("session = %s/%s, want done/%s", session.State, session.Outcome, tc.outcome)
			}
		})
	}
}

// Completion and the response arrive separately: an empty response on
// completion is waited on, the response is delivered when it lands, and
// only a response still missing after the grace window is given up on.
func TestResponseRecoveryIsWaitedFor(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	threadID, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted})
	session := f.waitSession("sess-1", "completion seen", func(s store.LinearSession) bool { return s.CompletedSeenAt != nil })
	if session.State != stateStarted {
		t.Fatalf("session = %s, want still started", session.State)
	}
	f.clock.Advance(responseGrace / 2)
	f.service.wake(f.service.sessionKick)
	time.Sleep(40 * time.Millisecond)
	if acts := f.linear.activitiesOf("sess-1"); len(acts) != 2 {
		t.Fatalf("premature report; activities = %+v", acts)
	}
	if err := f.threads.ObserveTurnResponse(context.Background(), threadID, "t3-turn-1", "Recovered later."); err != nil {
		t.Fatal(err)
	}
	acts := f.waitActivities("sess-1", 3)
	if acts[2].Type != contentResponse || acts[2].Body != "Recovered later." {
		t.Errorf("result = %+v", acts[2])
	}

	// A second session whose response never comes.
	f.process(t, "dlv-2", createdEvent("sess-2", f.clock.Now()))
	_, providerID = f.startedThread("sess-2")
	f.waitActivities("sess-2", 2)
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted})
	f.waitSession("sess-2", "completion seen", func(s store.LinearSession) bool { return s.CompletedSeenAt != nil })
	f.clock.Advance(responseGrace)
	f.service.wake(f.service.sessionKick)
	acts = f.waitActivities("sess-2", 3)
	if acts[2].Type != contentError || !strings.Contains(acts[2].Body, "could not recover its final response") {
		t.Errorf("result = %+v", acts[2])
	}
}

// Time alone ends nothing: a session working or waiting for days stays
// tracked and reports its outcome when it comes.
func TestLongRunsStayTracked(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	_, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	for range 3 {
		f.clock.Advance(24 * time.Hour)
		f.service.wake(f.service.sessionKick)
		time.Sleep(30 * time.Millisecond)
	}
	f.report(providerID, api.ThreadWaitingForInput, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	f.waitActivities("sess-1", 3)
	f.clock.Advance(7 * 24 * time.Hour)
	f.service.wake(f.service.sessionKick)
	time.Sleep(30 * time.Millisecond)
	if session := f.session("sess-1"); session.State != stateStarted {
		t.Fatalf("session = %s after a week, want still started", session.State)
	}
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "Finally."})
	acts := f.waitActivities("sess-1", 4)
	if acts[3].Body != "Finally." {
		t.Errorf("result = %+v", acts[3])
	}
}
