package codex

import "github.com/jeremytondo/atc/internal/api"

// tracker is one session's stateful reducer. Codex's hook stream carries
// no background-work snapshots, so the root status is the whole state —
// plus the interrupt latch: after Interrupt, the interrupted foreground
// tool can finish long after the conversational interrupt (18.9 s
// observed in ATC-281), and its late events must not resurrect working.
//
// The two accepted lags are contract, not bugs (ATC-280): no event marks
// approval resolution, so waiting_for_permission overstays by the
// approved tool's runtime; statuses come from evidence, and nothing here
// infers transitions from timers or guesses.
type tracker struct {
	root api.ThreadStatus
	// interrupted holds every turn_id an Interrupt closed; the latch is
	// turn-keyed, so the next turn's events pass while interrupted turns'
	// stragglers stay silenced — a set, because a second interrupt must
	// not free the first turn's still-running tool to resurrect working.
	interrupted map[string]bool
}

// newTracker starts at idle — a session switch lands at its prompt.
func newTracker() *tracker {
	return &tracker{root: api.ThreadIdle, interrupted: map[string]bool{}}
}

// seededTracker starts at unknown: the session was adopted mid-flight
// (post-restart), so nothing about it is established until evidence says
// so.
func seededTracker() *tracker {
	return &tracker{root: api.ThreadUnknown, interrupted: map[string]bool{}}
}

// latched reports whether the event belongs to an interrupted turn.
func (t *tracker) latched(turnID string) bool {
	return turnID != "" && t.interrupted[turnID]
}

// apply folds one hook payload in and reports the status, whether the
// payload carried a signal worth forwarding (unrecognized events are
// never guessed at), and a lastError change when the payload proves one.
func (t *tracker) apply(p payload) (status api.ThreadStatus, signal bool, lastError *string) {
	switch p.HookEventName {
	case "UserPromptSubmit":
		t.root = api.ThreadWorking
		// A new turn supersedes the previous turn's failure detail.
		cleared := ""
		return t.root, true, &cleared
	case "PreToolUse", "PostToolUse":
		if t.latched(p.TurnID) {
			return t.root, false, nil
		}
		t.root = api.ThreadWorking
		return t.root, true, nil
	case "PermissionRequest":
		if t.latched(p.TurnID) {
			return t.root, false, nil
		}
		t.root = api.ThreadWaitingForPermission
		return t.root, true, nil
	case "Stop":
		t.root = api.ThreadIdle
		return t.root, true, nil
	case "Interrupt":
		// Terminal for conversational state. Codex 0.151 emits it though
		// its documentation does not list it — opportunistic evidence: if
		// a future Codex stops emitting it, behavior degrades to Claude's
		// accepted interrupt lag and nothing breaks.
		t.root = api.ThreadIdle
		if p.TurnID != "" {
			t.interrupted[p.TurnID] = true
		}
		return t.root, true, nil
	}
	return t.root, false, nil
}
