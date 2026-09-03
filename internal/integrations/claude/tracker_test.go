package claude

import (
	"encoding/json"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// event builds a payload the way the wire delivers it, so the tests read
// like recorded hook sequences.
func event(raw string) payload {
	var p payload
	if err := json.Unmarshal([]byte(raw), &p); err != nil {
		panic(err)
	}
	return p
}

// step applies one event and asserts the aggregate and signal.
func step(t *testing.T, tr *tracker, raw string, wantStatus api.ThreadStatus, wantSignal bool) {
	t.Helper()
	status, signal, _ := tr.apply(event(raw))
	if signal != wantSignal {
		t.Fatalf("%s: signal = %v, want %v", raw, signal, wantSignal)
	}
	if wantSignal && status != wantStatus {
		t.Fatalf("%s: status = %s, want %s", raw, status, wantStatus)
	}
}

const sess = `"session_id":"s1",`

func TestTrackerFullTurn(t *testing.T) {
	tr := newTracker()
	if got := tr.aggregate(); got != api.ThreadIdle {
		t.Fatalf("fresh session = %s, want idle", got)
	}
	step(t, tr, `{`+sess+`"hook_event_name":"UserPromptSubmit","prompt":"fix it"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"PreToolUse","tool_name":"Bash"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"PostToolUse","tool_name":"Bash"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"Stop","background_tasks":[]}`, api.ThreadIdle, true)
}

func TestTrackerPermissionAndQuestion(t *testing.T) {
	tr := newTracker()
	step(t, tr, `{`+sess+`"hook_event_name":"UserPromptSubmit"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"PermissionRequest","tool_name":"Bash"}`, api.ThreadWaitingForPermission, true)
	// AskUserQuestion rides the permission machinery but is a question.
	step(t, tr, `{`+sess+`"hook_event_name":"PermissionRequest","tool_name":"AskUserQuestion"}`, api.ThreadWaitingForInput, true)
	// Denial resolves the prompt; the turn keeps going.
	step(t, tr, `{`+sess+`"hook_event_name":"PermissionDenied"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"Notification","notification_type":"permission_prompt"}`, api.ThreadWaitingForPermission, true)
	// A question outranks a permission prompt — a pending question is
	// never papered over by later generic permission evidence, in either
	// arrival order.
	step(t, tr, `{`+sess+`"hook_event_name":"Notification","notification_type":"elicitation_dialog"}`, api.ThreadWaitingForInput, true)
	step(t, tr, `{`+sess+`"hook_event_name":"Notification","notification_type":"permission_prompt"}`, api.ThreadWaitingForInput, true)
	step(t, tr, `{`+sess+`"hook_event_name":"PermissionRequest","tool_name":"Bash"}`, api.ThreadWaitingForInput, true)
	step(t, tr, `{`+sess+`"hook_event_name":"UserPromptSubmit"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"Stop"}`, api.ThreadIdle, true)
}

// Root prompt, Stop, and StopFailure are the turn's start and its two
// observable ends; a failure carries whatever detail the payload
// supplies, and nothing invented when it supplies none. Descendant and
// in-turn events say nothing about the turn.
func TestTrackerTurnEvidence(t *testing.T) {
	tr := newTracker()
	turnOf := func(raw string) *threads.TurnObservation {
		t.Helper()
		_, _, turn := tr.apply(event(raw))
		return turn
	}
	if turn := turnOf(`{` + sess + `"hook_event_name":"UserPromptSubmit"}`); turn == nil || turn.State != api.TurnRunning || turn.ProviderID != "" {
		t.Fatalf("prompt turn = %+v; want running with no provider id", turn)
	}
	if turn := turnOf(`{` + sess + `"hook_event_name":"PreToolUse","tool_name":"Bash"}`); turn != nil {
		t.Errorf("tool use claimed a turn: %+v", turn)
	}
	if turn := turnOf(`{` + sess + `"hook_event_name":"UserPromptSubmit","agent_id":"a1"}`); turn != nil {
		t.Errorf("a descendant prompt claimed a turn: %+v", turn)
	}
	if turn := turnOf(`{` + sess + `"hook_event_name":"Stop"}`); turn == nil || turn.State != api.TurnCompleted {
		t.Fatalf("Stop turn = %+v; want completed", turn)
	}
	if turn := turnOf(`{` + sess + `"hook_event_name":"Stop","agent_id":"a1"}`); turn != nil {
		t.Errorf("a descendant Stop claimed a turn: %+v", turn)
	}
	step(t, tr, `{`+sess+`"hook_event_name":"SubagentStop","agent_id":"a1"}`, api.ThreadIdle, true)

	step(t, tr, `{`+sess+`"hook_event_name":"UserPromptSubmit"}`, api.ThreadWorking, true)
	status, signal, turn := tr.apply(event(`{` + sess + `"hook_event_name":"StopFailure","error":"server_error","error_details":"upstream 500"}`))
	if !signal || status != api.ThreadIdle {
		t.Fatalf("StopFailure: status=%s signal=%v; want idle signal", status, signal)
	}
	if turn == nil || turn.State != api.TurnFailed || turn.Error != "upstream 500" {
		t.Fatalf("StopFailure turn = %+v; want failed with the details", turn)
	}
	if turn := turnOf(`{` + sess + `"hook_event_name":"StopFailure","error":"rate_limit"}`); turn == nil || turn.Error != "rate_limit" {
		t.Errorf("StopFailure without details = %+v; want the error kind", turn)
	}
	if turn := turnOf(`{` + sess + `"hook_event_name":"StopFailure","error":{"kind":"odd"}}`); turn == nil || turn.Error != `{"kind":"odd"}` {
		t.Errorf("StopFailure with an unexpected error shape = %+v; want its JSON text", turn)
	}
	if turn := turnOf(`{` + sess + `"hook_event_name":"StopFailure"}`); turn == nil || turn.Error != "" {
		t.Errorf("StopFailure with nothing = %+v; want no invented detail", turn)
	}
}

// The recorded subagent shape (ATC-158 probe): the root turn completes
// while a background subagent runs, the SubagentStop snapshot still
// contains the stopping agent, and the wake turn's empty snapshot lands
// idle.
func TestTrackerSubagentActivity(t *testing.T) {
	tr := newTracker()
	step(t, tr, `{`+sess+`"hook_event_name":"UserPromptSubmit"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"SubagentStart","agent_id":"a1"}`, api.ThreadWorking, true)
	// Root Stop with a running subagent snapshot: not idle.
	step(t, tr, `{`+sess+`"hook_event_name":"Stop","background_tasks":[{"id":"a1","type":"subagent","status":"running"}]}`, api.ThreadWorking, true)
	// The stopping agent is still in its own stop snapshot; the
	// subtraction lands the transition.
	step(t, tr, `{`+sess+`"hook_event_name":"SubagentStop","agent_id":"a1","background_tasks":[{"id":"a1","type":"subagent","status":"running"}]}`, api.ThreadIdle, true)
}

// A descendant's events never mark the root working, and its prompts are
// tracked per agent id.
func TestTrackerDescendantEvents(t *testing.T) {
	tr := newTracker()
	step(t, tr, `{`+sess+`"hook_event_name":"Stop"}`, api.ThreadIdle, true)
	step(t, tr, `{`+sess+`"hook_event_name":"PreToolUse","agent_id":"a1"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"PermissionRequest","agent_id":"a1"}`, api.ThreadWaitingForPermission, true)
	// The descendant's snapshot retains the prompt through a coarse
	// running status.
	step(t, tr, `{`+sess+`"hook_event_name":"Stop","background_tasks":[{"id":"a1","type":"subagent","status":"running"}]}`, api.ThreadWaitingForPermission, true)
	step(t, tr, `{`+sess+`"hook_event_name":"PostToolUse","agent_id":"a1"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"SubagentStop","agent_id":"a1"}`, api.ThreadIdle, true)
}

// The interrupt gap (accepted limitation): no hook fires on user
// interrupt, so working stands until the idle notification or the next
// prompt resolves it.
func TestTrackerInterruptGap(t *testing.T) {
	tr := newTracker()
	step(t, tr, `{`+sess+`"hook_event_name":"UserPromptSubmit"}`, api.ThreadWorking, true)
	if got := tr.aggregate(); got != api.ThreadWorking {
		t.Fatalf("after interrupt (no hook): %s, want the stale working", got)
	}
	step(t, tr, `{`+sess+`"hook_event_name":"Notification","notification_type":"idle_prompt"}`, api.ThreadIdle, true)
}

func TestTrackerBackgroundShellAndCrons(t *testing.T) {
	tr := newTracker()
	step(t, tr, `{`+sess+`"hook_event_name":"TaskCreated","task_id":"t1"}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"Stop","background_tasks":[{"id":"t1","type":"shell","status":"running"}]}`, api.ThreadWorking, true)
	step(t, tr, `{`+sess+`"hook_event_name":"TaskCompleted","task_id":"t1","background_tasks":[]}`, api.ThreadIdle, true)
	// A scheduled cron is not activity: the entries are schedule
	// definitions, so a dormant wakeup must not pin the session at
	// working (see the tracker doc).
	step(t, tr, `{`+sess+`"hook_event_name":"Stop","session_crons":[{"id":"c1"}]}`, api.ThreadIdle, true)
}

// Unrecognized events without a level snapshot carry no signal — the
// reducer never guesses.
func TestTrackerUnrecognizedEvents(t *testing.T) {
	tr := newTracker()
	if _, signal, _ := tr.apply(event(`{` + sess + `"hook_event_name":"SomethingNew"}`)); signal {
		t.Error("unrecognized event produced a signal")
	}
	if _, signal, _ := tr.apply(event(`{` + sess + `"hook_event_name":"Notification","notification_type":"mystery"}`)); signal {
		t.Error("unknown notification produced a signal")
	}
	// With a snapshot aboard, the snapshot is the signal.
	if status, signal, _ := tr.apply(event(`{` + sess + `"hook_event_name":"SomethingNew","background_tasks":[{"id":"a1"}]}`)); !signal || status != api.ThreadWorking {
		t.Errorf("snapshot on unrecognized event: status=%s signal=%v", status, signal)
	}
}

// A seeded (post-restart) tracker claims nothing until evidence arrives.
func TestTrackerSeededStartsUnknown(t *testing.T) {
	tr := seededTracker()
	if got := tr.aggregate(); got != api.ThreadUnknown {
		t.Fatalf("seeded tracker = %s, want unknown", got)
	}
	step(t, tr, `{`+sess+`"hook_event_name":"Stop"}`, api.ThreadIdle, true)
}
