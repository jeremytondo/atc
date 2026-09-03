package threads

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/store"
)

// The status ranking and the latest-turn model (ATC-301). A thread
// answers two questions — what is the agent doing now (status) and how
// did its most recent turn end (latestTurn) — and neither field carries
// the other's meaning: a failed turn leaves the thread idle, and a
// faulted session (status error, with the provider's explanation in
// statusDetail) is not a turn outcome. Both are decided here from the
// normalized evidence Integrations report; no Integration ranks or
// guesses on its own.

const turnPrefix = "turn-"

// ErrTurnPending refuses a second submission while the first submitted
// turn is still unbound — one in-flight submission per thread. The
// caller retries once the provider has reported the turn starting.
var ErrTurnPending = errors.New("a submitted turn is still pending")

// Rank decides a thread's status from coexisting evidence, one status
// per source: error outranks everything (the session cannot take a
// prompt), then a pending question, a pending approval, active work, and
// ignorance; idle requires every source to be at rest. No evidence at
// all is unknown. Every Integration feeds this function and none ranks
// on its own, so a question outranks an approval everywhere.
func Rank(evidence ...api.ThreadStatus) api.ThreadStatus {
	if len(evidence) == 0 {
		return api.ThreadUnknown
	}
	best := api.ThreadIdle
	for _, status := range evidence {
		if _, known := ranks[status]; !known {
			// Anything ATC does not recognize is honestly unknown.
			status = api.ThreadUnknown
		}
		if ranks[status] > ranks[best] {
			best = status
		}
	}
	return best
}

var ranks = map[api.ThreadStatus]int{
	api.ThreadError:                5,
	api.ThreadWaitingForInput:      4,
	api.ThreadWaitingForPermission: 3,
	api.ThreadWorking:              2,
	api.ThreadUnknown:              1,
	api.ThreadIdle:                 0,
}

// TurnObservation is what an Integration reports about a conversation's
// most recent turn. State empty means unknown: the Integration saw the
// turn but not how it stands. Zero timestamps mean not reported; ATC
// fills them in with the observation time, as best it knows.
type TurnObservation struct {
	// ProviderID is the provider's own turn id, private to the server;
	// empty for an Integration that has none (Claude). It binds a
	// submitted turn and re-matches a turn across a reconnect.
	ProviderID  string
	State       api.TurnState
	StartedAt   time.Time
	CompletedAt time.Time
	// Error is the provider's failure detail; recorded only for a failed
	// turn.
	Error string
}

// pendingTurn is a submitted turn not yet bound to a provider turn: the
// ATC id the submission returned, and the provider turn the thread held
// before it, so the provider re-reporting that older turn is not
// mistaken for the submitted one starting. Whether a submission is still
// pending is read off the record (pendingSubmission), never off this
// map alone, so a persist that fails leaves nothing to undo. Guarded by
// ops.
type pendingTurn struct {
	turnID          string
	priorProviderID string
}

// SubmitTurn records that ATC accepted a prompt submission for the
// thread: a fresh turn id, running from now, with the thread working —
// provisional facts the provider's first report of the turn replaces.
// The id is returned for the caller to wait on. A second submission
// while the first is unbound is refused (ErrTurnPending). The action
// that submits the prompt belongs to the Integration (ATC-289); this is
// the minting and binding seam it calls.
func (s *Service) SubmitTurn(ctx context.Context, id string) (string, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	entry, ok := s.view[id]
	var record store.ThreadRecord
	if ok {
		record = *entry
	}
	s.mu.Unlock()
	if !ok {
		return "", ErrNotFound
	}
	if pending, ok := s.pendingSubmission(record); ok {
		return "", fmt.Errorf("%w: %s", ErrTurnPending, pending.turnID)
	}
	now := s.now()
	prior := ""
	if record.Turn != nil {
		prior = record.Turn.ProviderID
	}
	record.Turn = &store.TurnRecord{ID: ids.NewLong(turnPrefix), State: string(api.TurnRunning), StartedAt: now}
	record.Status = string(api.ThreadWorking)
	record.StatusDetail = ""
	record.UpdatedAt = now
	updated, err := s.repository.Update(ctx, record)
	if err != nil {
		return "", err
	}
	if !updated {
		return "", ErrNotFound
	}
	s.pending[id] = pendingTurn{turnID: record.Turn.ID, priorProviderID: prior}
	s.mu.Lock()
	if entry, ok := s.view[id]; ok {
		*entry = record
	}
	s.mu.Unlock()
	s.hub.Publish(api.EventThreadUpdated, resource, id)
	return record.Turn.ID, nil
}

// pendingSubmission reports the record's unbound submitted turn, if its
// latest turn still is one. Caller holds ops.
func (s *Service) pendingSubmission(record store.ThreadRecord) (pendingTurn, bool) {
	pending, ok := s.pending[record.ID]
	if !ok || record.Turn == nil || record.Turn.ID != pending.turnID ||
		record.Turn.ProviderID != "" || record.Turn.State != string(api.TurnRunning) {
		return pendingTurn{}, false
	}
	return pending, true
}

// applyStatus folds one observation's status, status detail, and turn
// into the record, reporting whether anything changed. status "" claims
// nothing. A live status or a running turn is accepted only for a thread
// something holds (active): delayed evidence must not revive a
// conversation nothing displays. Caller holds ops.
func (s *Service) applyStatus(record *store.ThreadRecord, status api.ThreadStatus, detail string, turn *TurnObservation, at time.Time, active bool) bool {
	changed := false
	set := func(field *string, value string) {
		if *field != value {
			*field = value
			changed = true
		}
	}
	switch {
	case status == "":
	case isLive(status) && !active:
		s.logger.Debug("live status for an inactive thread ignored", "thread", record.ID, "status", status)
	default:
		// statusDetail rides an error status and nothing else.
		set(&record.Status, string(status))
		if status != api.ThreadError {
			detail = ""
		}
		set(&record.StatusDetail, detail)
	}
	if turn != nil {
		if turn.State == api.TurnRunning && !active {
			s.logger.Debug("running turn for an inactive thread ignored", "thread", record.ID)
		} else {
			changed = s.applyTurn(record, *turn, at) || changed
		}
	}
	// A submitted turn the provider has not started yet is not ended by
	// the provider's resting status: that status describes the thread
	// before the submission. Only a fault or a loss of observation ends it.
	_, pending := s.pendingSubmission(*record)
	return settleTurn(record, turn, at, pending) || changed
}

// applyTurn matches a reported turn to the record's latest turn and
// applies it: the same provider turn updates in place (the ATC id is
// kept across a reconnect) unless it already ended — an ended turn is
// final, whoever ended it; the first provider turn reported after a
// submission binds to the submitted id; otherwise a different turn
// replaces the held one, which ends unobserved. Without provider ids, a
// turn end can only belong to the running turn. Reports whether anything
// changed.
func (s *Service) applyTurn(record *store.ThreadRecord, o TurnObservation, at time.Time) bool {
	state := o.State
	if state == "" {
		state = api.TurnUnknown
	}
	current := ownTurn(record)
	if pending, ok := s.pendingSubmission(*record); ok && o.ProviderID != "" {
		if o.ProviderID == pending.priorProviderID {
			// The provider re-reporting the turn that preceded the
			// submission says nothing about the submitted one.
			return false
		}
		current.ProviderID = o.ProviderID
		updateTurn(current, o, state, at)
		return true
	}
	if current != nil {
		same := o.ProviderID != "" && o.ProviderID == current.ProviderID ||
			o.ProviderID == "" && current.ProviderID == "" && current.State == string(api.TurnRunning) && state != api.TurnRunning
		if same {
			if ended(current.State) {
				return false
			}
			return updateTurn(current, o, state, at)
		}
	}
	record.Turn = &store.TurnRecord{ID: ids.NewLong(turnPrefix), ProviderID: o.ProviderID}
	updateTurn(record.Turn, o, state, at)
	return true
}

// updateTurn writes a reported state and timestamps onto a turn: reported
// times win, a missing start is the observation time, and a turn that
// ends without a reported end time ends now. Reports whether the turn
// changed.
func updateTurn(turn *store.TurnRecord, o TurnObservation, state api.TurnState, at time.Time) bool {
	before := *turn
	turn.State = string(state)
	switch {
	case !o.StartedAt.IsZero():
		turn.StartedAt = o.StartedAt
	case turn.StartedAt.IsZero():
		turn.StartedAt = at
	}
	switch state {
	case api.TurnRunning, api.TurnUnknown:
		turn.CompletedAt = nil
	default:
		switch {
		case !o.CompletedAt.IsZero():
			completed := o.CompletedAt
			turn.CompletedAt = &completed
		case turn.CompletedAt == nil || before.State != turn.State:
			completed := at
			turn.CompletedAt = &completed
		}
	}
	turn.Error = ""
	if state == api.TurnFailed {
		turn.Error = o.Error
	}
	return !turnEqual(before, *turn)
}

// settleTurn ties a running turn to the thread's status: a faulted
// session fails it with the fault text, and a thread at rest or
// unobserved cannot have a turn running — it ended unobserved. A turn the
// observation itself reports running stands, as does a submitted turn
// the provider has not started yet. Reports whether it changed.
func settleTurn(record *store.ThreadRecord, reported *TurnObservation, at time.Time, pending bool) bool {
	if record.Turn == nil || record.Turn.State != string(api.TurnRunning) {
		return false
	}
	turn := ownTurn(record)
	switch api.ThreadStatus(record.Status) {
	case api.ThreadError:
		completed := at
		turn.State = string(api.TurnFailed)
		turn.CompletedAt = &completed
		if turn.Error == "" {
			turn.Error = record.StatusDetail
		}
		return true
	case api.ThreadIdle, api.ThreadUnknown:
		if pending || reported != nil && reported.State == api.TurnRunning {
			return false
		}
		turn.State = string(api.TurnUnknown)
		turn.CompletedAt = nil
		return true
	}
	return false
}

// coerceRecord coerces the claims only a live observation can back — a
// live status and a running turn — to unknown, reporting whether
// anything changed. Idle, error (with its detail), and finished turns
// persist as recorded.
func coerceRecord(record *store.ThreadRecord) bool {
	changed := false
	if isLive(api.ThreadStatus(record.Status)) {
		record.Status = string(api.ThreadUnknown)
		changed = true
	}
	if record.Turn != nil && record.Turn.State == string(api.TurnRunning) {
		turn := ownTurn(record)
		turn.State = string(api.TurnUnknown)
		turn.CompletedAt = nil
		changed = true
	}
	return changed
}

// ownTurn gives the record its own copy of its turn before a mutation:
// records are copied out of the view by value, and the turn pointer
// would otherwise write through to the view ahead of the persist.
func ownTurn(record *store.ThreadRecord) *store.TurnRecord {
	if record.Turn == nil {
		return nil
	}
	turn := *record.Turn
	if turn.CompletedAt != nil {
		completed := *turn.CompletedAt
		turn.CompletedAt = &completed
	}
	record.Turn = &turn
	return record.Turn
}

// ended reports a terminal turn state: completed, failed, or interrupted.
func ended(state string) bool {
	switch api.TurnState(state) {
	case api.TurnCompleted, api.TurnFailed, api.TurnInterrupted:
		return true
	}
	return false
}

func turnEqual(a, b store.TurnRecord) bool {
	if a.ID != b.ID || a.ProviderID != b.ProviderID || a.State != b.State || a.Error != b.Error || !a.StartedAt.Equal(b.StartedAt) {
		return false
	}
	if a.CompletedAt == nil || b.CompletedAt == nil {
		return a.CompletedAt == nil && b.CompletedAt == nil
	}
	return a.CompletedAt.Equal(*b.CompletedAt)
}

func isLive(status api.ThreadStatus) bool {
	switch status {
	case api.ThreadWorking, api.ThreadWaitingForInput, api.ThreadWaitingForPermission:
		return true
	}
	return false
}
