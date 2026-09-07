package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/ids"
	"github.com/jeremytondo/atc/internal/service"
)

// atc thread answer and atc thread stop (ATC-308): the two controls
// that resolve only on the provider's evidence. answer submits one
// complete answer set for a structured request and waits for the
// request to be resolved — with these answers, elsewhere, or by a
// failure — then exits; it never waits for the agent's reply, and send
// never asks questions. stop submits the stop and waits for its outcome:
// stopped or finished exit 0, a refusal or failure exits non-zero.
// Both submit with recovery — a retry recovers the same operation, never
// a second one — and both follow the change-event feed with a refetch
// per change, the way send does. Ctrl-C ends the wait and nothing else:
// the operation stays recorded and the server keeps reconciling it.

func newThreadAnswerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "answer <id> <request-id> (--answer <question>=<value> [--answer ...] | --reply <text>)",
		Short: "Answer a pending structured request and wait for it to resolve",
		Long: `Answer one structured request the conversation's agent is blocked on: with
answers to its questions, or with a reply in your own words. The requests,
their question ids, and the choices each offers are shown by
` + "`atc thread get`" + `.

Each --answer names a question and a value. A value that is one of the
question's choices selects it; repeat the flag for a question that allows
several choices. A value that is not a choice is sent as a custom text answer
where the question allows one, and refused otherwise. Not every question has
to be answered: the agent reads what it received. The server validates the
whole set before anything reaches the provider.

--reply sends the text as it is, for the agent to read against its questions:
an answer in prose, a partial answer, or a change of direction. It cannot be
combined with --answer.

The command waits for the provider to resolve the request, then exits: 0 when
it was resolved with these answers, non-zero when the answer failed or the
request was resolved another way. It does not wait for the agent's reply; use
` + "`atc thread send`" + ` for a message. Ctrl-C stops waiting and nothing else.
Submitting the same answers again recovers the answer already sent.`,
		Args: cobra.ExactArgs(2),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			values, err := cmd.Flags().GetStringArray("answer")
			if err != nil {
				return err
			}
			reply, err := cmd.Flags().GetString("reply")
			if err != nil {
				return err
			}
			var params api.InputAnswerParams
			switch {
			case strings.TrimSpace(reply) != "" && len(values) > 0:
				return errors.New("--reply and --answer cannot be combined")
			case strings.TrimSpace(reply) != "":
				params.Reply = reply
			case len(values) == 0:
				return errors.New("an answer is required: --answer question=value, or --reply text")
			default:
				request, err := client.ThreadInputRequest(cmd.Context(), args[0], args[1])
				if err != nil {
					return err
				}
				if params, err = answerParams(request, values); err != nil {
					return err
				}
			}
			return answerAndWait(cmd.Context(), client, args[0], args[1], params, cmd.OutOrStdout(), cmd.ErrOrStderr())
		}),
	}
	cmd.Flags().StringArray("answer", nil, "question=value; a choice's value, or a custom text where allowed (repeatable)")
	cmd.Flags().String("reply", "", "a reply in your own words, sent as it is")
	return cmd
}

// answerParams turns question=value pairs into the answer set: values
// that are all choices select them, a lone value that is none is a
// custom text. The server refuses what the question does not allow.
func answerParams(request api.ThreadInputRequest, pairs []string) (api.InputAnswerParams, error) {
	values := map[string][]string{}
	var order []string
	for _, pair := range pairs {
		id, value, ok := strings.Cut(pair, "=")
		if !ok || id == "" {
			return api.InputAnswerParams{}, fmt.Errorf("--answer %q is not question=value", pair)
		}
		if _, seen := values[id]; !seen {
			order = append(order, id)
		}
		values[id] = append(values[id], value)
	}
	var params api.InputAnswerParams
	for _, id := range order {
		answer := api.QuestionAnswer{QuestionID: id}
		index := slices.IndexFunc(request.Questions, func(q api.InputQuestion) bool { return q.ID == id })
		choices := true
		if index >= 0 {
			for _, value := range values[id] {
				if !slices.ContainsFunc(request.Questions[index].Options, func(o api.InputOption) bool { return o.Value == value }) {
					choices = false
				}
			}
		}
		if choices || len(values[id]) > 1 {
			answer.Choices = values[id]
		} else {
			answer.Text = values[id][0]
		}
		params.Answers = append(params.Answers, answer)
	}
	return params, nil
}

// answerAndWait submits the answer with recovery, then waits for the
// request's resolution and reports it.
func answerAndWait(ctx context.Context, client *api.Client, threadID, requestID string, params api.InputAnswerParams, stdout, stderr io.Writer) error {
	request, err := submitAnswer(ctx, client, threadID, requestID, params, stderr)
	if err != nil {
		return err
	}
	if request.Status == api.InputRequestPending {
		_, _ = fmt.Fprintf(stderr, "atc: answered %s; waiting for the provider to resolve it (Ctrl-C stops waiting; the answer stays sent)\n", requestID)
		request, err = waitForInputRequest(ctx, client, threadID, requestID, stderr)
		if errors.Is(err, context.Canceled) {
			_, _ = fmt.Fprintf(stderr, "atc: stopped waiting for %s; the answer stays sent and the server keeps reconciling it\n", requestID)
			return &service.ExitError{Code: exitInterrupted}
		}
		if err != nil {
			return err
		}
	}
	printInputRequest(stdout, request)
	if answer := request.Answer; answer != nil && answer.State != api.InputAnswerResolved {
		detail := answer.Detail
		if detail == "" {
			detail = "no detail"
		}
		return fmt.Errorf("request %s was not resolved with this answer: %s (%s)", requestID, answer.State, detail)
	}
	return nil
}

// submitAnswer submits the answer until the server records it as
// delivered, retrying the same answers — never different ones — when no
// answer arrived, when the delivery was uncertain, or while the provider
// is not connected. A refusal is final.
func submitAnswer(ctx context.Context, client *api.Client, threadID, requestID string, params api.InputAnswerParams, stderr io.Writer) (api.ThreadInputRequest, error) {
	backoff := sendBackoff
	for attempt := 1; ; attempt++ {
		request, err := client.AnswerThreadInputRequest(ctx, threadID, requestID, params)
		var problem *api.Problem
		retry := ""
		switch {
		case err == nil && (request.Answer == nil || request.Answer.Delivery == api.MessageAccepted || request.Status == api.InputRequestResolved):
			return request, nil
		case err == nil:
			retry = "delivery of the answer to the provider is uncertain"
		case errors.As(err, &problem) && problem.Code == api.CodeIntegrationNotConnected:
			retry = problem.Detail
		case errors.As(err, &problem):
			return api.ThreadInputRequest{}, err
		case ctx.Err() != nil:
			return api.ThreadInputRequest{}, ctx.Err()
		default:
			retry = err.Error()
		}
		if attempt >= sendAttempts {
			return api.ThreadInputRequest{}, fmt.Errorf("%s (after %d attempts; the same answers again reconcile it: `atc thread answer %s %s ...`)", retry, attempt, threadID, requestID)
		}
		_, _ = fmt.Fprintf(stderr, "atc: %s; retrying the same answer in %s\n", retry, backoff)
		if !sleep(ctx, backoff) {
			return api.ThreadInputRequest{}, ctx.Err()
		}
		backoff = min(backoff*2, sendBackoffMax)
	}
}

// waitForInputRequest follows the request until it is resolved, or the
// answer is settled short of resolving it (failed: the request stays
// pending for another answer).
func waitForInputRequest(ctx context.Context, client *api.Client, threadID, requestID string, stderr io.Writer) (api.ThreadInputRequest, error) {
	var request api.ThreadInputRequest
	err := follow(ctx, client, threadID, stderr, func(ctx context.Context) (bool, time.Duration, error) {
		got, err := client.ThreadInputRequest(ctx, threadID, requestID)
		if err != nil {
			return false, 0, err
		}
		request = got
		return got.Status == api.InputRequestResolved || got.Answer == nil || got.Answer.State != api.InputAnswerSent, 0, nil
	})
	return request, err
}

func newThreadStopCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stop <id>",
		Short: "Stop a conversation's work and wait for the provider to confirm",
		Long: `Stop the work on a conversation: the turn running, a message already sent
that has not started, and the question or approval it is blocked on. The
conversation is kept; a later ` + "`atc thread send`" + ` continues it.

The command waits for the provider's evidence, then exits: 0 when the work was
stopped, or had already finished by the time the stop applied (the outcome
says which); non-zero when the provider refused. Until the outcome is known
the thread refuses messages, answers, and decisions, and Ctrl-C changes
nothing about that: it stops waiting, the stop stays recorded, and the server
keeps reconciling it. Running the command again while the stop is being
confirmed recovers the same stop.

Submission is idempotent under --key: a retry with the same key on the same
thread recovers the stop already recorded, in whatever state, rather than
stopping later work. Without the flag a fresh key is minted for this
invocation.`,
		Args: cobra.ExactArgs(1),
		RunE: runWithClient(func(cmd *cobra.Command, args []string, client *api.Client, _ string) error {
			key, err := cmd.Flags().GetString("key")
			if err != nil {
				return err
			}
			if key == "" {
				key = ids.UUID()
			}
			return stopAndWait(cmd.Context(), client, args[0], api.ThreadStopParams{Key: key}, cmd.OutOrStdout(), cmd.ErrOrStderr())
		}),
	}
	cmd.Flags().String("key", "", "idempotency key for the submission; the same key never stops twice")
	return cmd
}

// stopAndWait submits the stop with recovery, then waits for its outcome
// and reports it.
func stopAndWait(ctx context.Context, client *api.Client, threadID string, params api.ThreadStopParams, stdout, stderr io.Writer) error {
	stop, err := submitStop(ctx, client, threadID, params, stderr)
	if err != nil {
		return err
	}
	if stop.State == api.StopStopping {
		_, _ = fmt.Fprintf(stderr, "atc: stop %s accepted; waiting for the provider to confirm (Ctrl-C stops waiting; the stop stays in force)\n", stop.ID)
		resolved, err := waitForStop(ctx, client, threadID, stop.ID, stderr)
		if errors.Is(err, context.Canceled) {
			_, _ = fmt.Fprintf(stderr, "atc: stopped waiting for stop %s; it stays in force and the server keeps reconciling it\n", stop.ID)
			return &service.ExitError{Code: exitInterrupted}
		}
		if err != nil {
			return err
		}
		stop = resolved
	}
	printStop(stdout, stop)
	if stop.State == api.StopFailed {
		return fmt.Errorf("stop %s failed: %s", stop.ID, stop.Detail)
	}
	return nil
}

// submitStop submits the stop until the server records it as delivered
// or resolved, retrying — the same operation, never another — when no
// answer arrived, when the delivery was uncertain, or while the provider
// is not connected. A refusal is final.
func submitStop(ctx context.Context, client *api.Client, threadID string, params api.ThreadStopParams, stderr io.Writer) (api.ThreadStop, error) {
	backoff := sendBackoff
	for attempt := 1; ; attempt++ {
		stop, err := client.StopThread(ctx, threadID, params)
		var problem *api.Problem
		retry := ""
		switch {
		case err == nil && (stop.Delivery == api.MessageAccepted || stop.State != api.StopStopping):
			return stop, nil
		case err == nil:
			retry = fmt.Sprintf("delivery of stop %s to the provider is uncertain", stop.ID)
		case errors.As(err, &problem) && problem.Code == api.CodeIntegrationNotConnected:
			retry = problem.Detail
		case errors.As(err, &problem):
			return api.ThreadStop{}, err
		case ctx.Err() != nil:
			return api.ThreadStop{}, ctx.Err()
		default:
			retry = err.Error()
		}
		if attempt >= sendAttempts {
			return api.ThreadStop{}, fmt.Errorf("%s (after %d attempts; `atc thread stop %s --key %s` reconciles it, and the thread refuses new work meanwhile)", retry, attempt, threadID, params.Key)
		}
		_, _ = fmt.Fprintf(stderr, "atc: %s; retrying the same stop in %s\n", retry, backoff)
		if !sleep(ctx, backoff) {
			return api.ThreadStop{}, ctx.Err()
		}
		backoff = min(backoff*2, sendBackoffMax)
	}
}

// waitForStop follows the stop until it is resolved.
func waitForStop(ctx context.Context, client *api.Client, threadID, stopID string, stderr io.Writer) (api.ThreadStop, error) {
	var stop api.ThreadStop
	err := follow(ctx, client, threadID, stderr, func(ctx context.Context) (bool, time.Duration, error) {
		got, err := client.ThreadStop(ctx, threadID, stopID)
		if err != nil {
			return false, 0, err
		}
		stop = got
		return got.State != api.StopStopping, 0, nil
	})
	return stop, err
}

// follow runs check now, on every change event for the thread, after
// every reconnect or resync, and on the quiet poll, until it reports
// done — its error then being the outcome. The feed is subscribed
// before each check, so a change between the two is delivered rather
// than missed, and re-established with the last event id whenever it
// drops. check may bound the wait before the next check (zero for the
// poll). An error from a check that is not done ends the follow when it
// is the server's typed problem (the thread deleted, a refusal) and is
// retried with backoff otherwise (the server unreachable).
func follow(ctx context.Context, client *api.Client, threadID string, stderr io.Writer, check func(ctx context.Context) (done bool, within time.Duration, err error)) error {
	var stream *api.EventStream
	defer func() {
		if stream != nil {
			_ = stream.Close()
		}
	}()
	lastEventID := ""
	backoff := sendBackoff
	for {
		if stream == nil {
			opened, err := client.Events(ctx, lastEventID, 0)
			if err != nil && ctx.Err() != nil {
				return ctx.Err()
			}
			if err != nil {
				// The feed is down; the check below still runs, then the
				// backoff before the next attempt.
				_, _ = fmt.Fprintf(stderr, "atc: %v; reconnecting in %s\n", err, backoff)
			}
			stream = opened
		}
		done, within, err := check(ctx)
		if done {
			return err
		}
		if err != nil {
			var problem *api.Problem
			switch {
			case ctx.Err() != nil:
				return ctx.Err()
			case errors.As(err, &problem) && problem.Code == api.CodeThreadNotFound:
				return fmt.Errorf("%s was deleted while waiting", threadID)
			case errors.As(err, &problem):
				return err
			}
			_, _ = fmt.Fprintf(stderr, "atc: %v; reconnecting in %s\n", err, backoff)
		}
		if err != nil || stream == nil {
			if !sleep(ctx, backoff) {
				return ctx.Err()
			}
			backoff = min(backoff*2, sendBackoffMax)
			continue
		}
		backoff = sendBackoff
		// Wait for a reason to check again: a change to this thread, a
		// resync, the poll, or the bound the check set.
		timeout := waitPoll
		if within > 0 {
			timeout = min(timeout, within)
		}
		waitCtx, cancel := context.WithTimeout(ctx, timeout)
		err = nextChange(waitCtx, stream, threadID)
		cancel()
		if err != nil && ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) {
			// The feed dropped: reconnect with the cursor, check first.
			lastEventID = stream.LastEventID
			_ = stream.Close()
			stream = nil
		}
	}
}

func printInputRequest(out io.Writer, request api.ThreadInputRequest) {
	w := newTabWriter(out)
	_, _ = fmt.Fprintf(w, "id\t%s\n", request.ID)
	_, _ = fmt.Fprintf(w, "thread\t%s\n", request.ThreadID)
	_, _ = fmt.Fprintf(w, "status\t%s\n", request.Status)
	if request.Resolution != "" {
		_, _ = fmt.Fprintf(w, "resolution\t%s\n", request.Resolution)
	}
	if request.Unanswerable != "" {
		_, _ = fmt.Fprintf(w, "unanswerable\t%s\n", request.Unanswerable)
	}
	writeQuestions(w, "", request)
	if answer := request.Answer; answer != nil {
		_, _ = fmt.Fprintf(w, "answer\t%s (%s)\n", answer.State, answer.Delivery)
		if answer.Reply != "" {
			_, _ = fmt.Fprintf(w, "  reply\t%q\n", answer.Reply)
		}
		for _, a := range answer.Answers {
			if a.Text != "" {
				_, _ = fmt.Fprintf(w, "  %s\t%q\n", a.QuestionID, a.Text)
			} else {
				_, _ = fmt.Fprintf(w, "  %s\t%s\n", a.QuestionID, strings.Join(a.Choices, ", "))
			}
		}
		if answer.Detail != "" {
			_, _ = fmt.Fprintf(w, "  detail\t%s\n", answer.Detail)
		}
	}
	_, _ = fmt.Fprintf(w, "requested\t%s\n", request.RequestedAt.Format("2006-01-02 15:04:05 MST"))
	if request.ResolvedAt != nil {
		_, _ = fmt.Fprintf(w, "resolved\t%s\n", request.ResolvedAt.Format("2006-01-02 15:04:05 MST"))
	}
	_ = w.Flush()
}

// writeQuestions writes a request's questions with their choices and
// allowances, each line indented by prefix.
func writeQuestions(w io.Writer, prefix string, request api.ThreadInputRequest) {
	for _, question := range request.Questions {
		header := question.Text
		if question.Header != "" {
			header = question.Header + ": " + question.Text
		}
		_, _ = fmt.Fprintf(w, "%squestion\t%s  %s\n", prefix, question.ID, header)
		for _, option := range question.Options {
			text := option.Value
			if option.Label != option.Value {
				text += " (" + option.Label + ")"
			}
			if option.Description != "" {
				text += " — " + option.Description
			}
			_, _ = fmt.Fprintf(w, "%s  choice\t%s\n", prefix, text)
		}
		var allows []string
		if question.AllowsCustom {
			allows = append(allows, "custom text")
		}
		if question.AllowsMultiple {
			allows = append(allows, "several choices")
		}
		if len(allows) > 0 {
			_, _ = fmt.Fprintf(w, "%s  allows\t%s\n", prefix, strings.Join(allows, ", "))
		}
	}
}

func printStop(out io.Writer, stop api.ThreadStop) {
	w := newTabWriter(out)
	_, _ = fmt.Fprintf(w, "id\t%s\n", stop.ID)
	_, _ = fmt.Fprintf(w, "thread\t%s\n", stop.ThreadID)
	_, _ = fmt.Fprintf(w, "state\t%s\n", stop.State)
	_, _ = fmt.Fprintf(w, "delivery\t%s\n", stop.Delivery)
	if stop.Detail != "" {
		_, _ = fmt.Fprintf(w, "detail\t%s\n", stop.Detail)
	}
	_, _ = fmt.Fprintf(w, "created\t%s\n", stop.CreatedAt.Format("2006-01-02 15:04:05 MST"))
	if stop.ResolvedAt != nil {
		_, _ = fmt.Fprintf(w, "resolved\t%s\n", stop.ResolvedAt.Format("2006-01-02 15:04:05 MST"))
	}
	_ = w.Flush()
}
