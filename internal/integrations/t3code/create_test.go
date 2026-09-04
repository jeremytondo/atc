package t3code

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/integrations/t3code/t3codetest"
)

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

// connect brings the fixture up connected with one T3 project rooted at
// workspace.
func (f *fixture) connect(workspace string) {
	f.t.Helper()
	f.writeRuntime(f.server.Origin())
	f.server.SetInitial(func(*uint64) []any {
		return []any{t3codetest.SnapshotItem(1, []any{t3codetest.ProjectItem("p1", "T3", workspace)}, nil), t3codetest.SynchronizedItem()}
	})
	f.start()
	f.waitState(api.IntegrationConnected)
}

// The command ATC sends: the ATC-owned defaults, execution at the
// project root, the same model selection in both places, options passed
// through verbatim, and ids ATC chose.
func TestCreateThreadCommand(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.connect(workspace)
	ctx := context.Background()

	prompt := "  Fix the   build\n and run the tests "
	prepared, err := f.service.PrepareThread(ctx, integrations.ThreadCreation{
		AgentID: "codex", Directory: workspace, Prompt: prompt, Model: "gpt-5.6-sol",
		Options: []api.ThreadOption{{ID: "reasoningEffort", Value: "high"}, {ID: "verbosity", Value: "low"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !uuidPattern.MatchString(prepared.ProviderID) || prepared.Title != "Fix the build and run the tests" {
		t.Fatalf("prepared = %+v", prepared)
	}
	if commands := f.server.Commands(); len(commands) != 0 {
		t.Fatalf("preparing sent %d commands", len(commands))
	}
	if err := prepared.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	commands := f.server.Commands()
	if len(commands) != 1 {
		t.Fatalf("commands sent = %d; want 1", len(commands))
	}
	got := commands[0]
	// The ids and timestamps ATC minted are checked by shape, then removed
	// so the rest diffs whole.
	message := got["message"].(map[string]any)
	create := got["bootstrap"].(map[string]any)["createThread"].(map[string]any)
	if id, _ := got["commandId"].(string); !uuidPattern.MatchString(id) {
		t.Errorf("commandId = %v", got["commandId"])
	}
	if id, _ := message["messageId"].(string); !uuidPattern.MatchString(id) {
		t.Errorf("messageId = %v", message["messageId"])
	}
	for _, at := range []any{got["createdAt"], create["createdAt"]} {
		if s, _ := at.(string); !strings.HasSuffix(s, "Z") {
			t.Errorf("createdAt = %v; want an ISO UTC timestamp", at)
		} else if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			t.Errorf("createdAt %q: %v", s, err)
		}
	}
	delete(got, "commandId")
	delete(got, "createdAt")
	delete(message, "messageId")
	delete(create, "createdAt")
	selection := map[string]any{
		"instanceId": "codex", "model": "gpt-5.6-sol",
		"options": []any{map[string]any{"id": "reasoningEffort", "value": "high"}, map[string]any{"id": "verbosity", "value": "low"}},
	}
	want := map[string]any{
		"type":            "thread.turn.start",
		"threadId":        prepared.ProviderID,
		"message":         map[string]any{"role": "user", "text": prompt, "attachments": []any{}},
		"modelSelection":  selection,
		"titleSeed":       "Fix the build and run the tests",
		"runtimeMode":     "auto",
		"interactionMode": "default",
		"bootstrap": map[string]any{"createThread": map[string]any{
			"projectId": "p1", "title": "Fix the build and run the tests", "modelSelection": selection,
			"runtimeMode": "auto", "interactionMode": "default", "branch": nil, "worktreePath": nil,
		}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("command (-want +got):\n%s", diff)
	}

	// Without options the selection omits them rather than sending an
	// empty list.
	prepared, err = f.service.PrepareThread(ctx, integrations.ThreadCreation{AgentID: "claudeAgent", Directory: workspace, Prompt: "hi", Model: "opus"})
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Dispatch(ctx); err != nil {
		t.Fatal(err)
	}
	second := f.server.Commands()[1]["modelSelection"]
	if diff := cmp.Diff(map[string]any{"instanceId": "claudeAgent", "model": "opus"}, second); diff != "" {
		t.Errorf("selection without options (-want +got):\n%s", diff)
	}
}

// Every refusal lands before a command is sent: each connection state
// other than connected, by name, and a directory T3 has no project for.
func TestCreateThreadRefusals(t *testing.T) {
	ctx := context.Background()
	states := []struct {
		name  string
		setup func(f *fixture)
		state api.IntegrationConnectionState
	}{
		{"unavailable", func(f *fixture) {}, api.IntegrationUnavailable},
		{"connecting", func(f *fixture) { f.writeRuntime("http://127.0.0.1:1") }, api.IntegrationConnecting},
		{"auth_failed", func(f *fixture) { f.writeRuntime(f.server.Origin()); f.server.SetScopeDenied(true) }, api.IntegrationAuthFailed},
	}
	for _, tc := range states {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			workspace := t.TempDir()
			tc.setup(f)
			f.start()
			f.waitState(tc.state)
			_, err := f.service.PrepareThread(ctx, integrations.ThreadCreation{AgentID: "codex", Directory: workspace, Prompt: "hi", Model: "m"})
			if !errors.Is(err, integrations.ErrNotConnected) || !strings.Contains(err.Error(), "T3 Code is "+tc.name+": ") {
				t.Errorf("prepare while %s = %v; want ErrNotConnected naming the state", tc.name, err)
			}
			if commands := f.server.Commands(); len(commands) != 0 {
				t.Errorf("commands sent = %d", len(commands))
			}
		})
	}

	t.Run("project not registered", func(t *testing.T) {
		f := newFixture(t)
		f.connect(t.TempDir())
		other := t.TempDir()
		_, err := f.service.PrepareThread(ctx, integrations.ThreadCreation{AgentID: "codex", Directory: other, Prompt: "hi", Model: "m"})
		if !errors.Is(err, integrations.ErrProjectNotRegistered) || !strings.Contains(err.Error(), other+" is not registered in T3 Code") {
			t.Errorf("prepare for an unregistered directory = %v", err)
		}
		if commands := f.server.Commands(); len(commands) != 0 {
			t.Errorf("commands sent = %d", len(commands))
		}
	})
}

// T3's answers to a dispatch: a typed rejection carries its message, a
// rolled-back bootstrap says so, and a session without the operate scope
// is refused inside the reply.
func TestCreateThreadRejected(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		reply t3codetest.DispatchReply
		want  string
	}{
		{"rejected", t3codetest.DispatchReply{Reject: "provider instance codex is not configured"}, "T3 Code rejected the command: provider instance codex is not configured"},
		{"rolled back", t3codetest.DispatchReply{Reject: "turn start failed", RolledBack: true}, "turn start failed; T3 Code rolled back the thread it had created"},
		{"denied", t3codetest.DispatchReply{Denied: true}, "T3 Code rejected the command: missing scope"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			workspace := t.TempDir()
			f.connect(workspace)
			f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return tc.reply })
			prepared, err := f.service.PrepareThread(ctx, integrations.ThreadCreation{AgentID: "codex", Directory: workspace, Prompt: "hi", Model: "m"})
			if err != nil {
				t.Fatal(err)
			}
			err = prepared.Dispatch(ctx)
			if !errors.Is(err, integrations.ErrThreadCreationFailed) || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("dispatch = %v; want ErrThreadCreationFailed with %q", err, tc.want)
			}
			// The connection is untouched by a rejection.
			if state := f.service.Connection().State; state != api.IntegrationConnected {
				t.Errorf("state after a rejection = %s", state)
			}
		})
	}
}

// One socket carries the subscription and the commands: shell events
// keep applying while a command is in flight, and a socket drop
// mid-command fails that command while the subscription reconnects as
// before.
func TestCommandsShareTheSubscriptionSocket(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.connect(workspace)
	ctx := context.Background()

	hold := make(chan struct{})
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply {
		<-hold
		return t3codetest.DispatchReply{}
	})
	prepared, err := f.service.PrepareThread(ctx, integrations.ThreadCreation{AgentID: "codex", Directory: workspace, Prompt: "hi", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- prepared.Dispatch(ctx) }()
	waitFor(t, "the command to reach T3", func() bool { return len(f.server.Commands()) == 1 })
	f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem("t2", "p1", "Two", t3codetest.WithSession("running", "codex"))))
	f.waitStatus("t2", api.ThreadWorking)
	select {
	case err := <-done:
		t.Fatalf("dispatch returned %v while T3 was still holding the command", err)
	default:
	}
	close(hold)
	if err := <-done; err != nil {
		t.Errorf("held dispatch = %v", err)
	}

	// A drop while a command is in flight fails it — nothing is queued
	// for the next connection — and the subscription comes back.
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { select {} })
	go func() { done <- prepared.Dispatch(ctx) }()
	waitFor(t, "the second command to reach T3", func() bool { return len(f.server.Commands()) == 2 })
	f.server.DropConns()
	if err := <-done; !errors.Is(err, integrations.ErrThreadCreationFailed) || !strings.Contains(err.Error(), "did not answer") {
		t.Errorf("dispatch across a drop = %v; want ErrThreadCreationFailed", err)
	}
	waitFor(t, "resubscription", func() bool { return len(f.server.Subscriptions()) == 2 })
	f.waitState(api.IntegrationConnected)
	if commands := f.server.Commands(); len(commands) != 2 {
		t.Errorf("commands after reconnect = %d; want the two sent, nothing replayed", len(commands))
	}
	if prepared, err = f.service.PrepareThread(ctx, integrations.ThreadCreation{AgentID: "codex", Directory: workspace, Prompt: "again", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { return t3codetest.DispatchReply{} })
	if err := prepared.Dispatch(ctx); err != nil {
		t.Errorf("dispatch on the new connection = %v", err)
	}
}

// A stored session granted only the read scope is retired and paired
// over once, before it is ever presented: no rejected ticket, one revoke,
// one pairing for the two-scope set.
func TestStaleScopeSessionRepairsOnce(t *testing.T) {
	f := newFixture(t)
	f.writeRuntime(f.server.Origin())
	if err := saveSession(f.sessionPath, &session{Origin: f.server.Origin(), Token: "stale", Label: "atc", SessionID: "sess-stale", Scope: "orchestration:read"}); err != nil {
		t.Fatal(err)
	}
	f.start()
	f.waitState(api.IntegrationConnected)
	if f.cli.Count("auth session revoke sess-stale") != 1 || f.cli.Count("auth pairing create") != 1 {
		t.Errorf("CLI calls = %v; want one revoke of the stale session and one pairing", f.cli.Calls())
	}
	if tickets, exchanges := f.server.Counts(); tickets != 1 || exchanges != 1 {
		t.Errorf("tickets, exchanges = %d, %d; want 1, 1 — the stale session is never presented", tickets, exchanges)
	}
	if stored := f.readSession(); stored.Token != "token-1" || stored.Scope != scope {
		t.Errorf("session after re-pair = %+v", stored)
	}
}

func TestThreadTitle(t *testing.T) {
	long := strings.Repeat("word ", 20)
	cases := map[string]string{
		"Fix the build":                 "Fix the build",
		"  Fix\tthe\n\n build  ":        "Fix the build",
		"":                              "New thread",
		"   \n\t ":                      "New thread",
		strings.TrimSpace(long):         "word word word word word word word word word word word word word word...",
		strings.Repeat("é", 80):         strings.Repeat("é", 69) + "...",
		strings.Repeat("x", titleLimit): strings.Repeat("x", titleLimit),
	}
	for prompt, want := range cases {
		if got := threadTitle(prompt); got != want {
			t.Errorf("threadTitle(%q) = %q; want %q", prompt, got, want)
		}
	}
	if !sameScopes("b a", "a b") || sameScopes("a", "a b") || sameScopes("", "a") {
		t.Error("sameScopes compares as sets")
	}
}

// An exit ATC cannot read ends the request waiting on it — never a hang —
// and then the connection, which comes back.
func TestMalformedExitFailsTheCommand(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.connect(workspace)
	ctx := context.Background()
	f.server.SetDispatch(func(map[string]any) t3codetest.DispatchReply { select {} })
	prepared, err := f.service.PrepareThread(ctx, integrations.ThreadCreation{AgentID: "codex", Directory: workspace, Prompt: "hi", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- prepared.Dispatch(ctx) }()
	waitFor(t, "the command to reach T3", func() bool { return len(f.server.Commands()) == 1 })
	// The subscription took request id 1; the command is 2.
	f.server.Raw(`{"_tag":"Exit","requestId":"2"}`)
	if err := <-done; !errors.Is(err, integrations.ErrThreadCreationFailed) || !strings.Contains(err.Error(), "exit omitted its exit") {
		t.Errorf("dispatch answered with a malformed exit = %v", err)
	}
	waitFor(t, "resubscription", func() bool { return len(f.server.Subscriptions()) == 2 })
	f.waitState(api.IntegrationConnected)
}

// A reply lost after T3 committed the thread is not a failed creation
// once T3's shell reports the thread: the outcome is what T3 reports.
func TestLostReplyAfterReportIsCreated(t *testing.T) {
	f := newFixture(t)
	workspace := t.TempDir()
	f.project(workspace, "mine")
	f.connect(workspace)
	ctx := context.Background()
	prepared, err := f.service.PrepareThread(ctx, integrations.ThreadCreation{AgentID: "codex", Directory: workspace, Prompt: "hi", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	// The fake's goroutine cannot fail the test itself: it polls with a
	// bound and the assertions follow the dispatch.
	f.server.SetDispatch(func(command map[string]any) t3codetest.DispatchReply {
		f.server.Push(t3codetest.Upserted(2, t3codetest.ThreadItem(prepared.ProviderID, "p1", "hi", t3codetest.WithSession("running", "codex"))))
		for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if _, _, known := f.threads.LookupIdentity(ID, prepared.ProviderID); known && f.thread(prepared.ProviderID).Status == api.ThreadWorking {
				break
			}
		}
		f.server.DropConns()
		select {}
	})
	if err := prepared.Dispatch(ctx); err != nil {
		t.Errorf("dispatch whose reply was lost after T3 reported the thread = %v; want created", err)
	}
	if thread := f.thread(prepared.ProviderID); thread.Status != api.ThreadWorking {
		t.Errorf("thread after the lost reply = %+v; want T3's report applied", thread)
	}
	waitFor(t, "resubscription", func() bool { return len(f.server.Subscriptions()) == 2 })
	f.waitState(api.IntegrationConnected)
}
