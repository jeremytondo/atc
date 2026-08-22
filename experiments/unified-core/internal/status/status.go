// Package status reduces provider-specific structured evidence into ATC
// activity. Reducers are stateful and correlation-aware; raw payloads never
// leave this adapter through the public API.
package status

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
)

type Registry struct {
	mu     sync.Mutex
	claude map[string]*claudeState
	codex  map[string]*codexState
	now    func() time.Time
}

func New(now func() time.Time) *Registry {
	if now == nil {
		now = time.Now
	}
	return &Registry{claude: make(map[string]*claudeState), codex: make(map[string]*codexState), now: now}
}

func (r *Registry) Observe(threadID string, provider domain.Agent, raw []byte) (ports.ProviderObservation, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	switch provider {
	case domain.AgentClaude:
		state := r.claude[threadID]
		if state == nil {
			state = &claudeState{descendants: make(map[string]domain.Activity)}
			r.claude[threadID] = state
		}
		activity, rule, ok := state.apply(raw)
		return observation(activity, rule, raw, r.now()), ok
	case domain.AgentCodex:
		state := r.codex[threadID]
		if state == nil {
			state = &codexState{children: make(map[string]string), activity: make(map[string]domain.Activity)}
			r.codex[threadID] = state
		}
		activity, rule, ok := state.apply(raw)
		return observation(activity, rule, raw, r.now()), ok
	default:
		return ports.ProviderObservation{}, false
	}
}

func (r *Registry) Restore(_ context.Context, threadID string, provider domain.Agent, root string) (ports.ProviderObservation, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if provider == domain.AgentCodex {
		state := r.codex[threadID]
		if state == nil {
			state = &codexState{children: make(map[string]string), activity: make(map[string]domain.Activity)}
			r.codex[threadID] = state
		}
		state.root = root
	}
	return observation(domain.ActivityUnknown, "restart.requires_structured_reconciliation", nil, r.now()), nil
}

func observation(activity domain.Activity, rule string, raw []byte, now time.Time) ports.ProviderObservation {
	return ports.ProviderObservation{Activity: activity, ObservedAt: now.UTC(), Rule: rule, Raw: append([]byte(nil), raw...)}
}

type claudeEvent struct {
	HookEvent    string `json:"hook_event_name"`
	Notification string `json:"notification_type"`
	ToolName     string `json:"tool_name"`
	PromptID     string `json:"prompt_id"`
	AgentID      string `json:"agent_id"`
	Background   []struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	} `json:"background_tasks"`
}

type claudeState struct {
	promptID      string
	root          domain.Activity
	pendingPrompt string
	pendingKind   domain.RequestKind
	descendants   map[string]domain.Activity
}

func (s *claudeState) apply(raw []byte) (domain.Activity, string, bool) {
	var event claudeEvent
	if json.Unmarshal(raw, &event) != nil || event.HookEvent == "" {
		return "", "", false
	}
	if event.PromptID != "" {
		s.promptID = event.PromptID
	}
	if event.Background != nil {
		s.descendants = make(map[string]domain.Activity)
		for _, task := range event.Background {
			if task.Status == "running" || task.Status == "active" {
				s.descendants[task.ID] = domain.ActivityWorking
			}
		}
	}
	switch event.HookEvent {
	case "UserPromptSubmit", "PreToolUse", "PostToolUse":
		s.root = domain.ActivityWorking
		return s.aggregate(), event.HookEvent, true
	case "SubagentStart":
		if event.AgentID != "" {
			s.descendants[event.AgentID] = domain.ActivityWorking
		}
		return s.aggregate(), event.HookEvent, true
	case "SubagentStop":
		// Claude's level snapshot still contains the stopping descendant.
		delete(s.descendants, event.AgentID)
		return s.aggregate(), event.HookEvent, true
	case "PermissionRequest":
		s.pendingPrompt = event.PromptID
		s.pendingKind = domain.RequestApproval
		if event.ToolName == "AskUserQuestion" {
			s.pendingKind = domain.RequestQuestion
		}
		return domain.ActivityNeedsInput, event.HookEvent, true
	case "Notification":
		return s.applyNotification(event)
	case "Stop", "StopFailure":
		s.root = domain.ActivityIdle
		return s.aggregate(), event.HookEvent, true
	case "SessionEnd":
		s.root = domain.ActivityIdle
		s.descendants = make(map[string]domain.Activity)
		return domain.ActivityIdle, event.HookEvent, true
	default:
		return "", "", false
	}
}

func (s *claudeState) applyNotification(event claudeEvent) (domain.Activity, string, bool) {
	samePendingPrompt := event.PromptID != "" && event.PromptID == s.pendingPrompt
	switch event.Notification {
	case "elicitation_dialog", "agent_needs_input":
		s.pendingPrompt = event.PromptID
		s.pendingKind = domain.RequestQuestion
		return domain.ActivityNeedsInput, "Notification." + event.Notification, true
	case "permission_prompt":
		if samePendingPrompt && s.pendingKind == domain.RequestQuestion {
			return domain.ActivityNeedsInput, "Notification.permission_prompt.preserved_question", true
		}
		s.pendingPrompt = event.PromptID
		s.pendingKind = domain.RequestApproval
		return domain.ActivityNeedsInput, "Notification.permission_prompt", true
	case "idle_prompt":
		if samePendingPrompt {
			return domain.ActivityNeedsInput, "Notification.idle_prompt.preserved_request", true
		}
		s.root = domain.ActivityIdle
		return s.aggregate(), "Notification.idle_prompt", true
	default:
		return "", "", false
	}
}

func (s *claudeState) aggregate() domain.Activity {
	activities := []domain.Activity{s.root}
	for _, activity := range s.descendants {
		activities = append(activities, activity)
	}
	return aggregate(activities)
}

type codexStatus struct {
	Type        string   `json:"type"`
	ActiveFlags []string `json:"activeFlags"`
}

type codexEvent struct {
	Method    string `json:"method"`
	ExactRoot string `json:"atcExactRoot"`
	Result    struct {
		Data []struct {
			ID             string      `json:"id"`
			ParentThreadID *string     `json:"parentThreadId"`
			Status         codexStatus `json:"status"`
		} `json:"data"`
		Thread struct {
			ID             string      `json:"id"`
			ParentThreadID *string     `json:"parentThreadId"`
			Status         codexStatus `json:"status"`
		} `json:"thread"`
	} `json:"result"`
	Params struct {
		ThreadID string      `json:"threadId"`
		Status   codexStatus `json:"status"`
		Thread   struct {
			ID             string      `json:"id"`
			ParentThreadID *string     `json:"parentThreadId"`
			Status         codexStatus `json:"status"`
		} `json:"thread"`
		Item struct {
			Type          string `json:"type"`
			Kind          string `json:"kind"`
			AgentThreadID string `json:"agentThreadId"`
		} `json:"item"`
	} `json:"params"`
}

type codexState struct {
	root     string
	children map[string]string
	activity map[string]domain.Activity
}

func (s *codexState) apply(raw []byte) (domain.Activity, string, bool) {
	var event codexEvent
	if json.Unmarshal(raw, &event) != nil {
		return "", "", false
	}
	if event.ExactRoot != "" {
		if s.root != "" && s.root != event.ExactRoot {
			return "", "", false
		}
		s.root = event.ExactRoot
	}
	switch event.Method {
	case "thread/started":
		thread := event.Params.Thread
		if thread.ID == "" || thread.ParentThreadID != nil {
			return "", "", false
		}
		if s.root == "" {
			s.root = thread.ID
		}
		if thread.ID != s.root {
			return "", "", false
		}
		s.activity[s.root] = normalizeCodex(thread.Status)
		return s.aggregate(), "thread/started.exact_root", true
	case "item/started", "item/completed":
		item := event.Params.Item
		if event.Params.ThreadID != s.root || item.Type != "subAgentActivity" || item.AgentThreadID == "" {
			return "", "", false
		}
		s.children[item.AgentThreadID] = s.root
		if item.Kind == "started" || item.Kind == "interacted" {
			s.activity[item.AgentThreadID] = domain.ActivityWorking
		}
		if item.Kind == "interrupted" {
			s.activity[item.AgentThreadID] = domain.ActivityIdle
		}
		return s.aggregate(), event.Method + ".subAgentActivity", true
	case "thread/status/changed":
		id := event.Params.ThreadID
		if id != s.root && !s.descendsFromRoot(id) {
			return "", "", false
		}
		s.activity[id] = normalizeCodex(event.Params.Status)
		return s.aggregate(), "thread/status/changed.correlated", true
	case "thread/read":
		return s.applyRead(event.Result.Thread)
	case "thread/loaded/list":
		// Loaded-list payloads do not contain ancestry. They only delimit ids
		// that callers must follow with exact thread/read requests.
		return s.aggregate(), "thread/loaded/list.requires_exact_reads", s.root != ""
	default:
		return "", "", false
	}
}

func (s *codexState) applyRead(thread struct {
	ID             string      `json:"id"`
	ParentThreadID *string     `json:"parentThreadId"`
	Status         codexStatus `json:"status"`
}) (domain.Activity, string, bool) {
	if thread.ID == "" {
		return "", "", false
	}
	if thread.ID == s.root {
		s.activity[thread.ID] = normalizeCodex(thread.Status)
		return s.aggregate(), "thread/read.root", true
	}
	if thread.ParentThreadID == nil {
		return "", "", false
	}
	parent := *thread.ParentThreadID
	if parent != s.root && !s.descendsFromRoot(parent) {
		return "", "", false
	}
	s.children[thread.ID] = parent
	s.activity[thread.ID] = normalizeCodex(thread.Status)
	return s.aggregate(), "thread/read.descendant", true
}

func (s *codexState) descendsFromRoot(id string) bool {
	seen := make(map[string]bool)
	for id != "" && !seen[id] {
		seen[id] = true
		parent, ok := s.children[id]
		if !ok {
			return false
		}
		if parent == s.root {
			return true
		}
		id = parent
	}
	return false
}

func (s *codexState) aggregate() domain.Activity {
	activities := make([]domain.Activity, 0, len(s.activity))
	for id, activity := range s.activity {
		if id == s.root || s.descendsFromRoot(id) {
			activities = append(activities, activity)
		}
	}
	return aggregate(activities)
}

func normalizeCodex(status codexStatus) domain.Activity {
	switch status.Type {
	case "idle":
		return domain.ActivityIdle
	case "active":
		for _, flag := range status.ActiveFlags {
			if flag == "waitingOnUserInput" || flag == "waitingOnApproval" {
				return domain.ActivityNeedsInput
			}
		}
		return domain.ActivityWorking
	case "notLoaded", "systemError":
		return domain.ActivityUnknown
	default:
		return domain.ActivityUnknown
	}
}

func aggregate(activities []domain.Activity) domain.Activity {
	for _, activity := range activities {
		if activity == domain.ActivityNeedsInput {
			return activity
		}
	}
	for _, activity := range activities {
		if activity == domain.ActivityWorking {
			return activity
		}
	}
	for _, activity := range activities {
		if activity == domain.ActivityUnknown || activity == "" {
			return domain.ActivityUnknown
		}
	}
	return domain.ActivityIdle
}
