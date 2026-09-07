package main

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
	"github.com/jeremytondo/atc/internal/service"
)

// hasRow reports whether a tabwriter row with these cells, in order, is
// in the output.
func hasRow(out string, cells ...string) bool {
	pattern := `(?m)^\s*` + regexp.QuoteMeta(cells[0])
	for _, cell := range cells[1:] {
		pattern += `\s+` + regexp.QuoteMeta(cell)
	}
	return regexp.MustCompile(pattern).MatchString(out)
}

// asking has the fake T3 report the thread blocked on a two-question
// request and returns ATC's record with the request on it.
func (ts *testServer) asking(t *testing.T, sequence uint64) api.Thread {
	t.Helper()
	ts.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.UserInputRequested("a1", "req-1",
			t3codetest.Question("color", "Color", "Which color?", t3codetest.QuestionOption("Red", "warm"), t3codetest.QuestionOption("Blue", "cool"), t3codetest.AllowCustom(false)),
			t3codetest.Question("tools", "Tools", "Which tools?", t3codetest.QuestionOption("go", "Go"), t3codetest.QuestionOption("make", "Make"), t3codetest.MultiSelect()))))
	thread := ts.t3Thread(t, sequence, "t1", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(false, true))
	waitForCLI(t, "the request", func() bool { thread, _ = ts.threads.Get(thread.ID); return len(thread.InputRequests) == 1 })
	return thread
}

// The answer path (ATC-308): get shows the request with its questions,
// choices, and allowances; answer builds the set from question=value
// pairs — several values for a multi-select question, a value that is
// no choice as custom text — sends it once, and exits only when T3's
// evidence resolves the request, never on the command's acceptance and
// never on the agent's reply; send gained no question prompting.
func TestThreadAnswerCLI(t *testing.T) {
	ts, _ := connectedT3(t)
	thread := ts.asking(t, 3)
	request := thread.InputRequests[0]
	stdout, _, err := runCLI(t, "thread", "get", thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range [][]string{{"status", "waiting_for_input"}, {"input request", request.ID}, {"question", "q1  Color: Which color?"}, {"choice", "Red — warm"}, {"question", "q2  Tools: Which tools?"}, {"choice", "go — Go"}, {"allows", "custom text, several choices"}} {
		if !hasRow(stdout, row...) {
			t.Errorf("get output lacks row %q:\n%s", row, stdout)
		}
	}
	// A wrong pair is refused locally; a set the request refuses, by the
	// server, before anything reaches T3.
	if _, _, err := runCLI(t, "thread", "answer", thread.ID, request.ID, "--answer", "q1"); err == nil || !strings.Contains(err.Error(), "question=value") {
		t.Errorf("malformed pair = %v", err)
	}
	if _, _, err := runCLI(t, "thread", "answer", thread.ID, request.ID, "--answer", "q1=Green", "--answer", "q2=go"); err == nil || !strings.Contains(err.Error(), "does not allow a custom text") {
		t.Errorf("custom text where refused = %v", err)
	}
	if len(ts.t3Server.Commands()) != 0 {
		t.Fatalf("refusals reached T3: %v", ts.t3Server.Commands())
	}

	cli := startCLI(context.Background(), "", "thread", "answer", thread.ID, request.ID, "--answer", "q2=go", "--answer", "q1=Blue", "--answer", "q2=make")
	waitForCLI(t, "the command to reach T3", func() bool { return len(ts.t3Server.Commands()) == 1 })
	command := ts.t3Server.Commands()[0]
	answers := command["answers"].(map[string]any)
	if command["type"] != "thread.user-input.respond" || answers["color"] != "Blue" || len(answers["tools"].([]any)) != 2 {
		t.Errorf("command = %v", command)
	}
	waitForCLI(t, "the wait to start", func() bool { return strings.Contains(cli.stderr.String(), "waiting for the provider") })
	// The agent replies without resolving the request: still waiting.
	ts.t3Server.Push(t3codetest.Upserted(4, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(false, true))))
	time.Sleep(50 * time.Millisecond)
	select {
	case <-cli.done:
		t.Fatalf("answer exited on acceptance alone: %q %q", cli.stdout.String(), cli.stderr.String())
	default:
	}
	// T3 resolves the request with these answers.
	ts.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.UserInputRequested("a1", "req-1", t3codetest.Question("color", "Color", "Which color?", t3codetest.QuestionOption("Blue", "")), t3codetest.Question("tools", "Tools", "Which tools?", t3codetest.QuestionOption("go", ""), t3codetest.MultiSelect())),
		t3codetest.ActivityAt(t3codetest.UserInputResolved("b2", "req-1", map[string]any{"color": "Blue", "tools": []any{"go", "make"}}), command["createdAt"].(string))))
	ts.t3Server.Push(t3codetest.Upserted(5, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))))
	stdout, _, err = cli.wait(t)
	if err != nil {
		t.Fatalf("answer = %v; stdout %q", err, stdout)
	}
	for _, row := range [][]string{{"status", "resolved"}, {"resolution", "answer"}, {"answer", "resolved (accepted)"}, {"q1", "Blue"}, {"q2", "go, make"}} {
		if !hasRow(stdout, row...) {
			t.Errorf("answer output lacks row %q:\n%s", row, stdout)
		}
	}
	if len(ts.t3Server.Commands()) != 1 {
		t.Errorf("T3 received %d commands; want 1", len(ts.t3Server.Commands()))
	}
	if _, _, err := runCLI(t, "thread", "answer", thread.ID, request.ID, "--answer", "q1=Blue", "--answer", "q2=go"); err == nil || !strings.Contains(err.Error(), "resolved") {
		t.Errorf("answer on a resolved request = %v", err)
	}
	// send has no question flow: with another question pending, a message
	// reaches T3 as a plain message, never as an answer.
	ts.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.UserInputRequested("a3", "req-2", t3codetest.Question("more", "More", "More?", t3codetest.QuestionOption("Yes", ""), t3codetest.QuestionOption("No", "")))))
	ts.t3Server.Push(t3codetest.Upserted(6, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(false, true))))
	waitForCLI(t, "the next request", func() bool { got, _ := ts.threads.Get(thread.ID); return len(got.InputRequests) == 1 })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sending := startCLI(ctx, "", "thread", "send", thread.ID, "the second one")
	waitForCLI(t, "the message to reach T3", func() bool { return len(ts.t3Server.Commands()) == 2 })
	if last := ts.t3Server.Commands()[1]; last["type"] != "thread.turn.start" || last["message"].(map[string]any)["text"] != "the second one" {
		t.Errorf("send during a question = %v", last)
	}
	cancel()
	_, _, _ = sending.wait(t)
}

// A reply (ATC-309) goes to T3 as the first question's custom answer,
// verbatim; --reply and --answer are exclusive, and one is required.
func TestThreadAnswerCLIReply(t *testing.T) {
	ts, _ := connectedT3(t)
	thread := ts.asking(t, 3)
	request := thread.InputRequests[0]
	if _, _, err := runCLI(t, "thread", "answer", thread.ID, request.ID, "--reply", "Blue", "--answer", "q2=go"); err == nil || !strings.Contains(err.Error(), "cannot be combined") {
		t.Errorf("reply with answers = %v", err)
	}
	if _, _, err := runCLI(t, "thread", "answer", thread.ID, request.ID); err == nil || !strings.Contains(err.Error(), "an answer is required") {
		t.Errorf("no answer = %v", err)
	}
	cli := startCLI(context.Background(), "", "thread", "answer", thread.ID, request.ID, "--reply", "Blue, and skip the tools for now")
	waitForCLI(t, "the command to reach T3", func() bool { return len(ts.t3Server.Commands()) == 1 })
	command := ts.t3Server.Commands()[0]
	if answers := command["answers"].(map[string]any); command["type"] != "thread.user-input.respond" || answers["color"] != "Blue, and skip the tools for now" || len(answers) != 1 {
		t.Errorf("command = %v", command)
	}
	ts.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.UserInputRequested("a1", "req-1", t3codetest.Question("color", "Color", "Which color?", t3codetest.QuestionOption("Blue", "")), t3codetest.Question("tools", "Tools", "Which tools?", t3codetest.QuestionOption("go", ""), t3codetest.MultiSelect())),
		t3codetest.ActivityAt(t3codetest.UserInputResolved("b2", "req-1", map[string]any{"color": "Blue, and skip the tools for now"}), command["createdAt"].(string))))
	ts.t3Server.Push(t3codetest.Upserted(4, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))))
	stdout, _, err := cli.wait(t)
	if err != nil {
		t.Fatalf("answer = %v; stdout %q", err, stdout)
	}
	for _, row := range [][]string{{"status", "resolved"}, {"resolution", "answer"}, {"reply", `"Blue, and skip the tools for now"`}} {
		if !hasRow(stdout, row...) {
			t.Errorf("answer output lacks row %q:\n%s", row, stdout)
		}
	}
}

// Answer outcomes short of resolution (ATC-308): a failure T3 reports
// exits non-zero naming it; Ctrl-C ends the wait only — the answer
// stays sent and nothing more reaches T3.
func TestThreadAnswerCLIOutcomes(t *testing.T) {
	ts, _ := connectedT3(t)
	thread := ts.asking(t, 3)
	request := thread.InputRequests[0]
	ctx, cancel := context.WithCancel(context.Background())
	cli := startCLI(ctx, "", "thread", "answer", thread.ID, request.ID, "--answer", "q1=Red", "--answer", "q2=make")
	waitForCLI(t, "the command to reach T3", func() bool { return len(ts.t3Server.Commands()) == 1 })
	waitForCLI(t, "the wait to start", func() bool { return strings.Contains(cli.stderr.String(), "waiting for the provider") })
	cancel()
	stdout, stderr, err := cli.wait(t)
	var exit *service.ExitError
	if !errors.As(err, &exit) || exit.Code != exitInterrupted || stdout != "" || !strings.Contains(stderr, "stays sent") {
		t.Errorf("interrupted = %q, %q, %v", stdout, stderr, err)
	}
	if got, _ := ts.threads.InputRequest(thread.ID, request.ID); got.Answer == nil || got.Answer.State != api.InputAnswerSent {
		t.Errorf("answer after Ctrl-C = %+v", got.Answer)
	}
	// The same answers again recover the answer and wait on; T3 then
	// reports the failure.
	cli = startCLI(context.Background(), "", "thread", "answer", thread.ID, request.ID, "--answer", "q1=Red", "--answer", "q2=make")
	waitForCLI(t, "the wait to start", func() bool { return strings.Contains(cli.stderr.String(), "waiting for the provider") })
	if len(ts.t3Server.Commands()) != 1 {
		t.Errorf("the retry sent T3 %d commands", len(ts.t3Server.Commands())-1)
	}
	created := ts.t3Server.Commands()[0]["createdAt"].(string)
	ts.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.UserInputRequested("a1", "req-1", t3codetest.Question("color", "Color", "Which color?", t3codetest.QuestionOption("Red", "")), t3codetest.Question("tools", "Tools", "Which tools?", t3codetest.QuestionOption("make", ""), t3codetest.MultiSelect())),
		t3codetest.ActivityAt(t3codetest.UserInputRespondFailed("a2", "req-1", "No active provider session is bound to this thread."), created)))
	stdout, _, err = cli.wait(t)
	if err == nil || !strings.Contains(err.Error(), "failed") || !strings.Contains(err.Error(), "No active provider session") || !hasRow(stdout, "status", "pending") {
		t.Errorf("failed answer = %q, %v", stdout, err)
	}
}

// The stop path (ATC-308): stop sends one thread.session.stop, waits
// for T3's evidence, and exits 0 reporting stopped; an idle thread
// reports finished at once with nothing sent; a refusal exits non-zero;
// Ctrl-C ends the wait only, the stop staying in force.
func TestThreadStopCLI(t *testing.T) {
	ts, thread := connectedT3(t)
	stdout, _, err := runCLI(t, "thread", "stop", thread.ID)
	if err != nil || !hasRow(stdout, "state", "finished") || len(ts.t3Server.Commands()) != 0 {
		t.Errorf("stop on idle = %q, %v, %d commands", stdout, err, len(ts.t3Server.Commands()))
	}
	running := ts.t3Thread(t, 3, "t1", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))
	ctx, cancel := context.WithCancel(context.Background())
	cli := startCLI(ctx, "", "thread", "stop", running.ID)
	waitForCLI(t, "the command to reach T3", func() bool { return len(ts.t3Server.Commands()) == 1 })
	waitForCLI(t, "the wait to start", func() bool { return strings.Contains(cli.stderr.String(), "waiting for the provider to confirm") })
	cancel()
	stdout, stderr, err := cli.wait(t)
	var exit *service.ExitError
	if !errors.As(err, &exit) || exit.Code != exitInterrupted || stdout != "" || !strings.Contains(stderr, "stays in force") {
		t.Errorf("interrupted = %q, %q, %v", stdout, stderr, err)
	}
	if got, _ := ts.threads.Get(running.ID); got.Stop == nil {
		t.Fatal("Ctrl-C lifted the stop")
	}
	if _, _, err := runCLI(t, "thread", "send", running.ID, "more"); err == nil || !strings.Contains(err.Error(), "stop is being confirmed") {
		t.Errorf("send while stopping = %v", err)
	}
	// Again: the same stop, nothing more sent, the wait resumed; T3's
	// evidence ends it.
	cli = startCLI(context.Background(), "", "thread", "stop", running.ID)
	waitForCLI(t, "the wait to resume", func() bool { return strings.Contains(cli.stderr.String(), "waiting for the provider to confirm") })
	commands := ts.t3Server.Commands()
	if len(commands) != 1 || commands[0]["type"] != "thread.session.stop" {
		t.Fatalf("commands = %v", commands)
	}
	created := commands[0]["createdAt"].(string)
	ts.t3Server.Push(t3codetest.Upserted(4, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("stopped", "codex"), t3codetest.SessionUpdatedAt(created),
		t3codetest.LatestTurn("pt-1", "interrupted", "2026-09-01T00:00:03Z", created))))
	stdout, _, err = cli.wait(t)
	if err != nil || !hasRow(stdout, "state", "stopped") || !strings.Contains(stdout, "interrupted") {
		t.Errorf("confirmed stop = %q, %v", stdout, err)
	}
	// Refused by T3.
	ts.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{Reject: "cannot stop"} })
	ts.t3Thread(t, 5, "t1", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-2", "running", "2026-09-01T00:00:05Z", nil))
	waitForCLI(t, "the new turn", func() bool {
		got, _ := ts.threads.Get(running.ID)
		return got.LatestTurn != nil && got.LatestTurn.State == api.TurnRunning
	})
	if _, _, err := runCLI(t, "thread", "stop", running.ID); err == nil || !strings.Contains(err.Error(), "cannot stop") {
		t.Errorf("refused stop = %v", err)
	}
}
