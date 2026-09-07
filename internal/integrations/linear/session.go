package linear

import (
	"context"
	"errors"
	"fmt"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/threads"
)

// The session worker: one goroutine per session at a time, driving
// what the session owes the program — its start while the session is
// accepted, then its submissions in the order Linear sent them — through
// the application coordinator, and recording each outcome with the
// Linear updates it owes in one write. Every dispatch carries an
// identity derived from the submission's, so a retry after a lost
// answer, a crash, or a restart presents the same operation and never a
// second one; a dispatch the program cannot take yet backs off,
// bounded, and is tried again on the program's return. A refusal by the
// shared capability is final for the submission and reported as it is:
// nothing here reinterprets, re-targets, or queues.

// drive runs a session's owed work until nothing is due, reporting
// whether anything was done.
func (s *Service) drive(ctx context.Context, sessionID string, force bool) bool {
	progressed := false
	for ctx.Err() == nil {
		session, err := s.repo.GetSession(ctx, sessionID)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Error("linear: reading a session to drive it", "session", sessionID, "error", err)
			}
			return progressed
		}
		subs, err := s.repo.Submissions(ctx, sessionID, true)
		if err != nil {
			if ctx.Err() == nil {
				s.logger.Error("linear: reading a session's submissions", "session", sessionID, "error", err)
			}
			return progressed
		}
		now := s.now()
		switch session.State {
		case stateAccepted, stateStarting:
			if session.State == stateStarting && session.ThreadID != "" {
				// T3 may hold the conversation; the reconcile resolves it.
				return progressed
			}
			if stop := firstNeeding(subs, kindStop); stop != nil {
				// Stopped before anything was sent: the start is owed no
				// more, and nothing here ever starts it.
				s.cancelStart(ctx, session, subs, *stop)
				return true
			}
			start := firstNeeding(subs, kindStart)
			if start == nil || !force && now.Before(start.NextAttemptAt) {
				return progressed
			}
			progressed = true
			if !s.start(ctx, session, *start) {
				return progressed
			}
		case stateBound:
			next := firstNeeding(subs, "")
			if next == nil || next.Kind == kindStart {
				return progressed
			}
			if !force && now.Before(next.NextAttemptAt) {
				return progressed
			}
			progressed = true
			if !s.dispatch(ctx, session, *next) {
				return progressed
			}
		default:
			return progressed
		}
		// One attempt per submission per pass: what follows sees the
		// fresh rows, and a backoff just recorded ends the pass.
		force = false
	}
	return progressed
}

// firstNeeding is the oldest submission still owed a dispatch — pending,
// or sent with its delivery uncertain — of the kind, or of any kind for
// "". Order is Linear's order.
func firstNeeding(subs []store.LinearSubmission, kind string) *store.LinearSubmission {
	for i := range subs {
		sub := &subs[i]
		if kind != "" && sub.Kind != kind {
			continue
		}
		if sub.State == subPending || sub.State == subSent && sub.Delivery == string(api.MessageUncertain) {
			return sub
		}
	}
	return nil
}

// cancelStart ends a session whose stop arrived before its start was
// sent: the session is done, the start and the stop are done, and
// every other submission is cancelled — nothing runs after a stop.
func (s *Service) cancelStart(ctx context.Context, session store.LinearSession, subs []store.LinearSubmission, stop store.LinearSubmission) {
	now := s.now()
	session.State, session.Outcome, session.UpdatedAt = stateDone, outcomeCancelled, now
	var updates []store.LinearSubmission
	for _, sub := range subs {
		sub.State, sub.UpdatedAt = subDone, now
		if sub.ID == stop.ID {
			sub.Outcome = outcomeStopped
		} else {
			sub.Outcome, sub.Detail = outcomeCancelled, "stopped before the conversation started"
		}
		if sub.Kind == kindStart {
			sub.Text = ""
		}
		updates = append(updates, sub)
	}
	change := store.LinearChange{Session: &session, Submissions: updates, Rows: []store.LinearOutboxRow{s.activity(session.ID, "sub/"+stop.ID+"/done", contentResponse, startCancelled())}}
	if err := s.repo.Record(ctx, change); err != nil {
		s.logger.Error("linear: recording a cancelled start", "session", session.ID, "error", err)
		return
	}
	s.logger.Info("linear: start cancelled by a stop", "session", session.ID)
	s.wake(s.sendKick)
}

// start records the attempt, asks the coordinator for the Thread with
// the fixed profile and the start submission's prompt, and records how
// it went: bound (owed the links), accepted again with a backoff on the
// start when the program could not take it, or done — refused for a
// definite reason, or bound and uncertain when T3 may have the
// conversation. The ids are persisted before dispatch, so a crash in
// between is recognized at the next boot as a start that may have been
// submitted. Reports whether the session may have more to do.
func (s *Service) start(ctx context.Context, session store.LinearSession, start store.LinearSubmission) bool {
	setup, ok := s.currentSetup()
	if !ok {
		// Nothing to start against; the row stays accepted until the
		// operator finishes setup.
		return false
	}
	session.State, session.ThreadID, session.UpdatedAt = stateStarting, "", s.now()
	if err := s.repo.Record(ctx, store.LinearChange{Session: &session}); err != nil {
		s.logger.Error("linear: recording a start attempt", "session", session.ID, "error", err)
		return false
	}
	params := api.ThreadCreateParams{
		IntegrationID: profileIntegration,
		Agent:         profileAgent,
		ProjectID:     setup.ProjectID,
		Prompt:        start.Text,
		Model:         profileModel,
		Options:       []api.ThreadOption{{ID: "reasoningEffort", Value: profileEffort}},
	}
	thread, err := s.coordinator.StartThread(ctx, params, func(threadID, turnID string) error {
		session.ThreadID = threadID
		session.UpdatedAt = s.now()
		start.State, start.TurnID, start.OperationID, start.UpdatedAt = subSent, turnID, turnID, session.UpdatedAt
		return s.repo.Record(ctx, store.LinearChange{Session: &session, Submissions: []store.LinearSubmission{start}})
	})
	if err != nil && ctx.Err() != nil {
		// Shutdown mid-start: no outcome is known. The rows stay as
		// recorded — owed again if nothing was dispatched, uncertain if
		// the thread was — for the next boot to pick up.
		return false
	}
	now := s.now()
	session.UpdatedAt, start.UpdatedAt = now, now
	var owed []store.LinearOutboxRow
	more := true
	switch {
	case err == nil:
		session.State = stateBound
		start.State, start.Delivery, start.Text = subSent, string(api.MessageAccepted), ""
		owed = append(owed, s.activity(session.ID, "started", contentThought, started(thread.Links)))
		if thread.Links != nil {
			owed = append(owed, s.links(session.ID, *thread.Links))
		}
		s.logger.Info("linear: thread started", "session", session.ID, "thread", thread.ID, "turn", start.TurnID)
	case errors.Is(err, integrations.ErrThreadCreationUncertain):
		// T3 may hold the conversation; the coordinator kept the record,
		// and T3's report of the thread will find it. Watch it, and say
		// what is known.
		session.State = stateBound
		start.State, start.Delivery, start.Text = subSent, string(api.MessageUncertain), ""
		owed = append(owed, s.activity(session.ID, "uncertain", contentThought, uncertain()))
		s.logger.Warn("linear: thread start uncertain", "session", session.ID, "thread", session.ThreadID, "error", err)
	case errors.Is(err, integrations.ErrNotConnected):
		// Nothing was dispatched: the start is owed again once T3 is
		// back — on its return, or after a bounded wait — and the user
		// hears why nothing has happened yet, once.
		session.State, session.ThreadID = stateAccepted, ""
		start.State, start.TurnID, start.OperationID = subPending, "", ""
		s.later(&start, err)
		owed = append(owed, s.activity(session.ID, "waiting-t3", contentThought, waitingForT3()))
		more = false
	default:
		session.State, session.Outcome = stateDone, outcomeFailed
		start.State, start.Outcome, start.Detail, start.Text = subDone, outcomeFailed, err.Error(), ""
		owed = append(owed, s.activity(session.ID, "failed", contentError, startFailed(err)))
		more = false
		s.logger.Warn("linear: thread start failed", "session", session.ID, "error", err)
	}
	if err := s.repo.Record(ctx, store.LinearChange{Session: &session, Submissions: []store.LinearSubmission{start}, Rows: owed}); err != nil {
		s.logger.Error("linear: recording a start outcome", "session", session.ID, "error", err)
		return false
	}
	s.wake(s.sendKick)
	return more
}

// dispatch sends one submission through the shared capability it names
// and records the outcome — unless an observation settled the
// submission meanwhile (the turn a retried message directed ended, say):
// a settled outcome is never overwritten by a dispatch's view of it.
// Reports whether the worker should go on to the next submission: false
// after a backoff, a refusal that ends the session, or a failure to
// record.
func (s *Service) dispatch(ctx context.Context, session store.LinearSession, sub store.LinearSubmission) bool {
	var rows []store.LinearOutboxRow
	switch sub.Kind {
	case kindMessage:
		rows = s.dispatchMessage(ctx, session, &sub)
	case kindReply, kindChoice:
		rows = s.dispatchAnswer(ctx, session, &sub)
	case kindDecision:
		rows = s.dispatchDecision(ctx, session, &sub)
	case kindStop:
		rows = s.dispatchStop(ctx, session, &sub)
	default:
		rows = s.refuse(&sub, "sub/"+sub.ID+"/refused", "This", fmt.Errorf("unknown submission kind %q", sub.Kind))
	}
	if ctx.Err() != nil {
		// Shutdown mid-dispatch: no outcome is known; the row stands as
		// it was, for the next boot to retry under the same identity.
		return false
	}
	if settled, err := s.settled(ctx, sub); err != nil || settled {
		return err == nil
	}
	sub.UpdatedAt = s.now()
	if err := s.repo.Record(ctx, store.LinearChange{Submissions: []store.LinearSubmission{sub}, Rows: rows}); err != nil {
		s.logger.Error("linear: recording a dispatch outcome", "session", session.ID, "submission", sub.ID, "error", err)
		return false
	}
	if len(rows) > 0 {
		s.wake(s.sendKick)
	}
	return sub.State == subDone || sub.State == subSent && sub.Delivery != string(api.MessageUncertain)
}

// settled reports whether a submission is done on the record now.
func (s *Service) settled(ctx context.Context, sub store.LinearSubmission) (bool, error) {
	active, err := s.repo.Submissions(ctx, sub.SessionID, true)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("linear: re-reading a submission after its dispatch", "session", sub.SessionID, "submission", sub.ID, "error", err)
		}
		return false, err
	}
	for _, current := range active {
		if current.ID == sub.ID {
			return false, nil
		}
	}
	return true, nil
}

// refuse ends a submission the shared capability would not take, with
// the reason as it gave it. The purpose keys the report; what names the
// submission in the text.
func (s *Service) refuse(sub *store.LinearSubmission, purpose, what string, err error) []store.LinearOutboxRow {
	sub.State, sub.Outcome, sub.Detail = subDone, outcomeRefused, err.Error()
	return []store.LinearOutboxRow{s.activity(sub.SessionID, purpose, contentError, submissionRefused(what, err))}
}

// later records a dispatch to try again — the program could not take
// it, or did not answer — with a bounded backoff; the submission keeps
// its state, so an uncertain delivery stays uncertain.
func (s *Service) later(sub *store.LinearSubmission, err error) {
	sub.Attempts++
	sub.NextAttemptAt = s.now().Add(s.backoff(sub.Attempts))
	sub.Detail = err.Error()
	s.logger.Info("linear: dispatch deferred", "session", sub.SessionID, "submission", sub.ID, "kind", sub.Kind, "attempt", sub.Attempts, "error", err)
}

// final reports the refusals the domains and Integrations make for
// good; any other failure — the program not connected or not answering,
// a store hiccup — earns a later attempt under the same identity.
func final(err error) bool {
	for _, known := range []error{
		threads.ErrNotFound, threads.ErrMessageInvalid, threads.ErrMessageRejected, threads.ErrMessageWithdrawn, threads.ErrTurnPending, threads.ErrThreadStopping,
		threads.ErrApprovalNotFound, threads.ErrApprovalResolved, threads.ErrDecisionNotOffered, threads.ErrDecisionPending,
		threads.ErrInputNotFound, threads.ErrInputResolved, threads.ErrInputUnanswerable, threads.ErrAnswerInvalid, threads.ErrAnswerPending,
		threads.ErrStopNotFound,
		integrations.ErrMessageRejected, integrations.ErrDecisionRejected, integrations.ErrAnswerRejected, integrations.ErrStopRejected,
		integrations.ErrThreadSendUnsupported, integrations.ErrThreadDecideUnsupported, integrations.ErrThreadAnswerUnsupported, integrations.ErrThreadStopUnsupported,
	} {
		if errors.Is(err, known) {
			return true
		}
	}
	return false
}

// dispatchMessage sends a message under the submission's own key: the
// same key again recovers the same message, so a lost answer is
// reconciled rather than sent twice. Delivered, the message's turn is
// watched for the result.
func (s *Service) dispatchMessage(ctx context.Context, session store.LinearSession, sub *store.LinearSubmission) []store.LinearOutboxRow {
	message, err := s.coordinator.SendMessage(ctx, session.ThreadID, api.ThreadMessageParams{Text: sub.Text, Key: sub.ID})
	switch {
	case err == nil:
		sub.State, sub.OperationID, sub.TurnID, sub.Delivery, sub.Detail = subSent, message.ID, message.TurnID, string(message.Delivery), ""
		if message.Delivery != api.MessageAccepted {
			s.later(sub, errors.New("delivery uncertain"))
			return nil
		}
		return []store.LinearOutboxRow{s.activity(session.ID, "sub/"+sub.ID+"/sent", contentThought, messageSent())}
	case !final(err):
		s.later(sub, err)
		return nil
	default:
		return s.refuse(sub, "sub/"+sub.ID+"/refused", "Your message", err)
	}
}

// dispatchAnswer sends a reply or a choice to the exact request it was
// addressed to. A request no longer open refuses it, and the person is
// told the reply went nowhere — never silently turned into a message.
func (s *Service) dispatchAnswer(ctx context.Context, session store.LinearSession, sub *store.LinearSubmission) []store.LinearOutboxRow {
	params := api.InputAnswerParams{Reply: sub.Text}
	if sub.Kind == kindChoice {
		params = api.InputAnswerParams{Answers: []api.QuestionAnswer{{QuestionID: sub.QuestionID, Choices: []string{sub.Value}}}}
	}
	request, err := s.coordinator.AnswerInput(ctx, session.ThreadID, sub.RequestID, params)
	switch {
	case err == nil:
		sub.State, sub.Detail = subSent, ""
		if request.Status != api.InputRequestResolved && (request.Answer == nil || request.Answer.Delivery != api.MessageAccepted) {
			sub.Delivery = string(api.MessageUncertain)
			s.later(sub, errors.New("delivery uncertain"))
			return nil
		}
		sub.Delivery = string(api.MessageAccepted)
		text := replySent()
		if sub.Kind == kindChoice {
			requests, _ := s.repo.Requests(ctx, session.ID)
			text = choiceSent(label(requests, sub.RequestID, target{questionID: sub.QuestionID, choice: sub.Value}))
		}
		return []store.LinearOutboxRow{s.activity(session.ID, "sub/"+sub.ID+"/sent", contentThought, text)}
	case !final(err):
		s.later(sub, err)
		return nil
	case errors.Is(err, threads.ErrInputResolved), errors.Is(err, threads.ErrInputNotFound):
		sub.State, sub.Outcome, sub.Detail = subDone, outcomeRefused, err.Error()
		return []store.LinearOutboxRow{s.activity(session.ID, "sub/"+sub.ID+"/refused", contentError, replyStale(err))}
	default:
		what := "Your reply"
		if sub.Kind == kindChoice {
			what = "Your choice"
		}
		return s.refuse(sub, "sub/"+sub.ID+"/refused", what, err)
	}
}

// dispatchDecision decides the exact request and decision the selection
// named. The domain's key for the pair keeps a retry the same decision;
// a refusal — resolved elsewhere, no longer offered, another decision in
// flight — is reported as it is, and nothing is approved by it.
func (s *Service) dispatchDecision(ctx context.Context, session store.LinearSession, sub *store.LinearSubmission) []store.LinearOutboxRow {
	_, err := s.coordinator.DecideApproval(ctx, session.ThreadID, sub.RequestID, api.ApprovalDecisionParams{Decision: api.ApprovalDecision(sub.Value)})
	switch {
	case err == nil:
		sub.State, sub.Delivery, sub.Outcome, sub.Detail = subDone, string(api.MessageAccepted), outcomeDelivered, ""
		requests, _ := s.repo.Requests(ctx, session.ID)
		return []store.LinearOutboxRow{s.activity(session.ID, "sub/"+sub.ID+"/sent", contentThought, decisionSent(label(requests, sub.RequestID, target{decision: sub.Value})))}
	case errors.Is(err, integrations.ErrNotConnected):
		s.later(sub, err)
		return nil
	case final(err):
		// Refused for good. A decision the program never answered may
		// still have landed; that uncertainty is reported as it is.
		text := decisionStale(err)
		if sub.Delivery == string(api.MessageUncertain) {
			text = decisionUncertain(err)
		}
		sub.State, sub.Outcome, sub.Detail = subDone, outcomeRefused, err.Error()
		return []store.LinearOutboxRow{s.activity(session.ID, "sub/"+sub.ID+"/refused", contentError, text)}
	default:
		// The program may hold the decision; the same one again, under
		// the same key, reconciles — never another.
		sub.State, sub.Delivery = subSent, string(api.MessageUncertain)
		s.later(sub, err)
		return nil
	}
}

// dispatchStop stops the Thread's work under the submission's own key,
// so a retry recovers this stop and never stops later work. Accepted,
// the stop is watched for the evidence that resolves it; resolved at
// once (nothing was running), it is reported at once.
func (s *Service) dispatchStop(ctx context.Context, session store.LinearSession, sub *store.LinearSubmission) []store.LinearOutboxRow {
	stop, err := s.coordinator.StopThread(ctx, session.ThreadID, api.ThreadStopParams{Key: sub.ID})
	switch {
	case err == nil:
		sub.OperationID, sub.Delivery, sub.Detail = stop.ID, string(stop.Delivery), ""
		if stop.State != api.StopStopping {
			return s.stopResolved(sub, stop)
		}
		sub.State = subSent
		if stop.Delivery != api.MessageAccepted {
			s.later(sub, errors.New("delivery uncertain"))
			return nil
		}
		return []store.LinearOutboxRow{s.activity(session.ID, "sub/"+sub.ID+"/sent", contentThought, stopSent())}
	case !final(err):
		s.later(sub, err)
		return nil
	default:
		return s.refuse(sub, "sub/"+sub.ID+"/refused", "The stop", err)
	}
}

// stopResolved ends a stop submission with the stop's outcome.
func (s *Service) stopResolved(sub *store.LinearSubmission, stop api.ThreadStop) []store.LinearOutboxRow {
	sub.State, sub.Outcome, sub.Detail = subDone, string(stop.State), stop.Detail
	kind := contentResponse
	if stop.State == api.StopFailed {
		kind = contentError
	}
	return []store.LinearOutboxRow{s.activity(sub.SessionID, "sub/"+sub.ID+"/done", kind, stopOutcome(stop))}
}
