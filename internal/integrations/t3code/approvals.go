package t3code

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/threads"
)

// Approval requests (ATC-307). The shell projection says only whether
// a thread has approvals pending; which ones, and what they ask, is read
// from the thread detail snapshot's activities (requests.go), where an
// approval.requested entry with no later approval.resolved, and no
// failure marking the request stale, for the same request id is
// pending. A decision is one thread.approval.respond command in T3's
// decision vocabulary, with a command id derived from the
// request-and-decision key so a retry after a lost answer is
// deduplicated by T3's receipts.

// approvalPayload is the payload of the approval activities: the request
// id every one carries, and the presentation the request carries.
type approvalPayload struct {
	RequestID   string `json:"requestId"`
	RequestKind string `json:"requestKind"`
	RequestType string `json:"requestType"`
	Detail      string `json:"detail"`
	AppName     string `json:"appName"`
	Options     []struct {
		Decision string `json:"decision"`
		Label    string `json:"label"`
		Warning  string `json:"warning"`
	} `json:"options"`
}

// decisions maps ATC's decision vocabulary to T3's, and back.
var (
	decisions = map[api.ApprovalDecision]string{
		api.DecisionApprove:           "accept",
		api.DecisionApproveForSession: "acceptForSession",
		api.DecisionApproveAlways:     "acceptAlways",
		api.DecisionDeny:              "decline",
		api.DecisionCancel:            "cancel",
	}
	decisionsFromT3 = func() map[string]api.ApprovalDecision {
		m := make(map[string]api.ApprovalDecision, len(decisions))
		for ours, theirs := range decisions {
			m[theirs] = ours
		}
		return m
	}()
	// defaultOptions is what a request offers when T3 names no options:
	// the pair T3's own surfaces always offer.
	defaultOptions = []api.ApprovalOption{
		{Decision: api.DecisionApprove, Label: "Approve"},
		{Decision: api.DecisionDeny, Label: "Decline"},
	}
)

// DecideApproval dispatches one decision. The domain validated it
// against the request; T3's refusal is final, T3 never answering leaves
// the outcome unknown for the same key to reconcile.
func (s *Service) DecideApproval(ctx context.Context, req integrations.ApprovalDecision) error {
	s.mu.Lock()
	connection, client := s.connection, s.client
	s.mu.Unlock()
	if connection.State != api.IntegrationConnected {
		return fmt.Errorf("%w: T3 Code is %s: %s", integrations.ErrNotConnected, connection.State, connection.Detail)
	}
	decision, ok := decisions[req.Decision]
	if !ok {
		return fmt.Errorf("%w: decision %q has no T3 Code equivalent", integrations.ErrDecisionRejected, req.Decision)
	}
	return outcome(s.deliver(ctx, client, map[string]any{
		"type":      "thread.approval.respond",
		"commandId": ids.UUIDFrom("t3code/approval/" + req.Key),
		"threadId":  req.ProviderID,
		"requestId": req.RequestID,
		"decision":  decision,
		"createdAt": timestamp(s.now()),
	}), integrations.ErrDecisionRejected, "decision")
}

// pendingApprovals derives the pending requests from a thread's
// activities, in request order: each approval.requested without a later
// approval.resolved for its request id, or a respond failure naming the
// request stale — T3 resolves those in its own projection too.
func pendingApprovals(activities []activity) []threads.ApprovalObservation {
	var pending []threads.ApprovalObservation
	resolved := map[string]bool{}
	seen := map[string]bool{}
	for _, activity := range activities {
		var payload approvalPayload
		if json.Unmarshal(activity.Payload, &payload) != nil || payload.RequestID == "" {
			continue
		}
		switch activity.Kind {
		case "approval.requested":
			if seen[payload.RequestID] {
				continue
			}
			seen[payload.RequestID] = true
			pending = append(pending, threads.ApprovalObservation{
				RequestID:   payload.RequestID,
				Kind:        approvalKind(payload),
				Summary:     activity.Summary,
				Detail:      payload.Detail,
				AppName:     payload.AppName,
				Options:     approvalOptions(payload),
				RequestedAt: activity.CreatedAt,
			})
		case "approval.resolved":
			resolved[payload.RequestID] = true
		case "provider.approval.respond.failed":
			if stale(payload.Detail) {
				resolved[payload.RequestID] = true
			}
		}
	}
	kept := pending[:0]
	for _, request := range pending {
		if !resolved[request.RequestID] {
			kept = append(kept, request)
		}
	}
	return kept
}

// stale recognizes T3's own wording for a decision or an answer that
// reached no live request — the same test T3's projection applies.
func stale(detail string) bool {
	detail = strings.ToLower(detail)
	for _, wording := range []string{
		"stale pending approval request", "unknown pending approval request", "unknown pending permission request",
		"stale pending user-input request", "unknown pending user-input request", "unknown pending user input request", "unknown pending codex user input request",
	} {
		if strings.Contains(detail, wording) {
			return true
		}
	}
	return false
}

// approvalKind normalizes T3's request kind, falling back to the
// provider's request type.
func approvalKind(payload approvalPayload) api.ApprovalKind {
	switch payload.RequestKind {
	case "command":
		return api.ApprovalCommand
	case "file-read":
		return api.ApprovalFileRead
	case "file-change":
		return api.ApprovalFileChange
	case "mcp-elicitation":
		return api.ApprovalAppAccess
	}
	switch payload.RequestType {
	case "command_execution_approval", "exec_command_approval":
		return api.ApprovalCommand
	case "file_read_approval":
		return api.ApprovalFileRead
	case "file_change_approval", "apply_patch_approval":
		return api.ApprovalFileChange
	case "mcp_elicitation_approval":
		return api.ApprovalAppAccess
	}
	return api.ApprovalUnknown
}

// approvalOptions translates the options T3 names, dropping a decision
// ATC has no word for — a request offering only such decisions offers
// nothing ATC can send; a request naming no options offers the default
// pair.
func approvalOptions(payload approvalPayload) []api.ApprovalOption {
	if payload.Options == nil {
		return append([]api.ApprovalOption(nil), defaultOptions...)
	}
	options := []api.ApprovalOption{}
	for _, option := range payload.Options {
		decision, ok := decisionsFromT3[option.Decision]
		if !ok {
			continue
		}
		options = append(options, api.ApprovalOption{Decision: decision, Label: option.Label, Warning: option.Warning})
	}
	return options
}
