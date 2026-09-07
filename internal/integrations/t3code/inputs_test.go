package t3code

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
	"github.com/jeremytondo/atc/internal/threads"
)

// The pending questions derived from a thread's activities: requested
// minus resolved minus stale-failed, in request order, T3's allowances
// normalized — custom text allowed unless refused, the option value
// falling back to the label — content ATC cannot answer named rather
// than trimmed, and every resolution and failure reported as evidence.
func TestPendingInputs(t *testing.T) {
	items := []map[string]any{
		t3codetest.UserInputRequested("a1", "req-1",
			t3codetest.Question("Which color?", "Color", "Which color?", t3codetest.QuestionOption("Red", "warm"), t3codetest.QuestionOption("Blue", "cool", "blue-id"), t3codetest.AllowCustom(false)),
			t3codetest.Question("tools", "Tools", "Which tools?", t3codetest.QuestionOption("Go", "the language"), t3codetest.MultiSelect())),
		t3codetest.UserInputRequested("a2", "req-2", t3codetest.Question("x", "X", "Pick", t3codetest.AllowCustom(false))),
		t3codetest.UserInputRequested("a3", "req-3", t3codetest.Question("y", "Y", "Type anything")),
		t3codetest.UserInputRequested("a4", "req-4"),
		t3codetest.UserInputRequested("a5", "req-5", t3codetest.Question("z", "Z", "Done?", t3codetest.QuestionOption("Yes", "")), t3codetest.Question("z", "Z2", "Again?", t3codetest.QuestionOption("No", ""))),
		t3codetest.UserInputResolved("b3", "req-3", map[string]any{"y": "free text"}),
		t3codetest.UserInputRespondFailed("a6", "req-5", "Stale pending user-input request: req-5. Provider callback state does not survive app restarts."),
		t3codetest.UserInputRespondFailed("a7", "req-1", "No active provider session is bound to this thread."),
		t3codetest.UserInputResolved("b8", "req-9", map[string]any{"q": []any{"a", "b"}, "n": 3}),
		t3codetest.UserInputRequested("a9", "req-1", t3codetest.Question("dup", "D", "duplicate report")),
		t3codetest.ActivityItem("a0", "user-input.requested", "not an object"),
	}
	data, _ := json.Marshal(items)
	var activities []activity
	if err := json.Unmarshal(data, &activities); err != nil {
		t.Fatal(err)
	}
	at := func(second int) time.Time { return time.Date(2026, 9, 1, 0, 0, second, 0, time.UTC) }
	pending, resolutions := pendingInputs(activities)
	want := []threads.InputObservation{
		{RequestID: "req-1", RequestedAt: at(1), Questions: []threads.QuestionObservation{
			{ProviderID: "Which color?", Header: "Color", Text: "Which color?", Options: []api.InputOption{{Value: "Red", Label: "Red", Description: "warm"}, {Value: "blue-id", Label: "Blue", Description: "cool"}}},
			{ProviderID: "tools", Header: "Tools", Text: "Which tools?", Options: []api.InputOption{{Value: "Go", Label: "Go", Description: "the language"}}, AllowsCustom: true, AllowsMultiple: true},
		}},
		{RequestID: "req-2", RequestedAt: at(2), Unanswerable: "question 1 offers no choices and allows no custom text",
			Questions: []threads.QuestionObservation{{ProviderID: "x", Header: "X", Text: "Pick", Options: []api.InputOption{}}}},
		{RequestID: "req-4", RequestedAt: at(4), Unanswerable: "the request carries no questions"},
	}
	if diff := cmp.Diff(want, pending); diff != "" {
		t.Errorf("pending (-want +got):\n%s", diff)
	}
	wantEvidence := []threads.InputResolution{
		{RequestID: "req-3", Answers: []threads.ProviderAnswer{{QuestionID: "y", Values: []string{"free text"}}}, At: at(3)},
		{RequestID: "req-5", Failure: "Stale pending user-input request: req-5. Provider callback state does not survive app restarts.", Stale: true, At: at(6)},
		{RequestID: "req-1", Failure: "No active provider session is bound to this thread.", At: at(7)},
		{RequestID: "req-9", Answers: []threads.ProviderAnswer{{QuestionID: "q", Values: []string{"a", "b"}, Multiple: true}}, At: at(8)},
	}
	if diff := cmp.Diff(wantEvidence, resolutions); diff != "" {
		t.Errorf("evidence (-want +got):\n%s", diff)
	}
	// A request repeating a question id is unanswerable as a whole, and
	// so is one whose questions ATC cannot read — never hidden.
	dup, _ := pendingInputs(activities[4:5])
	if len(dup) != 1 || !strings.Contains(dup[0].Unanswerable, "question 2 repeats") {
		t.Errorf("duplicate question ids = %+v", dup)
	}
	odd, _ := json.Marshal([]map[string]any{t3codetest.ActivityItem("a1", "user-input.requested", map[string]any{"requestId": "req-odd", "questions": map[string]any{"not": "a list"}})})
	var unreadable []activity
	if err := json.Unmarshal(odd, &unreadable); err != nil {
		t.Fatal(err)
	}
	if got, _ := pendingInputs(unreadable); len(got) != 1 || got[0].RequestID != "req-odd" || !strings.Contains(got[0].Unanswerable, "cannot read") {
		t.Errorf("unreadable questions = %+v", got)
	}
}

// An answer (ATC-308) is one thread.user-input.respond command in T3's
// shape — a string per single-selection question, a list per
// multi-selection one — with a command id from the answer id, and the
// request is then watched: the evidence T3 appends resolves the answer
// through the domain whether the shell changes (the pending flag
// dropping) or not (a failure that leaves the request pending).
func TestAnswerInputOverT3(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.connect(workspace)
	ctx := context.Background()
	if _, err := f.service.PrepareAnswer(ctx, "t-unknown"); !errors.Is(err, integrations.ErrNotConnected) {
		t.Errorf("unknown thread = %v", err)
	}
	asking := func(sequence uint64, pending bool) map[string]any {
		return t3codetest.Upserted(sequence, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"),
			t3codetest.LatestTurn("tu-1", "running", "2026-09-01T00:00:02Z", nil), t3codetest.Pending(false, pending)))
	}
	requested := t3codetest.UserInputRequested("a1", "req-1",
		t3codetest.Question("color", "Color", "Which color?", t3codetest.QuestionOption("Red", ""), t3codetest.QuestionOption("Blue", "")),
		t3codetest.Question("tools", "Tools", "Which tools?", t3codetest.QuestionOption("go", ""), t3codetest.QuestionOption("make", ""), t3codetest.MultiSelect()))
	detail := func(activities ...map[string]any) map[string]any {
		return t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")), activities...)
	}
	f.server.SetThreadDetail("t1", detail(requested))
	f.server.Push(asking(2, true))
	thread := f.waitStatus("t1", api.ThreadWaitingForInput)
	waitFor(t, "the request on the thread", func() bool { thread, _ = f.threads.Get(thread.ID); return len(thread.InputRequests) == 1 })
	request := thread.InputRequests[0]
	if request.Questions[0].ID != "q1" || request.Questions[1].AllowsMultiple != true || request.Questions[0].AllowsCustom != true {
		t.Fatalf("request = %+v", request)
	}

	answers := []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Blue"}}, {QuestionID: "q2", Choices: []string{"go", "make"}}}
	req, err := f.threads.BeginAnswer(ctx, thread.ID, request.ID, answers)
	if err != nil {
		t.Fatal(err)
	}
	dispatch, err := f.service.PrepareAnswer(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	answer := integrations.InputAnswer{RequestID: req.RequestID, Answers: req.Answers, Key: req.AnswerID, CreatedAt: req.CreatedAt}
	if err := dispatch(ctx, answer); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(ctx, answer); err != nil {
		t.Fatal(err)
	}
	commands := f.server.Commands()
	if len(commands) != 2 || commands[0]["commandId"] != commands[1]["commandId"] {
		t.Fatalf("commands = %v", commands)
	}
	got := commands[0]
	if id, _ := got["commandId"].(string); !uuidPattern.MatchString(id) {
		t.Errorf("commandId = %v", got["commandId"])
	}
	if at, _ := got["createdAt"].(string); at != timestamp(req.CreatedAt) {
		t.Errorf("createdAt = %v; want %s", got["createdAt"], timestamp(req.CreatedAt))
	}
	delete(got, "commandId")
	delete(got, "createdAt")
	want := map[string]any{"type": "thread.user-input.respond", "threadId": "t1", "requestId": "req-1", "answers": map[string]any{"color": "Blue", "tools": []any{"go", "make"}}}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("command (-want +got):\n%s", diff)
	}
	if _, err := f.threads.AnswerDelivered(ctx, thread.ID, req.AnswerID); err != nil {
		t.Fatal(err)
	}
	// T3 delivers the answer: the request resolves with these answers
	// and the pending flag drops; the read that follows finds the
	// evidence and resolves the answer.
	f.server.SetThreadDetail("t1", detail(requested, t3codetest.ActivityAt(t3codetest.UserInputResolved("b2", "req-1", map[string]any{"color": "Blue", "tools": []any{"make", "go"}}), timestamp(req.CreatedAt))))
	f.server.Push(asking(3, false))
	waitFor(t, "the answer resolved", func() bool {
		got, _ := f.threads.InputRequest(thread.ID, request.ID)
		return got.Answer != nil && got.Answer.State == api.InputAnswerResolved
	})
	if got, _ := f.threads.InputRequest(thread.ID, request.ID); got.Resolution != api.InputResolvedByAnswer {
		t.Errorf("request = %+v", got)
	}
	if got, _ := f.threads.Get(thread.ID); len(got.InputRequests) != 0 {
		t.Errorf("pending after resolution = %+v", got.InputRequests)
	}

	// A second request, answered, fails in T3 without the shell
	// changing: the watch reads again on its own and the answer fails,
	// the request still pending.
	second := t3codetest.UserInputRequested("a3", "req-2", t3codetest.Question("more", "More", "More?", t3codetest.QuestionOption("Yes", ""), t3codetest.QuestionOption("No", "")))
	f.server.SetThreadDetail("t1", detail(requested, second))
	f.server.Push(asking(4, true))
	waitFor(t, "the second request", func() bool {
		thread, _ = f.threads.Get(thread.ID)
		return len(thread.InputRequests) == 1 && thread.InputRequests[0].ID != request.ID
	})
	next := thread.InputRequests[0]
	req, err = f.threads.BeginAnswer(ctx, thread.ID, next.ID, []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Yes"}}})
	if err != nil {
		t.Fatal(err)
	}
	reads := f.server.DetailReads("t1")
	if err := dispatch(ctx, integrations.InputAnswer{RequestID: req.RequestID, Answers: req.Answers, Key: req.AnswerID, CreatedAt: req.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "a read after the dispatch", func() bool { return f.server.DetailReads("t1") > reads })
	failed := t3codetest.ActivityAt(t3codetest.UserInputRespondFailed("a5", "req-2", "No active provider session is bound to this thread."), timestamp(req.CreatedAt))
	f.server.SetThreadDetail("t1", detail(requested, second, failed))
	waitFor(t, "the answer failed", func() bool {
		got, _ := f.threads.InputRequest(thread.ID, next.ID)
		return got.Answer != nil && got.Answer.State == api.InputAnswerFailed
	})
	if got, _ := f.threads.InputRequest(thread.ID, next.ID); got.Status != api.InputRequestPending || !strings.Contains(got.Answer.Detail, "No active provider session") {
		t.Errorf("failed answer = %+v (answer %+v)", got, got.Answer)
	}
	// With nothing awaited the watch ends: no more reads.
	time.Sleep(60 * time.Millisecond)
	reads = f.server.DetailReads("t1")
	time.Sleep(60 * time.Millisecond)
	if f.server.DetailReads("t1") != reads {
		t.Errorf("reads went on after the answer settled: %d then %d", reads, f.server.DetailReads("t1"))
	}

	// The dispatch outcomes by name: T3's refusal, and T3 never
	// answering.
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		return t3codetest.DispatchReply{Reject: "unknown request"}
	})
	if err := dispatch(ctx, answer); !errors.Is(err, integrations.ErrAnswerRejected) || !strings.Contains(err.Error(), "unknown request") {
		t.Errorf("rejected = %v", err)
	}
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { select {} })
	sent := len(f.server.Commands())
	done := make(chan error, 1)
	go func() { done <- dispatch(ctx, answer) }()
	waitFor(t, "the command to reach T3", func() bool { return len(f.server.Commands()) == sent+1 })
	f.server.DropConns()
	if err := <-done; !errors.Is(err, integrations.ErrDeliveryUncertain) {
		t.Errorf("across a drop = %v", err)
	}
}

// After a reconnect (ATC-308) the Integration watches every thread the
// domain still awaits evidence for, so an answer sent before the drop
// resolves once T3 shows its outcome.
func TestAnswerWatchedAfterReconnect(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.connect(workspace)
	ctx := context.Background()
	asking := func(sequence uint64, pending bool) map[string]any {
		return t3codetest.Upserted(sequence, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.Pending(false, pending)))
	}
	requested := t3codetest.UserInputRequested("a1", "req-1", t3codetest.Question("ok", "OK", "Proceed?", t3codetest.QuestionOption("Yes", ""), t3codetest.QuestionOption("No", "")))
	f.server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")), requested))
	f.server.Push(asking(2, true))
	thread := f.waitStatus("t1", api.ThreadWaitingForInput)
	waitFor(t, "the request", func() bool { thread, _ = f.threads.Get(thread.ID); return len(thread.InputRequests) == 1 })
	req, err := f.threads.BeginAnswer(ctx, thread.ID, thread.InputRequests[0].ID, []api.QuestionAnswer{{QuestionID: "q1", Choices: []string{"Yes"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.threads.AnswerDelivered(ctx, thread.ID, req.AnswerID); err != nil {
		t.Fatal(err)
	}
	// The connection drops; T3 resolves the request meanwhile and its
	// snapshot after the reconnect shows the thread quiet.
	f.server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")), requested,
		t3codetest.ActivityAt(t3codetest.UserInputResolved("b2", "req-1", map[string]any{"ok": "Yes"}), timestamp(req.CreatedAt))))
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(5, []any{t3codetest.ProjectItem("p1", "T3", workspace)},
			[]any{t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"))}), t3codetest.SynchronizedItem()}
	})
	f.server.DropConns()
	waitFor(t, "the answer resolved after the reconnect", func() bool {
		got, _ := f.threads.InputRequest(thread.ID, thread.InputRequests[0].ID)
		return got.Answer != nil && got.Answer.State == api.InputAnswerResolved
	})
}
