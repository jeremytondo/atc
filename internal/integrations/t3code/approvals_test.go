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

// The pending set derived from a thread's activities: requested minus
// resolved minus stale, in request order, kinds normalized, T3's options
// translated and the default pair offered when T3 names none.
func TestPendingApprovals(t *testing.T) {
	items := []map[string]any{
		t3codetest.ApprovalRequested("a1", "req-1", "command_execution_approval", "rm -rf build",
			t3codetest.ApprovalOption("decline", "Decline"), t3codetest.ApprovalOption("acceptAlways", "Always allow"), t3codetest.ApprovalOption("accept", "Approve"), t3codetest.ApprovalOption("later", "Unknown")),
		t3codetest.ApprovalRequested("a2", "req-2", "file_change_approval", "main.go"),
		t3codetest.ApprovalRequested("a3", "req-3", "mcp_elicitation_approval", "Allow ChatGPT to use Safari?"),
		t3codetest.ApprovalRequested("a4", "req-4", "auth_tokens_refresh", ""),
		t3codetest.ApprovalRequested("a5", "req-5", "command_execution_approval", "only novel choices", t3codetest.ApprovalOption("allowSandboxed", "Sandboxed")),
		t3codetest.ApprovalResolved("b5", "req-2", "accept"),
		t3codetest.ApprovalRespondFailed("a6", "req-3", "Stale pending approval request: req-3. Provider callback state does not survive app restarts."),
		t3codetest.ApprovalRespondFailed("a7", "req-1", "No active provider session is bound to this thread."),
		t3codetest.ApprovalRequested("a8", "req-1", "command_execution_approval", "duplicate report"),
		t3codetest.ActivityItem("a9", "runtime.error", map[string]any{"message": "x"}),
		t3codetest.ActivityItem("a0", "approval.requested", "not an object"),
	}
	data, _ := json.Marshal(items)
	var activities []approvalActivity
	if err := json.Unmarshal(data, &activities); err != nil {
		t.Fatal(err)
	}
	at := func(second int) time.Time { return time.Date(2026, 9, 1, 0, 0, second, 0, time.UTC) }
	want := []threads.ApprovalObservation{
		{RequestID: "req-1", Kind: api.ApprovalCommand, Summary: "Command approval requested", Detail: "rm -rf build", RequestedAt: at(1),
			Options: []api.ApprovalOption{{Decision: api.DecisionDeny, Label: "Decline"}, {Decision: api.DecisionApproveAlways, Label: "Always allow"}, {Decision: api.DecisionApprove, Label: "Approve"}}},
		{RequestID: "req-4", Kind: api.ApprovalUnknown, Summary: "Approval requested", RequestedAt: at(4),
			Options: []api.ApprovalOption{{Decision: api.DecisionApprove, Label: "Approve"}, {Decision: api.DecisionDeny, Label: "Decline"}}},
		// Only choices ATC has no word for: nothing is offered, and nothing
		// is invented.
		{RequestID: "req-5", Kind: api.ApprovalCommand, Summary: "Command approval requested", Detail: "only novel choices", RequestedAt: at(5), Options: []api.ApprovalOption{}},
	}
	if diff := cmp.Diff(want, pendingApprovals(activities)); diff != "" {
		t.Errorf("pending (-want +got):\n%s", diff)
	}
	app := pendingApprovals(activities[2:3])
	if len(app) != 1 || app[0].Kind != api.ApprovalAppAccess || app[0].Summary != "App access approval requested" {
		t.Errorf("app access request = %+v", app)
	}
}

// Pending approvals reach the thread (ATC-307): a thread reported with
// approvals pending is read from the detail snapshot and its requests
// appear on the Thread with stable ids; reported again with none, they
// resolve without a read; reports while a read is in flight coalesce
// into one more read; and a failed read changes nothing.
func TestApprovalsObserved(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.connect(workspace)
	blocked := func(sequence uint64, pending bool) map[string]any {
		return t3codetest.Upserted(sequence, t3codetest.ThreadItem("t1", "p1", "One", t3codetest.WithSession("running", "codex"),
			t3codetest.LatestTurn("tu-1", "running", "2026-09-01T00:00:02Z", nil), t3codetest.Pending(pending, false)))
	}
	f.server.Push(blocked(2, false))
	thread := f.waitStatus("t1", api.ThreadWorking)
	if f.server.DetailReads("t1") != 0 {
		t.Errorf("a thread without approvals was read %d times", f.server.DetailReads("t1"))
	}
	detail := t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One"))
	f.server.SetThreadDetail("t1", t3codetest.WithActivities(detail,
		t3codetest.ApprovalRequested("a1", "req-1", "command_execution_approval", "make test", t3codetest.ApprovalOption("accept", "Approve"), t3codetest.ApprovalOption("decline", "Decline"))))
	f.server.Push(blocked(3, true))
	var pending []api.ThreadApproval
	waitFor(t, "the request on the thread", func() bool {
		thread, _ = f.threads.Get(thread.ID)
		pending = thread.Approvals
		return len(pending) == 1
	})
	if thread.Status != api.ThreadWaitingForPermission || pending[0].Kind != api.ApprovalCommand || pending[0].Detail != "make test" || !strings.HasPrefix(pending[0].ID, "aprv-") {
		t.Errorf("blocked thread = %s %+v", thread.Status, pending)
	}
	if !strings.Contains(strings.Join(f.events(), " "), "thread.updated "+thread.ID) {
		t.Error("no thread.updated for the request")
	}
	// Resolved in T3: no read, the request gone.
	f.server.Push(blocked(4, false))
	waitFor(t, "the request resolved", func() bool {
		thread, _ = f.threads.Get(thread.ID)
		return len(thread.Approvals) == 0
	})
	if f.server.DetailReads("t1") != 1 {
		t.Errorf("reads = %d; want the one for the pending report", f.server.DetailReads("t1"))
	}
	if _, err := f.threads.BeginDecision(thread.ID, pending[0].ID, api.DecisionApprove); !errors.Is(err, threads.ErrApprovalResolved) {
		t.Errorf("decision on the request resolved in T3 = %v", err)
	}
	// Reported pending again: the same request has the same id; the
	// thread is reported thrice in a row while the read is slow, and one
	// more read follows the first.
	f.server.SetThreadDetail("t1", t3codetest.WithActivities(t3codetest.ThreadDetailItem(t3codetest.ThreadItem("t1", "p1", "One")),
		t3codetest.ApprovalRequested("a1", "req-1", "command_execution_approval", "make test"),
		t3codetest.ApprovalRequested("a2", "req-2", "file_change_approval", "main.go")))
	f.server.SetDetailDelay(150 * time.Millisecond)
	f.server.Push(blocked(5, true), blocked(6, true), blocked(7, true))
	waitFor(t, "the second request", func() bool {
		thread, _ = f.threads.Get(thread.ID)
		return len(thread.Approvals) == 1 && thread.Approvals[0].Kind == api.ApprovalFileChange
	})
	waitFor(t, "the reads to settle", func() bool { return f.server.DetailReads("t1") >= 3 })
	time.Sleep(50 * time.Millisecond)
	if reads := f.server.DetailReads("t1"); reads != 3 {
		t.Errorf("reads after three reports = %d; want one in flight plus one queued", reads)
	}
	if thread.Approvals[0].ID == pending[0].ID {
		t.Error("the second request took the first's id")
	}
	// A read that fails leaves what ATC holds standing.
	f.server.SetDetailDelay(0)
	f.server.SetThreadDetail("t1", nil)
	f.server.Push(blocked(8, true))
	waitFor(t, "the failed read", func() bool { return f.server.DetailReads("t1") == 4 })
	time.Sleep(20 * time.Millisecond)
	if thread, _ = f.threads.Get(thread.ID); len(thread.Approvals) != 1 {
		t.Errorf("after a failed read = %+v", thread.Approvals)
	}
}

// A decision (ATC-307) is one thread.approval.respond command in T3's
// vocabulary, with a command id from the key — the same decision again
// is the same command — and every outcome by name.
func TestDecideApproval(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.start()
	ctx := context.Background()
	req := integrations.ApprovalDecision{ProviderID: "t1", RequestID: "req-1", Decision: api.DecisionApproveForSession, Key: "aprv-x/approve_for_session"}
	if err := f.service.DecideApproval(ctx, req); !errors.Is(err, integrations.ErrNotConnected) {
		t.Errorf("not running = %v", err)
	}
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, nil), t3codetest.SynchronizedItem()}
	})
	f.waitState(api.IntegrationConnected)
	if err := f.service.DecideApproval(ctx, req); err != nil {
		t.Fatal(err)
	}
	if err := f.service.DecideApproval(ctx, req); err != nil {
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
	if at, _ := got["createdAt"].(string); !strings.HasSuffix(at, "Z") {
		t.Errorf("createdAt = %v", got["createdAt"])
	}
	delete(got, "commandId")
	delete(got, "createdAt")
	want := map[string]any{"type": "thread.approval.respond", "threadId": "t1", "requestId": "req-1", "decision": "acceptForSession"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("command (-want +got):\n%s", diff)
	}
	for decision, theirs := range map[api.ApprovalDecision]string{api.DecisionApprove: "accept", api.DecisionApproveAlways: "acceptAlways", api.DecisionDeny: "decline", api.DecisionCancel: "cancel"} {
		other := req
		other.Decision, other.Key = decision, "aprv-x/"+string(decision)
		if err := f.service.DecideApproval(ctx, other); err != nil {
			t.Fatal(err)
		}
		commands = f.server.Commands()
		if last := commands[len(commands)-1]; last["decision"] != theirs {
			t.Errorf("%s sent as %v; want %s", decision, last["decision"], theirs)
		}
	}
	if err := f.service.DecideApproval(ctx, integrations.ApprovalDecision{ProviderID: "t1", RequestID: "req-1", Decision: "maybe", Key: "k"}); !errors.Is(err, integrations.ErrDecisionRejected) {
		t.Errorf("unknown decision = %v", err)
	}

	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		return t3codetest.DispatchReply{Reject: "thread not found"}
	})
	if err := f.service.DecideApproval(ctx, req); !errors.Is(err, integrations.ErrDecisionRejected) || !strings.Contains(err.Error(), "thread not found") {
		t.Errorf("rejected = %v", err)
	}
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { select {} })
	done := make(chan error, 1)
	go func() { done <- f.service.DecideApproval(ctx, req) }()
	waitFor(t, "the command to reach T3", func() bool { return len(f.server.Commands()) == 8 })
	f.server.DropConns()
	if err := <-done; !errors.Is(err, integrations.ErrDeliveryUncertain) {
		t.Errorf("across a drop = %v", err)
	}
}
