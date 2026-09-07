package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/webhooks"
)

// Processing (the shared webhook contract's Process): the inbox worker
// hands over an accepted delivery and this decides what ATC now owes for
// it, durably, without waiting for anything — not Linear, not T3. A
// `created` session from an explicit mention becomes an accepted session
// row owed a start, plus the acknowledgement Linear must hear within ten
// seconds; a session with nothing to act on becomes a done row owed an
// explanation. A `prompted` activity becomes a submission row under
// Linear's activity id: a stop signal, a choice among the options ATC
// offered on a request still open in the session, a reply to the
// question still open, or a message — decided from what this session
// has presented, never from the text's meaning. Every write is
// idempotent (the session insert ignores a known id, the submission
// insert a known activity id, the outbox insert a known key), so
// processing the same delivery again changes nothing, and nothing here
// ever reaches T3 — the session worker does that, from the rows.

// event is the AgentSessionEvent payload as this Integration reads it:
// the session, the comment that mentioned the app, the issue named in the
// prompt, the user's activity with its signal, and Linear's formatted
// context.
type event struct {
	envelope
	AgentSession struct {
		ID      string `json:"id"`
		Comment *struct {
			Body string `json:"body"`
		} `json:"comment"`
		Issue *struct {
			Identifier string `json:"identifier"`
			URL        string `json:"url"`
		} `json:"issue"`
	} `json:"agentSession"`
	AgentActivity *struct {
		ID      string `json:"id"`
		Signal  string `json:"signal"`
		Content struct {
			Body string `json:"body"`
		} `json:"content"`
	} `json:"agentActivity"`
	PromptContext string `json:"promptContext"`
}

// Process implements webhooks.Handler.
func (s *Service) Process(ctx context.Context, delivery webhooks.Accepted) error {
	var e event
	if err := json.Unmarshal(delivery.Payload, &e); err != nil {
		// Verified as JSON already; a payload this cannot read is a
		// contract change, recorded and not retried into forever.
		s.logger.Warn("linear: unreadable accepted delivery dropped", "delivery", delivery.ID, "error", err)
		return nil
	}
	if e.Type != eventType {
		s.logger.Debug("linear: delivery of an unhandled type dropped", "type", e.Type, "action", e.Action)
		return nil
	}
	sessionID := e.AgentSession.ID
	if sessionID == "" {
		s.logger.Warn("linear: session event without a session id dropped", "delivery", delivery.ID)
		return nil
	}
	switch e.Action {
	case "created":
		return s.created(ctx, e)
	case "prompted":
		return s.prompted(ctx, e, delivery.DeliveryID)
	default:
		s.logger.Debug("linear: session action ignored", "action", e.Action, "session", sessionID)
		return nil
	}
}

// created records a new session with the acknowledgement it is owed, in
// one write: accepted with its prompt and the start it owes when the
// mention carries one, done (refused) with the reason otherwise. Issue
// delegation creates a session without a comment; it is refused here,
// never executed.
func (s *Service) created(ctx context.Context, e event) error {
	now := s.now()
	session := store.LinearSession{ID: e.AgentSession.ID, CreatedAt: now, UpdatedAt: now}
	identifier, url := "", ""
	if issue := e.AgentSession.Issue; issue != nil {
		identifier, url = issue.Identifier, issue.URL
	}
	mention := e.AgentSession.Comment != nil && strings.TrimSpace(e.AgentSession.Comment.Body) != ""
	var owed store.LinearOutboxRow
	var submissions []store.LinearSubmission
	switch {
	case mention && strings.TrimSpace(e.PromptContext) != "":
		session.State = stateAccepted
		submissions = []store.LinearSubmission{{
			ID: startKey(session.ID), SessionID: session.ID, Kind: kindStart, Text: composePrompt(identifier, url, e.PromptContext),
			State: subPending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
		}}
		owed = s.activity(session.ID, "ack", contentThought, acknowledgement())
	case mention:
		session.State, session.Outcome = stateDone, outcomeRefused
		owed = s.activity(session.ID, "refused", contentError, refusedNoContext())
	default:
		session.State, session.Outcome = stateDone, outcomeRefused
		owed = s.activity(session.ID, "refused", contentError, refusedNoMention())
	}
	inserted, err := s.repo.InsertSession(ctx, session, submissions, owed)
	if err != nil {
		return fmt.Errorf("recording the session: %w", err)
	}
	if !inserted {
		s.logger.Debug("linear: session already recorded", "session", session.ID)
	}
	s.wake(s.sendKick)
	if session.State == stateAccepted {
		s.wake(s.sessionKick)
	}
	return nil
}

// startKey is the submission id of a session's start.
func startKey(sessionID string) string {
	return sessionID + "/start"
}

// prompted turns a user's activity into a submission: a stop, a choice
// among the options this session was offered, a reply to its open
// question, or a message. A session ATC never recorded, or one whose
// conversation ended, is told so and nothing is recorded. The reply's
// question is the one open when the activity is processed; Linear does
// not say which elicitation a text answers, and a question resolved and
// replaced between the typing and the processing is a window a few
// seconds wide that this does not close.
func (s *Service) prompted(ctx context.Context, e event, deliveryID string) error {
	sessionID := e.AgentSession.ID
	activityID, signal, body := "", "", ""
	if a := e.AgentActivity; a != nil {
		activityID, signal, body = a.ID, a.Signal, a.Content.Body
	}
	if activityID == "" {
		// Linear names every activity; a delivery that does not is still
		// answered once, under the delivery's own identity.
		activityID = "delivery/" + deliveryID
	}
	reply := func(text string) error {
		if _, err := s.repo.Enqueue(ctx, s.activity(sessionID, "reply/"+activityID, contentError, text)); err != nil {
			return fmt.Errorf("queueing the reply: %w", err)
		}
		s.wake(s.sendKick)
		return nil
	}
	session, err := s.repo.GetSession(ctx, sessionID)
	switch {
	case errors.Is(err, store.ErrLinearSessionNotFound):
		return reply(notTracked())
	case err != nil:
		return fmt.Errorf("reading the session: %w", err)
	case session.State == stateDone:
		return reply(sessionOver(session.Outcome))
	}
	now := s.now()
	sub := store.LinearSubmission{
		ID: activityID, SessionID: sessionID, Text: body, State: subPending, NextAttemptAt: now, CreatedAt: now, UpdatedAt: now,
	}
	switch {
	case signal == "stop":
		sub.Kind = kindStop
	case strings.TrimSpace(body) == "":
		return reply(emptyMessage())
	default:
		requests, err := s.repo.Requests(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("reading the session's requests: %w", err)
		}
		requestID, target, found, ambiguous := match(requests, body)
		switch {
		case ambiguous:
			return reply(ambiguousChoice())
		case found && target.decision != "":
			sub.Kind, sub.RequestID, sub.Value = kindDecision, requestID, target.decision
		case found:
			sub.Kind, sub.RequestID, sub.QuestionID, sub.Value = kindChoice, requestID, target.questionID, target.choice
		case openInput(requests) != "":
			sub.Kind, sub.RequestID = kindReply, openInput(requests)
		default:
			sub.Kind = kindMessage
		}
	}
	if err := s.repo.Record(ctx, store.LinearChange{NewSubmissions: []store.LinearSubmission{sub}}); err != nil {
		return fmt.Errorf("recording the submission: %w", err)
	}
	s.wake(s.sessionKick)
	return nil
}

// openInput is the newest open input request of a session, "" for none:
// the question a reply in words is addressed to.
func openInput(requests []presented) string {
	id := ""
	for _, request := range requests {
		if request.State == requestOpen && request.Kind == kindInput {
			id = request.ID
		}
	}
	return id
}
