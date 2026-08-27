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
	"github.com/jeremytondo/atc/internal/store"
)

// ErrNotFound reports an id with no record; the API layer maps it to 404.
var ErrNotFound = errors.New("terminal not found")

// Repository is the durable-facts seam (store.Terminals in production).
type Repository interface {
	Insert(ctx context.Context, record store.TerminalRecord) error
	List(ctx context.Context) ([]store.TerminalRecord, error)
	IDTaken(ctx context.Context, id string) (bool, error)
	UpdateName(ctx context.Context, id, name string, at time.Time) (bool, error)
	RecordStopIntent(ctx context.Context, id string, at time.Time) (bool, error)
	RecordExit(ctx context.Context, id string, at time.Time, code *int) error
	Delete(ctx context.Context, id string) (bool, error)
}

// resource is the event-payload resource kind.
const resource = "terminal"

// Options wires a Service. Now defaults to time.Now; Logger to discard.
type Options struct {
	Repository Repository
	Adapter    Adapter
	Markers    Markers
	Hub        *events.Hub
	Logger     *slog.Logger
	Now        func() time.Time
	// HomeDir is the default directory for created terminals.
	HomeDir string
}

// Service owns the in-memory view and the reconciliation that keeps it
// honest. Construct with NewService, Load once at boot, run one blocking
// Reconcile before serving reads, then Run for the background loop.
type Service struct {
	repository Repository
	adapter    Adapter
	markers    Markers
	hub        *events.Hub
	logger     *slog.Logger
	now        func() time.Time
	home       string
	// verifyInterval is VerifyInterval in production; tests shrink it.
	verifyInterval time.Duration

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
		markers:        opts.Markers,
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
	inventory, inventoryErr := s.adapter.Inventory(ctx)
	if inventoryErr != nil {
		s.logger.Warn("terminal inventory unavailable", "error", inventoryErr)
	}
	present := make(map[string]bool, len(inventory)) // name → reachable
	for _, session := range inventory {
		present[session.Name] = session.Reachable
	}

	s.mu.Lock()
	var changed []string
	var orphans []string
	for id, e := range s.view {
		if _, ok := s.settling[id]; ok {
			continue
		}
		status := s.decide(ctx, e, present, inventoryErr)
		if status != e.status {
			e.status = status
			changed = append(changed, id)
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

// decide applies the status table to one terminal and records marker
// evidence the first time it is seen. Callers hold s.mu.
func (s *Service) decide(ctx context.Context, e *entry, present map[string]bool, inventoryErr error) api.TerminalStatus {
	if e.record.ExitedAt != nil {
		return api.TerminalExited
	}
	if inventoryErr != nil {
		return api.TerminalUnreachable
	}
	if reachable, ok := present[e.record.ID]; ok {
		if reachable {
			return api.TerminalRunning
		}
		return api.TerminalUnreachable
	}
	marker, err := s.markers.Read(e.record.ID)
	if err != nil {
		// Unreadable evidence is no evidence; the honest answer for an
		// absent session without valid evidence is missing.
		s.logger.Warn("unreadable exit marker", "terminal", e.record.ID, "error", err)
		return api.TerminalMissing
	}
	if !marker.Exited() {
		return api.TerminalMissing
	}
	s.recordExit(ctx, e, marker.ExitedAt, marker.Code)
	return api.TerminalExited
}

// recordExit persists marker evidence into the record. An ATC-initiated
// stop suppresses the exit code — a kill is not a meaningful program
// result. Callers hold s.mu.
func (s *Service) recordExit(ctx context.Context, e *entry, exitedAt *time.Time, code *int) {
	if e.record.StopRequestedAt != nil {
		code = nil
	}
	if err := s.repository.RecordExit(ctx, e.record.ID, *exitedAt, code); err != nil {
		s.logger.Error("recording exit evidence", "terminal", e.record.ID, "error", err)
		return
	}
	e.record.ExitedAt = exitedAt
	e.record.ExitCode = code
	e.record.UpdatedAt = s.now()
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

	id, err := s.mint(ctx)
	if err != nil {
		return api.Terminal{}, err
	}
	now := s.now()
	record := store.TerminalRecord{
		ID: id, Name: name, Directory: directory, App: params.App,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.repository.Insert(ctx, record); err != nil {
		return api.Terminal{}, err
	}
	s.mu.Lock()
	s.view[id] = &entry{record: record, status: api.TerminalUnreachable}
	s.settling[id] = struct{}{}
	s.mu.Unlock()
	s.hub.Publish(api.EventTerminalCreated, resource, id)

	if err := s.adapter.Create(ctx, id, CreateSpec{Directory: directory, App: params.App}); err != nil {
		// The record stays: the session may have been born after the
		// client gave up, and the status machinery reports the truth.
		s.logger.Warn("session create failed", "terminal", id, "error", err)
	}
	s.awaitSettled(ctx, id)

	s.mu.Lock()
	delete(s.settling, id)
	s.mu.Unlock()
	s.Reconcile(ctx)
	return s.Get(id)
}

// awaitSettled polls until the session is visibly running or its wrapper
// has recorded an exit, for up to VerifyPasses complete inventories.
func (s *Service) awaitSettled(ctx context.Context, id string) {
	passes, failures := 0, 0
	for passes < VerifyPasses && failures < VerifyFailureCap {
		if marker, err := s.markers.Read(id); err == nil && marker.Exited() {
			return
		}
		inventory, err := s.adapter.Inventory(ctx)
		if err != nil {
			failures++
		} else {
			passes++
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

// mint draws IDs until one is free in both the database and the view.
func (s *Service) mint(ctx context.Context) (string, error) {
	for {
		id := randomID()
		taken, err := s.repository.IDTaken(ctx, id)
		if err != nil {
			return "", err
		}
		s.mu.Lock()
		_, inView := s.view[id]
		s.mu.Unlock()
		if !taken && !inView {
			return id, nil
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
	now := s.now()
	ok, err := s.repository.UpdateName(ctx, id, name, now)
	if err != nil {
		return api.Terminal{}, err
	}
	if !ok {
		return api.Terminal{}, ErrNotFound
	}
	s.mu.Lock()
	if e, exists := s.view[id]; exists {
		e.record.Name = name
		e.record.UpdatedAt = now
	}
	s.mu.Unlock()
	s.hub.Publish(api.EventTerminalUpdated, resource, id)
	return s.Get(id)
}

// Delete is best-effort by design: stop intent is persisted first, the
// kill is attempted, and the record is removed even when the kill cannot
// be verified — the user's intent wins over zmx's health. A session that
// survives is reaped by reconciliation once it is reachable, recordless,
// and inside ATC's private directory.
func (s *Service) Delete(ctx context.Context, id string) error {
	now := s.now()
	ok, err := s.repository.RecordStopIntent(ctx, id, now)
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

	if err := s.adapter.Kill(ctx, id); err != nil {
		s.logger.Warn("session kill unverified", "terminal", id, "error", err)
	}

	if _, err := s.repository.Delete(ctx, id); err != nil {
		return err
	}
	if err := s.markers.Remove(id); err != nil {
		s.logger.Warn("removing exit marker", "terminal", id, "error", err)
	}
	s.mu.Lock()
	delete(s.view, id)
	delete(s.settling, id)
	s.mu.Unlock()
	s.hub.Publish(api.EventTerminalDeleted, resource, id)
	s.Reconcile(ctx)
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
