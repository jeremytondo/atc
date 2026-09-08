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

// The status ranking and the latest-turn model (ATC-301, ATC-307). A
// thread answers two questions — what is the agent doing now (status)
// and how did its most recent turn end (latestTurn) — and neither field
// carries the other's meaning: a failed turn leaves the thread idle, and
// a faulted session (status error, with the provider's explanation in
// statusDetail) is not a turn outcome. A third slot, the pending turn,
// holds a submission the provider has not started yet: the id a thread
// create returned, kept apart from the latest turn so a turn the
// provider is still running is never displaced and its end never lost.
// Both are decided here from the normalized evidence Integrations
// report; no Integration ranks or guesses on its own.

const turnPrefix = "turn-"

// ErrTurnPending refuses a second submission while the first is still
// pending — one unstarted submission per thread. The caller retries once
// the provider has reported the turn starting.
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
	// Response is the provider-identified final assistant message of the
	// turn (ATC-303), reported only with an ended state — never alongside
	// a running one. Empty means none recovered this time, which never
	// clears a response already recorded. An Integration that recovers
	// the response after the fact reports it through ObserveTurnResponse.
	Response string
}

// SubmitTurn records that ATC accepted a prompt submission for the
// thread that starts a new turn: a pending turn with a fresh id, and the
// thread provisionally working — facts the provider's first report of a
// new turn replaces by binding that turn to the id. The id is returned
// for the caller to wait on. A second submission while the first is
// pending is refused (ErrTurnPending). The action that submits the
// prompt belongs to the Integration (ATC-289); this is the minting and
// binding seam it calls.
func (s *Service) SubmitTurn(ctx context.Context, id string) (string, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	record, ok := s.snapshot(id)
	if !ok {
		return "", ErrNotFound
	}
	if record.Pending != nil {
		return "", fmt.Errorf("%w: %s", ErrTurnPending, record.Pending.ID)
	}
	s.mintPending(&record)
	if err := s.persist(ctx, record); err != nil {
		return "", err
	}
	s.hub.Publish(api.EventThreadUpdated, resource, id)
	return record.Pending.ID, nil
}

// mintPending puts a fresh pending turn on the record and marks the
// thread working, remembering the status it replaces so a rejected
// submission can restore it. Caller holds ops.
func (s *Service) mintPending(record *store.ThreadRecord) {
	now := s.now()
	prior := ""
	if record.Turn != nil {
		prior = record.Turn.ProviderID
	}
	record.Pending = &store.PendingTurnRecord{ID: ids.NewLong(turnPrefix), Prior: prior, SubmittedAt: now}
	s.mu.Lock()
	s.priorStatus[record.ID] = priorStatus{status: record.Status, detail: record.StatusDetail}
	s.mu.Unlock()
	record.Status = string(api.ThreadWorking)
	record.StatusDetail = ""
	record.UpdatedAt = now
}

// priorStatus is the status a submission provisionally replaced, kept in
// memory for the request that may revert it; after a restart the
// provisional status has been coerced anyway.
type priorStatus struct {
	status, detail string
}

// snapshot copies a thread's record out of the view. Caller holds ops.
func (s *Service) snapshot(id string) (store.ThreadRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, ok := s.view[id]
	if !ok {
		return store.ThreadRecord{}, false
	}
	return *entry, true
}

// persist writes a record and installs it in the view; ErrNotFound when
// the row is gone. Caller holds ops.
func (s *Service) persist(ctx context.Context, record store.ThreadRecord) error {
	updated, err := s.repository.Update(ctx, record)
	if err != nil {
		return err
	}
	if !updated {
		return ErrNotFound
	}
	s.mu.Lock()
	if entry, ok := s.view[record.ID]; ok {
		*entry = record
	}
	s.mu.Unlock()
	return nil
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
	changed = settleTurn(record, turn, at) || changed
	if record.Pending == nil {
		s.forgetPrior(record.ID)
	}
	return changed
}

// applyTurn matches a reported turn to the record's turns and applies
// it: the first provider turn reported after a submission that is not
// the turn the thread held at submission binds to the pending id and
// becomes the latest turn; otherwise the same provider turn as the
// latest updates in place (the ATC id is kept across a reconnect) unless
// it already ended — an ended turn is final, whoever ended it — and a
// different turn replaces the latest, which ends unobserved. Without
// provider ids, a turn end can only belong to the running turn, and a
// pending submission binds nothing. Reports whether anything changed.
func (s *Service) applyTurn(record *store.ThreadRecord, o TurnObservation, at time.Time) bool {
	state := o.State
	if state == "" {
		state = api.TurnUnknown
	}
	if pending := record.Pending; pending != nil && o.ProviderID != "" && o.ProviderID != pending.Prior {
		record.Turn = &store.TurnRecord{ID: pending.ID, ProviderID: o.ProviderID}
		updateTurn(record.Turn, o, state, at)
		record.Pending = nil
		return true
	}
	if current := ownTurn(record); current != nil {
		same := o.ProviderID != "" && o.ProviderID == current.ProviderID ||
			o.ProviderID == "" && current.ProviderID == "" && current.State == string(api.TurnRunning) && state != api.TurnRunning
		if same {
			if ended(current.State) {
				// An ended turn is final, whoever ended it; only its
				// response can still arrive.
				return adoptResponse(current, state, o.Response)
			}
			return updateTurn(current, o, state, at)
		}
	}
	record.Turn = &store.TurnRecord{ID: ids.NewLong(turnPrefix), ProviderID: o.ProviderID}
	updateTurn(record.Turn, o, state, at)
	return true
}

// forgetPrior drops the status a submission replaced, once the
// submission can no longer be reverted.
func (s *Service) forgetPrior(threadID string) {
	s.mu.Lock()
	delete(s.priorStatus, threadID)
	s.mu.Unlock()
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
	// A response belongs to a turn that ended; a running or unknown turn
	// never carries one.
	turn.Response = ""
	if ended(string(state)) {
		turn.Response = o.Response
	}
	return !turnEqual(before, *turn)
}

// adoptResponse records a response reported for a turn that already
// ended — a non-empty text, reported with an ended state, that differs
// from the one recorded. Reports whether the turn changed.
func adoptResponse(turn *store.TurnRecord, state api.TurnState, response string) bool {
	if !ended(string(state)) || response == "" || response == turn.Response {
		return false
	}
	turn.Response = response
	return true
}

// ObserveTurnResponse records a final assistant message an Integration
// recovered after the turn had ended (ATC-303). The read that recovers
// it finishes later than the observation that settled the turn, so it
// arrives on its own rather than on a turn observation — which would
// claim the turn as the latest — and applies only when the thread's
// latest turn is the named provider turn and has ended. A late result
// for an earlier turn, a turn running or unknown, an unknown thread, and
// an empty text are dropped; a text that changes the recorded response
// publishes thread.updated, the same text again publishes nothing.
func (s *Service) ObserveTurnResponse(ctx context.Context, threadID, providerTurnID, response string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	record, known := s.snapshot(threadID)
	if !known {
		s.logger.Debug("turn response for an unknown thread dropped", "thread", threadID)
		return nil
	}
	if record.Turn == nil || providerTurnID == "" || record.Turn.ProviderID != providerTurnID {
		s.logger.Debug("turn response for a turn no longer latest dropped", "thread", threadID)
		return nil
	}
	if !adoptResponse(ownTurn(&record), api.TurnState(record.Turn.State), response) {
		return nil
	}
	at := s.now()
	record.LastEvidenceAt = &at
	record.UpdatedAt = at
	if err := s.persist(ctx, record); err != nil {
		if errors.Is(err, ErrNotFound) {
			s.logger.Debug("turn response for a deleted thread dropped", "thread", threadID)
			return nil
		}
		return err
	}
	s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	return nil
}

// settleTurn ties the turns to the thread's status: a faulted session
// fails the running turn with the fault text and fails a pending
// submission the same way — the provider cannot start it — and a thread
// at rest or unobserved cannot have a turn running: it ended unobserved.
// A turn the observation itself reports running stands. A pending
// submission is untouched by rest: that status describes the thread
// before the submission. Reports whether anything changed.
func settleTurn(record *store.ThreadRecord, reported *TurnObservation, at time.Time) bool {
	changed := false
	if record.Turn != nil && record.Turn.State == string(api.TurnRunning) {
		turn := ownTurn(record)
		switch api.ThreadStatus(record.Status) {
		case api.ThreadError:
			completed := at
			turn.State = string(api.TurnFailed)
			turn.CompletedAt = &completed
			if turn.Error == "" {
				turn.Error = record.StatusDetail
			}
			changed = true
		case api.ThreadIdle, api.ThreadUnknown:
			if reported == nil || reported.State != api.TurnRunning {
				turn.State = string(api.TurnUnknown)
				turn.CompletedAt = nil
				changed = true
			}
		}
	}
	if pending := record.Pending; pending != nil && api.ThreadStatus(record.Status) == api.ThreadError {
		completed := at
		record.Turn = &store.TurnRecord{
			ID: pending.ID, State: string(api.TurnFailed), StartedAt: pending.SubmittedAt, CompletedAt: &completed, Error: record.StatusDetail,
		}
		record.Pending = nil
		changed = true
	}
	return changed
}

// coerceRecord coerces the claims only a live observation can back — a
// live status and a running turn — to unknown, reporting whether
// anything changed. Idle, error (with its detail), finished turns, and a
// pending submission persist as recorded: the submission still binds to
// the provider's first new turn once observation resumes (ATC-302).
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
	return api.TurnState(state).Ended()
}

func turnEqual(a, b store.TurnRecord) bool {
	if a.ID != b.ID || a.ProviderID != b.ProviderID || a.State != b.State || a.Error != b.Error || a.Response != b.Response || !a.StartedAt.Equal(b.StartedAt) {
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
