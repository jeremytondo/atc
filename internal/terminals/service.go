package terminals

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/store"
	"github.com/jeremytondo/atc/internal/terminals/exitmarker"
)

// ErrNotFound reports an id with no record; the API layer maps it to 404.
var ErrNotFound = errors.New("terminal not found")

// ErrDirectoryInvalid refuses a create whose working directory — the one
// supplied, or the space's — does not exist at that moment. The
// existence check is deliberate (ATC-256 reversed the earlier
// skip-it choice); the launch-failure path remains as the backstop for
// races.
var ErrDirectoryInvalid = errors.New("terminal directory does not exist")

// ErrInvalidUpdate refuses a PATCH that nulls a field that cannot be
// cleared.
var ErrInvalidUpdate = errors.New("invalid update")

// resource is the event-payload resource kind.
const resource = "terminal"

// Options wires a Service. Now defaults to time.Now; Logger to discard.
type Options struct {
	Repository *store.Terminals
	Driver     Driver
	// Spaces is the repository behind the space view (ATC-296).
	Spaces *store.Spaces
	// HomeDir is the server user's home directory: the Default space's
	// directory, and the default for a space created without one.
	HomeDir string
	// MarkerDir is where wrappers record exit evidence.
	MarkerDir string
	Hub       *events.Hub
	Logger    *slog.Logger
	Now       func() time.Time
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
//	mu          guards the view, settling set, and the space view only.
type Service struct {
	repository *store.Terminals
	driver     Driver
	spaces     *store.Spaces
	homeDir    string
	markerDir  string
	hub        *events.Hub
	logger     *slog.Logger
	now        func() time.Time
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
	// spaceView is the in-memory copy of the space records; defaultSpace
	// the Default space's id; deletingSpaces the spaces whose deletion is
	// in progress, which no create or move may target.
	spaceView      map[string]store.SpaceRecord
	defaultSpace   string
	deletingSpaces map[string]struct{}
}

type entry struct {
	record store.TerminalRecord
	status api.TerminalStatus
}

func NewService(opts Options) *Service {
	if opts.Spaces == nil || opts.HomeDir == "" {
		// A nil repository or an empty home would panic or mint a broken
		// Default space at the first request rather than at boot; fail at
		// construction instead (the server.NewHandler Verify precedent).
		panic("terminals.NewService: Spaces and HomeDir must be set")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		repository:     opts.Repository,
		driver:         opts.Driver,
		spaces:         opts.Spaces,
		homeDir:        opts.HomeDir,
		markerDir:      opts.MarkerDir,
		hub:            opts.Hub,
		logger:         opts.Logger,
		now:            opts.Now,
		verifyInterval: VerifyInterval,
		view:           make(map[string]*entry),
		settling:       make(map[string]struct{}),
		spaceView:      make(map[string]store.SpaceRecord),
		deletingSpaces: make(map[string]struct{}),
	}
}

// Load rebuilds the space and terminal views from the database at boot,
// minting the Default space when none exists. Statuses start from
// durable evidence alone (exited where recorded, unreachable otherwise)
// and settle in the startup Reconcile that must follow before reads are
// served.
func (s *Service) Load(ctx context.Context) error {
	if err := s.loadSpaces(ctx); err != nil {
		return err
	}
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

	inventory, inventoryErr := s.driver.Inventory(ctx)
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
		if err := s.driver.Kill(ctx, name); err != nil {
			s.logger.Warn("orphan session not cleaned", "session", name, "error", err)
		}
	}
}

// Create resolves the space (Default when unnamed) and the working
// directory (the space's when unnamed; it must exist), mints the ID,
// persists the record before starting the session (no orphan window),
// starts it, and waits a short verification window so the common case
// returns running and a fast-failing command returns exited with real
// evidence. Failures after the record exists surface through the normal
// status machinery — there is no separate launch-error path.
func (s *Service) Create(ctx context.Context, params api.TerminalCreateParams) (api.Terminal, error) {
	return s.create(ctx, params, AppLaunch{})
}

// AppLaunch is a create the Integration catalog resolved (ATC-294): the
// qualified App id recorded on the terminal, and the App's two hooks into
// the create — both opaque here, so the domain never learns App
// vocabulary.
type AppLaunch struct {
	// AppID is the qualified App id recorded on the terminal — this is the
	// only writer of the immutable appId field.
	AppID string
	// Prepare, when set, runs once the working directory is resolved and
	// before the commit lock: launch-time work that may block — waiting
	// on another launch in the same directory, starting a provider's
	// shared server — must not stall every terminal mutation server-wide.
	// abort is called if the create fails afterwards, so the preparation
	// can be undone; a successful create never calls it.
	Prepare func(ctx context.Context, directory string) (abort func(), err error)
	// Compose supplies the App-composed command once the terminal id is
	// minted, so per-launch context (ATC-255 hook settings, a pending
	// launch keyed by directory) can reference the identity before the
	// session starts. It runs under the commit lock and must be quick. The
	// composed command is Integration-private: it is stored and run but
	// never exposed on the wire.
	Compose func(terminalID, directory string) (string, error)
}

// CreateForApp is Create for a launch the API layer resolved through the
// Integration catalog: Prepare runs outside the commit lock, and
// everything past Compose is the normal create path.
func (s *Service) CreateForApp(ctx context.Context, params api.TerminalCreateParams, launch AppLaunch) (api.Terminal, error) {
	return s.create(ctx, params, launch)
}

func (s *Service) create(ctx context.Context, params api.TerminalCreateParams, launch AppLaunch) (api.Terminal, error) {
	// The space (Default when unnamed) is read here for its directory and
	// re-checked live under the commit lock; a space deleted in between
	// refuses the commit.
	spaceID := params.SpaceID
	if spaceID == "" {
		s.mu.Lock()
		spaceID = s.defaultSpace
		s.mu.Unlock()
	}
	s.ops.Lock()
	space, err := s.liveSpace(spaceID)
	s.ops.Unlock()
	if err != nil {
		return api.Terminal{}, err
	}
	directory := params.Directory
	if directory == "" {
		directory = space.Directory
	}
	// The directory must exist right now; a vanished folder refuses the
	// create before any record is written. Stored canonical.
	canonical, err := paths.CanonicalDir(directory)
	if err != nil {
		return api.Terminal{}, fmt.Errorf("%w: %s", ErrDirectoryInvalid, directory)
	}
	directory = canonical
	name := strings.TrimSpace(params.Name)
	if name == "" {
		name = filepath.Base(directory)
	}
	abort := func() {}
	if launch.Prepare != nil {
		prepared, err := launch.Prepare(ctx, directory)
		if err != nil {
			return api.Terminal{}, err
		}
		if prepared != nil {
			abort = prepared
		}
	}
	terminal, err := s.commitCreate(ctx, params, space.ID, name, directory, launch)
	if err != nil {
		abort()
	}
	return terminal, err
}

// commitCreate is the create from the record on: mint the id under the
// commit lock, compose, persist, start the session, and settle.
func (s *Service) commitCreate(ctx context.Context, params api.TerminalCreateParams, spaceID, name, directory string,
	launch AppLaunch) (api.Terminal, error) {
	now := s.now()
	record := store.TerminalRecord{
		SpaceID: spaceID, Name: name, Directory: directory, Command: params.Command,
		AppID:     launch.AppID,
		CreatedAt: now, UpdatedAt: now,
	}
	s.ops.Lock()
	// The space must still be live at commit: a deletion that began
	// while the preparation ran must not gain a terminal.
	if _, err := s.liveSpace(spaceID); err != nil {
		s.ops.Unlock()
		return api.Terminal{}, err
	}
	// Insertion is the collision check: a taken ID inserts nothing and
	// re-rolls, with no check-then-insert window. Compose runs inside the
	// loop so the composed command always references the id that actually
	// inserts — but only after the candidate clears the in-memory view
	// (authoritative under ops), so its side effects (hook files, secret
	// registrations) can never overwrite a live terminal's. Side effects
	// for a candidate that still fails to insert are keyed by an id no
	// session will ever use; boot-time cleanup reaps them.
	for {
		record.ID = randomID()
		s.mu.Lock()
		_, taken := s.view[record.ID]
		s.mu.Unlock()
		if taken {
			continue
		}
		if launch.Compose != nil {
			command, err := launch.Compose(record.ID, record.Directory)
			if err != nil {
				s.ops.Unlock()
				return api.Terminal{}, err
			}
			record.Command = command
		}
		inserted, err := s.repository.Insert(ctx, record)
		if errors.Is(err, store.ErrForeignKeyViolation) {
			// Impossible while the deleting mark holds creates off a space
			// being deleted; the backstop still answers honestly.
			s.ops.Unlock()
			return api.Terminal{}, fmt.Errorf("%w: %q", ErrSpaceNotFound, spaceID)
		}
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

	if err := s.driver.Create(ctx, record.ID, CreateSpec{Directory: record.Directory, Command: record.Command}); err != nil {
		// The record stays: the session may have been born after the
		// client gave up, and the status machinery reports the truth.
		s.logger.Warn("session create failed", "terminal", record.ID, "error", err)
	}
	s.awaitSettled(ctx, record.ID)

	s.mu.Lock()
	delete(s.settling, record.ID)
	s.mu.Unlock()
	// Detached: a client that disconnects here must not hand the reconcile
	// a dead context, whose failed inventory would flip every terminal to
	// unreachable until the next background pass.
	s.Reconcile(context.WithoutCancel(ctx))
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
		inventory, err := s.driver.Inventory(ctx)
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

// List serves terminals from the in-memory view in creation order. A
// non-empty spaceID filters to that space's terminals; empty returns
// everything (the API imposes no scoping — presentation belongs to UIs).
func (s *Service) List(spaceID string) []api.Terminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	terminals := make([]api.Terminal, 0, len(s.view))
	for _, e := range s.view {
		if spaceID != "" && e.record.SpaceID != spaceID {
			continue
		}
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

// Update applies a merge patch to the two mutable fields: the name, and
// the space (a move, which changes nothing else — not the session, the
// directory, the App, or any thread). Neither accepts null; a move into
// a space being deleted is refused. An empty patch returns the terminal
// unchanged.
func (s *Service) Update(ctx context.Context, id string, params api.TerminalUpdateParams) (api.Terminal, error) {
	if params.Name.Null() || params.SpaceID.Null() {
		return api.Terminal{}, fmt.Errorf("%w: name and spaceId cannot be null", ErrInvalidUpdate)
	}
	if !params.Name.Set && !params.SpaceID.Set {
		return s.Get(id)
	}
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	e, exists := s.view[id]
	var current store.TerminalRecord
	if exists {
		current = e.record
	}
	s.mu.Unlock()
	if !exists {
		return api.Terminal{}, ErrNotFound
	}
	name, spaceID := current.Name, current.SpaceID
	if params.Name.Set {
		if name = strings.TrimSpace(*params.Name.Value); name == "" {
			return api.Terminal{}, fmt.Errorf("%w: name cannot be empty", ErrInvalidUpdate)
		}
	}
	if params.SpaceID.Set && *params.SpaceID.Value != current.SpaceID {
		// Both ends must be live: a terminal must not leave a space whose
		// deletion has already counted it, nor join one being deleted.
		if _, err := s.liveSpace(current.SpaceID); err != nil {
			return api.Terminal{}, err
		}
		space, err := s.liveSpace(*params.SpaceID.Value)
		if err != nil {
			return api.Terminal{}, err
		}
		spaceID = space.ID
	}
	if name == current.Name && spaceID == current.SpaceID {
		return s.Get(id)
	}
	now := s.now()
	ok, err := s.repository.Update(ctx, id, name, spaceID, now)
	if errors.Is(err, store.ErrForeignKeyViolation) {
		return api.Terminal{}, fmt.Errorf("%w: %q", ErrSpaceNotFound, spaceID)
	}
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
		e.record.SpaceID = spaceID
		e.record.UpdatedAt = now
		terminal = e.terminal()
	}
	s.mu.Unlock()
	if terminal.ID == "" {
		// The row updated but the view has no entry — impossible while ops
		// serializes commits, and never worth answering with a zero value.
		return api.Terminal{}, ErrNotFound
	}
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

	if err := s.driver.Kill(detached, id); err != nil {
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

// terminal converts the entry to its wire shape. Callers hold s.mu. An
// App-launched terminal exposes its App, never the command the
// Integration composed for it.
func (e *entry) terminal() api.Terminal {
	terminal := api.Terminal{
		ID:        e.record.ID,
		Name:      e.record.Name,
		SpaceID:   e.record.SpaceID,
		Directory: e.record.Directory,
		AppID:     e.record.AppID,
		Status:    e.status,
		CreatedAt: e.record.CreatedAt,
		UpdatedAt: e.record.UpdatedAt,
	}
	if e.record.AppID == "" {
		terminal.Command = e.record.Command
	}
	if e.status == api.TerminalExited && e.record.ExitCode != nil {
		code := *e.record.ExitCode
		terminal.ExitCode = &code
	}
	return terminal
}
