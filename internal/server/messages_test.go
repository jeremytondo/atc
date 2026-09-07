package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
	"github.com/jeremytondo/atc/internal/threads"
)

var messageIDPattern = regexp.MustCompile(`^msg-[a-z2-9]{10}$`)

func decodeMessage(t *testing.T, rec *httptest.ResponseRecorder) api.ThreadMessage {
	t.Helper()
	var message api.ThreadMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &message); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return message
}

func decodeApproval(t *testing.T, rec *httptest.ResponseRecorder) api.ThreadApproval {
	t.Helper()
	var approval api.ThreadApproval
	if err := json.Unmarshal(rec.Body.Bytes(), &approval); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	return approval
}

// t3Thread has the fake environment report a Codex thread at rest and
// returns ATC's record of it.
func (f *fixture) t3Thread(t *testing.T, sequence uint64, t3ID string, opts ...t3codetest.ThreadOpt) api.Thread {
	t.Helper()
	opts = append([]t3codetest.ThreadOpt{t3codetest.WithSession("idle", "codex")}, opts...)
	f.t3Server.Push(t3codetest.Upserted(sequence, t3codetest.ThreadItem(t3ID, "p1", "One", opts...)))
	var thread api.Thread
	waitUntil(t, "T3's thread "+t3ID, func() bool {
		id, _, ok := f.threads.LookupIdentity("t3code", t3ID)
		if !ok {
			return false
		}
		thread, _ = f.threads.Get(id)
		return thread.LastEvidenceAt != nil
	})
	return thread
}

// The acceptance path (ATC-307): a message on an idle T3 thread returns
// 202 accepted with the pending turn it starts, T3 received one
// thread.turn.start without a bootstrap, the same key returns the same
// message without a second command, another submission meanwhile is a
// conflict, and T3's report of the new turn binds it — the message's
// turnId becomes latestTurn, with its reply.
func TestThreadMessageOverTheWire(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.t3Thread(t, 2, "t1", t3codetest.LatestTurn("pt-0", "completed", "2026-09-01T00:00:01Z", "2026-09-01T00:00:02Z"))
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)

	rec := f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"  run the tests ","key":"k1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send: got %d; body %s", rec.Code, rec.Body)
	}
	message := decodeMessage(t, rec)
	after := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
	if !messageIDPattern.MatchString(message.ID) || after.PendingTurn == nil || message.TurnID != after.PendingTurn.ID || after.Status != api.ThreadWorking || after.LatestTurn.ID != thread.LatestTurn.ID {
		t.Fatalf("message = %+v; thread after = %+v", message, after)
	}
	want := api.ThreadMessage{ID: message.ID, ThreadID: thread.ID, Key: "k1", Text: "  run the tests ", TurnID: after.PendingTurn.ID, Delivery: api.MessageAccepted, CreatedAt: message.CreatedAt}
	if diff := cmp.Diff(want, message); diff != "" {
		t.Errorf("message (-want +got):\n%s", diff)
	}
	commands := f.t3Server.Commands()
	if len(commands) != 1 {
		t.Fatalf("T3 received %d commands; want 1", len(commands))
	}
	if commands[0]["type"] != "thread.turn.start" || commands[0]["threadId"] != "t1" || commands[0]["bootstrap"] != nil || commands[0]["modelSelection"] != nil ||
		commands[0]["message"].(map[string]any)["text"] != "  run the tests " {
		t.Errorf("command = %v", commands[0])
	}
	if got := changes(sub); !slices.Equal(got, []string{"thread.updated " + thread.ID}) {
		t.Errorf("events on send = %v", got)
	}

	// The same key: the same message, no second command, no event.
	rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"whatever","key":"k1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("replay: got %d; body %s", rec.Code, rec.Body)
	}
	if diff := cmp.Diff(want, decodeMessage(t, rec)); diff != "" {
		t.Errorf("replay (-want +got):\n%s", diff)
	}
	if len(f.t3Server.Commands()) != 1 || len(changes(sub)) != 0 {
		t.Error("the replay sent a command or published")
	}
	// Another submission while one pends is a conflict naming the turn.
	rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"more"}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusConflict || problem.Code != api.CodeThreadTurnPending || !strings.Contains(problem.Detail, after.PendingTurn.ID) {
		t.Errorf("second submission: got %d %+v", rec.Code, problem)
	}

	// T3 starts the turn: bound to the message's turn id, then finished
	// with its reply.
	f.t3Server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"),
		t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))))
	waitUntil(t, "the turn to bind", func() bool {
		after = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
		return after.LatestTurn != nil && after.LatestTurn.ID == message.TurnID
	})
	if after.PendingTurn != nil || after.LatestTurn.State != api.TurnRunning {
		t.Errorf("bound = %+v pending %+v", after.LatestTurn, after.PendingTurn)
	}
	done := t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"),
		t3codetest.LatestTurn("pt-1", "completed", "2026-09-01T00:00:03Z", "2026-09-01T00:00:09Z"), t3codetest.AssistantMessage("m1"))
	f.t3Server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(done, t3codetest.MessageItem("m1", "assistant", "All green.", "pt-1", false)))
	f.t3Server.Push(t3codetest.Upserted(4, done))
	waitUntil(t, "the reply", func() bool {
		after = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
		return after.LatestTurn != nil && after.LatestTurn.Response == "All green."
	})
	if after.LatestTurn.ID != message.TurnID || after.LatestTurn.State != api.TurnCompleted {
		t.Errorf("finished = %+v", after.LatestTurn)
	}
	// The next message starts the next turn.
	rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"and lint"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("next send: got %d; body %s", rec.Code, rec.Body)
	}
	if next := decodeMessage(t, rec); next.TurnID == message.TurnID || next.Key != "" {
		t.Errorf("next message = %+v", next)
	}
}

// A message while the agent waits on a question (ATC-307) is the normal
// message operation: sent as any other, no answer translation, and the
// thread keeps showing what T3 reports until T3 says otherwise.
func TestThreadMessageDuringQuestion(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.t3Thread(t, 2, "t1", t3codetest.WithSession("running", "codex"),
		t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(false, true))
	if thread.Status != api.ThreadWaitingForInput {
		t.Fatalf("status = %s", thread.Status)
	}
	rec := f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"the second option"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("send: got %d; body %s", rec.Code, rec.Body)
	}
	message := decodeMessage(t, rec)
	commands := f.t3Server.Commands()
	if len(commands) != 1 || commands[0]["type"] != "thread.turn.start" || commands[0]["message"].(map[string]any)["text"] != "the second option" {
		t.Errorf("commands = %v; want the plain message", commands)
	}
	after := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
	if after.PendingTurn == nil || after.PendingTurn.ID != message.TurnID || after.LatestTurn.State != api.TurnRunning {
		t.Errorf("after = pending %+v latest %+v", after.PendingTurn, after.LatestTurn)
	}
	// T3 still waiting: ATC still says so.
	f.t3Server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"),
		t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(false, true))))
	time.Sleep(30 * time.Millisecond)
	if after = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, "")); after.Status != api.ThreadWaitingForInput || after.PendingTurn == nil {
		t.Errorf("while T3 keeps waiting = %s pending %+v", after.Status, after.PendingTurn)
	}
}

// Every refusal, by name, and none of them reaching T3.
func TestThreadMessageRefusals(t *testing.T) {
	f := newFixture(t)
	// A T3 thread known before T3 is connected.
	id, err := f.threads.ObserveExternal(context.Background(), threads.ExternalObservation{
		IntegrationID: "t3code", ProviderID: "t1", InitialDirectory: f.projectDir, Status: api.ThreadIdle, Title: "One",
	})
	if err != nil {
		t.Fatal(err)
	}
	f.threads.ReleaseIntegration(context.Background(), "t3code")
	terminal := f.createRunningTerminal(t)
	claude := f.observeThread(t, terminal.ID, "sess-1", api.ThreadIdle)
	cases := []struct {
		name   string
		path   string
		body   string
		status int
		code   string
		detail string
	}{
		{"blank text", "/v1/threads/" + id + "/messages", `{"text":" \n"}`, http.StatusBadRequest, api.CodeValidationFailed, "text is empty"},
		{"no text", "/v1/threads/" + id + "/messages", `{}`, http.StatusUnprocessableEntity, api.CodeValidationFailed, "validation failed"},
		{"unknown thread", "/v1/threads/thrd-zzzzz/messages", `{"text":"hi"}`, http.StatusNotFound, api.CodeThreadNotFound, "thread not found"},
		{"integration without messages", "/v1/threads/" + claude + "/messages", `{"text":"hi"}`, http.StatusBadRequest, api.CodeThreadSendUnsupported, "does not support sending messages"},
		{"not connected", "/v1/threads/" + id + "/messages", `{"text":"hi"}`, http.StatusServiceUnavailable, api.CodeIntegrationNotConnected, "T3 Code is unavailable"},
	}
	for _, tc := range cases {
		rec := f.request(t, http.MethodPost, tc.path, tc.body)
		problem := decodeProblem(t, rec)
		if rec.Code != tc.status || problem.Code != tc.code || !strings.Contains(problem.Detail, tc.detail) {
			t.Errorf("%s: got %d %+v; want %d %s containing %q", tc.name, rec.Code, problem, tc.status, tc.code, tc.detail)
		}
	}
	if commands := f.t3Server.Commands(); len(commands) != 0 {
		t.Errorf("T3 received %d commands", len(commands))
	}
	if thread, _ := f.threads.Get(id); thread.PendingTurn != nil {
		t.Errorf("a refusal left a pending turn: %+v", thread.PendingTurn)
	}
}

// T3 rejecting a message: 502 with T3's message, the pending turn
// withdrawn and the status restored, and the key replaying the rejection
// without a second command.
func TestThreadMessageRejected(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.t3Thread(t, 2, "t1")
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		return t3codetest.DispatchReply{Reject: "no provider session"}
	})
	for range 2 {
		rec := f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"hi","key":"k1"}`)
		problem := decodeProblem(t, rec)
		if rec.Code != http.StatusBadGateway || problem.Code != api.CodeThreadMessageRejected || !strings.Contains(problem.Detail, "no provider session") {
			t.Errorf("rejected: got %d %+v", rec.Code, problem)
		}
	}
	if commands := f.t3Server.Commands(); len(commands) != 1 {
		t.Errorf("T3 received %d commands; want the one, the replay answered from the record", len(commands))
	}
	after := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
	if after.PendingTurn != nil || after.Status != api.ThreadIdle {
		t.Errorf("after rejection = status %s pending %+v", after.Status, after.PendingTurn)
	}
	if got := changes(sub); !slices.Equal(got, []string{"thread.updated " + thread.ID, "thread.updated " + thread.ID}) {
		t.Errorf("events = %v; want the submission and its withdrawal", got)
	}
}

// T3 never answering: 202 with delivery uncertain and the message kept
// under its key; once T3 is back, the same key retries the exact same
// command — which T3's receipt deduplicates — and reports accepted.
func TestThreadMessageUncertainThenReconciled(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	thread := f.t3Thread(t, 2, "t1")
	// The fake answers every subscription with a snapshot; the one after
	// the drop must still report the thread.
	f.t3Server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(3, []any{t3codetest.ProjectItem("p1", "T3", f.projectDir)}, []any{
			t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex")),
		}), t3codetest.SynchronizedItem()}
	})
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		f.t3Server.DropConns()
		select {}
	})
	rec := f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"hi","key":"k1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("uncertain send: got %d; body %s", rec.Code, rec.Body)
	}
	message := decodeMessage(t, rec)
	if message.Delivery != api.MessageUncertain {
		t.Errorf("message = %+v; want delivery uncertain", message)
	}
	after := decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
	if after.PendingTurn == nil || after.PendingTurn.ID != message.TurnID {
		t.Errorf("thread after an uncertain send = pending %+v", after.PendingTurn)
	}
	// While T3 is away, the replay is refused as not connected — nothing
	// is resent under a fresh identity.
	waitUntil(t, "T3 to reconnect", func() bool {
		return len(f.t3Server.Subscriptions()) == 2 && f.t3.Connection().State == api.IntegrationConnected
	})
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{} })
	rec = f.request(t, http.MethodPost, "/v1/threads/"+thread.ID+"/messages", `{"text":"hi","key":"k1"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reconcile: got %d; body %s", rec.Code, rec.Body)
	}
	reconciled := decodeMessage(t, rec)
	if reconciled.ID != message.ID || reconciled.TurnID != message.TurnID || reconciled.Delivery != api.MessageAccepted {
		t.Errorf("reconciled = %+v; want %s accepted", reconciled, message.ID)
	}
	commands := f.t3Server.Commands()
	if len(commands) != 2 || commands[0]["commandId"] != commands[1]["commandId"] || commands[0]["message"].(map[string]any)["messageId"] != commands[1]["message"].(map[string]any)["messageId"] {
		t.Errorf("commands = %v; want the same command twice", commands)
	}
}

// Approval requests over the wire (ATC-307): a blocked T3 thread shows
// its pending requests with stable ids and offered decisions, a decision
// resolves one through T3's command and the thread stops showing it,
// and every refusal by name.
func TestThreadApprovalOverTheWire(t *testing.T) {
	f := newFixture(t)
	f.connectT3(t, f.projectDir)
	sub := f.hub.Subscribe(0, false)
	t.Cleanup(sub.Close)
	blocked := func(sequence uint64, pending bool) map[string]any {
		return t3codetest.Upserted(sequence, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"),
			t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(pending, false)))
	}
	f.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.ApprovalRequested("a1", "req-1", "command_execution_approval", "make test", t3codetest.ApprovalOption("accept", "Approve"), t3codetest.ApprovalOption("acceptForSession", "Approve for session"), t3codetest.ApprovalOption("decline", "Decline")),
		t3codetest.ApprovalRequested("a2", "req-2", "file_change_approval", "main.go")))
	f.t3Server.Push(blocked(2, true))
	var thread api.Thread
	waitUntil(t, "the requests on the thread", func() bool {
		id, _, ok := f.threads.LookupIdentity("t3code", "t1")
		if !ok {
			return false
		}
		thread = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+id, ""))
		return len(thread.Approvals) == 2
	})
	first, second := thread.Approvals[0], thread.Approvals[1]
	want := api.ThreadApproval{
		ID: first.ID, ThreadID: thread.ID, Status: api.ApprovalPending, Kind: api.ApprovalCommand, Summary: "Command approval requested", Detail: "make test",
		Options:     []api.ApprovalOption{{Decision: api.DecisionApprove, Label: "Approve"}, {Decision: api.DecisionApproveForSession, Label: "Approve for session"}, {Decision: api.DecisionDeny, Label: "Decline"}},
		RequestedAt: time.Date(2026, 9, 1, 0, 0, 1, 0, time.UTC),
	}
	if diff := cmp.Diff(want, first); diff != "" {
		t.Errorf("approval (-want +got):\n%s", diff)
	}
	if thread.Status != api.ThreadWaitingForPermission || !strings.HasPrefix(first.ID, "aprv-") || strings.Contains(f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, "").Body.String(), "req-1") {
		t.Errorf("thread = %s %+v", thread.Status, thread.Approvals)
	}
	if got := changes(sub); count(got, "thread.updated "+thread.ID) == 0 {
		t.Errorf("events = %v; want thread.updated for the requests", got)
	}

	path := func(approvalID string) string {
		return "/v1/threads/" + thread.ID + "/approvals/" + approvalID + "/decide"
	}
	refusals := []struct {
		name   string
		path   string
		body   string
		status int
		code   string
	}{
		{"decision not offered", path(first.ID), `{"decision":"approve_always"}`, http.StatusBadRequest, api.CodeApprovalDecisionInvalid},
		{"decision unknown", path(first.ID), `{"decision":"maybe"}`, http.StatusUnprocessableEntity, api.CodeValidationFailed},
		{"unknown approval", path("aprv-zzzzzzzzzz"), `{"decision":"approve"}`, http.StatusNotFound, api.CodeApprovalNotFound},
		{"unknown thread", "/v1/threads/thrd-zzzzz/approvals/" + first.ID + "/decide", `{"decision":"approve"}`, http.StatusNotFound, api.CodeThreadNotFound},
	}
	for _, tc := range refusals {
		rec := f.request(t, http.MethodPost, tc.path, tc.body)
		if problem := decodeProblem(t, rec); rec.Code != tc.status || problem.Code != tc.code {
			t.Errorf("%s: got %d %+v; want %d %s", tc.name, rec.Code, problem, tc.status, tc.code)
		}
	}
	if commands := f.t3Server.Commands(); len(commands) != 0 {
		t.Fatalf("refusals sent %d commands", len(commands))
	}

	// The decision: T3 gets thread.approval.respond in its vocabulary, the
	// request is resolved with the decision, and it leaves the thread.
	rec := f.request(t, http.MethodPost, path(first.ID), `{"decision":"approve_for_session"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("decide: got %d; body %s", rec.Code, rec.Body)
	}
	resolved := decodeApproval(t, rec)
	if resolved.ID != first.ID || resolved.Status != api.ApprovalResolved || resolved.Decision != api.DecisionApproveForSession || resolved.ResolvedAt == nil {
		t.Errorf("resolved = %+v", resolved)
	}
	commands := f.t3Server.Commands()
	if len(commands) != 1 || commands[0]["type"] != "thread.approval.respond" || commands[0]["threadId"] != "t1" || commands[0]["requestId"] != "req-1" || commands[0]["decision"] != "acceptForSession" {
		t.Errorf("commands = %v", commands)
	}
	thread = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
	if len(thread.Approvals) != 1 || thread.Approvals[0].ID != second.ID {
		t.Errorf("pending after the decision = %+v", thread.Approvals)
	}
	rec = f.request(t, http.MethodPost, path(first.ID), `{"decision":"deny"}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusConflict || problem.Code != api.CodeApprovalResolved || !strings.Contains(problem.Detail, "approve_for_session") {
		t.Errorf("deciding a resolved request: got %d %+v", rec.Code, problem)
	}

	// T3 rejects the second: 502, the request stays open.
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		return t3codetest.DispatchReply{Reject: "unknown request"}
	})
	rec = f.request(t, http.MethodPost, path(second.ID), `{"decision":"deny"}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusBadGateway || problem.Code != api.CodeApprovalDecisionFailed || !strings.Contains(problem.Detail, "unknown request") {
		t.Errorf("rejected decision: got %d %+v", rec.Code, problem)
	}
	// T3 never answers: 502 unknown; a different decision is refused
	// until the answer is known; the same one, once T3 is back, presents
	// the same command and resolves. The snapshot after the drop still
	// reports the thread blocked.
	f.t3Server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(3, []any{t3codetest.ProjectItem("p1", "T3", f.projectDir)}, []any{
			t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"),
				t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(true, false)),
		}), t3codetest.SynchronizedItem()}
	})
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		f.t3Server.DropConns()
		select {}
	})
	rec = f.request(t, http.MethodPost, path(second.ID), `{"decision":"approve"}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusBadGateway || problem.Code != api.CodeApprovalDecisionUnknown {
		t.Errorf("unanswered decision: got %d %+v", rec.Code, problem)
	}
	rec = f.request(t, http.MethodPost, path(second.ID), `{"decision":"deny"}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusConflict || problem.Code != api.CodeApprovalDecisionPending {
		t.Errorf("different decision while unanswered: got %d %+v", rec.Code, problem)
	}
	waitUntil(t, "T3 to reconnect", func() bool {
		return len(f.t3Server.Subscriptions()) == 2 && f.t3.Connection().State == api.IntegrationConnected
	})
	f.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{} })
	rec = f.request(t, http.MethodPost, path(second.ID), `{"decision":"approve"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("reconciled decision: got %d; body %s", rec.Code, rec.Body)
	}
	commands = f.t3Server.Commands()
	sent := commands[len(commands)-2:]
	if sent[0]["commandId"] != sent[1]["commandId"] || sent[1]["decision"] != "accept" {
		t.Errorf("reconciled commands = %v; want the same command twice", sent)
	}
	// Resolved elsewhere: T3 reports nothing pending; a late decision is
	// refused as resolved.
	f.t3Server.Push(blocked(4, false))
	waitUntil(t, "the thread to unblock", func() bool {
		thread = decodeThread(t, f.request(t, http.MethodGet, "/v1/threads/"+thread.ID, ""))
		return thread.Status == api.ThreadWorking
	})
	if len(thread.Approvals) != 0 {
		t.Errorf("approvals after resolution = %+v", thread.Approvals)
	}
	// An Integration without the seam is refused before any lookup.
	terminal := f.createRunningTerminal(t)
	claude := f.observeThread(t, terminal.ID, "sess-1", api.ThreadWaitingForPermission)
	rec = f.request(t, http.MethodPost, fmt.Sprintf("/v1/threads/%s/approvals/%s/decide", claude, first.ID), `{"decision":"approve"}`)
	if problem := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || problem.Code != api.CodeThreadDecideUnsupported {
		t.Errorf("integration without decisions: got %d %+v", rec.Code, problem)
	}
}
