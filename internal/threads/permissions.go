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

// Permission requests (ATC-307): the approvals an agent is blocked on,
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
	permissionPrefix = "perm-"
	// resolvedKept bounds the resolved requests remembered per thread.
	resolvedKept = 16
)

// ErrPermissionNotFound reports a permission id the thread has no record
// of; the API layer maps it to 404.
var ErrPermissionNotFound = errors.New("permission request not found")

// ErrPermissionResolved refuses deciding a request that is already
// resolved — through ATC, or elsewhere; the API layer maps it to 409.
var ErrPermissionResolved = errors.New("permission request already resolved")

// ErrDecisionNotOffered refuses a decision the request does not offer;
// the API layer maps it to 400.
var ErrDecisionNotOffered = errors.New("decision not offered")

// ErrDecisionPending refuses a decision while a different one, already
// sent for the same request, awaits the provider's answer: uncertainty
// is never turned into a second decision. The API layer maps it to 409.
var ErrDecisionPending = errors.New("a different decision awaits the provider's answer")

// PermissionObservation is one pending permission request as an
// Integration reports it: the provider's own request id (private), and
// the request as the provider presents it.
type PermissionObservation struct {
	RequestID   string
	Kind        api.PermissionKind
	Summary     string
	Detail      string
	AppName     string
	Options     []api.PermissionOption
	RequestedAt time.Time
}

// DecisionRequest is what an Integration needs to decide a request: the
// private identities and the decision, and a key stable for the
// (request, decision) pair for the Integration's own deduplication, so a
// retry after a lost answer presents the same command.
type DecisionRequest struct {
	IntegrationID string
	ProviderID    string
	RequestID     string
	Decision      api.PermissionDecision
	Key           string
}

// permissionEntry is one request the thread knows: its wire shape, the
// private request id, and a decision dispatched but not yet answered.
type permissionEntry struct {
	permission api.ThreadPermission
	requestID  string
	deciding   api.PermissionDecision
}

// ObservePermissions replaces what is pending on a conversation with the
// Integration's current report: a request not seen before is pending, a
// pending one not reported any more is resolved — elsewhere, with no
// decision recorded — and a request already resolved stays so whatever
// the report says. An unmapped identity is dropped. Publishes
// thread.updated when the pending set or a pending request's presentation
// changed, whether or not the thread's status did.
func (s *Service) ObservePermissions(ctx context.Context, integrationID, providerID string, pending []PermissionObservation) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	defer s.mu.Unlock()
	threadID, known := s.identities[identityKey{integrationID, providerID}]
	if !known {
		s.logger.Debug("permission evidence for unmapped conversation dropped", "integration", integrationID)
		return nil
	}
	now := s.now()
	entries := s.permissions[threadID]
	changed := false
	reported := make(map[string]bool, len(pending))
	for _, o := range pending {
		id := ids.Derive(permissionPrefix, integrationID+"\x00"+providerID+"\x00"+o.RequestID)
		reported[id] = true
		presented := api.ThreadPermission{
			ID: id, ThreadID: threadID, Status: api.PermissionPending, Kind: o.Kind, Summary: o.Summary, Detail: o.Detail,
			AppName: o.AppName, Options: slices.Clone(o.Options), RequestedAt: o.RequestedAt,
		}
		if presented.Options == nil {
			presented.Options = []api.PermissionOption{}
		}
		index := slices.IndexFunc(entries, func(e *permissionEntry) bool { return e.permission.ID == id })
		switch {
		case index < 0:
			entries = append(entries, &permissionEntry{permission: presented, requestID: o.RequestID})
			changed = true
		case entries[index].permission.Status == api.PermissionResolved:
		case !permissionEqual(entries[index].permission, presented):
			entries[index].permission = presented
			changed = true
		}
	}
	for _, entry := range entries {
		if entry.permission.Status == api.PermissionPending && !reported[entry.permission.ID] {
			resolve(entry, "", now)
			changed = true
		}
	}
	s.permissions[threadID] = trimResolved(entries)
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return nil
}

// resolve marks a request resolved with the decision, "" for one made
// elsewhere.
func resolve(entry *permissionEntry, decision api.PermissionDecision, at time.Time) {
	entry.permission.Status = api.PermissionResolved
	entry.permission.Decision = decision
	resolved := at
	entry.permission.ResolvedAt = &resolved
	entry.deciding = ""
}

// trimResolved keeps every pending request and the newest resolvedKept
// resolved ones.
func trimResolved(entries []*permissionEntry) []*permissionEntry {
	resolved := 0
	for _, entry := range entries {
		if entry.permission.Status == api.PermissionResolved {
			resolved++
		}
	}
	excess := resolved - resolvedKept
	if excess <= 0 {
		return entries
	}
	kept := entries[:0:0]
	for _, entry := range entries {
		if entry.permission.Status == api.PermissionResolved && excess > 0 {
			excess--
			continue
		}
		kept = append(kept, entry)
	}
	return kept
}

func permissionEqual(a, b api.ThreadPermission) bool {
	return a.ID == b.ID && a.Status == b.Status && a.Kind == b.Kind && a.Summary == b.Summary && a.Detail == b.Detail &&
		a.AppName == b.AppName && a.Decision == b.Decision && a.RequestedAt.Equal(b.RequestedAt) && slices.Equal(a.Options, b.Options)
}

// Permission serves one of a thread's permission requests, pending or
// remembered as resolved.
func (s *Service) Permission(threadID, permissionID string) (api.ThreadPermission, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.permission(threadID, permissionID)
	if err != nil {
		return api.ThreadPermission{}, err
	}
	return clonePermission(entry.permission), nil
}

// permission finds an entry. Caller holds mu.
func (s *Service) permission(threadID, permissionID string) (*permissionEntry, error) {
	if _, ok := s.view[threadID]; !ok {
		return nil, ErrNotFound
	}
	for _, entry := range s.permissions[threadID] {
		if entry.permission.ID == permissionID {
			return entry, nil
		}
	}
	return nil, fmt.Errorf("%w: %s", ErrPermissionNotFound, permissionID)
}

// BeginDecision checks a decision against the request — it must be
// pending, the decision offered, and no different decision awaiting the
// provider's answer — records the decision as sent, and returns what the
// Integration needs to dispatch it. The same decision begun again (a
// retry after a lost answer) presents the same key.
func (s *Service) BeginDecision(threadID, permissionID string, decision api.PermissionDecision) (DecisionRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, err := s.permission(threadID, permissionID)
	if err != nil {
		return DecisionRequest{}, err
	}
	if entry.permission.Status == api.PermissionResolved {
		how := "elsewhere"
		if entry.permission.Decision != "" {
			how = "with " + string(entry.permission.Decision)
		}
		return DecisionRequest{}, fmt.Errorf("%w: %s was resolved %s", ErrPermissionResolved, permissionID, how)
	}
	if !slices.ContainsFunc(entry.permission.Options, func(o api.PermissionOption) bool { return o.Decision == decision }) {
		return DecisionRequest{}, fmt.Errorf("%w: %s does not offer %q", ErrDecisionNotOffered, permissionID, decision)
	}
	if entry.deciding != "" && entry.deciding != decision {
		return DecisionRequest{}, fmt.Errorf("%w: %s was sent %s", ErrDecisionPending, permissionID, entry.deciding)
	}
	entry.deciding = decision
	key := s.keys[threadID]
	return DecisionRequest{
		IntegrationID: key.integrationID, ProviderID: key.providerID, RequestID: entry.requestID, Decision: decision,
		Key: permissionID + "/" + string(decision),
	}, nil
}

// ResolveDecision records the provider committing the decision sent: the
// request is resolved with it, and thread.updated publishes.
func (s *Service) ResolveDecision(threadID, permissionID string) (api.ThreadPermission, error) {
	s.mu.Lock()
	entry, err := s.permission(threadID, permissionID)
	if err != nil {
		s.mu.Unlock()
		return api.ThreadPermission{}, err
	}
	changed := entry.permission.Status == api.PermissionPending
	if changed {
		resolve(entry, entry.deciding, s.now())
	}
	permission := clonePermission(entry.permission)
	s.mu.Unlock()
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return permission, nil
}

// AbandonDecision records the provider refusing the decision sent: the
// request stays pending and takes another decision. A decision the
// provider never answered is not abandoned — it stays sent, so that
// uncertainty is never turned into a second decision.
func (s *Service) AbandonDecision(threadID, permissionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if entry, err := s.permission(threadID, permissionID); err == nil && entry.permission.Status == api.PermissionPending {
		entry.deciding = ""
	}
}

// pendingPermissions lists a thread's pending requests for the wire.
// Caller holds mu.
func (s *Service) pendingPermissions(threadID string) []api.ThreadPermission {
	var pending []api.ThreadPermission
	for _, entry := range s.permissions[threadID] {
		if entry.permission.Status == api.PermissionPending {
			pending = append(pending, clonePermission(entry.permission))
		}
	}
	return pending
}

func clonePermission(p api.ThreadPermission) api.ThreadPermission {
	p.Options = slices.Clone(p.Options)
	if p.ResolvedAt != nil {
		at := *p.ResolvedAt
		p.ResolvedAt = &at
	}
	return p
}
