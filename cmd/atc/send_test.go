package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/service"
)

// cliRun is one CLI invocation running in the background, for commands
// that wait.
type cliRun struct {
	stdout, stderr *syncBuffer
	done           chan error
}

func startCLI(ctx context.Context, input string, args ...string) *cliRun {
	r := &cliRun{stdout: &syncBuffer{}, stderr: &syncBuffer{}, done: make(chan error, 1)}
	go func() { r.done <- run(ctx, args, strings.NewReader(input), r.stdout, r.stderr) }()
	return r
}

// wait returns the invocation's outcome, failing the test if it never
// ends.
func (r *cliRun) wait(t *testing.T) (string, string, error) {
	t.Helper()
	select {
	case err := <-r.done:
		return r.stdout.String(), r.stderr.String(), err
	case <-time.After(10 * time.Second):
		t.Fatalf("the command never ended; stdout %q stderr %q", r.stdout.String(), r.stderr.String())
		return "", "", nil
	}
}

func waitForCLI(t *testing.T, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !condition() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// t3Thread has the fake environment report a thread at rest and returns
// ATC's record of it.
func (ts *testServer) t3Thread(t *testing.T, sequence uint64, t3ID string, opts ...t3codetest.ThreadOpt) api.Thread {
	t.Helper()
	opts = append([]t3codetest.ThreadOpt{t3codetest.WithSession("idle", "codex")}, opts...)
	ts.t3Server.Push(t3codetest.Upserted(sequence, t3codetest.ThreadItem(t3ID, "p1", "One", opts...)))
	var thread api.Thread
	waitForCLI(t, "T3's thread "+t3ID, func() bool {
		id, _, ok := ts.threads.LookupIdentity("t3code", t3ID)
		if !ok {
			return false
		}
		thread, _ = ts.threads.Get(id)
		return thread.LastEvidenceAt != nil
	})
	return thread
}

func connectedT3(t *testing.T) (*testServer, api.Thread) {
	t.Helper()
	ts := startTestServerFull(t)
	projectDir, err := paths.CanonicalDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	createProjectCLI(t, projectDir)
	ts.connectT3(t, projectDir)
	return ts, ts.t3Thread(t, 2, "t1")
}

// The acceptance path (ATC-307): send waits for the message's own turn
// and prints its reply on stdout, with the diagnostics on stderr; the
// message reached T3 as one command with the text untouched.
func TestThreadSendCLI(t *testing.T) {
	ts, thread := connectedT3(t)
	cli := startCLI(context.Background(), "", "thread", "send", thread.ID, "  run the tests ")
	waitForCLI(t, "the command to reach T3", func() bool { return len(ts.t3Server.Commands()) == 1 })
	if text := ts.t3Server.Commands()[0]["message"].(map[string]any)["text"]; text != "  run the tests " {
		t.Errorf("text sent = %v", text)
	}
	var pending api.Thread
	waitForCLI(t, "the pending turn", func() bool {
		pending, _ = ts.threads.Get(thread.ID)
		return pending.PendingTurn != nil
	})
	// T3 starts the turn and, later, ends it with its reply; a change on
	// another thread in between is not a reason to end the wait.
	ts.t3Server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"),
		t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil))))
	ts.t3Thread(t, 4, "t2")
	done := t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"),
		t3codetest.LatestTurn("pt-1", "completed", "2026-09-01T00:00:03Z", "2026-09-01T00:00:09Z"), t3codetest.AssistantMessage("m1"))
	ts.t3Server.SetThreadDetail("t1", t3codetest.ThreadDetailItem(done, t3codetest.MessageItem("m1", "assistant", "All **green**.\n\n- tests pass", "pt-1", false)))
	ts.t3Server.Push(t3codetest.Upserted(5, done))
	stdout, stderr, err := cli.wait(t)
	if err != nil {
		t.Fatalf("send: %v; stderr %s", err, stderr)
	}
	if stdout != "All **green**.\n\n- tests pass\n" {
		t.Errorf("stdout = %q", stdout)
	}
	if !strings.Contains(stderr, "waiting for the reply to turn "+pending.PendingTurn.ID) || !strings.Contains(stderr, "Ctrl-C") {
		t.Errorf("stderr = %q", stderr)
	}
	// The message comes from stdin when the argument is absent or "-".
	ts.t3Server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{Reject: "no"} })
	for _, args := range [][]string{{"thread", "send", thread.ID}, {"thread", "send", thread.ID, "-"}} {
		if _, _, err := runCLIInput(t, "From stdin\n", args...); err == nil || !strings.Contains(err.Error(), "T3 Code rejected the message: no") {
			t.Errorf("%v = %v; want the rejection", args, err)
		}
	}
	commands := ts.t3Server.Commands()
	if len(commands) != 3 || commands[1]["message"].(map[string]any)["text"] != "From stdin\n" {
		t.Errorf("commands = %v", commands)
	}
	if _, _, err := runCLIInput(t, " \n", "thread", "send", thread.ID); err == nil || !strings.Contains(err.Error(), "message is empty") {
		t.Errorf("empty stdin = %v", err)
	}
	if _, _, err := runCLI(t, "thread", "send", "thrd-zzzzz", "hi"); err == nil || !strings.Contains(err.Error(), "thread not found") {
		t.Errorf("unknown thread = %v", err)
	}
	if len(ts.t3Server.Commands()) != 3 {
		t.Error("a local refusal sent a command")
	}
}

// Every way the wait ends without a reply, by name: the turn failing,
// being replaced, and Ctrl-C — which leaves T3's work alone.
func TestThreadSendCLIOutcomes(t *testing.T) {
	ts, thread := connectedT3(t)
	// Failed: T3 accepts, then the session faults.
	cli := startCLI(context.Background(), "", "thread", "send", thread.ID, "go")
	waitForCLI(t, "the command to reach T3", func() bool { return len(ts.t3Server.Commands()) == 1 })
	ts.t3Server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("error", "codex"), t3codetest.LastError("Provider turn start failed: no session"))))
	stdout, _, err := cli.wait(t)
	if err == nil || !strings.Contains(err.Error(), "failed: Provider turn start failed: no session") || stdout != "" {
		t.Errorf("faulted = %q, %v", stdout, err)
	}
	// Replaced: T3 starts the turn, ends it with a reply, and another
	// turn takes its place before the reply is recovered.
	ts.t3Server.Push(t3codetest.Upserted(4, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"))))
	waitForCLI(t, "the thread to recover", func() bool { got, _ := ts.threads.Get(thread.ID); return got.Status == api.ThreadIdle })
	cli = startCLI(context.Background(), "", "thread", "send", thread.ID, "again")
	waitForCLI(t, "the second command", func() bool { return len(ts.t3Server.Commands()) == 2 })
	ts.t3Server.Push(t3codetest.Upserted(5, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-2", "running", "2026-09-01T00:00:03Z", nil))))
	waitForCLI(t, "the turn to bind", func() bool {
		got, _ := ts.threads.Get(thread.ID)
		return got.PendingTurn == nil && got.LatestTurn != nil && got.LatestTurn.State == api.TurnRunning
	})
	ts.t3Server.Push(t3codetest.Upserted(6, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-3", "running", "2026-09-01T00:00:04Z", nil))))
	stdout, _, err = cli.wait(t)
	if err == nil || !strings.Contains(err.Error(), "withdrawn or replaced") || stdout != "" {
		t.Errorf("replaced = %q, %v", stdout, err)
	}
	// Ctrl-C: the wait ends with the interrupt status, nothing is sent to
	// T3, and its work goes on.
	ts.t3Server.Push(t3codetest.Upserted(7, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"))))
	waitForCLI(t, "the thread to rest", func() bool { got, _ := ts.threads.Get(thread.ID); return got.Status == api.ThreadIdle })
	ctx, cancel := context.WithCancel(context.Background())
	cli = startCLI(ctx, "", "thread", "send", thread.ID, "once more")
	waitForCLI(t, "the third command", func() bool { return len(ts.t3Server.Commands()) == 3 })
	waitForCLI(t, "the wait to start", func() bool { return strings.Contains(cli.stderr.String(), "waiting for the reply") })
	cancel()
	stdout, stderr, err := cli.wait(t)
	var exit *service.ExitError
	if !errors.As(err, &exit) || exit.Code != exitInterrupted || stdout != "" || !strings.Contains(stderr, "stopped waiting") {
		t.Errorf("interrupted = %q, %q, %v", stdout, stderr, err)
	}
	time.Sleep(20 * time.Millisecond)
	if commands := ts.t3Server.Commands(); len(commands) != 3 {
		t.Errorf("Ctrl-C sent T3 %d more commands", len(commands)-3)
	}
	if got, _ := ts.threads.Get(thread.ID); got.PendingTurn == nil {
		t.Error("Ctrl-C withdrew the pending turn")
	}
}

// The wait's decisions, one reading at a time.
func TestEvaluateReply(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	thread := func(state api.TurnState, response string) api.Thread {
		return api.Thread{ID: "thrd-a", LatestTurn: &api.ThreadTurn{ID: "turn-a", State: state, Response: response}}
	}
	archived := func(thread api.Thread) api.Thread { thread.Archived = true; return thread }
	cases := map[string]struct {
		thread    api.Thread
		seen      time.Time
		wantReply string
		wantDone  bool
		wantErr   string
	}{
		"pending":              {api.Thread{PendingTurn: &api.PendingTurn{ID: "turn-a"}}, time.Time{}, "", false, ""},
		"pending, dropped":     {archived(api.Thread{PendingTurn: &api.PendingTurn{ID: "turn-a"}}), time.Time{}, "", true, "dropped the thread before starting"},
		"running":              {thread(api.TurnRunning, ""), time.Time{}, "", false, ""},
		"unknown":              {thread(api.TurnUnknown, ""), time.Time{}, "", false, ""},
		"running, dropped":     {archived(thread(api.TurnRunning, "")), time.Time{}, "", true, "dropped the thread before the turn ended"},
		"reply":                {thread(api.TurnCompleted, "done"), time.Time{}, "done", true, ""},
		"reply after archive":  {archived(thread(api.TurnCompleted, "done")), time.Time{}, "done", true, ""},
		"completed, no reply":  {thread(api.TurnCompleted, ""), time.Time{}, "", false, ""},
		"reply still missing":  {thread(api.TurnCompleted, ""), now.Add(-time.Minute), "", false, ""},
		"reply given up":       {thread(api.TurnCompleted, ""), now.Add(-3 * time.Minute), "", true, "could not be recovered"},
		"failed":               {api.Thread{LatestTurn: &api.ThreadTurn{ID: "turn-a", State: api.TurnFailed, Error: "boom"}}, time.Time{}, "", true, "failed: boom"},
		"failed, no detail":    {thread(api.TurnFailed, ""), time.Time{}, "", true, "no detail"},
		"interrupted":          {thread(api.TurnInterrupted, ""), time.Time{}, "", true, "interrupted"},
		"replaced":             {api.Thread{LatestTurn: &api.ThreadTurn{ID: "turn-b", State: api.TurnCompleted, Response: "other"}}, time.Time{}, "", true, "latest turn is turn-b"},
		"pending is another's": {api.Thread{PendingTurn: &api.PendingTurn{ID: "turn-b"}, LatestTurn: &api.ThreadTurn{ID: "turn-a", State: api.TurnCompleted, Response: "mine"}}, time.Time{}, "mine", true, ""},
		"no turn":              {api.Thread{}, time.Time{}, "", true, "no longer known"},
	}
	for name, tc := range cases {
		seen := tc.seen
		reply, done, err := evaluateReply(tc.thread, "turn-a", &seen, now)
		if reply != tc.wantReply || done != tc.wantDone || (err == nil) != (tc.wantErr == "") || err != nil && !strings.Contains(err.Error(), tc.wantErr) {
			t.Errorf("%s: = %q, %t, %v; want %q, %t, %q", name, reply, done, err, tc.wantReply, tc.wantDone, tc.wantErr)
		}
		if name == "completed, no reply" && !seen.Equal(now) {
			t.Errorf("%s: completion not marked as seen", name)
		}
	}
}

// approve, deny, and decide (ATC-307): the thread shows its pending
// requests with their ids and decisions, a decision resolves one through
// T3, and the refusals surface as the CLI's error.
func TestThreadApproveDenyCLI(t *testing.T) {
	ts, _ := connectedT3(t)
	ts.t3Server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.ApprovalRequested("a1", "req-1", "command_execution_approval", "make test", t3codetest.ApprovalOption("accept", "Approve"), t3codetest.ApprovalOption("acceptAlways", "Always"), t3codetest.ApprovalOption("decline", "Decline")),
		t3codetest.ApprovalRequested("a2", "req-2", "mcp_elicitation_approval", "Allow Safari?")))
	thread := ts.t3Thread(t, 3, "t1", t3codetest.WithSession("running", "codex"), t3codetest.LatestTurn("pt-1", "running", "2026-09-01T00:00:03Z", nil), t3codetest.Pending(true, false))
	waitForCLI(t, "the requests", func() bool { thread, _ = ts.threads.Get(thread.ID); return len(thread.Approvals) == 2 })
	first, second := thread.Approvals[0].ID, thread.Approvals[1].ID

	stdout, _, err := runCLI(t, "thread", "get", thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"waiting_for_permission", first, second, "command: Command approval requested", "make test",
		"approve (Approve), approve_always (Always), deny (Decline)", "app_access: App access approval requested", "approve (Approve), deny (Decline)"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("get output lacks %q:\n%s", want, stdout)
		}
	}
	stdout, _, err = runCLI(t, "thread", "decide", thread.ID, first, "approve_always")
	if err != nil {
		t.Fatalf("decide: %v", err)
	}
	for _, want := range []string{first, "resolved", "approve_always", "make test"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("approve output lacks %q:\n%s", want, stdout)
		}
	}
	if stdout, _, err = runCLI(t, "thread", "deny", thread.ID, second); err != nil || !strings.Contains(stdout, "deny") {
		t.Errorf("deny = %q, %v", stdout, err)
	}
	commands := ts.t3Server.Commands()
	if len(commands) != 2 || commands[0]["decision"] != "acceptAlways" || commands[0]["requestId"] != "req-1" || commands[1]["decision"] != "decline" || commands[1]["requestId"] != "req-2" {
		t.Errorf("commands = %v", commands)
	}
	if _, _, err := runCLI(t, "thread", "approve", thread.ID, first); err == nil || !strings.Contains(err.Error(), "resolved") {
		t.Errorf("approve of a resolved request = %v", err)
	}
	if _, _, err := runCLI(t, "thread", "deny", thread.ID, "aprv-zzzzzzzzzz"); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("deny of an unknown request = %v", err)
	}
	if _, _, err := runCLI(t, "thread", "decide", thread.ID, first, "maybe"); err == nil {
		t.Error("an unknown decision was accepted")
	}
	if got, _ := ts.threads.Get(thread.ID); len(got.Approvals) != 0 {
		t.Errorf("pending after decisions = %+v", got.Approvals)
	}
}

// The wait survives the feed dropping (ATC-307): it reconnects with the
// last event id, refetches, and still ends on its own turn's reply —
// never a fresh submission.
func TestWaitForReplyReconnects(t *testing.T) {
	var mu sync.Mutex
	streams, fetches := 0, 0
	thread := api.Thread{ID: "thrd-aaaaa", Status: api.ThreadWorking, LatestTurn: &api.ThreadTurn{ID: "turn-a", State: api.TurnRunning}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/v1/threads/thrd-aaaaa":
			fetches++
			if streams >= 2 {
				// The reply lands once the second stream is up.
				thread.Status = api.ThreadIdle
				thread.LatestTurn = &api.ThreadTurn{ID: "turn-a", State: api.TurnCompleted, Response: "reconnected reply"}
			}
			_ = json.NewEncoder(w).Encode(thread)
		case "/v1/events":
			streams++
			w.Header().Set("Content-Type", "text/event-stream")
			_, _ = fmt.Fprint(w, ": connected\n\n")
			w.(http.Flusher).Flush()
			if streams == 1 {
				// The first connection drops right after opening, and the
				// cursor is empty since nothing was delivered.
				if r.Header.Get("Last-Event-ID") != "" {
					t.Errorf("first connection presented Last-Event-ID %q", r.Header.Get("Last-Event-ID"))
				}
				return
			}
			_, _ = fmt.Fprint(w, "event: thread.updated\nid: 3\ndata: {\"seq\":3,\"resource\":\"thread\",\"id\":\"thrd-aaaaa\"}\n\n")
			w.(http.Flusher).Flush()
			mu.Unlock()
			<-r.Context().Done()
			mu.Lock()
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	client := api.NewClient(srv.URL, "tok", "v1", nil, nil)
	var stderr strings.Builder
	reply, err := waitForReply(context.Background(), client, "thrd-aaaaa", "turn-a", &stderr)
	if err != nil || reply != "reconnected reply" {
		t.Fatalf("waitForReply = %q, %v; stderr %s", reply, err, stderr.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if streams != 2 || fetches < 2 {
		t.Errorf("streams = %d, fetches = %d; want a reconnect and a refetch", streams, fetches)
	}
}
