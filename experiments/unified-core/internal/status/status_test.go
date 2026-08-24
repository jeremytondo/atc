package status

import (
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
)

func TestClaudePreservesSpecificQuestionAndTracksDescendants(t *testing.T) {
	registry := New(func() time.Time { return time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC) })
	thread := "thread-1"
	apply := func(payload string) domain.Activity {
		t.Helper()
		observation, ok := registry.Observe(thread, domain.AgentClaude, []byte(payload))
		if !ok {
			t.Fatalf("unrecognized: %s", payload)
		}
		return observation.Activity
	}
	if got := apply(`{"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion","prompt_id":"p1"}`); got != domain.ActivityNeedsInput {
		t.Fatalf("question = %s", got)
	}
	if got := apply(`{"hook_event_name":"Notification","notification_type":"permission_prompt","prompt_id":"p1"}`); got != domain.ActivityNeedsInput {
		t.Fatalf("generic permission replaced question: %s", got)
	}
	if got := apply(`{"hook_event_name":"Notification","notification_type":"idle_prompt","prompt_id":"p1"}`); got != domain.ActivityNeedsInput {
		t.Fatalf("idle reminder replaced question: %s", got)
	}
	if got := apply(`{"hook_event_name":"SubagentStart","agent_id":"child-1","prompt_id":"p2"}`); got != domain.ActivityWorking {
		t.Fatalf("child start = %s", got)
	}
	if got := apply(`{"hook_event_name":"Stop","prompt_id":"p2","background_tasks":[{"id":"child-1","status":"running"}]}`); got != domain.ActivityWorking {
		t.Fatalf("root idle with child active = %s", got)
	}
	if got := apply(`{"hook_event_name":"SubagentStop","agent_id":"child-1","prompt_id":"p2","background_tasks":[{"id":"child-1","status":"running"}]}`); got != domain.ActivityIdle {
		t.Fatalf("stopping child remained active = %s", got)
	}
}

func TestCodexExactRootAndDescendantAggregation(t *testing.T) {
	registry := New(nil)
	thread := "thread-1"
	apply := func(payload string) (domain.Activity, bool) {
		t.Helper()
		observation, ok := registry.Observe(thread, domain.AgentCodex, []byte(payload))
		return observation.Activity, ok
	}
	if activity, ok := apply(`{"method":"thread/started","params":{"thread":{"id":"root-1","status":{"type":"active"}}}}`); !ok || activity != domain.ActivityWorking {
		t.Fatalf("root start = %s, %v", activity, ok)
	}
	if _, ok := apply(`{"method":"thread/started","params":{"thread":{"id":"unrelated","status":{"type":"active"}}}}`); ok {
		t.Fatal("adopted unrelated root")
	}
	if activity, ok := apply(`{"method":"item/started","params":{"threadId":"root-1","item":{"type":"subAgentActivity","kind":"started","agentThreadId":"child-1"}}}`); !ok || activity != domain.ActivityWorking {
		t.Fatalf("child mapping = %s, %v", activity, ok)
	}
	if activity, ok := apply(`{"method":"thread/status/changed","params":{"threadId":"root-1","status":{"type":"idle"}}}`); !ok || activity != domain.ActivityWorking {
		t.Fatalf("root idle hid child = %s, %v", activity, ok)
	}
	if activity, ok := apply(`{"method":"thread/status/changed","params":{"threadId":"child-1","status":{"type":"active","activeFlags":["waitingOnApproval"]}}}`); !ok || activity != domain.ActivityNeedsInput {
		t.Fatalf("child approval = %s, %v", activity, ok)
	}
	if _, ok := apply(`{"method":"thread/status/changed","params":{"threadId":"other-child","status":{"type":"active"}}}`); ok {
		t.Fatal("accepted uncorrelated descendant")
	}
	if activity, ok := apply(`{"method":"thread/read","result":{"thread":{"id":"grandchild","parentThreadId":"child-1","status":{"type":"active"}}}}`); !ok || activity != domain.ActivityNeedsInput {
		t.Fatalf("exact read reconciliation = %s, %v", activity, ok)
	}
}

func TestActivityPrecedence(t *testing.T) {
	cases := []struct {
		activities []domain.Activity
		want       domain.Activity
	}{
		{[]domain.Activity{domain.ActivityIdle, domain.ActivityUnknown}, domain.ActivityUnknown},
		{[]domain.Activity{domain.ActivityUnknown, domain.ActivityWorking}, domain.ActivityWorking},
		{[]domain.Activity{domain.ActivityWorking, domain.ActivityNeedsInput}, domain.ActivityNeedsInput},
	}
	for _, test := range cases {
		if got := aggregate(test.activities); got != test.want {
			t.Fatalf("aggregate(%v) = %s, want %s", test.activities, got, test.want)
		}
	}
}
