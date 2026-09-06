package threads

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/store"
)

// Messages (ATC-307): a text directed at an existing conversation. The
// domain records the submission under an ATC identity before anything is
// sent, so a lost answer — the client's or the provider's — is recovered
// as the same message: the client's key finds it, and the Integration
// re-presents the same command from the same id and text, which the
// provider deduplicates. The message directs one turn: the pending turn
// it starts, or — when the Integration folds a message into work in
// progress — the turn already running. Dispatching belongs to the
// Integration; the application coordinator sequences the two.

const (
	messagePrefix = "msg-"
	// messageRejected is the stored delivery of a message the provider
	// refused; it never appears on the wire, where a rejection is the
	// request's error.
	messageRejected = "rejected"
	// messagesKept bounds the messages retained per thread: enough for
	// any retry a client still remembers the key of, small enough never
	// to matter.
	messagesKept = 32
)

// ErrMessageInvalid refuses a message with no text after trimming.
var ErrMessageInvalid = errors.New("invalid message")

// ErrMessageRejected reports the provider refusing a message for good;
// the reason is the provider's own. A replay of the same key reports the
// same rejection and sends nothing.
var ErrMessageRejected = errors.New("message rejected")

// Submission is one message submission: the text, the client's optional
// idempotency key, and whether the Integration steers — folds a message
// into the turn the provider is running, when one is, instead of
// starting another.
type Submission struct {
	Text   string
	Key    string
	Steers bool
}

// SubmitMessage records a message on the thread and returns it for the
// caller to dispatch, delivery uncertain until the provider answers. The
// message directs the running latest turn when the submission steers and
// one runs; otherwise it mints the pending turn it will start, with the
// thread provisionally working, and a second submission while one is
// pending is refused (ErrTurnPending). A key already recorded on the
// thread returns that message as it stands — a rejected one as
// ErrMessageRejected with the recorded reason — so nothing is sent
// twice.
func (s *Service) SubmitMessage(ctx context.Context, id string, sub Submission) (api.ThreadMessage, error) {
	if strings.TrimSpace(sub.Text) == "" {
		return api.ThreadMessage{}, fmt.Errorf("%w: text is empty", ErrMessageInvalid)
	}
	s.ops.Lock()
	defer s.ops.Unlock()
	record, ok := s.snapshot(id)
	if !ok {
		return api.ThreadMessage{}, ErrNotFound
	}
	if sub.Key != "" {
		existing, err := s.repository.MessageByKey(ctx, id, sub.Key)
		if err == nil {
			return messageFrom(existing)
		}
		if !errors.Is(err, store.ErrMessageNotFound) {
			return api.ThreadMessage{}, err
		}
	}
	if record.Pending != nil {
		return api.ThreadMessage{}, fmt.Errorf("%w: %s", ErrTurnPending, record.Pending.ID)
	}
	now := s.now()
	message := store.ThreadMessageRecord{
		ThreadID: id, Key: sub.Key, Text: sub.Text, Delivery: string(api.MessageUncertain), CreatedAt: now, UpdatedAt: now,
	}
	changed := false
	if sub.Steers && record.Turn != nil && record.Turn.State == string(api.TurnRunning) {
		message.TurnID = record.Turn.ID
	} else {
		s.mintPending(&record)
		message.TurnID = record.Pending.ID
		changed = true
	}
	// Insertion is the id collision check: a taken id inserts nothing and
	// re-rolls; a key taken since the lookup is the concurrent submission
	// that took it.
	for {
		message.ID = ids.NewLong(messagePrefix)
		inserted, err := s.repository.SubmitMessage(ctx, record, message, messagesKept)
		if errors.Is(err, store.ErrMessageKeyTaken) {
			if changed {
				s.forgetPrior(id)
			}
			return api.ThreadMessage{}, fmt.Errorf("%w: a submission with the same key is in flight", ErrTurnPending)
		}
		if err != nil {
			if changed {
				s.forgetPrior(id)
			}
			return api.ThreadMessage{}, err
		}
		if inserted {
			break
		}
	}
	s.mu.Lock()
	if entry, ok := s.view[id]; ok {
		*entry = record
	}
	s.mu.Unlock()
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, id)
	}
	return messageFrom(message)
}

// MessageDelivered records the provider committing a message: delivery
// accepted, for good.
func (s *Service) MessageDelivered(ctx context.Context, id, messageID string) (api.ThreadMessage, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	message, err := s.message(ctx, id, messageID)
	if err != nil {
		return api.ThreadMessage{}, err
	}
	if message.Delivery != string(api.MessageAccepted) {
		now := s.now()
		if _, err := s.repository.SetMessageDelivery(ctx, messageID, string(api.MessageAccepted), "", now); err != nil {
			return api.ThreadMessage{}, err
		}
		message.Delivery, message.Detail, message.UpdatedAt = string(api.MessageAccepted), "", now
	}
	return messageFrom(message)
}

// MessageRejected records the provider refusing a message: the message
// is rejected for good with the provider's reason, and the pending turn
// it would have started is withdrawn — the thread's status restored to
// what the submission replaced, or unknown when that is no longer known.
// A message that steered a running turn withdraws nothing.
func (s *Service) MessageRejected(ctx context.Context, id, messageID, reason string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	message, err := s.message(ctx, id, messageID)
	if err != nil {
		return err
	}
	now := s.now()
	if message.Delivery != messageRejected {
		if _, err := s.repository.SetMessageDelivery(ctx, messageID, messageRejected, reason, now); err != nil {
			return err
		}
	}
	record, ok := s.snapshot(id)
	if !ok || record.Pending == nil || record.Pending.ID != message.TurnID {
		return nil
	}
	record.Pending = nil
	s.mu.Lock()
	prior, remembered := s.priorStatus[id]
	delete(s.priorStatus, id)
	s.mu.Unlock()
	if remembered {
		record.Status, record.StatusDetail = prior.status, prior.detail
	} else {
		record.Status, record.StatusDetail = string(api.ThreadUnknown), ""
	}
	record.UpdatedAt = now
	if err := s.persist(ctx, record); err != nil {
		return err
	}
	s.hub.Publish(api.EventThreadUpdated, resource, id)
	return nil
}

// message reads one of the thread's messages. Caller holds ops.
func (s *Service) message(ctx context.Context, id, messageID string) (store.ThreadMessageRecord, error) {
	message, err := s.repository.Message(ctx, messageID)
	if errors.Is(err, store.ErrMessageNotFound) || err == nil && message.ThreadID != id {
		return store.ThreadMessageRecord{}, fmt.Errorf("%w: message %s", ErrNotFound, messageID)
	}
	return message, err
}

// messageFrom converts a record to its wire shape; a rejected message is
// its rejection.
func messageFrom(record store.ThreadMessageRecord) (api.ThreadMessage, error) {
	if record.Delivery == messageRejected {
		return api.ThreadMessage{}, fmt.Errorf("%w: %s", ErrMessageRejected, record.Detail)
	}
	return api.ThreadMessage{
		ID:        record.ID,
		ThreadID:  record.ThreadID,
		Key:       record.Key,
		Text:      record.Text,
		TurnID:    record.TurnID,
		Delivery:  api.MessageDelivery(record.Delivery),
		CreatedAt: record.CreatedAt,
	}, nil
}
