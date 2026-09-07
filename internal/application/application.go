// Package application is the coordinator above the domains (ATC-296,
// ATC-297): the workflows that cross domain boundaries — creating a
// terminal in any of its modes, resuming a thread into one, deleting a
// terminal and everything other domains hold about it, deleting a space
// and every terminal in it, a project change and the thread
// classification that follows it, starting a thread in an Integration's
// program (ATC-289), sending a message into one, deciding an approval
// request on one, answering a structured request on one, and stopping
// its work (ATC-307, ATC-308) — composed once here and called from
// every entry point that needs them, so the HTTP handlers stay thin and
// no domain imports another. Domains keep their own invariants; this
// package only orders their calls.
//
// The writes that reach a program are dispatched one at a time per
// thread (threadLock): the domain records each before its dispatch, and
// serializing the dispatches gives a stop its defined place — a message
// accepted before the stop has reached the program by the time the stop
// does, so it is inside the stop's scope and never starts afterwards.
package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
)

// ErrLaunchModeConflict refuses a terminal create naming more than one of
// command, appId, and threadId: the modes are exclusive and never
// silently resolved.
var ErrLaunchModeConflict = errors.New("command, appId, and threadId are mutually exclusive")

// ErrThreadCreateInvalid refuses a thread create whose input cannot be
// acted on: an empty prompt or model, or an option pair without an id.
var ErrThreadCreateInvalid = errors.New("invalid thread create")

// Options wires a Coordinator; every domain is required. Cleanups run
// after a terminal delete commits: each clears per-terminal state owned
// outside the terminals domain — an Integration's hook secret
// registrations and their files. Each must be a barrier (hookauth
// Deregister's contract): it returns only once no delivery can mutate
// state on the launch's behalf, so the threads view can converge
// afterwards without racing late evidence. A nil Logger discards the
// failures a workflow absorbs rather than surfaces.
type Options struct {
	Terminals    *terminals.Service
	Threads      *threads.Service
	Projects     *projects.Service
	Integrations *integrations.Service
	Cleanups     []func(terminalID string)
	Logger       *slog.Logger
}

// Coordinator runs the cross-domain workflows. Construct with New.
type Coordinator struct {
	terminals    *terminals.Service
	threads      *threads.Service
	projects     *projects.Service
	integrations *integrations.Service
	cleanups     []func(terminalID string)
	logger       *slog.Logger

	// locks holds one dispatch lock per thread with a write in flight
	// (threadLock), dropped when the last holder releases it.
	mu    sync.Mutex
	locks map[string]*threadLock
}

// threadLock serializes the dispatches to one thread's program: a
// one-slot channel taken for the duration of a write, waited on with the
// caller's context, and a count of the goroutines holding or waiting for
// it so the entry can be dropped once none does.
type threadLock struct {
	slot  chan struct{}
	users int
}

// lockThread takes the thread's dispatch lock, waiting no longer than
// the context allows, and returns the release.
func (c *Coordinator) lockThread(ctx context.Context, threadID string) (func(), error) {
	c.mu.Lock()
	lock, ok := c.locks[threadID]
	if !ok {
		lock = &threadLock{slot: make(chan struct{}, 1)}
		c.locks[threadID] = lock
	}
	lock.users++
	c.mu.Unlock()
	release := func() {
		c.mu.Lock()
		lock.users--
		if lock.users == 0 {
			delete(c.locks, threadID)
		}
		c.mu.Unlock()
	}
	select {
	case lock.slot <- struct{}{}:
		return func() {
			<-lock.slot
			release()
		}, nil
	case <-ctx.Done():
		release()
		return nil, ctx.Err()
	}
}

// New wires a Coordinator. A missing domain is a boot-time mistake,
// refused here rather than at the first request that needs it.
func New(opts Options) *Coordinator {
	if opts.Terminals == nil || opts.Threads == nil || opts.Projects == nil || opts.Integrations == nil {
		panic("application.New: Terminals, Threads, Projects, and Integrations must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Coordinator{
		terminals: opts.Terminals, threads: opts.Threads, projects: opts.Projects, integrations: opts.Integrations,
		cleanups: opts.Cleanups, logger: opts.Logger, locks: make(map[string]*threadLock),
	}
}

// launchMode is the one selector a create names.
type launchMode int

const (
	modeShell launchMode = iota
	modeCommand
	modeApp
	modeThread
)

// mode validates selector exclusivity: at most one of command, appId,
// threadId.
func mode(params api.TerminalCreateParams) (launchMode, error) {
	selected, count := modeShell, 0
	for i, selector := range []string{params.Command, params.AppID, params.ThreadID} {
		if selector != "" {
			selected = launchMode(i + 1)
			count++
		}
	}
	if count > 1 {
		return modeShell, ErrLaunchModeConflict
	}
	return selected, nil
}

// placement strips the launch selectors: what the terminals domain sees
// of a create in App or thread mode.
func placement(params api.TerminalCreateParams) api.TerminalCreateParams {
	params.Command, params.AppID, params.ThreadID = "", "", ""
	return params
}

// CreateTerminal is the one launch surface: a shell, a command, an App,
// or a thread's resume, by the selector the request names (never more
// than one). It reports whether a terminal was created — a thread resume
// reuses the terminal already holding the thread when one is running or
// unreachable, and reuse changes nothing about it — so the API can
// answer 201 or 200. Every refusal lands before a record exists; a
// resume whose association cannot persist afterwards is discarded.
func (c *Coordinator) CreateTerminal(ctx context.Context, params api.TerminalCreateParams) (api.Terminal, bool, error) {
	selected, err := mode(params)
	if err != nil {
		return api.Terminal{}, false, err
	}
	switch selected {
	case modeThread:
		// The threads domain owns the reuse decision and serializes
		// concurrent resumes of one thread; the catalog resolves the
		// resume, placed as the request asks.
		return c.threads.Open(ctx, params.ThreadID, resumer{c: c, placement: placement(params)})
	case modeApp:
		launch, err := c.integrations.ResolveLaunch(ctx, params.AppID)
		if err != nil {
			return api.Terminal{}, false, err
		}
		terminal, err := c.terminals.CreateForApp(ctx, placement(params), launch)
		return terminal, err == nil, err
	default:
		terminal, err := c.terminals.Create(ctx, params)
		return terminal, err == nil, err
	}
}

// resumer is the threads domain's seam for one resume: it carries the
// request's placement to the terminal the catalog's resume input creates,
// and discards that terminal through the full deletion workflow when the
// association could not be persisted.
type resumer struct {
	c         *Coordinator
	placement api.TerminalCreateParams
}

func (r resumer) Resume(ctx context.Context, req threads.ResumeRequest) (api.Terminal, error) {
	launch, err := r.c.integrations.ResolveResume(ctx, req)
	if err != nil {
		return api.Terminal{}, err
	}
	return r.c.terminals.CreateForApp(ctx, r.placement, launch)
}

func (r resumer) Discard(ctx context.Context, terminalID string) error {
	return r.c.DeleteTerminal(ctx, terminalID)
}

// DeleteTerminal is the complete terminal deletion workflow: the domain's
// best-effort delete (stop intent, kill, record, event), the Integration
// cleanups, then the threads view converging on the cleared linkage.
// Detached like the delete itself: a client disconnect after the commit
// must not leave hook secrets live or the view linked to a deleted
// terminal.
func (c *Coordinator) DeleteTerminal(ctx context.Context, id string) error {
	detached := context.WithoutCancel(ctx)
	if err := c.terminals.Delete(detached, id); err != nil {
		return err
	}
	for _, cleanup := range c.cleanups {
		cleanup(id)
	}
	// The schema's ON DELETE SET NULL already cleared the rows; this
	// converges the threads view and publishes the linkage change.
	c.threads.TerminalRemoved(detached, id)
	return nil
}

// DeleteSpace deletes a space and every terminal in it, each through
// DeleteTerminal — the same workflow an individual delete runs.
func (c *Coordinator) DeleteSpace(ctx context.Context, id string) error {
	return c.terminals.DeleteSpace(context.WithoutCancel(ctx), id, c.DeleteTerminal)
}

// CreateProject creates the project, then classifies the unassigned
// threads its directory now contains (ATC-295).
func (c *Coordinator) CreateProject(ctx context.Context, params api.ProjectCreateParams) (api.Project, error) {
	project, err := c.projects.Create(ctx, params)
	if err != nil {
		return api.Project{}, err
	}
	c.backfill(ctx, project.ID)
	return project, nil
}

// UpdateProject applies the patch; a move classifies the unassigned
// threads under the new directory and never rewrites an existing
// association.
func (c *Coordinator) UpdateProject(ctx context.Context, id string, params api.ProjectUpdateParams) (api.Project, error) {
	project, moved, err := c.projects.Update(ctx, id, params)
	if err != nil {
		return api.Project{}, err
	}
	if moved {
		c.backfill(ctx, project.ID)
	}
	return project, nil
}

// DeleteProject deletes the project under the threads domain's mutation
// lock: the schema clears the project's thread associations and the
// threads view converges before any observation can copy the stale id
// back. Threads survive, unassigned.
func (c *Coordinator) DeleteProject(ctx context.Context, id string) error {
	return c.threads.DeleteProject(ctx, id, func() error { return c.projects.Delete(ctx, id) })
}

// CreateThread starts a new conversation with its first prompt in an
// Integration's program (ATC-289). The record is created, with a
// provisional running turn, before the command is dispatched: the
// program's first report of the conversation — which can arrive before
// the dispatch answers — must find the record and bind its turn, never
// mint a second thread. A failed dispatch discards that record, so no
// thread remains; the thread returned is the record as it stands once the
// program has committed the creation. Model and options pass through
// untouched: the program is their only judge.
func (c *Coordinator) CreateThread(ctx context.Context, params api.ThreadCreateParams) (api.Thread, error) {
	return c.StartThread(ctx, params, nil)
}

// StartThread is CreateThread with a durability seam (ATC-302): recorded,
// when non-nil, runs once the thread record and its provisional turn
// exist and before the command is dispatched, with the ids a caller must
// hold to recognize the conversation after a crash. A caller that owes
// work to someone else persists them there; if it cannot, the create is
// discarded before anything is sent, so a start that was never recorded
// is a start that never happened. What runs before recorded is
// idempotent from the program's point of view — nothing has reached it.
// A recorded create whose dispatch went unanswered
// (integrations.ErrThreadCreationUncertain) keeps its record: the program
// may hold the conversation, its later report finds the record and binds
// the turn, and the caller — not this workflow — judges what to tell its
// user. An unrecorded create discards it, as CreateThread always has.
func (c *Coordinator) StartThread(ctx context.Context, params api.ThreadCreateParams, recorded func(threadID, turnID string) error) (api.Thread, error) {
	switch {
	case strings.TrimSpace(params.Prompt) == "":
		return api.Thread{}, fmt.Errorf("%w: prompt is empty", ErrThreadCreateInvalid)
	case strings.TrimSpace(params.Model) == "":
		return api.Thread{}, fmt.Errorf("%w: model is empty", ErrThreadCreateInvalid)
	}
	for _, option := range params.Options {
		if strings.TrimSpace(option.ID) == "" {
			return api.Thread{}, fmt.Errorf("%w: an option has no id", ErrThreadCreateInvalid)
		}
	}
	prepare, err := c.integrations.ResolveThreadCreation(params.IntegrationID, params.Agent)
	if err != nil {
		return api.Thread{}, err
	}
	project, err := c.projects.Get(ctx, params.ProjectID)
	if err != nil {
		return api.Thread{}, err
	}
	prepared, err := prepare(ctx, integrations.ThreadCreation{
		AgentID: params.Agent, Directory: project.Directory, Prompt: params.Prompt, Model: params.Model, Options: params.Options,
	})
	if err != nil {
		return api.Thread{}, err
	}
	id, err := c.threads.ObserveExternal(ctx, threads.ExternalObservation{
		IntegrationID:    params.IntegrationID,
		ProviderID:       prepared.ProviderID,
		InitialDirectory: project.Directory,
		AgentID:          params.Agent,
		Title:            prepared.Title,
		Metadata:         threads.Metadata{Model: params.Model, Cwd: project.Directory},
	})
	if err != nil {
		return api.Thread{}, err
	}
	turnID, err := c.threads.SubmitTurn(ctx, id)
	if err != nil {
		c.discard(ctx, params.IntegrationID, prepared.ProviderID)
		return api.Thread{}, err
	}
	if recorded != nil {
		if err := recorded(id, turnID); err != nil {
			c.discard(ctx, params.IntegrationID, prepared.ProviderID)
			return api.Thread{}, fmt.Errorf("recording the thread before dispatch: %w", err)
		}
	}
	if err := prepared.Dispatch(ctx); err != nil {
		if recorded == nil || !errors.Is(err, integrations.ErrThreadCreationUncertain) {
			c.discard(ctx, params.IntegrationID, prepared.ProviderID)
		}
		return api.Thread{}, err
	}
	return c.threads.Get(id)
}

// discard removes the record a thread create pre-created when the
// creation did not happen. Detached: a client that gave up mid-dispatch
// must not leave a record for a conversation that does not exist. A
// failure is logged, not surfaced — the dispatch failure is the answer,
// and the orphan coerces to unknown at the next boot like any unheld
// thread.
func (c *Coordinator) discard(ctx context.Context, integrationID, providerID string) {
	if err := c.threads.DiscardExternal(context.WithoutCancel(ctx), integrationID, providerID); err != nil {
		c.logger.Error("discarding the thread pre-created for a failed create", "integration", integrationID, "error", err)
	}
}

// backfill reclassifies the unassigned threads after a project is created
// or moved. Detached: the project is committed, and a client that
// disconnects must not leave threads it should own unassigned. A failure
// is logged, not surfaced — the project exists either way, and because
// the backfill scans every unassigned thread, the next create or move
// repairs it.
func (c *Coordinator) backfill(ctx context.Context, projectID string) {
	if err := c.threads.Backfill(context.WithoutCancel(ctx)); err != nil {
		c.logger.Error("backfilling threads after a project change", "project", projectID, "error", err)
	}
}

// SendMessage directs a message at an existing thread (ATC-307): the
// domain records it — under the client's key, so a resubmission finds
// the same message — before the thread's Integration dispatches it, and
// records the outcome after. A message the program committed is
// accepted; one it refused is withdrawn and the refusal returned; one it
// never answered stays recorded with its delivery uncertain, for a
// resubmission under the same key to retry the exact same command. A
// key already recorded returns that message, retrying only an uncertain
// delivery. The submission is refused before anything is recorded when
// the text is blank, the Integration cannot send, or the program is not
// connected; while another submission is pending on the thread; and
// while a stop is being confirmed on it (ATC-308).
func (c *Coordinator) SendMessage(ctx context.Context, threadID string, params api.ThreadMessageParams) (api.ThreadMessage, error) {
	if strings.TrimSpace(params.Text) == "" {
		return api.ThreadMessage{}, fmt.Errorf("%w: text is empty", threads.ErrMessageInvalid)
	}
	integrationID, providerID, err := c.threads.Identity(threadID)
	if err != nil {
		return api.ThreadMessage{}, err
	}
	if params.Key != "" {
		// A replay is answered from the record whatever the program's
		// state; only an uncertain delivery goes back to it.
		recorded, err := c.threads.MessageByKey(ctx, threadID, params.Key)
		switch {
		case err == nil && recorded.Delivery == api.MessageAccepted:
			return recorded, nil
		case err != nil && !errors.Is(err, threads.ErrNotFound):
			return api.ThreadMessage{}, err
		}
	}
	messenger, err := c.integrations.ResolveThreadMessenger(integrationID)
	if err != nil {
		return api.ThreadMessage{}, err
	}
	unlock, err := c.lockThread(ctx, threadID)
	if err != nil {
		return api.ThreadMessage{}, err
	}
	defer unlock()
	prepared, err := messenger.PrepareMessage(ctx, providerID)
	if err != nil {
		return api.ThreadMessage{}, err
	}
	message, err := c.threads.SubmitMessage(ctx, threadID, threads.Submission{Text: params.Text, Key: params.Key, Steers: prepared.Steers})
	if err != nil {
		return api.ThreadMessage{}, err
	}
	if message.Delivery == api.MessageAccepted {
		return message, nil
	}
	err = prepared.Dispatch(ctx, integrations.ThreadMessage{ID: message.ID, Text: message.Text, CreatedAt: message.CreatedAt})
	switch {
	case err == nil:
		return c.threads.MessageDelivered(ctx, threadID, message.ID)
	case errors.Is(err, integrations.ErrMessageRejected):
		// Detached: a client that gave up mid-dispatch must not leave a
		// pending turn the program will never start.
		if recordErr := c.threads.MessageRejected(context.WithoutCancel(ctx), threadID, message.ID, err.Error()); recordErr != nil {
			c.logger.Error("recording a rejected message", "thread", threadID, "message", message.ID, "error", recordErr)
		}
		return api.ThreadMessage{}, err
	default:
		// The program may hold the message; nothing here can say. The
		// record stands, uncertain, for the same key to reconcile.
		c.logger.Warn("message delivery uncertain", "thread", threadID, "message", message.ID, "error", err)
		return message, nil
	}
}

// DecideApproval answers one pending approval request on a thread
// (ATC-307) with one of the decisions it offers. The domain validates
// the decision and records it as sent, the thread's Integration
// dispatches it, and the outcome is recorded: committed resolves the
// request; refused reopens it; unanswered leaves the decision sent — the
// same decision again reconciles under the same identity, a different
// one is refused until the answer is known.
func (c *Coordinator) DecideApproval(ctx context.Context, threadID, approvalID string, params api.ApprovalDecisionParams) (api.ThreadApproval, error) {
	integrationID, _, err := c.threads.Identity(threadID)
	if err != nil {
		return api.ThreadApproval{}, err
	}
	decider, err := c.integrations.ResolveApprovalDecider(integrationID)
	if err != nil {
		return api.ThreadApproval{}, err
	}
	unlock, err := c.lockThread(ctx, threadID)
	if err != nil {
		return api.ThreadApproval{}, err
	}
	defer unlock()
	req, err := c.threads.BeginDecision(threadID, approvalID, params.Decision)
	if err != nil {
		return api.ThreadApproval{}, err
	}
	err = decider.DecideApproval(ctx, integrations.ApprovalDecision{
		ProviderID: req.ProviderID, RequestID: req.RequestID, Decision: req.Decision, Key: req.Key,
	})
	switch {
	case err == nil:
		return c.threads.ResolveDecision(threadID, approvalID)
	case errors.Is(err, integrations.ErrDecisionRejected), errors.Is(err, integrations.ErrNotConnected):
		// Nothing reached the request: it takes another decision.
		c.threads.AbandonDecision(threadID, approvalID)
		return api.ThreadApproval{}, err
	default:
		c.logger.Warn("approval decision uncertain", "thread", threadID, "approval", approvalID, "error", err)
		return api.ThreadApproval{}, err
	}
}

// AnswerInput answers one pending structured request on a thread
// (ATC-308) with one complete answer set. The domain validates the set
// against the request and records the answer durably; the thread's
// Integration dispatches it; and the outcome of the dispatch is
// recorded: committed is delivery accepted — the request's resolution
// is still the Integration's evidence to report — refused fails the
// answer and the request takes another; unanswered leaves the answer
// sent with its delivery uncertain, for the same answers to reconcile.
// The same answers again recover the recorded answer — while the
// program is not connected too, when nothing needs sending; different
// ones are refused until the evidence is in. Refused before anything is
// recorded when the Integration cannot answer or the program is not
// connected, and while a stop is being confirmed on the thread.
func (c *Coordinator) AnswerInput(ctx context.Context, threadID, requestID string, params api.InputAnswerParams) (api.ThreadInputRequest, error) {
	integrationID, providerID, err := c.threads.Identity(threadID)
	if err != nil {
		return api.ThreadInputRequest{}, err
	}
	answerer, err := c.integrations.ResolveInputAnswerer(integrationID)
	if err != nil {
		return api.ThreadInputRequest{}, err
	}
	unlock, err := c.lockThread(ctx, threadID)
	if err != nil {
		return api.ThreadInputRequest{}, err
	}
	defer unlock()
	dispatch, err := answerer.PrepareAnswer(ctx, providerID)
	if err != nil {
		if recovered, found, recoverErr := c.threads.RecoverAnswer(threadID, requestID, params.Answers); recoverErr == nil && found && !recovered.Dispatch {
			return c.threads.InputRequest(threadID, requestID)
		}
		return api.ThreadInputRequest{}, err
	}
	req, err := c.threads.BeginAnswer(ctx, threadID, requestID, params.Answers)
	if err != nil {
		return api.ThreadInputRequest{}, err
	}
	if !req.Dispatch {
		return c.threads.InputRequest(threadID, requestID)
	}
	err = dispatch(ctx, integrations.InputAnswer{RequestID: req.RequestID, Answers: req.Answers, Key: req.AnswerID, CreatedAt: req.CreatedAt})
	switch {
	case err == nil:
		return c.threads.AnswerDelivered(ctx, threadID, req.AnswerID)
	case errors.Is(err, integrations.ErrAnswerRejected):
		// Detached: a client that gave up mid-dispatch must not leave an
		// answer sent that the program refused.
		if recordErr := c.threads.AnswerFailed(context.WithoutCancel(ctx), threadID, req.AnswerID, err.Error()); recordErr != nil {
			c.logger.Error("recording a refused answer", "thread", threadID, "answer", req.AnswerID, "error", recordErr)
		}
		return api.ThreadInputRequest{}, err
	default:
		c.logger.Warn("answer delivery uncertain", "thread", threadID, "answer", req.AnswerID, "error", err)
		return c.threads.InputRequest(threadID, requestID)
	}
}

// StopThread stops a thread's work (ATC-308): the domain records the
// stop with its scope — behind the thread's dispatch lock, so every
// write accepted before it has reached the program — the thread's
// Integration dispatches it, and the dispatch's outcome is recorded:
// committed is delivery accepted, the stop's resolution still the
// Integration's evidence to report; refused fails the stop for good;
// unanswered leaves it stopping with its delivery uncertain, for the
// same operation to reconcile. The stop recorded under the client's
// key, or one still stopping, is returned as it stands — while the
// program is not connected too, when nothing needs sending —
// re-dispatched only if its delivery was uncertain. Refused before
// anything is recorded when the Integration cannot stop or the program
// is not connected.
func (c *Coordinator) StopThread(ctx context.Context, threadID string, params api.ThreadStopParams) (api.ThreadStop, error) {
	integrationID, providerID, err := c.threads.Identity(threadID)
	if err != nil {
		return api.ThreadStop{}, err
	}
	stopper, err := c.integrations.ResolveThreadStopper(integrationID)
	if err != nil {
		return api.ThreadStop{}, err
	}
	unlock, err := c.lockThread(ctx, threadID)
	if err != nil {
		return api.ThreadStop{}, err
	}
	defer unlock()
	dispatch, err := stopper.PrepareStop(ctx, providerID)
	if err != nil {
		if recovered, stop, found, recoverErr := c.threads.RecoverStop(threadID, params.Key); recoverErr == nil && found && !recovered.Dispatch {
			return stop, nil
		}
		return api.ThreadStop{}, err
	}
	req, stop, err := c.threads.BeginStop(ctx, threadID, params.Key)
	if err != nil {
		return api.ThreadStop{}, err
	}
	if !req.Dispatch {
		return stop, nil
	}
	err = dispatch(ctx, integrations.ThreadStop{Key: req.StopID, CreatedAt: req.CreatedAt})
	switch {
	case err == nil:
		return c.threads.StopDelivered(ctx, threadID, req.StopID)
	case errors.Is(err, integrations.ErrStopRejected):
		// Detached: the refusal must be recorded, or the thread stays
		// refusing work for a stop the program will never perform.
		if _, recordErr := c.threads.StopFailed(context.WithoutCancel(ctx), threadID, req.StopID, err.Error()); recordErr != nil {
			c.logger.Error("recording a refused stop", "thread", threadID, "stop", req.StopID, "error", recordErr)
		}
		return api.ThreadStop{}, err
	default:
		c.logger.Warn("stop delivery uncertain", "thread", threadID, "stop", req.StopID, "error", err)
		return stop, nil
	}
}
