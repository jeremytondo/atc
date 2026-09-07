package t3code

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
)

// The command a message becomes (ATC-307): thread.turn.start on the
// existing thread with its own modes, no model selection, no bootstrap,
// and identities derived from the ATC message — the same message
// dispatched again is byte for byte the same command.
func TestSendMessageCommand(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.connect(workspace)
	ctx := context.Background()
	f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"),
		t3codetest.RuntimeMode("approval-required"), t3codetest.InteractionMode("plan"))))
	f.waitStatus("t1", api.ThreadIdle)

	prepared, err := f.service.PrepareMessage(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.Steers {
		t.Error("a Codex thread steers; want the next turn")
	}
	if commands := f.server.Commands(); len(commands) != 0 {
		t.Fatalf("preparing sent %d commands", len(commands))
	}
	message := integrations.ThreadMessage{ID: "msg-aaaaaaaaaa", Text: "  and the tests ", CreatedAt: time.Date(2026, 9, 6, 10, 0, 0, 500_000_000, time.UTC)}
	if err := prepared.Dispatch(ctx, message); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Dispatch(ctx, message); err != nil {
		t.Fatal(err)
	}
	commands := f.server.Commands()
	if len(commands) != 2 {
		t.Fatalf("commands sent = %d; want the message twice", len(commands))
	}
	got := commands[0]
	commandID, _ := got["commandId"].(string)
	messageID, _ := got["message"].(map[string]any)["messageId"].(string)
	if !uuidPattern.MatchString(commandID) || !uuidPattern.MatchString(messageID) || commandID == messageID {
		t.Errorf("ids = %q, %q", commandID, messageID)
	}
	if diff := cmp.Diff(commands[0], commands[1]); diff != "" {
		t.Errorf("the same message twice differs (-first +second):\n%s", diff)
	}
	delete(got, "commandId")
	delete(got["message"].(map[string]any), "messageId")
	want := map[string]any{
		"type":            "thread.turn.start",
		"threadId":        "t1",
		"message":         map[string]any{"role": "user", "text": "  and the tests ", "attachments": []any{}},
		"runtimeMode":     "approval-required",
		"interactionMode": "plan",
		"createdAt":       "2026-09-06T10:00:00.500Z",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("command (-want +got):\n%s", diff)
	}
	// Another message has other ids; a thread on T3's default interaction
	// mode (omitted in the shell) sends "default".
	f.server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t2", "p1", "Two", t3codetest.WithSession("running", "claudeAgent"))))
	f.waitStatus("t2", api.ThreadWorking)
	if prepared, err = f.service.PrepareMessage(ctx, "t2"); err != nil {
		t.Fatal(err)
	}
	if !prepared.Steers {
		t.Error("a Claude Code thread does not steer; want the running turn continued")
	}
	if err := prepared.Dispatch(ctx, integrations.ThreadMessage{ID: "msg-bbbbbbbbbb", Text: "x", CreatedAt: message.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	third := f.server.Commands()[2]
	if third["commandId"] == commandID || third["interactionMode"] != "default" || third["runtimeMode"] != "full-access" {
		t.Errorf("second message's command = %v", third)
	}
}

// Every refusal and outcome, by name: not connected before and after
// preparation, a thread T3 no longer reports, T3's typed rejection
// (final — and final again on a retry, from T3's receipt), and a lost
// answer, which is uncertain and reconciles on the same command.
func TestSendMessageOutcomes(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.start()
	ctx := context.Background()
	if _, err := f.service.PrepareMessage(ctx, "t1"); !errors.Is(err, integrations.ErrNotConnected) || !strings.Contains(err.Error(), "T3 Code is unavailable") {
		t.Errorf("not running = %v", err)
	}
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, []any{
			t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex")),
		}), t3codetest.SynchronizedItem()}
	})
	f.waitState(api.IntegrationConnected)
	f.waitStatus("t1", api.ThreadIdle)
	if _, err := f.service.PrepareMessage(ctx, "t-gone"); !errors.Is(err, integrations.ErrMessageRejected) || !strings.Contains(err.Error(), "no longer reports") {
		t.Errorf("unreported thread = %v", err)
	}

	message := integrations.ThreadMessage{ID: "msg-aaaaaaaaaa", Text: "go", CreatedAt: time.Now()}
	rejected := 0
	f.server.SetDispatch(func(command map[string]any) t3codetest.DispatchReply {
		rejected++
		return t3codetest.DispatchReply{Reject: "thread has no provider session"}
	})
	prepared, err := f.service.PrepareMessage(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		err := prepared.Dispatch(ctx, message)
		if !errors.Is(err, integrations.ErrMessageRejected) || !strings.Contains(err.Error(), "thread has no provider session") {
			t.Errorf("rejected dispatch = %v", err)
		}
	}
	if rejected != 2 {
		t.Errorf("T3 answered %d rejections; want the retry presented again", rejected)
	}
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{Denied: true} })
	if err := prepared.Dispatch(ctx, message); !errors.Is(err, integrations.ErrMessageRejected) || !strings.Contains(err.Error(), "missing scope") {
		t.Errorf("denied dispatch = %v", err)
	}

	// T3 never answers: the socket drops mid-command. Uncertain, and the
	// same message on the new connection presents the same command.
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { select {} })
	done := make(chan error, 1)
	go func() { done <- prepared.Dispatch(ctx, message) }()
	waitFor(t, "the command to reach T3", func() bool { return len(f.server.Commands()) == 4 })
	f.server.DropConns()
	if err := <-done; !errors.Is(err, integrations.ErrDeliveryUncertain) || !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("dispatch across a drop = %v; want ErrDeliveryUncertain", err)
	}
	// The old connection is gone: dispatching on the stale preparation
	// fails at once, still uncertain, and the retry prepares afresh.
	if err := prepared.Dispatch(ctx, message); !errors.Is(err, integrations.ErrDeliveryUncertain) {
		t.Errorf("dispatch on the dropped connection = %v", err)
	}
	waitFor(t, "resubscription", func() bool { return len(f.server.Subscriptions()) == 2 })
	f.waitState(api.IntegrationConnected)
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{} })
	if prepared, err = f.service.PrepareMessage(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
	if err := prepared.Dispatch(ctx, message); err != nil {
		t.Errorf("retry on the new connection = %v", err)
	}
	commands := f.server.Commands()
	if diff := cmp.Diff(commands[3]["commandId"], commands[len(commands)-1]["commandId"]); diff != "" {
		t.Errorf("retry presented another command id:\n%s", diff)
	}
	// A caller that gives up is the uncertain outcome too.
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { select {} })
	cancelled, cancel := context.WithCancel(ctx)
	go func() { done <- prepared.Dispatch(cancelled, message) }()
	waitFor(t, "the last command to reach T3", func() bool { return len(f.server.Commands()) == len(commands)+1 })
	cancel()
	if err := <-done; !errors.Is(err, integrations.ErrDeliveryUncertain) {
		t.Errorf("abandoned dispatch = %v", err)
	}
}

// T3 accepts a message and then fails to start the turn: the failure
// arrives through the session's error, which the domain turns into the
// pending turn failing with T3's detail — never a reply.
func TestSendMessageAcceptedThenFailed(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.connect(workspace)
	ctx := context.Background()
	f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("idle", "codex"))))
	thread := f.waitStatus("t1", api.ThreadIdle)
	turnID, err := f.threads.SubmitTurn(ctx, thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := f.service.PrepareMessage(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Dispatch(ctx, integrations.ThreadMessage{ID: "msg-aaaaaaaaaa", Text: "go", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("error", "codex"), t3codetest.LastError("Provider turn start failed: no session"))))
	failed := f.waitStatus("t1", api.ThreadError)
	if failed.PendingTurn != nil || failed.LatestTurn == nil || failed.LatestTurn.ID != turnID || failed.LatestTurn.State != api.TurnFailed || failed.LatestTurn.Error != "Provider turn start failed: no session" {
		t.Errorf("after T3's asynchronous failure = pending %+v latest %+v", failed.PendingTurn, failed.LatestTurn)
	}
}
