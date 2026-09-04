// Package application is the coordinator above the domains (ATC-296,
// ATC-297): the workflows that cross domain boundaries — creating a
// terminal in any of its modes, resuming a thread into one, deleting a
// terminal and everything other domains hold about it, deleting a space
// and every terminal in it, a project change and the thread
// classification that follows it, starting a thread in an Integration's
// program (ATC-289) — composed once here and called from
// every entry point that needs them, so the HTTP handlers stay thin and
// no domain imports another. Domains keep their own invariants; this
// package only orders their calls.
package application

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"

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
		cleanups: opts.Cleanups, logger: opts.Logger,
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
	if _, err := c.threads.SubmitTurn(ctx, id); err != nil {
		c.discard(ctx, params.IntegrationID, prepared.ProviderID)
		return api.Thread{}, err
	}
	if err := prepared.Dispatch(ctx); err != nil {
		c.discard(ctx, params.IntegrationID, prepared.ProviderID)
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
