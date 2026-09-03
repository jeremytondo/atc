// Package application is the coordinator above the domains (ATC-296):
// the workflows that cross domain boundaries — deleting a terminal and
// everything other domains hold about it, deleting a space and every
// terminal in it, a project change and the thread classification that
// follows it — composed once here and called from every entry point that
// needs them, so the HTTP handlers stay thin and no domain imports
// another. Domains keep their own invariants; this package only orders
// their calls.
package application

import (
	"context"
	"log/slog"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
)

// Options wires a Coordinator; every domain is required. Cleanups run
// after a terminal delete commits: each clears per-terminal state owned
// outside the terminals domain — an Integration's hook secret
// registrations and their files. Each must be a barrier (hookauth
// Deregister's contract): it returns only once no delivery can mutate
// state on the launch's behalf, so the threads view can converge
// afterwards without racing late evidence. A nil Logger discards the
// failures a workflow absorbs rather than surfaces.
type Options struct {
	Terminals *terminals.Service
	Threads   *threads.Service
	Projects  *projects.Service
	Cleanups  []func(terminalID string)
	Logger    *slog.Logger
}

// Coordinator runs the cross-domain workflows. Construct with New.
type Coordinator struct {
	terminals *terminals.Service
	threads   *threads.Service
	projects  *projects.Service
	cleanups  []func(terminalID string)
	logger    *slog.Logger
}

// New wires a Coordinator. A missing domain is a boot-time mistake,
// refused here rather than at the first request that needs it.
func New(opts Options) *Coordinator {
	if opts.Terminals == nil || opts.Threads == nil || opts.Projects == nil {
		panic("application.New: Terminals, Threads, and Projects must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Coordinator{
		terminals: opts.Terminals, threads: opts.Threads, projects: opts.Projects,
		cleanups: opts.Cleanups, logger: opts.Logger,
	}
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
