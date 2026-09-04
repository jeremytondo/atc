package t3code

import (
	"context"
	"net/http"
	"net/url"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// Response recovery (ATC-303). The shell projection carries no
// transcript, so a turn's final assistant message is read after the turn
// ended from T3's thread detail snapshot — the one-shot HTTP read, never a
// per-thread subscription — and reported to the threads domain apart from
// the turn observation, which drops it if a newer turn is the latest by
// then. Best effort throughout: nothing read here is evidence about the
// turn or the thread, and a response that cannot be recovered stays
// absent.

// threadDetail is what ATC reads of the detail snapshot: the latest turn,
// to confirm the turn and name its assistant message, and the messages to
// find it in.
type threadDetail struct {
	Thread struct {
		LatestTurn *latestTurnShell `json:"latestTurn"`
		Messages   []detailMessage  `json:"messages"`
	} `json:"thread"`
}

// detailMessage is what ATC reads of a T3 message: its text, and whether
// T3 is still streaming it.
type detailMessage struct {
	ID        string `json:"id"`
	Text      string `json:"text"`
	Streaming bool   `json:"streaming"`
}

// recoverResponse starts recovering the response of a T3 thread's latest
// turn when the turn has ended and ATC's record holds no response for it
// — after the normal turn end, for a thread first seen already ended, and
// for a turn that ended while ATC was away alike. One recovery runs per
// (thread, turn) per connection. Runs on the Run goroutine.
func (s *Service) recoverResponse(ctx context.Context, threadID, t3ThreadID string, turn *threads.TurnObservation) {
	if turn == nil || !turn.State.Ended() || s.recovery[t3ThreadID] == turn.ProviderID {
		return
	}
	record, err := s.threads.Get(threadID)
	if err != nil || record.LatestTurn == nil || record.LatestTurn.State == api.TurnRunning || record.LatestTurn.Response != "" {
		// Nothing to recover: a response already recorded, or ATC's
		// latest turn is a submitted one T3 has not started yet.
		return
	}
	s.recovery[t3ThreadID] = turn.ProviderID
	token := ""
	if s.session != nil {
		token = s.session.Token
	}
	s.mu.Lock()
	origin := s.origin
	s.mu.Unlock()
	s.reads.Add(1)
	go func() {
		defer s.reads.Done()
		select {
		case s.responseSlots <- struct{}{}:
			defer func() { <-s.responseSlots }()
		case <-ctx.Done():
			return
		}
		s.readResponse(ctx, origin, token, threadID, t3ThreadID, turn.ProviderID)
	}()
}

// readResponse is one turn's recovery: up to responseReads snapshot reads,
// responseRetry apart, until the turn's assistant message is present and
// no longer streaming; then the text goes to the threads domain. A message
// still missing or streaming after the last attempt, a turn T3 no longer
// reports as latest, an empty message, or a failed read leaves the
// response absent.
func (s *Service) readResponse(ctx context.Context, origin, token, threadID, t3ThreadID, turnID string) {
	for attempt := 0; attempt < responseReads; attempt++ {
		if attempt > 0 && !wait(ctx, s.responseRetry) {
			return
		}
		text, final, err := s.fetchResponse(ctx, origin, token, t3ThreadID, turnID)
		switch {
		case err != nil:
			s.logger.Debug("t3code: reading a turn's response", "thread", t3ThreadID, "turn", turnID, "error", err)
			continue
		case !final:
			continue
		case text == "":
			return
		}
		if err := s.threads.ObserveTurnResponse(ctx, threadID, turnID, text); err != nil {
			s.logger.Warn("t3code: recording a turn's response", "thread", t3ThreadID, "error", err)
		}
		return
	}
	s.logger.Debug("t3code: turn response not recovered", "thread", t3ThreadID, "turn", turnID)
}

// fetchResponse reads the thread detail snapshot once and reports the
// turn's final message: final is false while the message is not yet
// named or still streaming, true with the text otherwise — empty when T3
// has moved on to another turn or the message has no text.
func (s *Service) fetchResponse(ctx context.Context, origin, token, t3ThreadID, turnID string) (string, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin+"/api/orchestration/threads/"+url.PathEscape(t3ThreadID), nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var detail threadDetail
	if err := doJSONBounded(s.httpClient, req, &detail, maxSnapshotBytes); err != nil {
		return "", false, err
	}
	turn := detail.Thread.LatestTurn
	if turn == nil || turn.TurnID != turnID {
		return "", true, nil
	}
	if turn.AssistantMessageID == nil {
		return "", false, nil
	}
	for _, message := range detail.Thread.Messages {
		if message.ID == *turn.AssistantMessageID {
			return message.Text, !message.Streaming, nil
		}
	}
	return "", false, nil
}
