package claude

import "github.com/jeremytondo/atc/internal/api"

// tracker is one session's stateful reducer: root status, agent-owned
// background work (subagents, backgrounded shells), and the aggregation
// between them. Individual hook events are not a state machine — level
// snapshots are authoritative wherever they appear, and edge events fill
// the gaps between them.
//
// session_crons is deliberately not status evidence: the SDK types it as
// schedule definitions ({id, schedule, recurring, prompt} — see
// experiments/subagent-activity), so an entry proves a wakeup is
// scheduled, not that anything runs right now, and no live payload has
// ever been recorded. A firing cron's actual work surfaces through the
// same prompt/tool/task events as any turn.
//
// Known limitation, accepted (ATC-255): Claude fires no hook on user
// interrupt, so working can overstay by up to roughly a minute until the
// next prompt or the idle notification.
type tracker struct {
	root       api.ThreadStatus
	background map[string]api.ThreadStatus
}

// newTracker starts at idle — SessionStart means a fresh conversation at
// its prompt.
func newTracker() *tracker {
	return &tracker{root: api.ThreadIdle, background: map[string]api.ThreadStatus{}}
}

// seededTracker starts at unknown: the session was adopted mid-flight
// (post-restart), so nothing about the root is established until
// evidence says so.
func seededTracker() *tracker {
	return &tracker{root: api.ThreadUnknown, background: map[string]api.ThreadStatus{}}
}

// rank orders statuses for aggregation: a blocked prompt outranks work,
// work outranks ignorance, and idle requires every member known-inactive.
// A specific question outranks a generic permission prompt, so a pending
// question is never papered over by later permission evidence.
func rank(status api.ThreadStatus) int {
	switch status {
	case api.ThreadWaitingForInput:
		return 4
	case api.ThreadWaitingForPermission:
		return 3
	case api.ThreadWorking:
		return 2
	case api.ThreadUnknown:
		return 1
	}
	return 0 // idle
}

func (t *tracker) aggregate() api.ThreadStatus {
	status := t.root
	for _, member := range t.background {
		if rank(member) > rank(status) {
			status = member
		}
	}
	return status
}

// waiting reports a status that blocks on the user.
func waiting(status api.ThreadStatus) bool {
	return status == api.ThreadWaitingForInput || status == api.ThreadWaitingForPermission
}

// apply folds one hook payload in and reports the aggregate, whether the
// payload carried a signal worth forwarding (unrecognized events without
// a snapshot are never guessed at), and a lastError change when the
// payload proves one.
func (t *tracker) apply(p payload) (status api.ThreadStatus, signal bool, lastError *string) {
	// Level snapshots first — authoritative wherever they appear. A
	// retained id keeps its waiting flavor: the coarse snapshot status
	// cannot see prompts.
	if p.BackgroundTasks != nil {
		replaced := make(map[string]api.ThreadStatus, len(*p.BackgroundTasks))
		for _, entry := range *p.BackgroundTasks {
			if entry.ID == "" {
				continue
			}
			status := api.ThreadWorking
			if previous, ok := t.background[entry.ID]; ok && waiting(previous) {
				status = previous
			}
			replaced[entry.ID] = status
		}
		t.background = replaced
		signal = true
	}
	root := p.AgentID == ""
	set := func(status api.ThreadStatus) {
		if root {
			t.root = status
		} else {
			t.background[p.AgentID] = status
		}
	}
	// A generic permission prompt never downgrades a pending question on
	// the same member — the specific evidence stands until the turn moves.
	setPermission := func() {
		if root {
			if t.root != api.ThreadWaitingForInput {
				t.root = api.ThreadWaitingForPermission
			}
		} else if t.background[p.AgentID] != api.ThreadWaitingForInput {
			t.background[p.AgentID] = api.ThreadWaitingForPermission
		}
	}

	switch p.HookEventName {
	case "UserPromptSubmit":
		set(api.ThreadWorking)
		if root {
			// A new turn supersedes the previous turn's failure detail.
			cleared := ""
			lastError = &cleared
		}
		return t.aggregate(), true, lastError
	case "PreToolUse", "PostToolUse", "PostToolUseFailure", "PermissionDenied":
		// The failure and denial events also resolve a descendant's
		// pending prompt — the turn moved on.
		set(api.ThreadWorking)
		return t.aggregate(), true, nil
	case "Stop":
		if root {
			t.root = api.ThreadIdle
			return t.aggregate(), true, nil
		}
		return t.aggregate(), signal, nil
	case "StopFailure":
		if root {
			t.root = api.ThreadIdle
			failed := "the last turn failed"
			return t.aggregate(), true, &failed
		}
		return t.aggregate(), signal, nil
	case "SubagentStart":
		if p.AgentID != "" {
			t.background[p.AgentID] = api.ThreadWorking
		}
		return t.aggregate(), true, nil
	case "SubagentStop":
		// The payload's level snapshot still contains the stopping agent
		// (probed 2026-08-10); this subtraction lands the last-child idle
		// transition.
		delete(t.background, p.AgentID)
		return t.aggregate(), true, nil
	case "TaskCreated":
		if p.TaskID != "" {
			t.background[p.TaskID] = api.ThreadWorking
		}
		return t.aggregate(), true, nil
	case "TaskCompleted":
		delete(t.background, p.TaskID)
		return t.aggregate(), true, nil
	case "PermissionRequest":
		// AskUserQuestion arrives through the permission machinery but is
		// a question — the split the spec keeps.
		if p.ToolName == "AskUserQuestion" {
			set(api.ThreadWaitingForInput)
		} else {
			setPermission()
		}
		return t.aggregate(), true, nil
	case "Notification":
		switch p.NotificationType {
		case "idle_prompt":
			// The idle nudge is root-only evidence; from a descendant it
			// proves nothing.
			if root {
				t.root = api.ThreadIdle
				return t.aggregate(), true, nil
			}
			return t.aggregate(), signal, nil
		case "permission_prompt":
			setPermission()
			return t.aggregate(), true, nil
		case "elicitation_dialog", "agent_needs_input":
			set(api.ThreadWaitingForInput)
			return t.aggregate(), true, nil
		}
		return t.aggregate(), signal, nil
	}
	return t.aggregate(), signal, nil
}
