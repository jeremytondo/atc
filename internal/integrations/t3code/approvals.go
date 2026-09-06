package t3code

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/threads"
)

// Approval requests (ATC-307). The shell projection says only whether
// a thread has approvals pending; which ones, and what they ask, is read
// from the thread detail snapshot's activities — the same one-shot read
// as response recovery — where an approval.requested entry with no
// later approval.resolved, and no failure marking the request stale,
// for the same request id is pending. A thread reported with approvals
// pending is read, one read in flight per thread with one more queued
// behind it, so every report is eventually reflected without a read per
// event; a thread reported without any resolves whatever ATC still
// held, with no read at all. A decision is one thread.approval.respond
// command in T3's decision vocabulary, with a command id derived from
// the request-and-decision key so a retry after a lost answer is
// deduplicated by T3's receipts.

// approvalActivity is what ATC reads of a thread activity: its kind, its
// payload — read only for the approval kinds — and when it happened.
type approvalActivity struct {
	Kind      string          `json:"kind"`
	Summary   string          `json:"summary"`
	Payload   json.RawMessage `json:"payload"`
	CreatedAt time.Time       `json:"createdAt"`
}

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
	err := s.deliver(ctx, client, map[string]any{
		"type":      "thread.approval.respond",
		"commandId": ids.UUIDFrom("t3code/approval/" + req.Key),
		"threadId":  req.ProviderID,
		"requestId": req.RequestID,
		"decision":  decision,
		"createdAt": timestamp(s.now()),
	})
	var failure *rpcFailure
	switch {
	case err == nil:
		return nil
	case errors.As(err, &failure):
		return fmt.Errorf("%w: T3 Code rejected the decision: %s", integrations.ErrDecisionRejected, failureMessage(failure))
	case errors.Is(err, integrations.ErrNotConnected):
		return err
	default:
		return fmt.Errorf("%w: T3 Code did not answer: %w", integrations.ErrDeliveryUncertain, err)
	}
}

// observeApprovals reflects a thread's pending approvals: a read when
// T3 reports some pending, the empty set right away when it reports
// none. Runs on the Run goroutine.
func (s *Service) observeApprovals(ctx context.Context, t3ThreadID string, pending bool) {
	s.mu.Lock()
	s.approvalReports[t3ThreadID]++
	s.mu.Unlock()
	if !pending {
		if err := s.threads.ObserveApprovals(ctx, ID, t3ThreadID, nil); err != nil {
			s.logger.Warn("t3code: recording resolved approvals", "thread", t3ThreadID, "error", err)
		}
		return
	}
	s.mu.Lock()
	origin := s.origin
	inflight := s.approvalReads[t3ThreadID]
	s.approvalReads[t3ThreadID] = readQueued
	s.mu.Unlock()
	if inflight != readIdle {
		// A read is in flight; it runs once more when it lands.
		return
	}
	token := ""
	if s.session != nil {
		token = s.session.Token
	}
	s.reads.Add(1)
	go func() {
		defer s.reads.Done()
		for {
			s.mu.Lock()
			s.approvalReads[t3ThreadID] = readRunning
			generation := s.approvalReports[t3ThreadID]
			s.mu.Unlock()
			pending, ok := s.readApprovals(ctx, origin, token, t3ThreadID)
			s.mu.Lock()
			again := s.approvalReads[t3ThreadID] == readQueued
			// A report that arrived meanwhile supersedes what was read: a
			// newer "none pending" must not be undone by an older snapshot,
			// and a newer "some pending" is read again below.
			current := generation == s.approvalReports[t3ThreadID]
			if !again {
				delete(s.approvalReads, t3ThreadID)
			}
			s.mu.Unlock()
			if ok && current {
				if err := s.threads.ObserveApprovals(ctx, ID, t3ThreadID, pending); err != nil {
					s.logger.Warn("t3code: recording pending approvals", "thread", t3ThreadID, "error", err)
				}
			}
			if !again || ctx.Err() != nil {
				return
			}
		}
	}()
}

// The per-thread approval read states: none, one running, one running
// with another owed.
const (
	readIdle = iota
	readRunning
	readQueued
)

// readApprovals reads the detail snapshot once and returns the pending
// approvals it shows; false for a failed read, which reports nothing —
// what ATC holds stands until the next report.
func (s *Service) readApprovals(ctx context.Context, origin, token, t3ThreadID string) ([]threads.ApprovalObservation, bool) {
	select {
	case s.responseSlots <- struct{}{}:
		defer func() { <-s.responseSlots }()
	case <-ctx.Done():
		return nil, false
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/orchestration/threads/"+url.PathEscape(t3ThreadID), nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var detail struct {
		Thread struct {
			Activities []approvalActivity `json:"activities"`
		} `json:"thread"`
	}
	if err := doJSONBounded(s.httpClient, req, &detail, maxSnapshotBytes); err != nil {
		s.logger.Debug("t3code: reading a thread's pending approvals", "thread", t3ThreadID, "error", err)
		return nil, false
	}
	return pendingApprovals(detail.Thread.Activities), true
}

// pendingApprovals derives the pending requests from a thread's
// activities, in request order: each approval.requested without a later
// approval.resolved for its request id, or a respond failure naming the
// request stale — T3 resolves those in its own projection too.
func pendingApprovals(activities []approvalActivity) []threads.ApprovalObservation {
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

// stale recognizes T3's own wording for a decision that reached no live
// request — the same test T3's projection applies.
func stale(detail string) bool {
	detail = strings.ToLower(detail)
	return strings.Contains(detail, "stale pending approval request") ||
		strings.Contains(detail, "unknown pending approval request") ||
		strings.Contains(detail, "unknown pending permission request")
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
