package t3code

import (
	"context"

	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/integrations"
)

// Stops (ATC-308): ATC stops a T3 thread's work with one
// thread.session.stop command — T3 closes the provider session behind
// the thread (the turn running, and any turn queued in the provider,
// die with it; a question or approval blocking it is abandoned), drops
// the turn start it still held for the thread, and keeps the
// conversation's resume information, so the next turn.start resumes
// the same conversation. Turn interruption is not used: it targets the
// active provider turn only and says nothing about a queued follow-up.
// T3 records the stop by setting the thread's session stopped at the
// command's own createdAt, and the shell reports that session with that
// time (shell.go): the evidence the threads domain confirms the stop on.

// PrepareStop resolves a stop against the live connection: the
// Integration must be connected and T3 must still report the thread.
// Nothing is sent until the dispatch runs.
func (s *Service) PrepareStop(ctx context.Context, providerID string) (integrations.StopDispatch, error) {
	client, err := s.prepareCommand(providerID)
	if err != nil {
		return nil, err
	}
	return func(ctx context.Context, stop integrations.ThreadStop) error {
		return outcome(s.deliver(ctx, client, map[string]any{
			"type":      "thread.session.stop",
			"commandId": ids.UUIDFrom("t3code/stop/" + stop.Key),
			"threadId":  providerID,
			"createdAt": timestamp(stop.CreatedAt),
		}), integrations.ErrStopRejected, "stop")
	}, nil
}
