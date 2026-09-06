package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/service"
)

// atc thread send (ATC-307): one message into an existing conversation,
// then the wait for that message's own execution and its reply — the
// print-mode contract of Claude Code and Codex: non-interactive, the
// reply on stdout, diagnostics on stderr, a non-zero exit for anything
// but a recovered reply. The wait follows exactly the turn the message
// directs, through the thread's pendingTurn and latestTurn, over the
// change-event feed with a refetch per change and after every reconnect;
// it never adopts a later turn's reply. Ctrl-C ends the wait and nothing
// else: the work continues in the provider. Submission is under a key,
// so a retry after a lost answer — the server's or the provider's —
// recovers the same message instead of sending another.

const (
	// sendAttempts bounds the submissions of one message, backing off
	// between them from sendBackoff to sendBackoffMax: enough to ride out
	// a provider reconnect, bounded so an outage is reported rather than
	// waited out silently.
	sendAttempts   = 5
	sendBackoff    = time.Second
	sendBackoffMax = 8 * time.Second
	// replyGrace is how long a completed turn may lack its reply before
	// the wait gives up: the provider's recovery reads span a few
	// seconds; this leaves ample room.
	replyGrace = 2 * time.Minute
	// waitPoll is the refetch cadence while the feed is quiet — the
	// safety net under the events, so a lost notification costs a poll,
	// never the wait.
	waitPoll = 30 * time.Second
	// exitInterrupted is the conventional exit status for a wait ended by
	// Ctrl-C.
	exitInterrupted = 130
)

func newThreadSendCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "send <id> [message]",
		Short: "Send a message to a conversation and print its reply",
		Long: `Send a message into an existing conversation and wait for its reply. The
message is the positional argument; when it is absent or "-", it is read from
standard input.

The message goes to the conversation under its current agent, model, and
settings. On an idle thread it starts the next turn; while a turn runs it is
applied at the next opportunity the provider supports. The command waits for
exactly the turn the message directs — never a later one — and prints its
final reply on stdout; a turn that fails, is interrupted, or is replaced
before its reply is recovered is reported on stderr with a non-zero exit.
Ctrl-C stops waiting and nothing else: the work continues in the provider.

A pending question or approval is not interpreted: the text is sent as any
other message. Answer an approval request with ` + "`atc thread approve`" + ` or
` + "`atc thread deny`" + `.

Submission is idempotent under --key: a retry with the same key on the same
thread recovers the message already sent rather than sending it again. Without
the flag a fresh key is minted for this invocation.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			key, err := cmd.Flags().GetString("key")
			if err != nil {
				return err
			}
			if key == "" {
				key = ids.UUID()
			}
			text := ""
			if len(args) == 2 && args[1] != "-" {
				text = args[1]
			} else {
				input, err := io.ReadAll(cmd.InOrStdin())
				if err != nil {
					return fmt.Errorf("reading the message from stdin: %w", err)
				}
				text = string(input)
			}
			if strings.TrimSpace(text) == "" {
				return fmt.Errorf("the message is empty; pass it as an argument or on stdin")
			}
			return sendAndWait(cmd.Context(), client, args[0], api.ThreadMessageParams{Text: text, Key: key}, cmd.OutOrStdout(), cmd.ErrOrStderr())
		}),
	}
	cmd.Flags().String("key", "", "idempotency key for the submission; the same key never sends twice")
	return cmd
}

// sendAndWait submits the message with recovery, then waits for its
// turn's reply and prints it.
func sendAndWait(ctx context.Context, client *api.Client, threadID string, params api.ThreadMessageParams, stdout, stderr io.Writer) error {
	message, err := submitMessage(ctx, client, threadID, params, stderr)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stderr, "atc: sent %s; waiting for the reply to turn %s (Ctrl-C stops waiting; the work continues)\n", message.ID, message.TurnID)
	reply, err := waitForReply(ctx, client, threadID, message.TurnID, stderr)
	if errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "atc: stopped waiting for turn %s; the work continues in the provider\n", message.TurnID)
		return &service.ExitError{Code: exitInterrupted}
	}
	if err != nil {
		return err
	}
	if !strings.HasSuffix(reply, "\n") {
		reply += "\n"
	}
	_, err = io.WriteString(stdout, reply)
	return err
}

// submitMessage submits the message until the server records it as
// accepted, retrying the same key — never a fresh message — when no
// answer arrived, when the delivery was uncertain, or while the provider
// is not connected. A refusal is final. Retries exhausted report the
// last state honestly: an uncertain delivery is neither delivered nor
// not.
func submitMessage(ctx context.Context, client *api.Client, threadID string, params api.ThreadMessageParams, stderr io.Writer) (api.ThreadMessage, error) {
	backoff := sendBackoff
	var last api.ThreadMessage
	for attempt := 1; ; attempt++ {
		message, err := client.SendThreadMessage(ctx, threadID, params)
		var problem *api.Problem
		retry := ""
		switch {
		case err == nil && message.Delivery == api.MessageAccepted:
			return message, nil
		case err == nil:
			last = message
			retry = fmt.Sprintf("delivery of %s to the provider is uncertain", message.ID)
		case errors.As(err, &problem) && problem.Code == api.CodeIntegrationNotConnected:
			retry = problem.Detail
		case errors.As(err, &problem):
			return api.ThreadMessage{}, err
		case ctx.Err() != nil:
			return api.ThreadMessage{}, ctx.Err()
		default:
			retry = err.Error()
		}
		if attempt >= sendAttempts {
			if last.ID != "" {
				return api.ThreadMessage{}, fmt.Errorf("delivery of message %s is uncertain after %d attempts: the provider may or may not have received it; check `atc thread get %s`, or resend with --key %s to reconcile once the provider is back", last.ID, attempt, threadID, params.Key)
			}
			return api.ThreadMessage{}, fmt.Errorf("%s (after %d attempts; nothing was sent, or the same key reconciles it: --key %s)", retry, attempt, params.Key)
		}
		_, _ = fmt.Fprintf(stderr, "atc: %s; retrying the same message in %s\n", retry, backoff)
		if !sleep(ctx, backoff) {
			return api.ThreadMessage{}, ctx.Err()
		}
		backoff = min(backoff*2, sendBackoffMax)
	}
}

// waitForReply follows one turn to its end: a refetch now, on every
// change event for the thread, after every reconnect or resync, and on
// the quiet poll. The feed is subscribed before each refetch, so a
// change between the two is delivered rather than missed, and
// re-established with the last event id whenever it drops. The reply is
// the turn's final response; any other end is the error.
func waitForReply(ctx context.Context, client *api.Client, threadID, turnID string, stderr io.Writer) (string, error) {
	var stream *api.EventStream
	defer func() {
		if stream != nil {
			_ = stream.Close()
		}
	}()
	var completedAt time.Time
	lastEventID := ""
	backoff := sendBackoff
	for {
		if stream == nil {
			opened, err := client.Events(ctx, lastEventID, 0)
			if err != nil && ctx.Err() != nil {
				return "", ctx.Err()
			}
			if err != nil {
				// The feed is down; the refetch below still runs, then the
				// backoff before the next attempt.
				_, _ = fmt.Fprintf(stderr, "atc: %v; reconnecting in %s\n", err, backoff)
			}
			stream = opened
		}
		thread, err := client.Thread(ctx, threadID)
		if err != nil {
			var problem *api.Problem
			switch {
			case ctx.Err() != nil:
				return "", ctx.Err()
			case errors.As(err, &problem) && problem.Status == http.StatusNotFound:
				return "", fmt.Errorf("turn %s: the thread was deleted while waiting", turnID)
			case errors.As(err, &problem):
				return "", err
			}
			_, _ = fmt.Fprintf(stderr, "atc: %v; reconnecting in %s\n", err, backoff)
		} else {
			reply, done, err := evaluateReply(thread, turnID, &completedAt, time.Now())
			if done {
				return reply, err
			}
		}
		if err != nil || stream == nil {
			if !sleep(ctx, backoff) {
				return "", ctx.Err()
			}
			backoff = min(backoff*2, sendBackoffMax)
			continue
		}
		backoff = sendBackoff
		// Wait for a reason to refetch: a change to this thread, a resync,
		// the poll, or the reply grace running out.
		timeout := waitPoll
		if !completedAt.IsZero() {
			timeout = min(timeout, time.Until(completedAt.Add(replyGrace))+time.Second)
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		err = nextChange(waitCtx, stream, threadID)
		cancel()
		if err != nil && ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			// The feed dropped: reconnect with the cursor, refetch first.
			lastEventID = stream.LastEventID
			_ = stream.Close()
			stream = nil
		}
	}
}

// nextChange reads the feed until a change to the thread or a resync —
// each a reason to refetch — or the end of the stream, its error; other
// resources' changes are skipped.
func nextChange(ctx context.Context, stream *api.EventStream, threadID string) error {
	for {
		event, err := stream.Next(ctx)
		if err != nil {
			return err
		}
		if event.Name == api.EventResync {
			return nil
		}
		change, err := event.Change()
		if err == nil && change.Resource == "thread" && change.ID == threadID {
			return nil
		}
	}
}

// evaluateReply decides what one reading of the thread means for the
// turn: its reply, its end without one, or nothing yet. Only the exact
// turn counts — a later one replacing it, or the provider dropping the
// thread, ends the wait with that fact, never a substitute. A completed
// turn without its reply is waited on for replyGrace before the recovery
// is given up. Pure, for tests.
func evaluateReply(thread api.Thread, turnID string, completedAt *time.Time, now time.Time) (string, bool, error) {
	if pending := thread.PendingTurn; pending != nil && pending.ID == turnID {
		if thread.Archived {
			return "", true, fmt.Errorf("turn %s: the provider dropped the thread before starting the turn", turnID)
		}
		return "", false, nil
	}
	turn := thread.LatestTurn
	if turn == nil || turn.ID != turnID {
		if turn != nil {
			return "", true, fmt.Errorf("turn %s was replaced by turn %s (%s) before its reply was recovered", turnID, turn.ID, turn.State)
		}
		return "", true, fmt.Errorf("turn %s is no longer known to the thread", turnID)
	}
	switch turn.State {
	case api.TurnCompleted:
		if turn.Response != "" {
			return turn.Response, true, nil
		}
		if completedAt.IsZero() {
			*completedAt = now
		} else if now.Sub(*completedAt) >= replyGrace {
			return "", true, fmt.Errorf("turn %s completed but its reply could not be recovered; open the thread in the provider to read it", turnID)
		}
		return "", false, nil
	case api.TurnFailed:
		detail := turn.Error
		if detail == "" {
			detail = "no detail from the provider"
		}
		return "", true, fmt.Errorf("turn %s failed: %s", turnID, detail)
	case api.TurnInterrupted:
		return "", true, fmt.Errorf("turn %s was interrupted", turnID)
	}
	// Running, or unknown while observation is lost: the turn is not
	// over. A thread the provider dropped cannot finish one.
	if thread.Archived {
		return "", true, fmt.Errorf("turn %s: the provider dropped the thread before the turn ended", turnID)
	}
	return "", false, nil
}

func sleep(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func newThreadApproveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "approve <id> <approval-id>",
		Short: "Approve a pending approval request",
		Long: `Approve one approval request the conversation's agent is blocked on: the
approve decision, which every request offers. The request ids and the other
decisions each offers are shown by ` + "`atc thread get`" + `; pick one of those with
` + "`atc thread decide`" + `.`,
		Args: cobra.ExactArgs(2),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			return decideApproval(cmd, client, args[0], args[1], api.DecisionApprove)
		}),
	}
}

func newThreadDenyCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "deny <id> <approval-id>",
		Short: "Deny a pending approval request",
		Long: `Deny one approval request the conversation's agent is blocked on: the
deny decision, which every request offers.`,
		Args: cobra.ExactArgs(2),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			return decideApproval(cmd, client, args[0], args[1], api.DecisionDeny)
		}),
	}
}

func newThreadDecideCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "decide <id> <approval-id> <decision>",
		Short: "Answer a pending approval request with one of its decisions",
		Long: `Answer one approval request the conversation's agent is blocked on with any
decision it offers — approve, approve_for_session, approve_always, deny, or
cancel — exactly as ` + "`atc thread get`" + ` lists them. approve and deny are the
common two; this takes the rest.`,
		Args: cobra.ExactArgs(3),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			return decideApproval(cmd, client, args[0], args[1], api.ApprovalDecision(args[2]))
		}),
	}
}

// decideApproval submits one decision and prints the request resolved.
func decideApproval(cmd *cobra.Command, client *api.Client, threadID, approvalID string, decision api.ApprovalDecision) error {
	approval, err := client.DecideThreadApproval(cmd.Context(), threadID, approvalID, api.ApprovalDecisionParams{Decision: decision})
	if err != nil {
		return err
	}
	printApproval(cmd.OutOrStdout(), approval)
	return nil
}
