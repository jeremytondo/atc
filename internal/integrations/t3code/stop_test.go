package t3code

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
)

// A stop (ATC-308) is one thread.session.stop command with a command
// id from the stop id and the stop's own createdAt; T3's later report
// of the session stopped at that instant, its turn interrupted, is the
// evidence that confirms it through the domain, and a stopped session
// from before the stop is not. The dispatch outcomes by name.
func TestStopThreadOverT3(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.connect(workspace)
	ctx := context.Background()
	if _, err := f.service.PrepareStop(ctx, "t-unknown"); !errors.Is(err, integrations.ErrNotConnected) {
		t.Errorf("unknown thread = %v", err)
	}
	// A session stopped long ago: the thread is idle, and a stop on it
	// resolves without a command.
	f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("stopped", "codex"), t3codetest.SessionUpdatedAt("2026-09-01T00:00:00Z"),
		t3codetest.LatestTurn("tu-0", "completed", "2026-09-01T00:00:01Z", "2026-09-01T00:00:02Z"))))
	thread := f.waitStatus("t1", api.ThreadIdle)
	req, stop, err := f.threads.BeginStop(ctx, thread.ID, "")
	if err != nil || req.Dispatch || stop.State != api.StopFinished {
		t.Fatalf("stop on an idle thread = %+v, %+v, %v", req, stop, err)
	}
	// Running: the stop is recorded and dispatched; the old stopped
	// session re-reported proves nothing; the session stopped at the
	// stop's instant confirms it.
	f.server.Push(t3codetest.Upserted(3, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"),
		t3codetest.LatestTurn("tu-1", "running", "2026-09-01T00:00:03Z", nil))))
	f.waitStatus("t1", api.ThreadWorking)
	req, stop, err = f.threads.BeginStop(ctx, thread.ID, "")
	if err != nil || !req.Dispatch {
		t.Fatalf("BeginStop = %+v, %+v, %v", req, stop, err)
	}
	dispatch, err := f.service.PrepareStop(ctx, "t1")
	if err != nil {
		t.Fatal(err)
	}
	if err := dispatch(ctx, integrations.ThreadStop{Key: req.StopID, CreatedAt: req.CreatedAt}); err != nil {
		t.Fatal(err)
	}
	if err := dispatch(ctx, integrations.ThreadStop{Key: req.StopID, CreatedAt: req.CreatedAt}); err != nil {
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
	delete(got, "commandId")
	if diff := cmp.Diff(map[string]any{"type": "thread.session.stop", "threadId": "t1", "createdAt": timestamp(req.CreatedAt)}, got); diff != "" {
		t.Errorf("command (-want +got):\n%s", diff)
	}
	if _, err := f.threads.StopDelivered(ctx, thread.ID, stop.ID); err != nil {
		t.Fatal(err)
	}
	f.server.Push(t3codetest.Upserted(4, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("stopped", "codex"), t3codetest.SessionUpdatedAt("2026-09-01T00:00:00Z"),
		t3codetest.LatestTurn("tu-1", "running", "2026-09-01T00:00:03Z", nil))))
	f.waitStatus("t1", api.ThreadIdle)
	if got, _ := f.threads.Stop(thread.ID, stop.ID); got.State != api.StopStopping {
		t.Fatalf("an old stopped session confirmed the stop: %+v", got)
	}
	f.server.Push(t3codetest.Upserted(5, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("stopped", "codex"), t3codetest.SessionUpdatedAt(timestamp(req.CreatedAt)),
		t3codetest.LatestTurn("tu-1", "interrupted", "2026-09-01T00:00:03Z", timestamp(req.CreatedAt)))))
	waitFor(t, "the stop confirmed", func() bool { got, _ := f.threads.Stop(thread.ID, stop.ID); return got.State != api.StopStopping })
	confirmed, _ := f.threads.Stop(thread.ID, stop.ID)
	after, _ := f.threads.Get(thread.ID)
	if confirmed.State != api.StopStopped || after.Stop != nil || after.LatestTurn.State != api.TurnInterrupted || after.Status != api.ThreadIdle {
		t.Errorf("confirmed = %+v; thread %+v", confirmed, after)
	}

	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		return t3codetest.DispatchReply{Reject: "thread not found"}
	})
	if err := dispatch(ctx, integrations.ThreadStop{Key: "stop-x", CreatedAt: req.CreatedAt}); !errors.Is(err, integrations.ErrStopRejected) || !strings.Contains(err.Error(), "thread not found") {
		t.Errorf("rejected = %v", err)
	}
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { select {} })
	done := make(chan error, 1)
	go func() { done <- dispatch(ctx, integrations.ThreadStop{Key: "stop-y", CreatedAt: req.CreatedAt}) }()
	waitFor(t, "the command to reach T3", func() bool { return len(f.server.Commands()) == 4 })
	f.server.DropConns()
	if err := <-done; !errors.Is(err, integrations.ErrDeliveryUncertain) {
		t.Errorf("across a drop = %v", err)
	}
}
