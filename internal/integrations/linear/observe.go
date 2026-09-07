package linear

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
)

// Observation: one reading of a bound session's Thread, applied to
// everything the session tracks. The turns its submissions directed are
// judged one by one — only the exact turn counts; a newer one replacing
// it, the Thread vanishing, or T3 dropping the Thread are established
// limitations, never substitutes, and a turn ATC did not submit from
// Linear is never reported. The requests the agent is blocked on are
// presented once each, with the exact options offered, and closed with
// how they resolved once the Thread no longer lists them; the stops and
// answers still awaiting evidence are settled from the domain's word.
// Everything an observation changes is one write with the Linear
// updates it owes, keyed by turn, request, or submission, so a repeated
// reading owes nothing twice and a later turn or request is never
// suppressed by an earlier receipt. A dispatch in flight for the same
// session may write the same submission; the dispatch re-reads before
// it records, so an outcome settled here is never regressed.

// observe refetches a session's Thread and applies what it shows.
func (s *Service) observe(ctx context.Context, session store.LinearSession) {
	thread, readErr := s.threads.Get(session.ThreadID)
	if readErr != nil && !errors.Is(readErr, threads.ErrNotFound) {
		s.logger.Warn("linear: reading a watched thread", "session", session.ID, "thread", session.ThreadID, "error", readErr)
		return
	}
	subs, err := s.repo.Submissions(ctx, session.ID, true)
	if err != nil {
		s.logger.Error("linear: reading a session's submissions", "session", session.ID, "error", err)
		return
	}
	requests, err := s.repo.Requests(ctx, session.ID)
	if err != nil {
		s.logger.Error("linear: reading a session's requests", "session", session.ID, "error", err)
		return
	}
	now := s.now()
	var change store.LinearChange
	if readErr != nil {
		change = s.lost(session, subs, requests, now)
	} else {
		change = s.evaluate(ctx, session, thread, subs, requests, now)
	}
	if change.Session == nil && len(change.Submissions) == 0 && len(change.NewRequests) == 0 && len(change.Requests) == 0 && len(change.Rows) == 0 {
		return
	}
	if err := s.repo.Record(ctx, change); err != nil {
		s.logger.Error("linear: recording an observation", "session", session.ID, "error", err)
		return
	}
	if len(change.Rows) > 0 {
		s.wake(s.sendKick)
	}
}

// lost ends a session whose Thread is gone from ATC: every submission
// still awaiting an outcome is unrecoverable, every open request
// closed, and the person told once.
func (s *Service) lost(session store.LinearSession, subs []store.LinearSubmission, requests []presented, now time.Time) store.LinearChange {
	session.State, session.Outcome, session.UpdatedAt = stateDone, outcomeUnrecoverable, now
	change := store.LinearChange{Session: &session, Rows: []store.LinearOutboxRow{s.activity(session.ID, "gone", contentError, threadGone())}}
	for _, sub := range subs {
		sub.State, sub.Outcome, sub.Detail, sub.UpdatedAt = subDone, outcomeUnrecoverable, "the thread no longer exists in ATC", now
		if sub.Kind == kindStart {
			sub.Text = ""
		}
		change.Submissions = append(change.Submissions, sub)
	}
	for _, request := range requests {
		if request.State == requestOpen {
			request.State, request.UpdatedAt = requestShut, now
			change.Requests = append(change.Requests, request)
		}
	}
	return change
}

// evaluate decides what one reading of the Thread means for the session:
// the requests first — a question closing reads before the answer the
// turn then gives — then each active submission's outcome.
func (s *Service) evaluate(ctx context.Context, session store.LinearSession, thread api.Thread, subs []store.LinearSubmission, requests []presented, now time.Time) store.LinearChange {
	var change store.LinearChange
	s.evaluateRequests(ctx, &change, session, thread, subs, requests, now)
	for _, sub := range subs {
		if sub.State != subSent {
			continue
		}
		var updated *store.LinearSubmission
		var rows []store.LinearOutboxRow
		switch sub.Kind {
		case kindStart, kindMessage:
			updated, rows = s.evaluateTurn(sub, thread, now)
		case kindStop:
			updated, rows = s.evaluateStop(session, sub)
		case kindReply, kindChoice:
			updated, rows = s.evaluateAnswer(session, sub)
		}
		if updated != nil {
			updated.UpdatedAt = now
			change.Submissions = append(change.Submissions, *updated)
		}
		change.Rows = append(change.Rows, rows...)
	}
	return change
}

// evaluateTurn judges the turn a submission directed: the outcome to
// report, or nothing yet. A Thread at rest or unknown says nothing
// about the turn's end; a completed turn without its response is waited
// on for grace before the recovery is given up.
func (s *Service) evaluateTurn(sub store.LinearSubmission, thread api.Thread, now time.Time) (*store.LinearSubmission, []store.LinearOutboxRow) {
	links := thread.Links
	finish := func(outcome, kind, body string) (*store.LinearSubmission, []store.LinearOutboxRow) {
		sub.State, sub.Outcome = subDone, outcome
		return &sub, []store.LinearOutboxRow{s.activity(sub.SessionID, "turn/"+sub.TurnID+"/result", kind, body)}
	}
	if pending := thread.PendingTurn; pending != nil && pending.ID == sub.TurnID {
		// T3 has not started the turn yet. A Thread T3 no longer reports
		// never will; otherwise there is nothing to say.
		if thread.Archived {
			return finish(outcomeUnrecoverable, contentError, threadDropped(links))
		}
		return nil, nil
	}
	turn := thread.LatestTurn
	if turn == nil || turn.ID != sub.TurnID {
		return finish(outcomeUnrecoverable, contentError, turnReplaced(links))
	}
	switch turn.State {
	case api.TurnCompleted:
		if turn.Response != "" {
			return finish(outcomeResponded, contentResponse, turn.Response)
		}
		if sub.CompletedSeenAt == nil {
			seen := now
			sub.CompletedSeenAt = &seen
			return &sub, nil
		}
		if now.Sub(*sub.CompletedSeenAt) >= s.responseGrace {
			return finish(outcomeUnrecoverable, contentError, responseMissing(links))
		}
		return nil, nil
	case api.TurnFailed:
		return finish(outcomeFailed, contentError, turnFailed(turn.Error, links))
	case api.TurnInterrupted:
		return finish(outcomeInterrupted, contentError, turnInterrupted(links))
	}
	// Running or unknown: the turn is not over. A Thread T3 no longer
	// reports cannot finish one.
	if thread.Archived {
		return finish(outcomeUnrecoverable, contentError, threadDropped(links))
	}
	return nil, nil
}

// evaluateStop reports a stop once the domain resolved it.
func (s *Service) evaluateStop(session store.LinearSession, sub store.LinearSubmission) (*store.LinearSubmission, []store.LinearOutboxRow) {
	if sub.OperationID == "" {
		return nil, nil
	}
	stop, err := s.threads.Stop(session.ThreadID, sub.OperationID)
	if err != nil || stop.State == api.StopStopping {
		return nil, nil
	}
	rows := s.stopResolved(&sub, stop)
	return &sub, rows
}

// evaluateAnswer settles a reply or a choice from the domain's word on
// the answer: resolved or superseded ends it quietly — the request's
// closing says how it went — and a failure the request stays open
// through is reported, so the person can answer again.
func (s *Service) evaluateAnswer(session store.LinearSession, sub store.LinearSubmission) (*store.LinearSubmission, []store.LinearOutboxRow) {
	request, err := s.threads.InputRequest(session.ThreadID, sub.RequestID)
	if err != nil || request.Answer == nil {
		return nil, nil
	}
	switch request.Answer.State {
	case api.InputAnswerResolved:
		sub.State, sub.Outcome = subDone, outcomeResolved
		return &sub, nil
	case api.InputAnswerSuperseded:
		sub.State, sub.Outcome, sub.Detail = subDone, outcomeSuperseded, request.Answer.Detail
		return &sub, nil
	case api.InputAnswerFailed:
		sub.State, sub.Outcome, sub.Detail = subDone, outcomeFailed, request.Answer.Detail
		return &sub, []store.LinearOutboxRow{s.activity(session.ID, "sub/"+sub.ID+"/done", contentError, answerFailed(request.Answer.Detail))}
	}
	return nil, nil
}

// evaluateRequests adds to the change the requests the Thread shows
// pending and this session has not seen, presented with their options,
// and the open ones the Thread no longer lists, closed with how they
// resolved. A request closes only on a current reading of its kind: a
// Thread whose status is unknown (T3 away, or ATC just restarted) says
// nothing; one waiting with nothing of that kind listed may be mid-read
// (the domain installs approvals and questions in two steps). A request
// a submission from here is still being dispatched to closes once that
// dispatch has its outcome; one closed by a decision or an answer from
// here, from elsewhere, or by a stop is reported as such.
func (s *Service) evaluateRequests(ctx context.Context, change *store.LinearChange, session store.LinearSession, thread api.Thread, active []store.LinearSubmission, requests []presented, now time.Time) {
	known := make(map[string]bool, len(requests))
	for _, request := range requests {
		known[request.ID] = true
	}
	pending := map[string]bool{}
	for _, approval := range thread.Approvals {
		pending[approval.ID] = true
		if known[approval.ID] {
			continue
		}
		options := approvalOptions(approval)
		change.NewRequests = append(change.NewRequests, presented{ID: approval.ID, SessionID: session.ID, Kind: kindApproval, Options: encodeOptions(options), State: requestOpen, CreatedAt: now, UpdatedAt: now})
		change.Rows = append(change.Rows, s.elicitation(session.ID, "req/"+approval.ID+"/ask", approvalAsk(approval), selectOptions(options)))
	}
	for _, request := range thread.InputRequests {
		pending[request.ID] = true
		if known[request.ID] {
			continue
		}
		kind, options := kindInput, inputOptions(request)
		if request.Unanswerable != "" {
			kind, options = kindNotice, nil
		}
		change.NewRequests = append(change.NewRequests, presented{ID: request.ID, SessionID: session.ID, Kind: kind, Options: encodeOptions(options), State: requestOpen, CreatedAt: now, UpdatedAt: now})
		change.Rows = append(change.Rows, s.elicitation(session.ID, "req/"+request.ID+"/ask", inputAsk(request, thread.Links), selectOptions(options)))
	}
	if thread.Status == api.ThreadUnknown {
		return
	}
	waiting := thread.Status == api.ThreadWaitingForInput || thread.Status == api.ThreadWaitingForPermission
	current := map[string]bool{
		kindApproval: len(thread.Approvals) > 0 || !waiting,
		kindInput:    len(thread.InputRequests) > 0 || !waiting,
	}
	current[kindNotice] = current[kindInput]
	var all []store.LinearSubmission
	for _, request := range requests {
		if request.State != requestOpen || pending[request.ID] || !current[request.Kind] {
			continue
		}
		if slices.ContainsFunc(active, func(sub store.LinearSubmission) bool { return sub.RequestID == request.ID }) {
			continue
		}
		var body string
		switch request.Kind {
		case kindNotice:
		case kindApproval:
			if all == nil {
				var err error
				if all, err = s.repo.Submissions(ctx, session.ID, false); err != nil {
					s.logger.Error("linear: reading a session's submissions", "session", session.ID, "error", err)
					return
				}
			}
			decided := ""
			if i := slices.IndexFunc(all, func(sub store.LinearSubmission) bool {
				return sub.Kind == kindDecision && sub.RequestID == request.ID && sub.Outcome == outcomeDelivered
			}); i >= 0 {
				decided = label([]presented{request}, request.ID, target{decision: all[i].Value})
			}
			body = requestClosed(kindApproval, api.ThreadInputRequest{}, decided)
		default:
			resolved, err := s.threads.InputRequest(session.ThreadID, request.ID)
			if err != nil && !errors.Is(err, threads.ErrInputNotFound) {
				s.logger.Warn("linear: reading a resolved request", "session", session.ID, "request", request.ID, "error", err)
				continue
			}
			body = requestClosed(kindInput, resolved, "")
		}
		request.State, request.UpdatedAt = requestShut, now
		change.Requests = append(change.Requests, request)
		if body != "" {
			change.Rows = append(change.Rows, s.activity(session.ID, "req/"+request.ID+"/closed", contentThought, body))
		}
	}
}
