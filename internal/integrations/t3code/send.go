package t3code

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/integrations"
)

// Messages (ATC-307): ATC continues a T3 thread by dispatching one
// thread.turn.start command without a bootstrap — the thread's own
// runtime and interaction modes, no model selection, so T3 runs the turn
// under the thread's current agent, model, and settings, never the
// defaults a creation sends. The command's identities derive from the
// ATC message id: the same message presents the same command every
// time, and T3's command receipts answer a repeat with the first
// outcome, so a retry after a lost answer never delivers twice.

// steeringAgents names the T3 provider kinds whose adapter folds a
// message sent during a running turn into that turn — Claude Code's SDK
// loop takes it as a steer and the same turn continues with the new
// direction. Every other kind (Codex in particular) takes the message
// as the next turn, started once the current one ends; T3 accepts the
// command either way, and a kind that refuses reports the failure
// through the session's error, which fails the pending turn.
var steeringAgents = map[string]bool{"claudeAgent": true}

// PrepareMessage resolves a message against the live connection: the
// Integration must be connected and T3 must still report the thread.
// Nothing is sent until Dispatch.
func (s *Service) PrepareMessage(ctx context.Context, providerID string) (integrations.PreparedMessage, error) {
	s.mu.Lock()
	connection, client := s.connection, s.client
	thread, known := s.shell.threads[providerID]
	s.mu.Unlock()
	if connection.State != api.IntegrationConnected {
		return integrations.PreparedMessage{}, fmt.Errorf("%w: T3 Code is %s: %s", integrations.ErrNotConnected, connection.State, connection.Detail)
	}
	if !known {
		return integrations.PreparedMessage{}, fmt.Errorf("%w: T3 Code no longer reports the thread; it may be archived or deleted there", integrations.ErrMessageRejected)
	}
	steers := false
	if thread.Session != nil && thread.Session.ProviderName != nil {
		steers = steeringAgents[*thread.Session.ProviderName]
	}
	return integrations.PreparedMessage{
		Steers: steers,
		// The connection the thread was resolved on is the one the
		// command goes to.
		Dispatch: func(ctx context.Context, msg integrations.ThreadMessage) error {
			return outcome(s.deliver(ctx, client, turnStartMessage(providerID, thread, msg)), integrations.ErrMessageRejected, "message")
		},
	}, nil
}

// turnStartMessage is the thread.turn.start command for a message on an
// existing thread: the thread's own modes, no model selection, no
// bootstrap, and identities derived from the message's.
func turnStartMessage(threadID string, thread threadShell, msg integrations.ThreadMessage) map[string]any {
	interactionMode := thread.InteractionMode
	if interactionMode == "" {
		interactionMode = interactionMode0
	}
	return map[string]any{
		"type":      "thread.turn.start",
		"commandId": ids.UUIDFrom("t3code/message/" + msg.ID + "/command"),
		"threadId":  threadID,
		"message": map[string]any{
			"messageId": ids.UUIDFrom("t3code/message/" + msg.ID + "/message"), "role": "user", "text": msg.Text, "attachments": []any{},
		},
		"runtimeMode":     thread.RuntimeMode,
		"interactionMode": interactionMode,
		"createdAt":       timestamp(msg.CreatedAt),
	}
}

// interactionMode0 is T3's interaction mode for a thread whose shell
// omits one (T3 defaults it the same way).
const interactionMode0 = "default"

// timestamp formats a command's createdAt as T3 reads it.
func timestamp(at time.Time) string {
	return at.UTC().Format("2006-01-02T15:04:05.000Z07:00")
}

// deliver sends one command over the given connection and waits for T3
// to commit it: nil on success, T3's typed refusal as an *rpcFailure, the
// connection's or the deadline's error when T3 never answered,
// ErrNotConnected without a connection.
func (s *Service) deliver(ctx context.Context, client *rpcClient, command map[string]any) error {
	if client == nil {
		return fmt.Errorf("%w: T3 Code's connection is not up", integrations.ErrNotConnected)
	}
	ctx, cancel := context.WithTimeout(ctx, dispatchTimeout)
	defer cancel()
	return client.call(ctx, dispatchMethod, command)
}

// outcome classifies a command's delivery, the same way for every
// command: nil once T3 committed it; the given rejection, with T3's own
// reason, when T3 refused it for good; ErrNotConnected as it came; and
// ErrDeliveryUncertain when T3 never answered — it may hold the command,
// and the same command again reconciles.
func outcome(err error, rejected error, what string) error {
	var failure *rpcFailure
	switch {
	case err == nil:
		return nil
	case errors.As(err, &failure):
		return fmt.Errorf("%w: T3 Code rejected the %s: %s", rejected, what, failureMessage(failure))
	case errors.Is(err, integrations.ErrNotConnected):
		return err
	default:
		return fmt.Errorf("%w: T3 Code did not answer: %w", integrations.ErrDeliveryUncertain, err)
	}
}

// failureMessage is T3's own words for a refusal.
func failureMessage(failure *rpcFailure) string {
	if failure.message != "" {
		return failure.message
	}
	return failure.Error()
}
