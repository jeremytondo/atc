package linear

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
)

// The whole interaction: a mention is acknowledged, starts exactly one
// Thread under the fixed profile, gets the Thread's links, and receives
// the exact Turn's final response; the session stays bound, and a
// follow-up continues the same Thread — under the message's own key,
// with its text — whose next turn's response comes back too, and again.
// Repeating the inbox deliveries afterwards starts nothing, sends
// nothing, and posts nothing.
func TestMentionStartsOneThreadAndTheConversationContinues(t *testing.T) {
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
	start := f.waitSubmission("sess-1", startKey("sess-1"), "start reported", func(s store.LinearSubmission) bool { return s.State == subDone })
	if start.Outcome != outcomeResponded || start.TurnID == "" {
		t.Errorf("start submission = %+v", start)
	}
	if session := f.session("sess-1"); session.State != stateBound || session.ThreadID != threadID || start.Text != "" {
		t.Errorf("session after the answer = %+v, start %+v", session, start)
	}

	// The follow-up continues the same Thread: the message reaches the
	// coordinator with its text under the activity's key, T3 starts the
	// next turn, and its answer comes back — attributed to that turn.
	f.prompt(t, "dlv-2", "sess-1", "and what about the sandbox?")
	acts = f.waitActivities("sess-1", 4)
	contains(t, acts[3].Body, "Sent to the agent")
	messages, _, _, _ := f.starter.sent()
	if len(messages) != 1 || messages[0].Text != "and what about the sandbox?" || messages[0].Key != "act-dlv-2" {
		t.Errorf("messages = %+v", messages)
	}
	sent := f.waitSubmission("sess-1", "act-dlv-2", "message sent", func(s store.LinearSubmission) bool { return s.State == subSent })
	thread, _ := f.threads.Get(threadID)
	if sent.Kind != kindMessage || sent.Delivery != string(api.MessageAccepted) || thread.PendingTurn == nil || sent.TurnID != thread.PendingTurn.ID {
		t.Errorf("message submission = %+v; thread pending %+v", sent, thread.PendingTurn)
	}
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnRunning})
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnCompleted, Response: "The sandbox blocks the API port."})
	acts = f.waitActivities("sess-1", 5)
	if acts[4].Type != contentResponse || acts[4].Body != "The sandbox blocks the API port." {
		t.Errorf("second result = %+v", acts[4])
	}
	// And again: a third turn is a third result, never suppressed by
	// the earlier ones.
	f.prompt(t, "dlv-3", "sess-1", "thanks, one more: is it configurable?")
	f.waitActivities("sess-1", 6)
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-3", State: api.TurnRunning})
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-3", State: api.TurnCompleted, Response: "Yes, via config.toml."})
	acts = f.waitActivities("sess-1", 7)
	if acts[6].Body != "Yes, via config.toml." {
		t.Errorf("third result = %+v", acts[6])
	}
	if f.starter.count() != 1 || f.session("sess-1").State != stateBound {
		t.Errorf("starts = %d, session %s; the conversation must continue on one thread", f.starter.count(), f.session("sess-1").State)
	}

	// The inbox processes every delivery again (a crash before
	// completion): nothing starts, sends, or posts twice.
	f.process(t, "dlv-1", body)
	f.prompt(t, "dlv-2", "sess-1", "and what about the sandbox?")
	f.prompt(t, "dlv-3", "sess-1", "thanks, one more: is it configurable?")
	f.service.wake(f.service.sessionKick)
	time.Sleep(60 * time.Millisecond)
	messages, _, _, _ = f.starter.sent()
	if f.starter.count() != 1 || len(messages) != 2 || len(f.linear.activitiesOf("sess-1")) != 7 || len(f.submissions("sess-1")) != 3 {
		t.Errorf("reprocessing: starts %d, messages %d, activities %d, submissions %d", f.starter.count(), len(messages), len(f.linear.activitiesOf("sess-1")), len(f.submissions("sess-1")))
	}
	if open, total, _ := f.store.Linear().CountSessions(context.Background()); open != 1 || total != 1 {
		t.Errorf("sessions open/total = %d/%d", open, total)
	}
}

// A T3 that never answers holds up neither the acknowledgement of its own
// session nor another session's; each session is its own start.
func TestSlowStartDoesNotDelayAcknowledgements(t *testing.T) {
	f := newFixture(t)
	release := make(chan struct{})
	f.starter.set(func(s *fakeCoordinator) { s.block = release })
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

// The agent's questions are relayed faithfully and answered through the
// shared capability: a request is presented once, as an elicitation
// offering the exact choices under values naming the request, question,
// and choice; a choice — by value or by its label — becomes a structured
// answer to that question alone; words become a reply passed verbatim;
// the request's resolution is reported; a reply to a question already
// resolved goes nowhere, and says so, rather than becoming a message.
func TestQuestionsAreRelayedAndAnswered(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	threadID, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)

	f.ask(providerID, "req-1")
	acts := f.waitActivities("sess-1", 3)
	thread, _ := f.threads.Get(threadID)
	request := thread.InputRequests[0]
	ask := acts[2]
	if ask.Type != contentElicitation || ask.Signal != signalSelect || !strings.Contains(ask.Body, "Which color?") || !strings.Contains(ask.Body, "Which tools?") || !strings.Contains(ask.Body, "Red — warm") {
		t.Fatalf("ask = %+v", ask)
	}
	if len(ask.Options) != 4 || ask.Options[0].Label != "Color: Red" || ask.Options[0].Value != request.ID+"/q1/Red" || ask.Options[3].Label != "Tools: Make" || ask.Options[3].Value != request.ID+"/q2/make" {
		t.Errorf("options = %+v", ask.Options)
	}
	// The same wait, observed again, presents nothing twice.
	for range 3 {
		f.ask(providerID, "req-1")
		f.service.wake(f.service.sessionKick)
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(f.linear.activitiesOf("sess-1")); n != 3 {
		t.Fatalf("repeated request changed the activities: %d", n)
	}

	// A choice by value answers that question alone; T3 resolves the
	// request with it, and Linear hears the agent received the answer.
	f.prompt(t, "dlv-2", "sess-1", request.ID+"/q1/Red")
	acts = f.waitActivities("sess-1", 4)
	contains(t, acts[3].Body, "Your choice (Color: Red) was sent")
	_, answers, _, _ := f.starter.sent()
	if len(answers) != 1 || answers[0].Reply != "" || len(answers[0].Answers) != 1 || answers[0].Answers[0].QuestionID != "q1" || answers[0].Answers[0].Choices[0] != "Red" {
		t.Errorf("answers = %+v", answers)
	}
	if got, _ := f.threads.InputRequest(threadID, request.ID); got.Answer == nil || got.Answer.State != api.InputAnswerSent || got.Answer.Delivery != api.MessageAccepted {
		t.Errorf("answer on the request = %+v", got.Answer)
	}
	f.resolveInput(providerID, "req-1", []threads.ProviderAnswer{{QuestionID: "color", Values: []string{"Red"}}})
	acts = f.waitActivities("sess-1", 5)
	contains(t, acts[4].Body, "received your answer")
	choice := f.waitSubmission("sess-1", "act-dlv-2", "choice resolved", func(s store.LinearSubmission) bool { return s.State == subDone })
	if choice.Kind != kindChoice || choice.Outcome != outcomeResolved || choice.RequestID != request.ID || choice.QuestionID != "q1" || choice.Value != "Red" {
		t.Errorf("choice submission = %+v", choice)
	}

	// Words to a new question are a reply, verbatim, addressed to it.
	f.ask(providerID, "req-2")
	f.waitActivities("sess-1", 6)
	thread, _ = f.threads.Get(threadID)
	second := thread.InputRequests[0]
	f.prompt(t, "dlv-3", "sess-1", "Blue, and skip the tools: just answer the question about the receiver.")
	acts = f.waitActivities("sess-1", 7)
	contains(t, acts[6].Body, "Your reply was sent to the agent")
	_, answers, _, _ = f.starter.sent()
	if len(answers) != 2 || answers[1].Reply != "Blue, and skip the tools: just answer the question about the receiver." || len(answers[1].Answers) != 0 {
		t.Errorf("reply answer = %+v", answers[1])
	}
	reply := f.waitSubmission("sess-1", "act-dlv-3", "reply sent", func(s store.LinearSubmission) bool { return s.State == subSent })
	if reply.Kind != kindReply || reply.RequestID != second.ID {
		t.Errorf("reply submission = %+v", reply)
	}
	// The label of an open option selects it too; a label no open option
	// carries is words.
	f.resolveInput(providerID, "req-2", []threads.ProviderAnswer{{QuestionID: "color", Values: []string{"Blue, and skip the tools: just answer the question about the receiver."}}})
	f.waitActivities("sess-1", 8)
	f.ask(providerID, "req-3")
	f.waitActivities("sess-1", 9)
	thread, _ = f.threads.Get(threadID)
	third := thread.InputRequests[0]
	f.prompt(t, "dlv-4", "sess-1", "Tools: Go")
	f.waitActivities("sess-1", 10)
	_, answers, _, _ = f.starter.sent()
	if len(answers) != 3 || len(answers[2].Answers) != 1 || answers[2].Answers[0].QuestionID != "q2" || answers[2].Answers[0].Choices[0] != "go" {
		t.Errorf("label choice = %+v", answers)
	}

	// A question resolved elsewhere before the reply is dispatched: the
	// reply is refused as stale and reported, never sent as a message.
	f.stop()
	f.prompt(t, "dlv-5", "sess-1", "make, actually")
	f.resolveInput(providerID, "req-3", []threads.ProviderAnswer{{QuestionID: "tools", Values: []string{"go"}, Multiple: true}})
	f.start()
	acts = f.waitActivities("sess-1", 12)
	bodies := acts[10].Body + acts[11].Body
	contains(t, bodies, "no longer open")
	contains(t, bodies, "not sent as an answer")
	messages, answers, _, _ := f.starter.sent()
	if len(messages) != 0 || len(answers) != 4 || answers[3].Reply != "make, actually" {
		t.Errorf("stale reply went elsewhere: messages %+v, answers %+v", messages, answers)
	}
	if got, _ := f.threads.InputRequest(threadID, third.ID); got.Answer != nil && got.Answer.Reply == "make, actually" {
		t.Errorf("stale reply recorded as an answer: %+v", got.Answer)
	}
	stale := f.waitSubmission("sess-1", "act-dlv-5", "stale reply refused", func(s store.LinearSubmission) bool { return s.State == subDone })
	if stale.Kind != kindReply || stale.Outcome != outcomeRefused || !strings.Contains(stale.Detail, "resolved") {
		t.Errorf("stale submission = %+v", stale)
	}
	// With no question open, words are a message.
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "Done."})
	f.waitActivities("sess-1", 13)
	f.prompt(t, "dlv-6", "sess-1", "make, actually")
	f.waitActivities("sess-1", 14)
	messages, _, _, _ = f.starter.sent()
	if len(messages) != 1 || messages[0].Text != "make, actually" {
		t.Errorf("message after resolution = %+v", messages)
	}
}

// An unanswerable request is presented with its reason and no options;
// it never takes a choice.
func TestUnanswerableRequestsArePresentedHonestly(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	_, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)
	f.report(providerID, api.ThreadWaitingForInput, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	if _, err := f.threads.ObserveInputs(context.Background(), providerT3, providerID, []threads.InputObservation{{RequestID: "req-odd", RequestedAt: f.clock.Now(), Unanswerable: "the request carries no questions"}}, nil); err != nil {
		t.Fatal(err)
	}
	acts := f.waitActivities("sess-1", 3)
	if acts[2].Type != contentElicitation || acts[2].Signal != "" || len(acts[2].Options) != 0 || !strings.Contains(acts[2].Body, "carries no questions") || !strings.Contains(acts[2].Body, "https://t3.test/env-1/"+providerID) {
		t.Errorf("ask = %+v", acts[2])
	}
}

// Approvals: presented with the decisions offered; only a choice among
// them decides, and exactly that request and decision; words — even
// "yes, go ahead" — are a message and approve nothing; a decision on a
// request resolved elsewhere is refused and reported as it is.
func TestApprovalsRequireAnExactChoice(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	threadID, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)

	f.approve(providerID, "apr-1")
	acts := f.waitActivities("sess-1", 3)
	thread, _ := f.threads.Get(threadID)
	approval := thread.Approvals[0]
	ask := acts[2]
	if ask.Type != contentElicitation || ask.Signal != signalSelect || !strings.Contains(ask.Body, "Run go test ./...") || !strings.Contains(ask.Body, "approves nothing") {
		t.Fatalf("ask = %+v", ask)
	}
	if len(ask.Options) != 2 || ask.Options[0].Label != "Approve" || ask.Options[0].Value != approval.ID+"/approve" || ask.Options[1].Value != approval.ID+"/deny" {
		t.Errorf("options = %+v", ask.Options)
	}

	// Natural language is a message, not a decision — the option's label
	// typed out included; only the exact value decides.
	f.prompt(t, "dlv-2", "sess-1", "yes, go ahead")
	f.waitActivities("sess-1", 4)
	f.prompt(t, "dlv-2b", "sess-1", "Approve")
	f.waitActivities("sess-1", 5)
	messages, _, decisions, _ := f.starter.sent()
	if len(decisions) != 0 || len(messages) != 2 || messages[0].Text != "yes, go ahead" || messages[1].Text != "Approve" {
		t.Errorf("words decided something: decisions %+v, messages %+v", decisions, messages)
	}
	if got, _ := f.threads.Get(threadID); len(got.Approvals) != 1 || got.Approvals[0].Status != api.ApprovalPending {
		t.Errorf("approval after words = %+v", got.Approvals)
	}
	// The exact choice decides.
	f.prompt(t, "dlv-3", "sess-1", approval.ID+"/approve")
	acts = f.waitActivities("sess-1", 6)
	contains(t, acts[5].Body, "Your decision (Approve) was delivered")
	_, _, decisions, _ = f.starter.sent()
	if len(decisions) != 1 || decisions[0].Decision != api.DecisionApprove {
		t.Errorf("decisions = %+v", decisions)
	}
	decision := f.waitSubmission("sess-1", "act-dlv-3", "decision delivered", func(s store.LinearSubmission) bool { return s.State == subDone })
	if decision.Kind != kindDecision || decision.Outcome != outcomeDelivered || decision.RequestID != approval.ID || decision.Value != "approve" {
		t.Errorf("decision submission = %+v", decision)
	}
	// T3 no longer lists the request: closed, decided from here.
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	if err := f.threads.ObserveApprovals(context.Background(), providerT3, providerID, nil); err != nil {
		t.Fatal(err)
	}
	acts = f.waitActivities("sess-1", 7)
	contains(t, acts[6].Body, "decided from here (Approve)")
	// A selection on that closed request keeps its target and is refused
	// as such — never re-routed as a message.
	f.prompt(t, "dlv-3b", "sess-1", approval.ID+"/deny")
	acts = f.waitActivities("sess-1", 8)
	contains(t, acts[7].Body, "was not delivered")
	if stale := f.waitSubmission("sess-1", "act-dlv-3b", "stale selection refused", func(s store.LinearSubmission) bool { return s.State == subDone }); stale.Kind != kindDecision || stale.RequestID != approval.ID || stale.Outcome != outcomeRefused {
		t.Errorf("stale selection = %+v", stale)
	}

	// A second request, resolved in T3 before the choice is dispatched:
	// the decision is refused as resolved, nothing else is chosen.
	f.approve(providerID, "apr-2")
	f.waitActivities("sess-1", 9)
	thread, _ = f.threads.Get(threadID)
	second := thread.Approvals[0]
	f.stop()
	f.prompt(t, "dlv-4", "sess-1", second.ID+"/deny")
	if err := f.threads.ObserveApprovals(context.Background(), providerT3, providerID, nil); err != nil {
		t.Fatal(err)
	}
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})
	f.start()
	acts = f.waitActivities("sess-1", 11)
	bodies := acts[9].Body + acts[10].Body
	contains(t, bodies, "was not delivered")
	contains(t, bodies, "resolved elsewhere")
	_, _, decisions, _ = f.starter.sent()
	if len(decisions) != 3 || decisions[2].Decision != api.DecisionDeny {
		t.Errorf("decisions = %+v", decisions)
	}
	stale := f.waitSubmission("sess-1", "act-dlv-4", "stale decision refused", func(s store.LinearSubmission) bool { return s.State == subDone })
	if stale.Outcome != outcomeRefused || stale.Value != "deny" {
		t.Errorf("stale decision = %+v", stale)
	}
}

// Stops: relayed through the shared capability under the submission's
// key, reported on the evidence — stopped when T3 closes the session
// with work in scope, finished when nothing was running — while
// submissions arriving during the stop are refused, and the
// conversation continues afterwards without reviving stopped work.
func TestStopIsRelayedAndReportedOnEvidence(t *testing.T) {
	f := newFixture(t)
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	threadID, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnRunning})

	f.stopSignal(t, "dlv-2", "sess-1")
	acts := f.waitActivities("sess-1", 3)
	contains(t, acts[2].Body, "The stop was sent")
	_, _, _, stops := f.starter.sent()
	if len(stops) != 1 || stops[0].Key != "act-dlv-2" {
		t.Errorf("stops = %+v", stops)
	}
	thread, _ := f.threads.Get(threadID)
	if thread.Stop == nil || thread.Stop.State != api.StopStopping {
		t.Fatalf("thread stop = %+v", thread.Stop)
	}
	// Work arriving while the stop is unresolved is refused, not queued.
	f.prompt(t, "dlv-3", "sess-1", "also this")
	acts = f.waitActivities("sess-1", 4)
	contains(t, acts[3].Body, "not sent")
	contains(t, acts[3].Body, "stop is being confirmed")
	if refused := f.waitSubmission("sess-1", "act-dlv-3", "refused", func(s store.LinearSubmission) bool { return s.State == subDone }); refused.Outcome != outcomeRefused {
		t.Errorf("submission during the stop = %+v", refused)
	}
	// T3 closes the session: the stop is confirmed, the turn reads
	// interrupted, and both are reported.
	if _, err := f.threads.ObserveExternal(context.Background(), threads.ExternalObservation{
		IntegrationID: providerT3, ProviderID: providerID, InitialDirectory: f.dir, Title: "T", Status: api.ThreadIdle,
		Turn: &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnInterrupted}, SessionClosedAt: f.clock.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	acts = f.waitActivities("sess-1", 6)
	bodies := acts[4].Body + acts[5].Body
	contains(t, bodies, "Stopped: T3 Code confirmed")
	contains(t, bodies, "stopped before it finished")
	stop := f.waitSubmission("sess-1", "act-dlv-2", "stop reported", func(s store.LinearSubmission) bool { return s.State == subDone })
	if stop.Outcome != outcomeStopped || stop.OperationID == "" {
		t.Errorf("stop submission = %+v", stop)
	}
	// The refused message is not revived; the conversation continues.
	if _, err := f.threads.MessageByKey(context.Background(), threadID, "act-dlv-3"); err == nil {
		t.Error("stopped work revived: the refused message is in the domain")
	}
	f.prompt(t, "dlv-4", "sess-1", "carry on then")
	f.waitActivities("sess-1", 7)
	if message, err := f.threads.MessageByKey(context.Background(), threadID, "act-dlv-4"); err != nil || message.Text != "carry on then" || f.session("sess-1").State != stateBound {
		t.Errorf("continuation = %+v, %v, session %s", message, err, f.session("sess-1").State)
	}
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnRunning})
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnCompleted, Response: "Carried on."})
	acts = f.waitActivities("sess-1", 8)
	if acts[7].Body != "Carried on." {
		t.Errorf("result after the stop = %+v", acts[7])
	}

	// A stop with nothing running resolves at once as finished.
	f.stopSignal(t, "dlv-5", "sess-1")
	acts = f.waitActivities("sess-1", 9)
	contains(t, acts[8].Body, "Nothing was stopped")
	if _, _, _, stops := f.starter.sent(); len(stops) != 2 {
		t.Errorf("stops = %+v", stops)
	}
}

// A stop before the start was sent cancels it: no Thread is started,
// then or later, and the session ends.
func TestStopBeforeStartCancelsIt(t *testing.T) {
	f := newFixture(t)
	f.retryBase, f.retryMax = time.Minute, 5*time.Minute
	f.starter.set(func(s *fakeCoordinator) { s.fail = errNotConnected })
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	f.waitActivities("sess-1", 2)
	waitFor(t, "start deferred", func() bool { return f.session("sess-1").State == stateAccepted })
	f.stopSignal(t, "dlv-2", "sess-1")
	acts := f.waitActivities("sess-1", 3)
	contains(t, acts[2].Body, "Stopped before the conversation started")
	session := f.waitSession("sess-1", "cancelled", func(s store.LinearSession) bool { return s.State == stateDone })
	if session.Outcome != outcomeCancelled {
		t.Errorf("session = %+v", session)
	}
	f.starter.set(func(s *fakeCoordinator) { s.fail = nil })
	f.hub.Publish(api.EventIntegrationUpdated, "integration", "t3code")
	time.Sleep(60 * time.Millisecond)
	if f.starter.count() != 1 {
		t.Errorf("starts = %d; a cancelled start must never run", f.starter.count())
	}
	f.prompt(t, "dlv-3", "sess-1", "hello?")
	acts = f.waitActivities("sess-1", 4)
	contains(t, acts[3].Body, "cannot be continued")
}

// A submission the program did not answer is reconciled under the same
// identity — never sent as a second one — and one the program cannot
// take yet waits, bounded, for its return.
func TestUncertainAndDeferredDispatchesReconcile(t *testing.T) {
	f := newFixture(t)
	f.retryBase, f.retryMax = time.Minute, 5*time.Minute
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	threadID, providerID := f.startedThread("sess-1")
	f.waitActivities("sess-1", 2)
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "First."})
	f.waitActivities("sess-1", 3)

	// Uncertain: the message is recorded once; retries present the same
	// key until the program commits it.
	f.starter.set(func(s *fakeCoordinator) { s.dispatch = integrations.ErrDeliveryUncertain })
	f.prompt(t, "dlv-2", "sess-1", "and now?")
	sub := f.waitSubmission("sess-1", "act-dlv-2", "uncertain", func(s store.LinearSubmission) bool {
		return s.State == subSent && s.Delivery == string(api.MessageUncertain)
	})
	if sub.Attempts == 0 || sub.OperationID == "" {
		t.Errorf("uncertain submission = %+v", sub)
	}
	f.clock.Advance(time.Minute)
	f.service.wake(f.service.sessionKick)
	waitFor(t, "a retry", func() bool { messages, _, _, _ := f.starter.sent(); return len(messages) >= 2 })
	f.starter.set(func(s *fakeCoordinator) { s.dispatch = nil })
	f.clock.Advance(5 * time.Minute)
	f.service.wake(f.service.sessionKick)
	sub = f.waitSubmission("sess-1", "act-dlv-2", "accepted", func(s store.LinearSubmission) bool { return s.Delivery == string(api.MessageAccepted) })
	messages, _, _, _ := f.starter.sent()
	for _, m := range messages {
		if m.Key != "act-dlv-2" || m.Text != "and now?" {
			t.Errorf("retry changed the message: %+v", m)
		}
	}
	if message, err := f.threads.MessageByKey(context.Background(), threadID, "act-dlv-2"); err != nil || message.ID != sub.OperationID {
		t.Errorf("one message in the domain: %+v, %v (submission %+v)", message, err, sub)
	}
	f.waitActivities("sess-1", 4)

	// Not connected: the next message waits, with backoff, and goes on
	// the program's return — woken by the connection change.
	f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnRunning})
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnCompleted, Response: "Second."})
	f.waitActivities("sess-1", 5)
	f.starter.set(func(s *fakeCoordinator) { s.dispatch = errNotConnected })
	f.prompt(t, "dlv-3", "sess-1", "third?")
	sub = f.waitSubmission("sess-1", "act-dlv-3", "deferred", func(s store.LinearSubmission) bool { return s.State == subPending && s.Attempts >= 1 })
	before, _, _, _ := f.starter.sent()
	for range 5 {
		f.service.wake(f.service.sessionKick)
		time.Sleep(10 * time.Millisecond)
	}
	after, _, _, _ := f.starter.sent()
	if len(after) != len(before) {
		t.Errorf("retries while disconnected within the backoff: %d, want none", len(after)-len(before))
	}
	if !sub.NextAttemptAt.After(f.clock.Now()) {
		t.Errorf("deferred submission = %+v, want a backoff", sub)
	}
	f.starter.set(func(s *fakeCoordinator) { s.dispatch = nil })
	f.hub.Publish(api.EventIntegrationUpdated, "integration", "t3code")
	f.waitSubmission("sess-1", "act-dlv-3", "sent on return", func(s store.LinearSubmission) bool { return s.State == subSent })
	f.waitActivities("sess-1", 6)
	if len(f.threads.List("", "", true)) != 1 {
		t.Errorf("threads = %d", len(f.threads.List("", "", true)))
	}
}

// Sessions with nothing to act on get an explanation and no Thread:
// delegation (no comment), a mention Linear supplied no context for, a
// message into a session ATC never recorded, and one into a session
// whose conversation ended.
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
	f.prompt(t, "dlv-3", "sess-unknown", "hello?")
	acts = f.waitActivities("sess-unknown", 1)
	contains(t, acts[0].Body, "not tracking a conversation for this session")
	f.prompt(t, "dlv-4", "sess-del", "hello?")
	acts = f.waitActivities("sess-del", 2)
	contains(t, acts[1].Body, "cannot be continued")
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
	f.prompt(t, "dlv-4", "sess-del", "hello?")
	time.Sleep(30 * time.Millisecond)
	if n := len(f.linear.activitiesOf("sess-del")); n != 2 {
		t.Errorf("refusals posted %d times", n)
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
			f.starter.set(func(s *fakeCoordinator) { s.fail, s.recorded = tc.fail, tc.recorded })
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
	f.starter.set(func(s *fakeCoordinator) { s.fail = errT3Silent })
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
// user hears once, the start is owed with a bounded backoff — the
// attempt's end does not trigger another at once — and it happens when
// T3 is back, woken by the Integration's connection change.
func TestStartWaitsForT3(t *testing.T) {
	f := newFixture(t)
	f.retryBase, f.retryMax = time.Minute, 5*time.Minute
	f.starter.set(func(s *fakeCoordinator) { s.fail = errNotConnected })
	f.start()
	f.process(t, "dlv-1", createdEvent("sess-1", f.clock.Now()))
	acts := f.waitActivities("sess-1", 2)
	contains(t, acts[1].Body, "T3 Code is not connected")
	f.waitSession("sess-1", "start owed again", func(s store.LinearSession) bool { return s.State == stateAccepted })
	if start := f.submissions("sess-1")[0]; start.State != subPending || start.Attempts != 1 || !start.NextAttemptAt.After(f.clock.Now()) {
		t.Errorf("start after a deferred attempt = %+v", start)
	}
	for range 5 {
		f.service.wake(f.service.sessionKick)
		time.Sleep(10 * time.Millisecond)
	}
	if n := f.starter.count(); n != 1 {
		t.Errorf("starts while disconnected = %d, want the one attempt until the backoff ends", n)
	}
	if n := len(f.linear.activitiesOf("sess-1")); n != 2 {
		t.Errorf("retries posted %d activities, want the notice once", n)
	}
	if threads := f.threads.List("", "", true); len(threads) != 0 {
		t.Errorf("a deferred start left threads %+v", threads)
	}
	// The backoff ends: one more attempt, still deferred, backing off
	// further.
	f.clock.Advance(f.retryMax)
	f.service.wake(f.service.sessionKick)
	waitFor(t, "second attempt", func() bool { return f.starter.count() == 2 })
	f.waitSubmission("sess-1", startKey("sess-1"), "deferred twice", func(s store.LinearSubmission) bool { return s.State == subPending && s.Attempts == 2 })
	f.starter.set(func(s *fakeCoordinator) { s.fail = nil })
	f.hub.Publish(api.EventIntegrationUpdated, "integration", "t3code")
	f.startedThread("sess-1")
	acts = f.waitActivities("sess-1", 3)
	contains(t, acts[2].Body, "conversation is running")
	if f.starter.count() != 3 {
		t.Errorf("starts = %d", f.starter.count())
	}
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
		session string
	}{
		"failed": {
			drive: func(f *fixture, _, providerID string) {
				f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnFailed, Error: "context window exceeded"})
			},
			outcome: outcomeFailed, want: "The run failed in T3 Code: context window exceeded", session: stateBound,
		},
		"interrupted": {
			drive: func(f *fixture, _, providerID string) {
				f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnInterrupted})
			},
			outcome: outcomeInterrupted, want: "stopped before it finished", session: stateBound,
		},
		"newer turn": {
			drive: func(f *fixture, _, providerID string) {
				// The user prompted again in T3 before the first turn's
				// response reached ATC: T3's latest turn is another one.
				f.report(providerID, api.ThreadWorking, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnRunning})
				f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-2", State: api.TurnCompleted, Response: "the second answer"})
			},
			outcome: outcomeUnrecoverable, want: "A newer turn replaced this one", session: stateBound,
		},
		"dropped by T3": {
			drive: func(f *fixture, _, providerID string) {
				if err := f.threads.ArchiveExternalThread(context.Background(), providerT3, providerID); err != nil {
					f.t.Fatal(err)
				}
			},
			outcome: outcomeUnrecoverable, want: "T3 Code no longer reports the conversation", session: stateBound,
		},
		"gone from ATC": {
			drive: func(f *fixture, _, providerID string) {
				if err := f.threads.DiscardExternal(context.Background(), providerT3, providerID); err != nil {
					f.t.Fatal(err)
				}
			},
			outcome: outcomeUnrecoverable, want: "no longer exists in ATC", session: stateDone,
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
			start := f.waitSubmission("sess-1", startKey("sess-1"), "reported", func(s store.LinearSubmission) bool { return s.State == subDone })
			if start.Outcome != tc.outcome {
				t.Errorf("start outcome = %s, want %s", start.Outcome, tc.outcome)
			}
			if session := f.session("sess-1"); session.State != tc.session {
				t.Errorf("session = %s, want %s", session.State, tc.session)
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
	f.waitSubmission("sess-1", startKey("sess-1"), "completion seen", func(s store.LinearSubmission) bool { return s.CompletedSeenAt != nil })
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
	f.waitSubmission("sess-2", startKey("sess-2"), "completion seen", func(s store.LinearSubmission) bool { return s.CompletedSeenAt != nil })
	f.clock.Advance(responseGrace)
	f.service.wake(f.service.sessionKick)
	acts = f.waitActivities("sess-2", 3)
	if acts[2].Type != contentError || !strings.Contains(acts[2].Body, "could not recover its final response") {
		t.Errorf("result = %+v", acts[2])
	}
}

// Time alone ends nothing: a session working or waiting for days stays
// bound and reports its outcome when it comes.
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
	f.ask(providerID, "req-1")
	f.waitActivities("sess-1", 3)
	f.clock.Advance(7 * 24 * time.Hour)
	f.service.wake(f.service.sessionKick)
	time.Sleep(30 * time.Millisecond)
	if session := f.session("sess-1"); session.State != stateBound {
		t.Fatalf("session = %s after a week, want still bound", session.State)
	}
	f.resolveInput(providerID, "req-1", nil)
	f.report(providerID, api.ThreadIdle, &threads.TurnObservation{ProviderID: "t3-turn-1", State: api.TurnCompleted, Response: "Finally."})
	acts := f.waitActivities("sess-1", 5)
	if !strings.Contains(acts[3].Body, "resolved elsewhere") || acts[4].Body != "Finally." {
		t.Errorf("closing and result = %+v", acts[3:])
	}
}
