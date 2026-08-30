package codex

import (
	"testing"

	"github.com/jeremytondo/atc/internal/api"
)

// step is one payload applied to the tracker with the expected outcome.
type step struct {
	event  string
	turn   string
	status api.ThreadStatus
	signal bool
}

func runSteps(t *testing.T, tracker *tracker, steps []step) {
	t.Helper()
	for i, s := range steps {
		status, signal, _ := tracker.apply(payload{HookEventName: s.event, TurnID: s.turn})
		if status != s.status || signal != s.signal {
			t.Errorf("step %d %s: got (%s, %v), want (%s, %v)", i, s.event, status, signal, s.status, s.signal)
		}
	}
}

// The approval turn proven in ATC-281: UserPromptSubmit -> PreToolUse ->
// PermissionRequest -> PostToolUse -> Stop, one stable turn_id. The
// waiting_for_permission exit edge lags by the approved tool's runtime —
// contract, not a bug: PostToolUse is the first progress evidence.
func TestApprovalTurn(t *testing.T) {
	runSteps(t, newTracker(), []step{
		{"UserPromptSubmit", "t1", api.ThreadWorking, true},
		{"PreToolUse", "t1", api.ThreadWorking, true},
		{"PermissionRequest", "t1", api.ThreadWaitingForPermission, true},
		{"PostToolUse", "t1", api.ThreadWorking, true},
		{"Stop", "t1", api.ThreadIdle, true},
	})
}

// The interrupt turn proven in ATC-281: the interrupted foreground tool
// finished 18.9s after the conversational interrupt, and its late
// PostToolUse must not resurrect working.
func TestInterruptLatch(t *testing.T) {
	tr := newTracker()
	runSteps(t, tr, []step{
		{"UserPromptSubmit", "t1", api.ThreadWorking, true},
		{"PreToolUse", "t1", api.ThreadWorking, true},
		{"Interrupt", "t1", api.ThreadIdle, true},
		{"PostToolUse", "t1", api.ThreadIdle, false},
		{"PermissionRequest", "t1", api.ThreadIdle, false},
	})
	// The next prompt opens the next turn; its events pass the latch.
	runSteps(t, tr, []step{
		{"UserPromptSubmit", "t2", api.ThreadWorking, true},
		{"PreToolUse", "t2", api.ThreadWorking, true},
		// The old turn's straggler stays silenced even mid-new-turn.
		{"PostToolUse", "t1", api.ThreadWorking, false},
		{"Stop", "t2", api.ThreadIdle, true},
	})
}

// Unrecognized events are ignored without guessing — including the
// documented events ATC does not wire meaning to yet.
func TestUnrecognizedEventsCarryNoSignal(t *testing.T) {
	tr := newTracker()
	runSteps(t, tr, []step{
		{"UserPromptSubmit", "t1", api.ThreadWorking, true},
		{"SubagentStart", "", api.ThreadWorking, false},
		{"SubagentStop", "", api.ThreadWorking, false},
		{"PreCompact", "", api.ThreadWorking, false},
		{"SomethingNew", "", api.ThreadWorking, false},
	})
}

// A new prompt clears the previous turn's failure detail.
func TestUserPromptSubmitClearsLastError(t *testing.T) {
	tr := newTracker()
	_, _, lastError := tr.apply(payload{HookEventName: "UserPromptSubmit", TurnID: "t1"})
	if lastError == nil || *lastError != "" {
		t.Errorf("lastError = %v, want pointer to empty string", lastError)
	}
	_, _, lastError = tr.apply(payload{HookEventName: "Stop", TurnID: "t1"})
	if lastError != nil {
		t.Errorf("Stop lastError = %v, want nil", lastError)
	}
}

// A seeded tracker starts at unknown and stays honest until evidence.
func TestSeededTracker(t *testing.T) {
	tr := seededTracker()
	if tr.root != api.ThreadUnknown {
		t.Fatalf("seeded root = %s", tr.root)
	}
	runSteps(t, tr, []step{
		{"SomethingNew", "", api.ThreadUnknown, false},
		{"PostToolUse", "t1", api.ThreadWorking, true},
	})
}

// An Interrupt without a turn_id still lands idle but latches nothing:
// an empty latch must not match events that also lack a turn_id.
func TestInterruptWithoutTurnID(t *testing.T) {
	runSteps(t, newTracker(), []step{
		{"UserPromptSubmit", "", api.ThreadWorking, true},
		{"Interrupt", "", api.ThreadIdle, true},
		{"PostToolUse", "", api.ThreadWorking, true},
	})
}
