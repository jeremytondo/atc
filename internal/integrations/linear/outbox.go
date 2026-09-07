package linear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/store"
)

// The outbox: every call owed to Linear is a row under a deterministic
// key, sent by one loop with bounded retries. Transient failures back
// off; an authentication failure renews the token once and otherwise
// waits for the operator; Linear's final refusal of an input is recorded
// on the row and never retried. An activity carries the id Linear
// deduplicates on, so a retry after an ambiguous answer cannot post it
// twice. Sent rows stay as receipts for as long as anything could try to
// enqueue their key again.

// Activity content types and signals, as Linear names them.
const (
	contentThought     = "thought"
	contentElicitation = "elicitation"
	contentResponse    = "response"
	contentError       = "error"
	signalSelect       = "select"

	kindActivity = "activity"
	kindLinks    = "links"

	// sendWorkers bounds how many sessions' calls go out at once.
	sendWorkers = 4
)

// activity builds the outbox row for one Agent Activity under the key
// (session, purpose). The activity id Linear deduplicates on derives from
// that key, so the same purpose presents the same identity even after
// the row's receipt is pruned.
func (s *Service) activity(sessionID, purpose, kind, body string) store.LinearOutboxRow {
	return s.row(sessionID, purpose, activityInput{Content: activityContent{Type: kind, Body: body}})
}

// elicitation builds the outbox row for an elicitation offering options
// under Linear's select signal (ATC-309); without options it is a plain
// elicitation.
func (s *Service) elicitation(sessionID, purpose, body string, options []selectOption) store.LinearOutboxRow {
	input := activityInput{Content: activityContent{Type: contentElicitation, Body: body}}
	if len(options) > 0 {
		input.Signal = signalSelect
		input.SignalMetadata = &signalMetadata{Options: options}
	}
	return s.row(sessionID, purpose, input)
}

func (s *Service) row(sessionID, purpose string, input activityInput) store.LinearOutboxRow {
	key := sessionID + "/" + purpose
	input.ID, input.AgentSessionID = ids.UUIDFrom("linear-activity:"+key), sessionID
	body, err := json.Marshal(input)
	if err != nil {
		panic("linear: encoding an activity: " + err.Error())
	}
	now := s.now()
	return store.LinearOutboxRow{ID: key, SessionID: sessionID, Kind: kindActivity, Body: body, NextAttemptAt: now, CreatedAt: now}
}

// links builds the outbox row that sets a session's external URLs to the
// Thread's links — a replacement, so sending it again changes nothing.
func (s *Service) links(sessionID string, links api.ThreadLinks) store.LinearOutboxRow {
	input, err := json.Marshal(linksInput{ID: sessionID, ExternalURLs: []externalURL{
		{Label: "T3 Code (web)", URL: links.Web},
		{Label: "T3 Code (desktop)", URL: links.App},
	}})
	if err != nil {
		panic("linear: encoding links: " + err.Error())
	}
	now := s.now()
	return store.LinearOutboxRow{ID: sessionID + "/links", SessionID: sessionID, Kind: kindLinks, Body: input, NextAttemptAt: now, CreatedAt: now}
}

// sendLoop posts due rows, waking on a kick or the poll, and prunes old
// receipts hourly.
func (s *Service) sendLoop(ctx context.Context) {
	s.prune(ctx)
	lastPrune := s.now()
	for {
		if ctx.Err() != nil {
			return
		}
		full := s.sendDue(ctx)
		if s.now().Sub(lastPrune) >= pruneInterval {
			s.prune(ctx)
			lastPrune = s.now()
		}
		if full {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-s.sendKick:
		case <-time.After(s.sendPoll):
		}
	}
}

// sendDue posts one batch — each due session's oldest outstanding row —
// and reports whether another pass is owed at once: the batch was full,
// or a row went out and the row behind it is due. Sessions are sent
// concurrently, sendWorkers at a time, so a slow call for one session
// never holds another session's acknowledgement past its deadline; a
// session's own rows go one at a time in order, so its story reads in
// sequence and a row backing off holds the rows behind it.
func (s *Service) sendDue(ctx context.Context) bool {
	due, err := s.repo.Due(ctx, s.now(), sendBatch)
	if err != nil {
		if ctx.Err() == nil {
			s.logger.Error("linear: reading the outbox", "error", err)
		}
		return false
	}
	bySession := map[string][]store.LinearOutboxRow{}
	var order []string
	for _, row := range due {
		if _, seen := bySession[row.SessionID]; !seen {
			order = append(order, row.SessionID)
		}
		bySession[row.SessionID] = append(bySession[row.SessionID], row)
	}
	slots := make(chan struct{}, sendWorkers)
	var wg sync.WaitGroup
	var mu sync.Mutex
	sent := false
	for _, sessionID := range order {
		rows := bySession[sessionID]
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			wg.Wait()
			return false
		}
		wg.Go(func() {
			defer func() { <-slots }()
			for _, row := range rows {
				if ctx.Err() != nil {
					return
				}
				if s.send(ctx, row) {
					mu.Lock()
					sent = true
					mu.Unlock()
				}
			}
		})
	}
	wg.Wait()
	return sent || len(due) == sendBatch
}

// send makes one attempt at a row and records the result, reporting
// whether the row is done — sent, or refused for good.
func (s *Service) send(ctx context.Context, row store.LinearOutboxRow) bool {
	token, err := s.token(ctx)
	if err == nil {
		err = s.post(ctx, token, row)
		if classify(err) == failureAuth {
			// One renewal, then one more try; a refused renewal is the
			// failure worth reporting, since it names the operator action.
			if renewErr := s.renew(ctx, token); renewErr != nil {
				err = renewErr
			} else if token, err = s.token(ctx); err == nil {
				err = s.post(ctx, token, row)
			}
		}
	}
	if ctx.Err() != nil {
		return false
	}
	now := s.now()
	attempts := row.Attempts + 1
	switch {
	case err == nil, classify(err) == failureDuplicate:
		if err := s.repo.Sent(ctx, row.ID, now); err != nil {
			s.logger.Error("linear: recording a sent call", "key", row.ID, "error", err)
			return false
		}
		s.logger.Debug("linear: sent", "key", row.ID)
		return true
	case errors.Is(err, ErrNotConfigured):
		// Not an attempt: nothing to send with. The row waits for setup.
		s.retry(ctx, row.ID, row.Attempts, now.Add(s.probeRetry))
	case classify(err) == failureAuth:
		s.reportAPIFailure(err, "posting to Linear")
		s.noteFailure(row, err)
		s.retry(ctx, row.ID, attempts, now.Add(s.authRetry))
	case classify(err) == failurePermanent:
		s.noteFailure(row, err)
		s.logger.Warn("linear: Linear refused a call", "key", row.ID, "error", err)
		if err := s.repo.Failed(ctx, row.ID, attempts, err.Error()); err != nil {
			s.logger.Error("linear: recording a refused call", "key", row.ID, "error", err)
		}
		if strings.HasSuffix(row.ID, "/result") {
			// The answer itself was refused (too long, most likely): the
			// user still learns the run is over and where the answer is.
			fallback := s.activity(row.SessionID, strings.TrimPrefix(row.ID, row.SessionID+"/")+"-rejected", contentError, responseRejected(s.sessionLinks(ctx, row.SessionID)))
			if _, err := s.repo.Enqueue(ctx, fallback); err != nil && ctx.Err() == nil {
				s.logger.Error("linear: queueing the fallback for a refused response", "key", row.ID, "error", err)
			}
			s.wake(s.sendKick)
		}
		return true
	default:
		s.noteFailure(row, err)
		s.logger.Warn("linear: posting to Linear failed", "key", row.ID, "attempt", attempts, "error", err)
		s.retry(ctx, row.ID, attempts, now.Add(s.backoff(attempts)))
	}
	return false
}

// post makes the GraphQL call a row describes and requires Linear to
// report success.
func (s *Service) post(ctx context.Context, token string, row store.LinearOutboxRow) error {
	var result mutationResult
	var err error
	switch row.Kind {
	case kindActivity:
		err = graphql(ctx, s.http, s.apiURL, token, activityMutation, map[string]any{"input": json.RawMessage(row.Body)}, &result)
	case kindLinks:
		var input linksInput
		if err := json.Unmarshal(row.Body, &input); err != nil {
			return &apiError{kind: failurePermanent, message: "unreadable links row: " + err.Error()}
		}
		err = graphql(ctx, s.http, s.apiURL, token, linksMutation, map[string]any{
			"id": input.ID, "input": map[string]any{"externalUrls": input.ExternalURLs},
		}, &result)
	default:
		return &apiError{kind: failurePermanent, message: "unknown outbox kind " + row.Kind}
	}
	if err != nil {
		return err
	}
	if !result.succeeded() {
		return &apiError{kind: failurePermanent, message: "Linear reported success: false"}
	}
	return nil
}

func (s *Service) retry(ctx context.Context, id string, attempts int, next time.Time) {
	if err := s.repo.Retry(ctx, id, attempts, next); err != nil && ctx.Err() == nil {
		s.logger.Error("linear: recording a retry", "key", id, "error", err)
	}
}

func (s *Service) noteFailure(row store.LinearOutboxRow, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastFailure = fmt.Sprintf("%s: %v", row.Kind, err)
}

func (s *Service) prune(ctx context.Context) {
	if err := s.repo.Prune(ctx, s.now().Add(-receiptRetention)); err != nil && ctx.Err() == nil {
		s.logger.Warn("linear: pruning outbox receipts", "error", err)
	}
}
