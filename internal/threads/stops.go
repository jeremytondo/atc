package threads

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/store"
)

// Stops (ATC-308): the explicit operation that stops a thread's work —
// the turn running and the submitted turn that has not started, the
// work blocked on a question or an approval included — while
// preserving the conversation for a later message. A stop is recorded
// durably with its scope before anything is dispatched, and while it is
// stopping the thread refuses messages, answers, and decisions, across
// restarts; a second stop meanwhile is the first. It resolves only on
// evidence: the provider reporting the conversation's execution session
// closed no earlier than the stop was accepted confirms it — stopped
// when something in scope was cut short, finished when the work had
// already ended — and the provider refusing the command fails it. A
// timeout or a lost connection resolves nothing. Confirmation closes
// the requests the stopped work was blocked on, records a submitted
// turn that never started as interrupted, and withdraws the messages
// still uncertain on the turns covered, so a replay cannot revive them.

const (
	stopPrefix = "stop-"
	// stopsKept bounds the stops retained per thread; a stop still
	// stopping is never pruned.
	stopsKept = 8
)

// ErrThreadStopping refuses new work — a message, an answer, a decision
// — while a stop is being confirmed on the thread; the message names
// the stop. The API layer maps it to 409.
var ErrThreadStopping = errors.New("a stop is being confirmed on the thread")

// ErrStopNotFound reports a stop id the thread has no record of; the
// API layer maps it to 404.
var ErrStopNotFound = errors.New("stop not found")

// StopRequest is what the Integration needs to dispatch a stop: the
// stop id the program-side identities derive from, and when it was
// accepted. Dispatch is false when nothing needs sending — the stop was
// already committed, or resolved without one.
type StopRequest struct {
	StopID    string
	CreatedAt time.Time
	Dispatch  bool
}

// stopping finds the thread's unresolved stop, nil for none. Caller
// holds mu.
func (s *Service) stopping(threadID string) *store.ThreadStopRecord {
	for _, stop := range s.stops[threadID] {
		if stop.State == string(api.StopStopping) {
			return stop
		}
	}
	return nil
}

// RecoverStop finds the stop a submission recovers instead of starting
// one: the stop recorded under the client's key, in whatever state, or
// else the stop still stopping on the thread. It reports whether there
// is one; the request says whether it still needs dispatching. Nothing
// is recorded.
func (s *Service) RecoverStop(threadID, key string) (StopRequest, api.ThreadStop, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.view[threadID]; !ok {
		return StopRequest{}, api.ThreadStop{}, false, ErrNotFound
	}
	existing := s.stopping(threadID)
	if key != "" {
		for _, stop := range s.stops[threadID] {
			if stop.Key == key {
				existing = stop
			}
		}
	}
	if existing == nil {
		return StopRequest{}, api.ThreadStop{}, false, nil
	}
	stop := *existing
	dispatch := stop.State == string(api.StopStopping) && stop.Delivery == string(api.MessageUncertain)
	return StopRequest{StopID: stop.ID, CreatedAt: stop.CreatedAt, Dispatch: dispatch}, stopFrom(stop), true, nil
}

// BeginStop accepts a stop on the thread and returns what the
// Integration needs to dispatch it. A stop recorded under the client's
// key, or one still stopping, is returned as it stands (RecoverStop) —
// dispatched again only if its delivery was uncertain — so a retry
// recovers the original operation and never stops later work. The
// scope is what the thread holds at this instant: the turn running, the
// turn pending, the status; a thread with nothing running, nothing
// pending, and nothing blocked resolves at once as finished, with
// nothing sent. Publishes thread.updated when a stop starts.
func (s *Service) BeginStop(ctx context.Context, threadID, key string) (StopRequest, api.ThreadStop, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	record, ok := s.snapshot(threadID)
	if !ok {
		return StopRequest{}, api.ThreadStop{}, ErrNotFound
	}
	if req, stop, found, err := s.RecoverStop(threadID, key); err != nil || found {
		return req, stop, err
	}
	s.mu.Lock()
	blocked := len(s.pendingApprovals(threadID))+len(s.pendingInputs(threadID)) > 0
	s.mu.Unlock()
	// Millisecond precision: the provider's evidence carries this instant
	// back as it read it from the command, and must compare equal.
	now := s.now().Truncate(time.Millisecond)
	stop := store.ThreadStopRecord{
		ThreadID: threadID, Key: key, State: string(api.StopStopping), Delivery: string(api.MessageUncertain),
		ScopeStatus: record.Status, CreatedAt: now, UpdatedAt: now,
	}
	if record.Turn != nil && record.Turn.State == string(api.TurnRunning) {
		stop.ScopeTurn = record.Turn.ID
	}
	if record.Pending != nil {
		stop.ScopePending = record.Pending.ID
	}
	status := api.ThreadStatus(record.Status)
	if stop.ScopeTurn == "" && stop.ScopePending == "" && !blocked && (status == api.ThreadIdle || status == api.ThreadError) {
		stop.State, stop.Delivery, stop.ResolvedAt = string(api.StopFinished), string(api.MessageAccepted), &now
		stop.Detail = "nothing was running or submitted on the thread; no stop was sent"
	}
	for {
		stop.ID = ids.NewLong(stopPrefix)
		inserted, err := s.repository.SubmitStop(ctx, stop, stopsKept)
		if err != nil {
			return StopRequest{}, api.ThreadStop{}, err
		}
		if inserted {
			break
		}
	}
	s.mu.Lock()
	kept := stop
	s.stops[threadID] = trimStops(append(s.stops[threadID], &kept))
	s.mu.Unlock()
	if stop.State == string(api.StopStopping) {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return StopRequest{StopID: stop.ID, CreatedAt: now, Dispatch: stop.State == string(api.StopStopping)}, stopFrom(stop), nil
}

// trimStops keeps the newest stopsKept stops and every unresolved one,
// mirroring the repository's prune.
func trimStops(stops []*store.ThreadStopRecord) []*store.ThreadStopRecord {
	excess := len(stops) - stopsKept
	if excess <= 0 {
		return stops
	}
	kept := stops[:0:0]
	for _, stop := range stops {
		if excess > 0 && stop.State != string(api.StopStopping) {
			excess--
			continue
		}
		kept = append(kept, stop)
	}
	return kept
}

// StopDelivered records the provider committing the stop command:
// delivery accepted, the outcome still awaited.
func (s *Service) StopDelivered(ctx context.Context, threadID, stopID string) (api.ThreadStop, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	stop, err := s.stop(threadID, stopID)
	if err != nil {
		return api.ThreadStop{}, err
	}
	if stop.Delivery != string(api.MessageAccepted) {
		stop.Delivery, stop.UpdatedAt = string(api.MessageAccepted), s.now()
		if err := s.persistStop(ctx, stop); err != nil {
			return api.ThreadStop{}, err
		}
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return stopFrom(stop), nil
}

// StopFailed records the provider refusing the stop for good: the stop
// fails with the provider's reason and the thread takes work again. A
// stop the provider never acknowledged is not failed — it stays
// stopping, for the same operation to reconcile.
func (s *Service) StopFailed(ctx context.Context, threadID, stopID, reason string) (api.ThreadStop, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	stop, err := s.stop(threadID, stopID)
	if err != nil {
		return api.ThreadStop{}, err
	}
	if stop.State == string(api.StopStopping) {
		now := s.now()
		stop.State, stop.Detail, stop.UpdatedAt, stop.ResolvedAt = string(api.StopFailed), reason, now, &now
		if err := s.persistStop(ctx, stop); err != nil {
			return api.ThreadStop{}, err
		}
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return stopFrom(stop), nil
}

// persistStop writes a stop's new state and installs it. Caller holds
// ops, not mu.
func (s *Service) persistStop(ctx context.Context, update store.ThreadStopRecord) error {
	if _, err := s.repository.UpdateStop(ctx, update); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.installStop(update)
	return nil
}

// installStop replaces the in-memory copy of a stop. Caller holds mu.
func (s *Service) installStop(update store.ThreadStopRecord) {
	for _, stop := range s.stops[update.ThreadID] {
		if stop.ID == update.ID {
			*stop = update
		}
	}
}

// stop copies one of the thread's stops out. Caller holds ops.
func (s *Service) stop(threadID, stopID string) (store.ThreadStopRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stopLocked(threadID, stopID)
}

// stopLocked is stop under mu.
func (s *Service) stopLocked(threadID, stopID string) (store.ThreadStopRecord, error) {
	if _, ok := s.view[threadID]; !ok {
		return store.ThreadStopRecord{}, ErrNotFound
	}
	for _, stop := range s.stops[threadID] {
		if stop.ID == stopID {
			return *stop, nil
		}
	}
	return store.ThreadStopRecord{}, fmt.Errorf("%w: %s", ErrStopNotFound, stopID)
}

// Stop serves one stop as it stands, resolved ones included while
// remembered.
func (s *Service) Stop(threadID, stopID string) (api.ThreadStop, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	stop, err := s.stopLocked(threadID, stopID)
	if err != nil {
		return api.ThreadStop{}, err
	}
	return stopFrom(stop), nil
}

// unresolvedStop is the thread's stopping stop for the wire, nil for
// none. Caller holds mu.
func (s *Service) unresolvedStop(threadID string) *api.ThreadStop {
	stop := s.stopping(threadID)
	if stop == nil {
		return nil
	}
	wire := stopFrom(*stop)
	return &wire
}

func stopFrom(record store.ThreadStopRecord) api.ThreadStop {
	stop := api.ThreadStop{
		ID: record.ID, ThreadID: record.ThreadID, Key: record.Key, State: api.StopState(record.State), Delivery: api.MessageDelivery(record.Delivery),
		Detail: record.Detail, CreatedAt: record.CreatedAt,
	}
	if record.ResolvedAt != nil {
		at := *record.ResolvedAt
		stop.ResolvedAt = &at
	}
	return stop
}

// confirmStop resolves the thread's stopping stop on evidence that the
// provider closed the work (reason says which): the outcome is decided
// from the scope — stopped when the turn running was cut short, the
// submitted turn never started, or the thread was live with no turn
// known; finished when the work had already ended — the submitted turn
// that never started is withdrawn from the pending slot, every pending
// approval and question closes, the answers still awaiting evidence are
// superseded, and the messages still uncertain on the covered turns are
// withdrawn. One transaction commits the record, the stop, the
// withdrawals, and the supersessions; the view and the request entries
// change only once it has. False means the thread is gone. Caller holds
// ops and publishes.
func (s *Service) confirmStop(ctx context.Context, record *store.ThreadRecord, stop store.ThreadStopRecord, at time.Time, reason string) (bool, error) {
	var notes []string
	cut := false
	var withdraw []string
	if stop.ScopeTurn != "" {
		withdraw = append(withdraw, stop.ScopeTurn)
		if turn := record.Turn; turn != nil && turn.ID == stop.ScopeTurn {
			switch {
			case turn.State != string(api.TurnCompleted):
				cut = true
				notes = append(notes, "turn "+turn.ID+" "+turn.State)
			case turn.CompletedAt != nil && !turn.CompletedAt.Before(stop.CreatedAt):
				// A completion the provider dates after the stop was
				// accepted is the stop's doing — a question abandoned ends
				// the turn as completed on some providers — not work that
				// finished on its own.
				cut = true
				notes = append(notes, "turn "+turn.ID+" ended after the stop was accepted")
			default:
				notes = append(notes, "turn "+turn.ID+" had completed")
			}
		}
	}
	if stop.ScopePending != "" {
		withdraw = append(withdraw, stop.ScopePending)
		switch {
		case record.Pending != nil && record.Pending.ID == stop.ScopePending:
			// Withdrawn, not promoted: a turn the provider never had must
			// not displace the provider's latest, whose id the next
			// submission is told apart from.
			record.Pending = nil
			s.forgetPrior(record.ID)
			cut = true
			notes = append(notes, "submitted turn "+stop.ScopePending+" withdrawn before it started")
		case record.Turn != nil && record.Turn.ID == stop.ScopePending && record.Turn.State != string(api.TurnCompleted):
			cut = true
			notes = append(notes, "turn "+record.Turn.ID+" "+record.Turn.State)
		}
	}
	if !cut && stop.ScopeTurn == "" && stop.ScopePending == "" && isLive(api.ThreadStatus(stop.ScopeStatus)) {
		cut = true
		notes = append(notes, "the thread was "+stop.ScopeStatus)
	}
	stop.State = string(api.StopFinished)
	if cut {
		stop.State = string(api.StopStopped)
	}
	stop.Detail = reason
	if len(notes) > 0 {
		stop.Detail += "; " + strings.Join(notes, "; ")
	}
	stop.UpdatedAt, stop.ResolvedAt = at, &at
	record.UpdatedAt = at
	supersession := "the work asking was stopped by " + stop.ID
	ok, err := s.repository.ResolveStop(ctx, *record, stop, withdraw, "withdrawn: its turn was stopped by "+stop.ID, supersession)
	if err != nil || !ok {
		return false, err
	}
	s.mu.Lock()
	if entry, ok := s.view[record.ID]; ok {
		*entry = *record
	}
	s.installStop(stop)
	s.closeApprovals(record.ID, at)
	s.closeInputs(record.ID, supersession, at)
	s.mu.Unlock()
	return true, nil
}
