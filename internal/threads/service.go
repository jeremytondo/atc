package threads

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/store"
)

// ErrNotFound reports an id with no record; the API layer maps it to 404.
var ErrNotFound = errors.New("thread not found")

// ErrActive refuses archiving or deleting a thread some terminal has open
// or some adapter's program still reports (deleting an active thread
// would silently re-mint a fresh record on the next evidence). The
// message names the holder; the API layer maps it to 409.
var ErrActive = errors.New("thread is active")

// ErrProjectRequired refuses an adapter observation of an unknown
// conversation that names no project to record it under.
var ErrProjectRequired = errors.New("a first observation needs a project")

// ErrResumeUnavailable refuses opening a dormant thread when the caller
// supplies no Resumer (a server without an agent catalog).
var ErrResumeUnavailable = errors.New("this server cannot resume conversations")

// Event-payload resource kinds. The service publishes terminal.updated
// when a terminal's activeThreadId projection changes — the terminals
// domain knows nothing about threads, so the change is announced here.
const (
	resource         = "thread"
	terminalResource = "terminal"
)

// TerminalReader is the read-only seam into the terminals domain the
// background sweep uses to notice terminals leaving (terminals.Service in
// production). The dependency is one-way: terminals never depends back.
type TerminalReader interface {
	Get(id string) (api.Terminal, error)
}

// Options wires a Service. Now defaults to time.Now; Logger to discard.
type Options struct {
	Repository *store.Threads
	Terminals  TerminalReader
	Hub        *events.Hub
	Logger     *slog.Logger
	Now        func() time.Time
}

// Service owns the in-memory view and the observation policy. Construct
// with NewService, Load once at boot before serving reads, then Run for
// the background sweep.
//
// Locks follow the terminals discipline, outermost first; no database IO
// happens under mu, and every mutation persists before it changes the
// view or publishes — reads and events never advertise state the database
// refused:
//
//	ops serializes each mutation's commit — database write, view change,
//	    and event publish move as one unit — and guards opening.
//	mu  guards the view, identity, hold, and active maps only.
type Service struct {
	repository *store.Threads
	terminals  TerminalReader
	hub        *events.Hub
	logger     *slog.Logger
	now        func() time.Time

	ops sync.Mutex
	// opening holds each thread with a resume launch in flight, closed
	// when it lands. Open releases ops for the launch's duration — a
	// multi-second external start must not stall evidence for every
	// other terminal — and a concurrent open of the same thread waits
	// here instead of racing it.
	opening map[string]chan struct{}

	// linkers derive deep links per adapter id at read time; set at
	// composition, before serving, and read without mu.
	linkers map[string]Linker

	mu   sync.Mutex
	view map[string]*store.ThreadRecord
	// identities is the in-memory copy of the private identity mapping;
	// keys is its inverse, so reads can find a thread's provider id
	// without scanning.
	identities map[identityKey]string
	keys       map[string]identityKey
	// active maps terminal id → the thread whose conversation that
	// terminal has open. Derived from evidence, never stored: it starts
	// empty at boot and is re-established by observation.
	active map[string]string
	// held is the set of threads whose producing adapter's connection
	// holds them: the external program still reports the conversation.
	// Like active, it is evidence, re-established by observation after a
	// boot.
	held map[string]struct{}
}

type identityKey struct {
	adapter    string
	providerID string
}

func NewService(opts Options) *Service {
	if opts.Terminals == nil {
		// A nil terminals reader would panic on the first sweep rather
		// than at boot; fail at construction instead (the server.NewHandler
		// Verify precedent).
		panic("threads.NewService: Terminals must not be nil")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Service{
		repository: opts.Repository,
		terminals:  opts.Terminals,
		hub:        opts.Hub,
		logger:     opts.Logger,
		now:        opts.Now,
		linkers:    make(map[string]Linker),
		opening:    make(map[string]chan struct{}),
		view:       make(map[string]*store.ThreadRecord),
		identities: make(map[identityKey]string),
		keys:       make(map[string]identityKey),
		active:     make(map[string]string),
		held:       make(map[string]struct{}),
	}
}

// SetLinker registers the read-time link derivation for one adapter's
// threads. Composition-time wiring: the adapter depends on this service
// for observation, so its linker is attached after both exist and before
// the server serves reads.
func (s *Service) SetLinker(adapter string, linker Linker) {
	s.linkers[adapter] = linker
}

// Load rebuilds the view and identity mapping from the database at boot.
// Live statuses (working, waiting_*) are unverifiable claims about an
// observation that no longer exists, so they coerce to unknown — persisted
// immediately, so the database never claims liveness it cannot back. Idle,
// error, and unknown persist as recorded.
func (s *Service) Load(ctx context.Context) error {
	records, err := s.repository.List(ctx)
	if err != nil {
		return err
	}
	identities, err := s.repository.ListIdentities(ctx)
	if err != nil {
		return err
	}
	now := s.now()
	for i := range records {
		if !isLive(api.ThreadStatus(records[i].Status)) {
			continue
		}
		records[i].Status = string(api.ThreadUnknown)
		records[i].UpdatedAt = now
		if _, err := s.repository.Update(ctx, records[i]); err != nil {
			return err
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, record := range records {
		entry := record
		s.view[entry.ID] = &entry
	}
	for _, identity := range identities {
		key := identityKey{identity.Adapter, identity.ProviderConversationID}
		s.identities[key] = identity.ThreadID
		s.keys[identity.ThreadID] = key
	}
	return nil
}

// Run is the background sweep loop.
func (s *Service) Run(ctx context.Context) {
	ticker := time.NewTicker(SweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			s.Sweep(ctx)
		}
	}
}

// Sweep deactivates threads whose terminal has definitively left: the TUI
// exited without provider evidence (kill -9), the terminal vanished, or
// its record was deleted. A merely unreachable terminal is left alone —
// unreachable means liveness cannot currently be verified, which is no
// evidence of leaving. A closed TUI never marks threads done — the
// conversations remain resumable.
func (s *Service) Sweep(ctx context.Context) {
	s.mu.Lock()
	terminals := make([]string, 0, len(s.active))
	for terminalID := range s.active {
		terminals = append(terminals, terminalID)
	}
	s.mu.Unlock()
	for _, terminalID := range terminals {
		terminal, err := s.terminals.Get(terminalID)
		switch {
		case err != nil:
			// No record at all: the terminal was deleted and the linkage
			// clears with it.
			s.TerminalRemoved(ctx, terminalID)
		case terminal.Status == api.TerminalExited || terminal.Status == api.TerminalMissing:
			s.Deactivate(ctx, terminalID)
		}
	}
}

// ObserveSession is the identity transition: the terminal has the given
// provider conversation open. A known identity reattaches to its thread
// (never a duplicate); an unknown one creates a record with the observing
// terminal's project. Only session observations move a terminal's active
// thread — delayed status evidence never selects a stale conversation. If
// two terminals observe the same conversation, the last observer wins.
// Returns the thread id.
func (s *Service) ObserveSession(ctx context.Context, o SessionObservation) (string, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	at := o.At
	if at.IsZero() {
		at = s.now()
	}
	key := identityKey{o.Adapter, o.ProviderID}

	s.mu.Lock()
	threadID, known := s.identities[key]
	var record store.ThreadRecord
	if known {
		entry, ok := s.view[threadID]
		if !ok {
			// Impossible while ops serializes commits; refuse to guess.
			s.mu.Unlock()
			return "", fmt.Errorf("identity for thread %s has no record", threadID)
		}
		record = *entry
	}
	s.mu.Unlock()

	if !known {
		return s.createObserved(ctx, o, at)
	}

	changed := false
	if record.TerminalID == nil || *record.TerminalID != o.TerminalID {
		terminalID := o.TerminalID
		record.TerminalID = &terminalID
		changed = true
	}
	if record.Archived {
		// Archiving an active thread is refused, so active ⇒ unarchived —
		// and a conversation resumed inside the TUI (which ATC cannot
		// refuse) is the user asking for the thread back. Observation
		// restores the invariant instead of leaving a terminal projecting
		// a thread that default lists hide.
		record.Archived = false
		record.ArchivedAt = nil
		changed = true
	}
	if o.Status != "" && record.Status != string(o.Status) {
		record.Status = string(o.Status)
		changed = true
	}
	changed = applyMetadata(&record, o.Metadata) || changed
	record.LastEvidenceAt = &at
	if changed {
		record.UpdatedAt = at
	}
	// Evidence always persists (a restart must not rewind lastEvidenceAt),
	// but only meaningful change publishes.
	updated, err := s.repository.Update(ctx, record)
	if err != nil {
		return "", err
	}
	if !updated {
		// The row was cascade-deleted since the view lookup; the pending
		// ProjectRemoved converges the view, and this observation has
		// nothing to record against.
		s.logger.Debug("observation for a deleted thread dropped", "thread", threadID)
		return "", nil
	}
	s.commit(ctx, record, threadID, o.TerminalID, changed)
	return threadID, nil
}

// createObserved mints and persists a new thread record with its identity
// mapping (one transaction — a thread must never exist unmapped). Caller
// holds ops.
func (s *Service) createObserved(ctx context.Context, o SessionObservation, at time.Time) (string, error) {
	status := o.Status
	if status == "" {
		status = api.ThreadUnknown
	}
	terminalID := o.TerminalID
	record := store.ThreadRecord{
		Adapter:        o.Adapter,
		Agent:          o.Agent,
		ProjectID:      o.ProjectID,
		TerminalID:     &terminalID,
		Status:         string(status),
		LastEvidenceAt: &at,
		CreatedAt:      at,
		UpdatedAt:      at,
	}
	applyMetadata(&record, o.Metadata)
	if err := s.insert(ctx, &record, identityKey{o.Adapter, o.ProviderID}); err != nil {
		return "", err
	}
	s.hub.Publish(api.EventThreadCreated, resource, record.ID)
	s.commit(ctx, record, record.ID, o.TerminalID, false)
	return record.ID, nil
}

// insert mints the id and persists a new record with its identity
// mapping (one transaction — a thread must never exist unmapped), then
// installs both in the view. Caller holds ops.
func (s *Service) insert(ctx context.Context, record *store.ThreadRecord, key identityKey) error {
	// Insertion is the collision check: a taken ID inserts nothing and
	// re-rolls. A vanished project or terminal surfaces as a foreign-key
	// error — the observation is refused rather than recorded against
	// nothing.
	for {
		record.ID = randomID()
		inserted, err := s.repository.InsertObserved(ctx, *record, store.ThreadIdentity{
			Adapter: key.adapter, ProviderConversationID: key.providerID, ThreadID: record.ID,
		})
		if err != nil {
			return err
		}
		if inserted {
			break
		}
	}
	s.mu.Lock()
	entry := *record
	s.view[record.ID] = &entry
	s.identities[key] = record.ID
	s.keys[record.ID] = key
	s.mu.Unlock()
	return nil
}

// commit applies a session observation's record to the view and moves the
// active projection, publishing what meaningfully changed: the previous
// occupant of the terminal coerces inactive, a previous holder of this
// thread releases it, and terminal.updated announces every projection
// move. Caller holds ops.
func (s *Service) commit(ctx context.Context, record store.ThreadRecord, threadID, terminalID string, changed bool) {
	var terminalsChanged []string
	var displaced string

	s.mu.Lock()
	if entry, ok := s.view[threadID]; ok {
		*entry = record
	}
	// Last observer wins: release the thread from any other terminal that
	// held it.
	for otherTerminal, otherThread := range s.active {
		if otherThread == threadID && otherTerminal != terminalID {
			delete(s.active, otherTerminal)
			terminalsChanged = append(terminalsChanged, otherTerminal)
		}
	}
	if previous, ok := s.active[terminalID]; ok && previous != threadID {
		displaced = previous
	}
	if s.active[terminalID] != threadID {
		s.active[terminalID] = threadID
		terminalsChanged = append(terminalsChanged, terminalID)
	}
	s.mu.Unlock()

	if displaced != "" && s.coerceInactive(ctx, displaced) {
		s.hub.Publish(api.EventThreadUpdated, resource, displaced)
	}
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	for _, id := range terminalsChanged {
		s.hub.Publish(api.EventTerminalUpdated, terminalResource, id)
	}
}

// ObserveStatus applies fresh status evidence to a known conversation.
// Evidence for an unmapped identity is dropped, and a live status for a
// thread no terminal holds is ignored — delayed evidence must not revive
// a conversation nothing displays (the provider layer re-establishes the
// session first). An evidence-only refresh persists lastEvidenceAt
// silently: no event, no updatedAt bump.
func (s *Service) ObserveStatus(ctx context.Context, o StatusObservation) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	at := o.At
	if at.IsZero() {
		at = s.now()
	}

	s.mu.Lock()
	threadID, known := s.identities[identityKey{o.Adapter, o.ProviderID}]
	var record store.ThreadRecord
	active := false
	if known {
		entry, ok := s.view[threadID]
		known = ok
		if ok {
			record = *entry
		}
		active = s.holder(threadID) != ""
	}
	s.mu.Unlock()
	if !known {
		s.logger.Debug("status evidence for unmapped conversation dropped", "adapter", o.Adapter)
		return nil
	}

	changed := false
	if o.Status != "" && record.Status != string(o.Status) {
		if isLive(o.Status) && !active {
			s.logger.Debug("live status for an inactive thread ignored", "thread", threadID, "status", o.Status)
		} else {
			record.Status = string(o.Status)
			changed = true
		}
	}
	if o.LastError != nil && record.LastError != *o.LastError {
		record.LastError = *o.LastError
		changed = true
	}
	changed = applyMetadata(&record, o.Metadata) || changed
	record.LastEvidenceAt = &at
	if changed {
		record.UpdatedAt = at
	}
	updated, err := s.repository.Update(ctx, record)
	if err != nil {
		return err
	}
	if !updated {
		s.logger.Debug("observation for a deleted thread dropped", "thread", threadID)
		return nil
	}
	s.mu.Lock()
	if entry, ok := s.view[threadID]; ok {
		*entry = record
	}
	s.mu.Unlock()
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return nil
}

// Deactivate clears a terminal's active thread: the TUI closed or the
// provider reported the conversation left without a successor. Idle
// persists; unverifiable live states coerce to unknown. On a failed
// persist the active entry stays, so the sweep retries.
func (s *Service) Deactivate(ctx context.Context, terminalID string) {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	threadID, ok := s.active[terminalID]
	var record store.ThreadRecord
	if ok {
		if entry, exists := s.view[threadID]; exists {
			record = *entry
		} else {
			ok = false
		}
	}
	s.mu.Unlock()
	if !ok {
		return
	}
	coerced := false
	if isLive(api.ThreadStatus(record.Status)) {
		record.Status = string(api.ThreadUnknown)
		record.UpdatedAt = s.now()
		updated, err := s.repository.Update(ctx, record)
		if err != nil || !updated {
			// The active entry stays, so the sweep retries the coercion.
			s.logger.Warn("coercing thread status", "thread", threadID, "error", err)
			return
		}
		s.mu.Lock()
		if entry, exists := s.view[threadID]; exists {
			*entry = record
		}
		s.mu.Unlock()
		coerced = true
	}
	s.mu.Lock()
	delete(s.active, terminalID)
	s.mu.Unlock()
	if coerced {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	s.hub.Publish(api.EventTerminalUpdated, terminalResource, terminalID)
}

// coerceInactive coerces a thread's live status to unknown —
// persist-first, then the view — reporting whether it changed. A failed
// persist changes nothing (boot-time coercion is the backstop). Caller
// holds ops.
func (s *Service) coerceInactive(ctx context.Context, threadID string) bool {
	s.mu.Lock()
	entry, ok := s.view[threadID]
	var record store.ThreadRecord
	if ok {
		record = *entry
	}
	s.mu.Unlock()
	if !ok || !isLive(api.ThreadStatus(record.Status)) {
		return false
	}
	record.Status = string(api.ThreadUnknown)
	record.UpdatedAt = s.now()
	updated, err := s.repository.Update(ctx, record)
	if err != nil || !updated {
		s.logger.Warn("coercing thread status", "thread", threadID, "error", err)
		return false
	}
	s.mu.Lock()
	if entry, ok := s.view[threadID]; ok {
		*entry = record
	}
	s.mu.Unlock()
	return true
}

// TerminalRemoved reacts to a terminal deletion: threads pointing at it
// lose their terminalId (the database foreign key already cleared the
// column) and become inactive with the usual coercion. Wired by the
// composition root after the terminals domain commits the delete — the
// domains stay decoupled. A thread whose persist fails keeps the active
// entry, so the sweep retries.
func (s *Service) TerminalRemoved(ctx context.Context, terminalID string) {
	s.ops.Lock()
	defer s.ops.Unlock()
	now := s.now()

	s.mu.Lock()
	var affected []store.ThreadRecord
	for _, entry := range s.view {
		if entry.TerminalID != nil && *entry.TerminalID == terminalID {
			record := *entry
			record.TerminalID = nil
			if isLive(api.ThreadStatus(record.Status)) {
				record.Status = string(api.ThreadUnknown)
			}
			record.UpdatedAt = now
			affected = append(affected, record)
		}
	}
	activeThread := s.active[terminalID]
	s.mu.Unlock()

	cleared := true
	for _, record := range affected {
		updated, err := s.repository.Update(ctx, record)
		if err != nil || !updated {
			s.logger.Warn("clearing thread linkage", "thread", record.ID, "error", err)
			if record.ID == activeThread {
				cleared = false
			}
			continue
		}
		s.mu.Lock()
		if entry, ok := s.view[record.ID]; ok {
			*entry = record
		}
		s.mu.Unlock()
		s.hub.Publish(api.EventThreadUpdated, resource, record.ID)
	}
	if cleared {
		s.mu.Lock()
		delete(s.active, terminalID)
		s.mu.Unlock()
	}
}

// ObserveAdapter applies an observing adapter's current view of one
// conversation its program owns (ATC-285): a known identity is brought
// into line with the program, an unknown one is minted under ProjectID.
// The adapter connection holds the thread from here — live statuses are
// accepted, and archive or delete are refused — until the adapter
// releases it. A conversation the program reports again after ATC
// archived it comes back unarchived. Returns the thread id.
func (s *Service) ObserveAdapter(ctx context.Context, o AdapterObservation) (string, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	at := o.At
	if at.IsZero() {
		at = s.now()
	}
	status := o.Status
	if status == "" {
		status = api.ThreadUnknown
	}
	key := identityKey{o.Adapter, o.ProviderID}

	s.mu.Lock()
	threadID, known := s.identities[key]
	var record store.ThreadRecord
	if known {
		entry, ok := s.view[threadID]
		if !ok {
			s.mu.Unlock()
			return "", fmt.Errorf("identity for thread %s has no record", threadID)
		}
		record = *entry
	}
	s.mu.Unlock()

	if !known {
		if o.ProjectID == "" {
			return "", ErrProjectRequired
		}
		record = store.ThreadRecord{
			Adapter:        o.Adapter,
			Agent:          o.Agent,
			ProjectID:      o.ProjectID,
			Title:          o.Title,
			Status:         string(status),
			LastError:      o.LastError,
			LastEvidenceAt: &at,
			CreatedAt:      at,
			UpdatedAt:      at,
		}
		applyMetadata(&record, o.Metadata)
		if err := s.insert(ctx, &record, key); err != nil {
			return "", err
		}
		s.mu.Lock()
		s.held[record.ID] = struct{}{}
		s.mu.Unlock()
		s.hub.Publish(api.EventThreadCreated, resource, record.ID)
		return record.ID, nil
	}

	// The program is the source of truth: every reported field applies
	// as-is, except a title the user set through ATC.
	changed := false
	set := func(field *string, value string) {
		if *field != value {
			*field = value
			changed = true
		}
	}
	set(&record.Agent, o.Agent)
	set(&record.Status, string(status))
	set(&record.LastError, o.LastError)
	if !record.TitleUserSet && o.Title != "" {
		set(&record.Title, o.Title)
	}
	changed = applyMetadata(&record, o.Metadata) || changed
	if record.Archived {
		record.Archived = false
		record.ArchivedAt = nil
		changed = true
	}
	record.LastEvidenceAt = &at
	if changed {
		record.UpdatedAt = at
	}
	updated, err := s.repository.Update(ctx, record)
	if err != nil {
		return "", err
	}
	if !updated {
		s.logger.Debug("observation for a deleted thread dropped", "thread", threadID)
		return "", nil
	}
	s.mu.Lock()
	if entry, ok := s.view[threadID]; ok {
		*entry = record
	}
	s.held[threadID] = struct{}{}
	s.mu.Unlock()
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return threadID, nil
}

// UnarchivedProviderIDs lists the provider conversation ids of an
// adapter's unarchived threads — what ATC currently believes the program
// reports. An adapter diffs a fresh snapshot against it, so a
// conversation the program dropped while ATC was not listening still
// archives.
func (s *Service) UnarchivedProviderIDs(adapter string) []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, entry := range s.view {
		if entry.Adapter != adapter || entry.Archived {
			continue
		}
		if key, ok := s.keys[id]; ok {
			ids = append(ids, key.providerID)
		}
	}
	sort.Strings(ids)
	return ids
}

// ReleaseAdapter drops every hold the adapter has — its connection went
// away — coercing the live statuses it vouched for to unknown, exactly
// as a terminal leaving does; idle, error, and unknown persist. A failed
// persist still releases: the hold is about the program reporting the
// thread, and the boot-time coercion is the backstop.
func (s *Service) ReleaseAdapter(ctx context.Context, adapter string) {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	var ids []string
	for id := range s.held {
		if entry, ok := s.view[id]; ok && entry.Adapter == adapter {
			ids = append(ids, id)
		}
	}
	s.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		coerced := s.coerceInactive(ctx, id)
		s.mu.Lock()
		delete(s.held, id)
		s.mu.Unlock()
		if coerced {
			s.hub.Publish(api.EventThreadUpdated, resource, id)
		}
	}
}

// ArchiveAdapterThread reacts to the program dropping a conversation
// from what it reports: the hold releases and the thread archives, with
// a live status coerced to unknown. Archiving is the lossless mirror —
// the program hides archived and deleted conversations alike, and a
// later report of the same identity unarchives the same record. An
// unknown identity is ignored.
func (s *Service) ArchiveAdapterThread(ctx context.Context, adapter, providerID string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	threadID, known := s.identities[identityKey{adapter, providerID}]
	var record store.ThreadRecord
	if known {
		entry, ok := s.view[threadID]
		known = ok
		if ok {
			record = *entry
		}
	}
	s.mu.Unlock()
	if !known {
		return nil
	}
	changed := false
	if isLive(api.ThreadStatus(record.Status)) {
		record.Status = string(api.ThreadUnknown)
		changed = true
	}
	if !record.Archived {
		at := s.now()
		record.Archived = true
		record.ArchivedAt = &at
		changed = true
	}
	if changed {
		record.UpdatedAt = s.now()
		updated, err := s.repository.Update(ctx, record)
		if err != nil {
			return err
		}
		if !updated {
			changed = false
		}
	}
	s.mu.Lock()
	if entry, ok := s.view[threadID]; ok && changed {
		*entry = record
	}
	delete(s.held, threadID)
	s.mu.Unlock()
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, threadID)
	}
	return nil
}

// ProjectRemoved reacts to a project deletion: the database cascade has
// already removed the project's thread rows and identity mappings, so
// only the in-memory state and the deletion events remain. Wired by the
// composition root after the projects domain commits the delete.
func (s *Service) ProjectRemoved(projectID string) {
	s.ops.Lock()
	defer s.ops.Unlock()
	removed := make(map[string]bool)
	s.mu.Lock()
	for id, entry := range s.view {
		if entry.ProjectID == projectID {
			removed[id] = true
			delete(s.view, id)
			s.forgetIdentity(id)
		}
	}
	for terminalID, threadID := range s.active {
		if removed[threadID] {
			delete(s.active, terminalID)
		}
	}
	s.mu.Unlock()
	for id := range removed {
		s.hub.Publish(api.EventThreadDeleted, resource, id)
	}
}

// Get serves one thread from the in-memory view.
func (s *Service) Get(id string) (api.Thread, error) {
	s.mu.Lock()
	entry, ok := s.view[id]
	var record store.ThreadRecord
	if ok {
		record = *entry
	}
	s.mu.Unlock()
	if !ok {
		return api.Thread{}, ErrNotFound
	}
	return s.thread(record), nil
}

// List serves threads from the in-memory view in creation order.
// Non-empty projectID/terminalID filter; archived threads are hidden
// unless includeArchived (the opt-in the spec requires).
func (s *Service) List(projectID, terminalID string, includeArchived bool) []api.Thread {
	s.mu.Lock()
	records := make([]store.ThreadRecord, 0, len(s.view))
	for _, entry := range s.view {
		if entry.Archived && !includeArchived {
			continue
		}
		if projectID != "" && entry.ProjectID != projectID {
			continue
		}
		if terminalID != "" && (entry.TerminalID == nil || *entry.TerminalID != terminalID) {
			continue
		}
		records = append(records, *entry)
	}
	s.mu.Unlock()
	threads := make([]api.Thread, 0, len(records))
	for _, record := range records {
		threads = append(threads, s.thread(record))
	}
	sort.Slice(threads, func(i, j int) bool {
		if !threads[i].CreatedAt.Equal(threads[j].CreatedAt) {
			return threads[i].CreatedAt.Before(threads[j].CreatedAt)
		}
		return threads[i].ID < threads[j].ID
	})
	return threads
}

// LookupIdentity resolves a private identity to its thread and that
// thread's last observed terminal. Provider observers use it as the seed
// check after a server restart: evidence for an already-mapped
// conversation still held by the same terminal may be accepted before a
// fresh identity transition arrives.
func (s *Service) LookupIdentity(adapter, providerID string) (threadID, terminalID string, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, known := s.identities[identityKey{adapter, providerID}]
	if !known {
		return "", "", false
	}
	entry, exists := s.view[id]
	if !exists {
		return "", "", false
	}
	if entry.TerminalID != nil {
		terminalID = *entry.TerminalID
	}
	return id, terminalID, true
}

// ActiveThreadID is the projection terminals expose: the thread whose
// conversation the terminal has open, or empty. The server layer decorates
// terminal responses with it.
func (s *Service) ActiveThreadID(terminalID string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active[terminalID]
}

// Update mutates the two writable fields. A user-set title is protected
// from observation from then on. Archiving an active thread is refused
// naming the holder; unarchiving clears the timestamp.
func (s *Service) Update(ctx context.Context, id string, params api.ThreadUpdateParams) (api.Thread, error) {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	entry, ok := s.view[id]
	var record store.ThreadRecord
	if ok {
		record = *entry
	}
	holder := s.holder(id)
	s.mu.Unlock()
	if !ok {
		return api.Thread{}, ErrNotFound
	}
	if _, opening := s.opening[id]; opening && params.Archived != nil && *params.Archived {
		// A resume in flight is about to hold the thread; archiving it
		// now would be undone the moment the launch lands.
		return api.Thread{}, fmt.Errorf("%w: open in progress", ErrActive)
	}

	changed, persist := false, false
	if params.Title != nil {
		if record.Title != *params.Title {
			record.Title = *params.Title
			changed = true
		}
		if !record.TitleUserSet {
			record.TitleUserSet = true
			persist = true
		}
	}
	if params.Archived != nil && *params.Archived != record.Archived {
		if *params.Archived {
			if holder != "" {
				return api.Thread{}, fmt.Errorf("%w: %s", ErrActive, holder)
			}
			at := s.now()
			record.Archived = true
			record.ArchivedAt = &at
		} else {
			record.Archived = false
			record.ArchivedAt = nil
		}
		changed = true
	}
	if changed || persist {
		record.UpdatedAt = s.now()
		updated, err := s.repository.Update(ctx, record)
		if err != nil {
			return api.Thread{}, err
		}
		if !updated {
			return api.Thread{}, ErrNotFound
		}
		s.mu.Lock()
		if entry, ok := s.view[id]; ok {
			*entry = record
		}
		s.mu.Unlock()
	}
	if changed {
		s.hub.Publish(api.EventThreadUpdated, resource, id)
	}
	return s.thread(record), nil
}

// Delete removes the record and its identity mapping only — the
// provider-side conversation is never touched. An active thread is
// refused: deleting it would silently re-mint a fresh record on the next
// evidence.
func (s *Service) Delete(ctx context.Context, id string) error {
	s.ops.Lock()
	defer s.ops.Unlock()
	s.mu.Lock()
	_, ok := s.view[id]
	holder := s.holder(id)
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	if holder != "" {
		return fmt.Errorf("%w: %s", ErrActive, holder)
	}
	if _, opening := s.opening[id]; opening {
		// A resume in flight would land against a deleted record and
		// re-mint it on the first evidence.
		return fmt.Errorf("%w: open in progress", ErrActive)
	}
	deleted, err := s.repository.Delete(ctx, id)
	if err != nil {
		return err
	}
	if !deleted {
		return ErrNotFound
	}
	s.mu.Lock()
	delete(s.view, id)
	s.forgetIdentity(id)
	s.mu.Unlock()
	s.hub.Publish(api.EventThreadDeleted, resource, id)
	return nil
}

// forgetIdentity drops a thread's identity mapping and any adapter hold.
// Caller holds mu.
func (s *Service) forgetIdentity(id string) {
	if key, ok := s.keys[id]; ok {
		delete(s.identities, key)
		delete(s.keys, id)
	}
	delete(s.held, id)
}

// Open resolves the thread to exactly one terminal (ATC-282), reporting
// whether it was created. resume launches the terminal for a dormant
// thread; nil refuses such opens. Concurrent opens of the same thread
// converge — one decides, the rest wait for its outcome and re-decide —
// so at most one creates:
//
//  1. a running terminal actively shows the thread   → reuse it
//  2. the thread's last terminal is still up and no
//     other conversation is known to hold it          → reuse it
//  3. otherwise the thread is dormant                → resume it in a new
//     terminal, linked here
//
// Rule 2 is deliberate conservatism: after a restart the active
// projection is empty while TUIs keep running, and a freshly resumed
// terminal has no evidence yet. Landing the user in a terminal whose
// contents are unknown is recoverable in the TUI; a second writer on a
// live provider session is not, so anything not definitively gone
// (exited, missing, deleted) counts as still up. The linkage a resume
// records is intent, not evidence — it is what makes the next open fall
// into rule 2 before any hook fires — and it persists on a detached
// context: a client that gives up mid-launch must not leave a live
// resume unlinked for the next open to duplicate. Opening unarchives.
func (s *Service) Open(ctx context.Context, id string, resume Resumer) (api.Terminal, bool, error) {
	for {
		s.ops.Lock()
		terminal, inflight, err := s.decideOpen(ctx, id)
		if err != nil || terminal.ID != "" {
			s.ops.Unlock()
			return terminal, false, err
		}
		if inflight == nil {
			break
		}
		// Another open is resuming this thread: wait for its outcome,
		// then decide afresh — its linkage lands the reuse rule.
		s.ops.Unlock()
		select {
		case <-inflight:
		case <-ctx.Done():
			return api.Terminal{}, false, ctx.Err()
		}
	}
	// Dormant, and this open owns the resume. ops is released for the
	// launch and retaken to link.
	if resume == nil {
		s.ops.Unlock()
		return api.Terminal{}, false, ErrResumeUnavailable
	}
	s.mu.Lock()
	record := *s.view[id]
	key := s.keys[id]
	s.mu.Unlock()
	done := make(chan struct{})
	s.opening[id] = done
	s.ops.Unlock()

	detached := context.WithoutCancel(ctx)
	terminal, err := resume(detached, ResumeRequest{
		Adapter: record.Adapter, Agent: record.Agent, ProviderID: key.providerID,
		ProjectID: record.ProjectID, Directory: record.Cwd,
	})

	s.ops.Lock()
	defer s.ops.Unlock()
	delete(s.opening, id)
	close(done)
	if err != nil {
		return api.Terminal{}, false, err
	}
	if err := s.link(detached, id, terminal.ID); err != nil {
		s.logger.Error("linking resumed terminal", "thread", id, "terminal", terminal.ID, "error", err)
		return api.Terminal{}, false, err
	}
	return terminal, true, nil
}

// decideOpen applies the reuse rules under ops. It returns the reused
// terminal (linked and unarchived), or the in-flight resume to wait on,
// or neither when the thread is dormant and free to resume. Caller holds
// ops.
func (s *Service) decideOpen(ctx context.Context, id string) (api.Terminal, chan struct{}, error) {
	s.mu.Lock()
	entry, ok := s.view[id]
	var record store.ThreadRecord
	if ok {
		record = *entry
	}
	holder := s.activeHolder(id)
	var lastHolds string
	if record.TerminalID != nil {
		lastHolds = s.active[*record.TerminalID]
	}
	s.mu.Unlock()
	if !ok {
		return api.Terminal{}, nil, ErrNotFound
	}
	var terminal api.Terminal
	switch {
	case holder != "" && s.terminalUp(holder, &terminal):
	case record.TerminalID != nil && (lastHolds == "" || lastHolds == id) && s.terminalUp(*record.TerminalID, &terminal):
	default:
		return api.Terminal{}, s.opening[id], nil
	}
	if err := s.link(ctx, id, terminal.ID); err != nil {
		return api.Terminal{}, nil, err
	}
	return terminal, nil, nil
}

// link records the terminal as the thread's and unarchives it, persisting
// and publishing only when something changed. Caller holds ops.
func (s *Service) link(ctx context.Context, id, terminalID string) error {
	s.mu.Lock()
	entry, ok := s.view[id]
	var record store.ThreadRecord
	if ok {
		record = *entry
	}
	s.mu.Unlock()
	if !ok {
		return ErrNotFound
	}
	changed := false
	if record.TerminalID == nil || *record.TerminalID != terminalID {
		record.TerminalID = &terminalID
		changed = true
	}
	if record.Archived {
		record.Archived = false
		record.ArchivedAt = nil
		changed = true
	}
	if !changed {
		return nil
	}
	record.UpdatedAt = s.now()
	updated, err := s.repository.Update(ctx, record)
	if err != nil {
		return err
	}
	if !updated {
		return ErrNotFound
	}
	s.mu.Lock()
	if entry, ok := s.view[id]; ok {
		*entry = record
	}
	s.mu.Unlock()
	s.hub.Publish(api.EventThreadUpdated, resource, id)
	return nil
}

// terminalUp reports whether the terminal may still be running — anything
// but definitive absence (exited, missing, or no record) — filling in its
// record when so. Unreachable counts as up: liveness merely unverifiable
// is no evidence the conversation left.
func (s *Service) terminalUp(id string, terminal *api.Terminal) bool {
	got, err := s.terminals.Get(id)
	if err != nil || got.Status == api.TerminalExited || got.Status == api.TerminalMissing {
		return false
	}
	*terminal = got
	return true
}

// activeHolder returns the terminal currently holding the thread, or
// empty. Callers hold mu.
func (s *Service) activeHolder(threadID string) string {
	for terminalID, id := range s.active {
		if id == threadID {
			return terminalID
		}
	}
	return ""
}

// holder describes whatever holds the thread — the terminal with its
// conversation open, else the adapter whose program still reports it —
// for refusal messages; empty when nothing does. Callers hold mu.
func (s *Service) holder(threadID string) string {
	if terminalID := s.activeHolder(threadID); terminalID != "" {
		return "open in terminal " + terminalID
	}
	if _, ok := s.held[threadID]; ok {
		if entry, exists := s.view[threadID]; exists {
			return "still reported by adapter " + entry.Adapter
		}
	}
	return ""
}

// applyMetadata folds non-empty observed metadata into the record,
// reporting whether anything changed. An observed title is a default: it
// fills an untitled thread once and never replaces an existing title —
// user-set or previously observed.
func applyMetadata(record *store.ThreadRecord, metadata Metadata) bool {
	changed := false
	if metadata.Title != "" && !record.TitleUserSet && record.Title == "" {
		record.Title = metadata.Title
		changed = true
	}
	set := func(field *string, value string) {
		if value != "" && *field != value {
			*field = value
			changed = true
		}
	}
	set(&record.Model, metadata.Model)
	set(&record.Effort, metadata.Effort)
	set(&record.Cwd, metadata.Cwd)
	set(&record.PermissionMode, metadata.PermissionMode)
	return changed
}

func isLive(status api.ThreadStatus) bool {
	switch status {
	case api.ThreadWorking, api.ThreadWaitingForInput, api.ThreadWaitingForPermission:
		return true
	}
	return false
}

// thread converts a record to its wire shape, deriving links from the
// producing adapter's live state. Callers must not hold mu: the linker is
// the adapter's, and it takes locks of its own.
func (s *Service) thread(record store.ThreadRecord) api.Thread {
	thread := threadFrom(record)
	if linker, ok := s.linkers[record.Adapter]; ok {
		s.mu.Lock()
		key := s.keys[record.ID]
		s.mu.Unlock()
		thread.Links = linker(key.providerID)
	}
	return thread
}

func threadFrom(record store.ThreadRecord) api.Thread {
	thread := api.Thread{
		ID:             record.ID,
		Adapter:        record.Adapter,
		Agent:          record.Agent,
		ProjectID:      record.ProjectID,
		Title:          record.Title,
		Model:          record.Model,
		Effort:         record.Effort,
		Cwd:            record.Cwd,
		PermissionMode: record.PermissionMode,
		Status:         api.ThreadStatus(record.Status),
		LastError:      record.LastError,
		Archived:       record.Archived,
		CreatedAt:      record.CreatedAt,
		UpdatedAt:      record.UpdatedAt,
	}
	if record.TerminalID != nil {
		thread.TerminalID = *record.TerminalID
	}
	if record.LastEvidenceAt != nil {
		at := *record.LastEvidenceAt
		thread.LastEvidenceAt = &at
	}
	if record.ArchivedAt != nil {
		at := *record.ArchivedAt
		thread.ArchivedAt = &at
	}
	return thread
}
