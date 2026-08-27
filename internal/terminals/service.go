package terminals

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/exitmarker"
	"github.com/jeremytondo/atc/internal/store"
)

// ErrNotFound reports an id with no record; the API layer maps it to 404.
var ErrNotFound = errors.New("terminal not found")

// resource is the event-payload resource kind.
const resource = "terminal"

// Options wires a Service. Now defaults to time.Now; Logger to discard.
type Options struct {
	Repository *store.Terminals
	Adapter    Adapter
	// MarkerDir is where wrappers record exit evidence.
	MarkerDir string
	Hub       *events.Hub
	Logger    *slog.Logger
	Now       func() time.Time
	// HomeDir is the default directory for created terminals.
	HomeDir string
}

// Service owns the in-memory view and the reconciliation that keeps it
// honest. Construct with NewService, Load once at boot, run one blocking
// Reconcile before serving reads, then Run for the background loop.
//
// Locks, outermost first; none is ever taken while holding a later one's
// follower, and no database or file IO happens under mu:
//
//	ops         serializes each mutation's commit — database write, view
//	            change, and event publish move as one unit, so concurrent
//	            mutations cannot leave the database and view divergent or
//	            publish events out of order.
//	reconcileMu serializes whole reconciliation passes (inventory +
//	            apply), so an older inventory can never overwrite a newer
//	            pass's conclusions.
//	mu          guards the view and settling set only.
type Service struct {
	repository *store.Terminals
	adapter    Adapter
	markerDir  string
	hub        *events.Hub
	logger     *slog.Logger
	now        func() time.Time
	home       string
	// verifyInterval is VerifyInterval in production; tests shrink it.
	verifyInterval time.Duration

	ops         sync.Mutex
	reconcileMu sync.Mutex

	mu   sync.Mutex
	view map[string]*entry
	// settling marks records whose create is still verifying: their status
	// is owned by that create, and reconciliation passes leave them alone
	// (they would otherwise flap to missing before the session appears).
	settling map[string]struct{}
}

type entry struct {
	record store.TerminalRecord
	status api.TerminalStatus
}

func NewService(opts Options) *Service {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		repository:     opts.Repository,
		adapter:        opts.Adapter,
		markerDir:      opts.MarkerDir,
		hub:            opts.Hub,
		logger:         opts.Logger,
		now:            opts.Now,
		home:           opts.HomeDir,
		verifyInterval: VerifyInterval,
		view:           make(map[string]*entry),
		settling:       make(map[string]struct{}),
	}
}

// Load rebuilds the view from the database at boot. Statuses start from
// durable evidence alone (exited where recorded, unreachable otherwise)
// and settle in the startup Reconcile that must follow before reads are
// served.
func (s *Service) Load(ctx context.Context) error {
	records, err := s.repository.List(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		status := api.TerminalUnreachable
		if record.ExitedAt != nil {
			status = api.TerminalExited
		}
		s.view[record.ID] = &entry{record: record, status: status}
	}
	return nil
}

// Run is the flat background reconciliation loop.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(ReconcileInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.reconcile(ctx, true)
		}
	}
}

// Reconcile takes one complete inventory and applies the decision table to
// every non-settling terminal:
//
//	recorded exit evidence            → exited (evidence is durable truth)
//	inventory unavailable             → unreachable
//	present and reachable             → running
//	present but unresponsive          → unreachable
//	absent with marker exit evidence  → exited (evidence recorded now)
//	absent without evidence           → missing
//
// Absence from the inventory is never by itself an exit. Reconcile is
// status-only — it is called on the request path (startup, mutations), and
// orphan reaping means bounded kill verification (~seconds per orphan)
// that must never block an HTTP handler. The background loop reaps.
func (s *Service) Reconcile(ctx context.Context) {
	s.reconcile(ctx, false)
}

// reconcile with reap additionally kills orphans: reachable sessions in
// ATC's private namespace that no record claims. It requires a complete
// inventory and refuses to act without one.
func (s *Service) reconcile(ctx context.Context, reap bool) {
	s.reconcileMu.Lock()
	defer s.reconcileMu.Unlock()

	inventory, inventoryErr := s.adapter.Inventory(ctx)
	if inventoryErr != nil {
		s.logger.Warn("terminal inventory unavailable", "error", inventoryErr)
	}
	present := make(map[string]bool, len(inventory)) // name → reachable
	for _, session := range inventory {
		present[session.Name] = session.Reachable
	}

	// Phase 1: decide everything decidable without IO, under the view
	// lock; absent sessions leave with a snapshot for the evidence phase.
	type absentTerminal struct {
		id            string
		createdAt     time.Time
		stopRequested bool
	}
	statuses := make(map[string]api.TerminalStatus)
	var absent []absentTerminal
	var orphans []string
	s.mu.Lock()
	for id, e := range s.view {
		if _, ok := s.settling[id]; ok {
			continue
		}
		switch {
		case e.record.ExitedAt != nil:
			statuses[id] = api.TerminalExited
		case inventoryErr != nil:
			statuses[id] = api.TerminalUnreachable
		default:
			if reachable, ok := present[id]; ok {
				if reachable {
					statuses[id] = api.TerminalRunning
				} else {
					statuses[id] = api.TerminalUnreachable
				}
			} else {
				absent = append(absent, absentTerminal{
					id:            id,
					createdAt:     e.record.CreatedAt,
					stopRequested: e.record.StopRequestedAt != nil,
				})
			}
		}
	}
	if reap && inventoryErr == nil {
		for _, session := range inventory {
			if _, claimed := s.view[session.Name]; !claimed && session.Reachable {
				orphans = append(orphans, session.Name)
			}
		}
	}
	s.mu.Unlock()

	// Phase 2: marker reads and evidence persistence, outside the view
	// lock — a slow disk or a held SQLite writer must not block reads.
	type exitEvidence struct {
		id                   string
		exitedAt, observedAt time.Time
		code                 *int
	}
	var exits []exitEvidence
	for _, terminal := range absent {
		marker, err := exitmarker.Read(s.markerDir, terminal.id)
		if err != nil {
			// Unreadable evidence is no evidence; the honest answer for an
			// absent session without valid evidence is missing.
			s.logger.Warn("unreadable exit marker", "terminal", terminal.id, "error", err)
			statuses[terminal.id] = api.TerminalMissing
			continue
		}
		if !marker.Exited() {
			statuses[terminal.id] = api.TerminalMissing
			continue
		}
		if marker.ExitedAt.Before(terminal.createdAt) {
			// Evidence predating the record belongs to an earlier
			// incarnation of a reused ID (a reaped orphan's late marker).
			s.logger.Warn("stale exit marker ignored", "terminal", terminal.id)
			statuses[terminal.id] = api.TerminalMissing
			continue
		}
		code := marker.Code
		if terminal.stopRequested {
			// An ATC-initiated stop suppresses the exit code — a kill is
			// not a meaningful program result.
			code = nil
		}
		observed := s.now()
		if err := s.repository.RecordExit(ctx, terminal.id, *marker.ExitedAt, observed, code); err != nil {
			// Leave the status untouched this pass; the next one retries.
			s.logger.Error("recording exit evidence", "terminal", terminal.id, "error", err)
			continue
		}
		exits = append(exits, exitEvidence{terminal.id, *marker.ExitedAt, observed, code})
		statuses[terminal.id] = api.TerminalExited
	}

	// Phase 3: apply, guarding entries that were deleted or entered a
	// create's settling window while the locks were down.
	var changed []string
	s.mu.Lock()
	for _, evidence := range exits {
		if e, ok := s.view[evidence.id]; ok && e.record.ExitedAt == nil {
			exitedAt := evidence.exitedAt
			e.record.ExitedAt = &exitedAt
			e.record.ExitCode = evidence.code
			e.record.UpdatedAt = evidence.observedAt
		}
	}
	for id, status := range statuses {
		e, ok := s.view[id]
		if !ok {
			continue
		}
		if _, ok := s.settling[id]; ok {
			continue
		}
		if e.status != status {
			e.status = status
			changed = append(changed, id)
		}
	}
	s.mu.Unlock()

	for _, id := range changed {
		s.hub.Publish(api.EventTerminalUpdated, resource, id)
	}
	for _, name := range orphans {
		// Record-first creation means a recordless session in our private
		// directory is provably ours and abandoned (a delete whose kill
		// could not be verified at the time).
		s.logger.Info("reaping orphan session", "session", name)
		if err := s.adapter.Kill(ctx, name); err != nil {
			s.logger.Warn("orphan session not cleaned", "session", name, "error", err)
		}
	}
}

// Create mints the ID, persists the record before starting the session (no
// orphan window), starts it, and waits a short verification window so the
// common case returns running and a fast-failing app returns exited with
// real evidence. Failures surface through the normal status machinery —
// there is no separate launch-error path.
func (s *Service) Create(ctx context.Context, params api.TerminalCreateParams) (api.Terminal, error) {
	name := params.Name
	if name == "" {
		name = params.App
	}
	if name == "" {
		name = "Shell"
	}
	directory := params.Directory
	if directory == "" {
		directory = s.home
	}

	now := s.now()
	record := store.TerminalRecord{
		Name: name, Directory: directory, App: params.App,
		CreatedAt: now, UpdatedAt: now,
	}
	s.ops.Lock()
	// Insertion is the collision check: a taken ID inserts nothing and
	// re-rolls, with no check-then-insert window.
	for {
		record.ID = randomID()
		inserted, err := s.repository.Insert(ctx, record)
		if err != nil {
			s.ops.Unlock()
			return api.Terminal{}, err
		}
		if inserted {
			break
		}
	}
	// A marker left by an earlier incarnation of this ID must not become
	// this terminal's evidence.
	if err := exitmarker.Remove(s.markerDir, record.ID); err != nil {
		s.logger.Warn("clearing stale exit marker", "terminal", record.ID, "error", err)
	}
	s.mu.Lock()
	s.view[record.ID] = &entry{record: record, status: api.TerminalUnreachable}
	s.settling[record.ID] = struct{}{}
	s.mu.Unlock()
	s.hub.Publish(api.EventTerminalCreated, resource, record.ID)
	s.ops.Unlock()

	if err := s.adapter.Create(ctx, record.ID, CreateSpec{Directory: directory, App: params.App}); err != nil {
		// The record stays: the session may have been born after the
		// client gave up, and the status machinery reports the truth.
		s.logger.Warn("session create failed", "terminal", record.ID, "error", err)
	}
	s.awaitSettled(ctx, record.ID)

	s.mu.Lock()
	delete(s.settling, record.ID)
	s.mu.Unlock()
	s.Reconcile(ctx)
	return s.Get(record.ID)
}

// awaitSettled polls until the session is visibly running or its wrapper
// has recorded an exit, for up to VerifyPasses complete inventories.
func (s *Service) awaitSettled(ctx context.Context, id string) {
	passes, failures := 0, 0
	for passes < VerifyPasses && failures < VerifyFailureCap {
		if marker, err := exitmarker.Read(s.markerDir, id); err == nil && marker.Exited() {
			return
		}
		inventory, err := s.adapter.Inventory(ctx)
		if err != nil {
			failures++
		} else {
			passes++
			failures = 0 // the cap is on consecutive failures
			for _, session := range inventory {
				if session.Name == id && session.Reachable {
					return
				}
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.verifyInterval):
		}
	}
}

// Get serves one terminal from the in-memory view.
func (s *Service) Get(id string) (api.Terminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	e, ok := s.view[id]
	if !ok {
		return api.Terminal{}, ErrNotFound
	}
	return e.terminal(), nil
}

// List serves every terminal from the in-memory view in creation order.
func (s *Service) List() []api.Terminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	terminals := make([]api.Terminal, 0, len(s.view))
	for _, e := range s.view {
		terminals = append(terminals, e.terminal())
	}
	sort.Slice(terminals, func(i, j int) bool {
		if !terminals[i].CreatedAt.Equal(terminals[j].CreatedAt) {
			return terminals[i].CreatedAt.Before(terminals[j].CreatedAt)
		}
		return terminals[i].ID < terminals[j].ID
	})
	return terminals
}

// UpdateName renames the terminal — the only mutable field.
func (s *Service) UpdateName(ctx context.Context, id, name string) (api.Terminal, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	now := s.now()
	ok, err := s.repository.UpdateName(ctx, id, name, now)
	if err != nil {
		return api.Terminal{}, err
	}
	if !ok {
		return api.Terminal{}, ErrNotFound
	}
	var terminal api.Terminal
	s.mu.Lock()
	if e, exists := s.view[id]; exists {
		e.record.Name = name
		e.record.UpdatedAt = now
		terminal = e.terminal()
	}
	s.mu.Unlock()
	s.hub.Publish(api.EventTerminalUpdated, resource, id)
	return terminal, nil
}

// Delete is best-effort by design: stop intent is persisted first, the
// kill is attempted, and the record is removed even when the kill cannot
// be verified — the user's intent wins over zmx's health. A session that
// survives is reaped by the background loop once it is reachable,
// recordless, and inside ATC's private directory.
//
// The whole operation runs on a detached context: a delete that reached
// the service is the user's intent, and a client disconnect mid-way must
// not leave it half-committed (intent recorded, record still listed).
func (s *Service) Delete(ctx context.Context, id string) error {
	detached := context.WithoutCancel(ctx)
	now := s.now()
	ok, err := s.repository.RecordStopIntent(detached, id, now)
	if err != nil {
		return err
	}
	if !ok {
		return ErrNotFound
	}
	s.mu.Lock()
	if e, exists := s.view[id]; exists {
		e.record.StopRequestedAt = &now
		e.record.UpdatedAt = now
	}
	s.mu.Unlock()

	if err := s.adapter.Kill(detached, id); err != nil {
		s.logger.Warn("session kill unverified", "terminal", id, "error", err)
	}

	s.ops.Lock()
	deleted, err := s.repository.Delete(detached, id)
	if err != nil {
		s.ops.Unlock()
		return err
	}
	if !deleted {
		// A concurrent delete won; for this caller the terminal no longer
		// exists, same as any other absent id.
		s.ops.Unlock()
		return ErrNotFound
	}
	s.mu.Lock()
	delete(s.view, id)
	delete(s.settling, id)
	s.mu.Unlock()
	if err := exitmarker.Remove(s.markerDir, id); err != nil {
		s.logger.Warn("removing exit marker", "terminal", id, "error", err)
	}
	s.hub.Publish(api.EventTerminalDeleted, resource, id)
	s.ops.Unlock()

	s.Reconcile(detached)
	return nil
}

// terminal converts the entry to its wire shape. Callers hold s.mu.
func (e *entry) terminal() api.Terminal {
	terminal := api.Terminal{
		ID:        e.record.ID,
		Name:      e.record.Name,
		Directory: e.record.Directory,
		App:       e.record.App,
		Status:    e.status,
		CreatedAt: e.record.CreatedAt,
		UpdatedAt: e.record.UpdatedAt,
	}
	if e.status == api.TerminalExited && e.record.ExitCode != nil {
		code := *e.record.ExitCode
		terminal.ExitCode = &code
	}
	return terminal
}
