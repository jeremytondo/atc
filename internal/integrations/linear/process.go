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
// explanation; a `prompted` message or stop signal becomes a fixed reply.
// Every write is idempotent (the session insert ignores a known id, the
// outbox insert a known key), so processing the same delivery again
// changes nothing, and nothing here ever starts a Thread — the session
// loop does that, from the row.

// event is the AgentSessionEvent payload as this Integration reads it:
// the session, the comment that mentioned the app, the issue named in the
// prompt, the signal on a user's message, and Linear's formatted context.
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
		Signal string `json:"signal"`
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
// one write: accepted with its prompt when the mention carries one, done
// (refused) with the reason otherwise. Issue delegation creates a session
// without a comment; it is refused here, never executed.
func (s *Service) created(ctx context.Context, e event) error {
	now := s.now()
	session := store.LinearSession{ID: e.AgentSession.ID, CreatedAt: now, UpdatedAt: now}
	identifier, url := "", ""
	if issue := e.AgentSession.Issue; issue != nil {
		identifier, url = issue.Identifier, issue.URL
	}
	mention := e.AgentSession.Comment != nil && strings.TrimSpace(e.AgentSession.Comment.Body) != ""
	var owed store.LinearOutboxRow
	switch {
	case mention && strings.TrimSpace(e.PromptContext) != "":
		session.State = stateAccepted
		session.Prompt = composePrompt(identifier, url, e.PromptContext)
		owed = s.activity(session.ID, "ack", contentThought, acknowledgement())
	case mention:
		session.State, session.Outcome = stateDone, outcomeRefused
		owed = s.activity(session.ID, "refused", contentError, refusedNoContext())
	default:
		session.State, session.Outcome = stateDone, outcomeRefused
		owed = s.activity(session.ID, "refused", contentError, refusedNoMention())
	}
	inserted, err := s.repo.InsertSession(ctx, session, owed)
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

// prompted answers a message or stop signal sent into a session ATC
// started: with the fixed explanation, never with a prompt to T3, and
// without touching what the session tracks. A session ATC never recorded
// gets told so.
func (s *Service) prompted(ctx context.Context, e event, deliveryID string) error {
	sessionID := e.AgentSession.ID
	links := s.sessionLinks(ctx, sessionID)
	var body string
	switch {
	case e.AgentActivity != nil && e.AgentActivity.Signal == "stop":
		body = stopNotSupported(links)
	default:
		body = followUpNotSupported(links)
	}
	if _, err := s.repo.GetSession(ctx, sessionID); err != nil {
		if !errors.Is(err, store.ErrLinearSessionNotFound) {
			return fmt.Errorf("reading the session: %w", err)
		}
		body = notTracked()
	}
	owed := s.activity(sessionID, "reply/"+deliveryID, contentThought, body)
	if _, err := s.repo.Enqueue(ctx, owed); err != nil {
		return fmt.Errorf("queueing the reply: %w", err)
	}
	s.wake(s.sendKick)
	return nil
}
