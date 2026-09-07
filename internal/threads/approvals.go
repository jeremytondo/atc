package threads

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
)

// Approval requests (ATC-307): the approvals an agent is blocked on,
// as the producing Integration observes them, with a decision seam that
// targets exactly one request and one offered decision. The set is
// evidence, held in memory like the active projection: an Integration
// reports a conversation's whole pending set at once, a request it stops
// reporting is resolved, and after a restart the set is empty until the
// Integration reports again. Ids derive from the private request
// identity, so the same request has the same ATC id across reconnects
// and restarts, and the provider's own id never leaves the server. A
// resolved request is remembered for a while so a late decision on it is
// refused as resolved rather than unknown; it never returns to pending.

const (
	approvalPrefix = "aprv-"
	// resolvedKept bounds the resolved requests remembered per thread.
	resolvedKept = 16
)

// ErrApprovalNotFound reports an approval id the thread has no record
// of; the API layer maps it to 404.
var ErrApprovalNotFound = errors.New("approval request not found")

// ErrApprovalResolved refuses deciding a request that is already
// resolved — through ATC, or elsewhere; the API layer maps it to 409.
var ErrApprovalResolved = errors.New("approval request already resolved")

// ErrDecisionNotOffered refuses a decision the request does not offer;
// the API layer maps it to 400.
var ErrDecisionNotOffered = errors.New("decision not offered")

// ErrDecisionPending refuses a decision while a different one, already
// sent for the same request, awaits the provider's answer: uncertainty
// is never turned into a second decision. The API layer maps it to 409.
var ErrDecisionPending = errors.New("a different decision awaits the provider's answer")

// ApprovalObservation is one pending approval request as an
// Integration reports it: the provider's own request id (private), and
// the request as the provider presents it.
type ApprovalObservation struct {
	RequestID   string
	Kind        api.ApprovalKind
	Summary     string
	Detail      string
	AppName     string
	Options     []api.ApprovalOption
	RequestedAt time.Time
}

// DecisionRequest is what an Integration needs to decide a request: the
// private identities and the decision, and a key stable for the
// (request, decision) pair for the Integration's own deduplication, so a
// retry after a lost answer presents the same command.
type DecisionRequest struct {
	ProviderID string
	RequestID  string
	Decision   api.ApprovalDecision
	Key        string
}

// approvalEntry is one request the thread knows: its wire shape, the
// private request id, and a decision dispatched but not yet answered.
type approvalEntry struct {
	approval  api.ThreadApproval
	requestID string
	deciding  api.ApprovalDecision
}

// ObserveApprovals replaces what is pending on a conversation with the
// Integration's current report: a request not seen before is pending, a
// pending one not reported any more is resolved — elsewhere, with no
// decision recorded — and a request already resolved stays so whatever
// the report says. An unmapped identity is dropped. Publishes
// thread.updated when the pending set or a pending request's presentation
// changed, whether or not the thread's status did.
func (s *Service) ObserveApprovals(ctx context.Context, integrationID, providerID string, pending []ApprovalObservation) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	threadID, changed := s.applyApprovals(integrationID, providerID, pending)
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return nil
}

// applyApprovals folds a report into the thread's entries, reporting
// the thread and whether anything changed. Caller holds ops.
func (s *Service) applyApprovals(integrationID, providerID string, pending []ApprovalObservation) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	threadID, known := s.identities[identityKey{integrationID, providerID}]
	if !known {
		s.logger.Debug("approval evidence for unmapped conversation dropped", "integration", integrationID)
		return "", false
	}
	now := s.now()
	entries := s.approvals[threadID]
	changed := false
	reported := make(map[string]bool, len(pending))
	for _, o := range pending {
		id := ids.Derive(approvalPrefix, integrationID+"\x00"+providerID+"\x00"+o.RequestID)
		reported[id] = true
		presented := api.ThreadApproval{
			ID: id, ThreadID: threadID, Status: api.ApprovalPending, Kind: o.Kind, Summary: o.Summary, Detail: o.Detail,
			AppName: o.AppName, Options: slices.Clone(o.Options), RequestedAt: o.RequestedAt,
		}
		if presented.Options == nil {
			presented.Options = []api.ApprovalOption{}
		}
		index := slices.IndexFunc(entries, func(e *approvalEntry) bool { return e.approval.ID == id })
		switch {
		case index < 0:
			entries = append(entries, &approvalEntry{approval: presented, requestID: o.RequestID})
			changed = true
		case entries[index].approval.Status == api.ApprovalResolved:
		case !approvalEqual(entries[index].approval, presented):
			entries[index].approval = presented
			changed = true
		}
	}
	for _, entry := range entries {
		if entry.approval.Status == api.ApprovalPending && !reported[entry.approval.ID] {
			resolve(entry, "", now)
			changed = true
		}
	}
	s.approvals[threadID] = trimResolved(entries)
	return threadID, changed
}

// resolve marks a request resolved with the decision, "" for one made
// elsewhere.
func resolve(entry *approvalEntry, decision api.ApprovalDecision, at time.Time) {
	entry.approval.Status = api.ApprovalResolved
	entry.approval.Decision = decision
	resolved := at
	entry.approval.ResolvedAt = &resolved
	entry.deciding = ""
}

// trimResolved keeps every pending request and the newest resolvedKept
// resolved ones.
func trimResolved(entries []*approvalEntry) []*approvalEntry {
	resolved := 0
	for _, entry := range entries {
		if entry.approval.Status == api.ApprovalResolved {
			resolved++
		}
	}
	excess := resolved - resolvedKept
	if excess <= 0 {
		return entries
	}
	kept := entries[:0:0]
	for _, entry := range entries {
		if entry.approval.Status == api.ApprovalResolved && excess > 0 {
			excess--
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func approvalEqual(a, b api.ThreadApproval) bool {
	return a.ID == b.ID && a.Status == b.Status && a.Kind == b.Kind && a.Summary == b.Summary && a.Detail == b.Detail &&
		a.AppName == b.AppName && a.Decision == b.Decision && a.RequestedAt.Equal(b.RequestedAt) && slices.Equal(a.Options, b.Options)
}

// approval finds an entry. Caller holds mu.
func (s *Service) approval(threadID, approvalID string) (*approvalEntry, error) {
	if _, ok := s.view[threadID]; !ok {
		return nil, ErrNotFound
	}
	for _, entry := range s.approvals[threadID] {
		if entry.approval.ID == approvalID {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrApprovalNotFound, approvalID)
}

// BeginDecision checks a decision against the request — it must be
// pending, the decision offered, no different decision awaiting the
// provider's answer, and no stop being confirmed on the thread — records
// the decision as sent, and returns what the Integration needs to
// dispatch it. The same decision begun again (a retry after a lost
// answer) presents the same key.
func (s *Service) BeginDecision(threadID, approvalID string, decision api.ApprovalDecision) (DecisionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.approval(threadID, approvalID)
	if err != nil {
		return DecisionRequest{}, err
	}
	if stop := s.stopping(threadID); stop != nil {
		return DecisionRequest{}, fmt.Errorf("%w: %s", ErrThreadStopping, stop.ID)
	}
	if entry.approval.Status == api.ApprovalResolved {
		how := "elsewhere"
		if entry.approval.Decision != "" {
			how = "with " + string(entry.approval.Decision)
		}
		return DecisionRequest{}, fmt.Errorf("%w: %s was resolved %s", ErrApprovalResolved, approvalID, how)
	}
	// A decision already sent is taken again whatever the request offers
	// now — that is the retry that reconciles a lost answer.
	if entry.deciding != "" && entry.deciding != decision {
		return DecisionRequest{}, fmt.Errorf("%w: %s was sent %s", ErrDecisionPending, approvalID, entry.deciding)
	}
	if entry.deciding == "" && !slices.ContainsFunc(entry.approval.Options, func(o api.ApprovalOption) bool { return o.Decision == decision }) {
		return DecisionRequest{}, fmt.Errorf("%w: %s does not offer %q", ErrDecisionNotOffered, approvalID, decision)
	}
	entry.deciding = decision
	return DecisionRequest{
		ProviderID: s.keys[threadID].providerID, RequestID: entry.requestID, Decision: decision,
		Key: approvalID + "/" + string(decision),
	}, nil
}

// ResolveDecision records the provider committing the decision sent: the
// request is resolved with it, and thread.updated publishes.
func (s *Service) ResolveDecision(threadID, approvalID string) (api.ThreadApproval, error) {
	s.mu.Lock()
	entry, err := s.approval(threadID, approvalID)
	if err != nil {
		s.mu.Unlock()
		return api.ThreadApproval{}, err
	}
	changed := entry.approval.Status == api.ApprovalPending
	if changed {
		resolve(entry, entry.deciding, s.now())
	}
	approval := cloneApproval(entry.approval)
	s.mu.Unlock()
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return approval, nil
}

// AbandonDecision records the provider refusing the decision sent: the
// request stays pending and takes another decision. A decision the
// provider never answered is not abandoned — it stays sent, so that
// uncertainty is never turned into a second decision.
func (s *Service) AbandonDecision(threadID, approvalID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, err := s.approval(threadID, approvalID); err == nil && entry.approval.Status == api.ApprovalPending {
		entry.deciding = ""
	}
}

// closeApprovals resolves every pending request with no decision — the
// work asking was stopped (ATC-308). Caller holds mu.
func (s *Service) closeApprovals(threadID string, at time.Time) {
	for _, entry := range s.approvals[threadID] {
		if entry.approval.Status == api.ApprovalPending {
			resolve(entry, "", at)
		}
	}
}

// pendingApprovals lists a thread's pending requests for the wire.
// Caller holds mu.
func (s *Service) pendingApprovals(threadID string) []api.ThreadApproval {
	var pending []api.ThreadApproval
	for _, entry := range s.approvals[threadID] {
		if entry.approval.Status == api.ApprovalPending {
			pending = append(pending, cloneApproval(entry.approval))
		}
	}
	return pending
}

func cloneApproval(p api.ThreadApproval) api.ThreadApproval {
	p.Options = slices.Clone(p.Options)
	if p.ResolvedAt != nil {
		at := *p.ResolvedAt
		p.ResolvedAt = &at
	}
	return p
}
