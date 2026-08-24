package core

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/child"
	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
	"github.com/elevenideas/atc/experiments/unified-core/internal/status"
	"github.com/elevenideas/atc/experiments/unified-core/internal/store"
)

type fakeChat struct {
	mu         sync.Mutex
	opens      []ports.ChatOpen
	sessions   []*fakeChatSession
	failResume bool
}

type promptResult struct {
	outcome domain.TurnOutcome
	err     error
}

type fakeChatSession struct {
	open       ports.ChatOpen
	started    chan string
	result     chan promptResult
	interrupts []string
	answers    map[string]string
	closed     bool
	mu         sync.Mutex
}

func (f *fakeChat) Open(_ context.Context, open ports.ChatOpen) (ports.ChatSession, string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opens = append(f.opens, open)
	if f.failResume && open.ProviderSession != "" {
		return nil, "", errors.New("exact provider session unavailable")
	}
	identity := open.ProviderSession
	if identity == "" {
		identity = "private-provider-session"
	}
	session := &fakeChatSession{open: open, started: make(chan string, 8), result: make(chan promptResult, 8), answers: make(map[string]string)}
	f.sessions = append(f.sessions, session)
	return session, identity, nil
}

func (s *fakeChatSession) Prompt(_ context.Context, turnID, _ string) (domain.TurnOutcome, error) {
	s.started <- turnID
	result := <-s.result
	return result.outcome, result.err
}

func (s *fakeChatSession) Interrupt(_ context.Context, turnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interrupts = append(s.interrupts, turnID)
	return nil
}

func (s *fakeChatSession) Answer(_ context.Context, request, answer string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[request] = answer
	return nil
}

func (s *fakeChatSession) Close(context.Context) error {
	s.mu.Lock()
	s.closed = true
	s.mu.Unlock()
	return nil
}

type fakeTerminal struct {
	mu      sync.Mutex
	entries map[string]ports.TerminalEntry
	listErr error
	opened  []ports.TerminalOpen
	killed  []string
}

func newFakeTerminal() *fakeTerminal {
	return &fakeTerminal{entries: make(map[string]ports.TerminalEntry)}
}

func (f *fakeTerminal) Open(_ context.Context, open ports.TerminalOpen) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.opened = append(f.opened, open)
	f.entries[open.TerminalID] = ports.TerminalEntry{Name: open.TerminalID, Reachable: true, DaemonPID: 100 + len(f.opened)}
	return nil
}

func (f *fakeTerminal) Inventory(context.Context) ([]ports.TerminalEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.listErr != nil {
		return nil, f.listErr
	}
	result := make([]ports.TerminalEntry, 0, len(f.entries))
	for _, entry := range f.entries {
		result = append(result, entry)
	}
	return result, nil
}

func (f *fakeTerminal) Terminate(_ context.Context, name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.entries, name)
	f.killed = append(f.killed, name)
	return nil
}

func TestThreadKindsCorrelationAndLateBackgroundEvidence(t *testing.T) {
	ctx := context.Background()
	chat := &fakeChat{}
	terminal := newFakeTerminal()
	service := newTestService(t, store.NewMemory(), chat, terminal, nil)
	chatThread := createThread(t, service, domain.ThreadChat, domain.AgentClaude)
	tuiThread := createThread(t, service, domain.ThreadTUI, domain.AgentCodex)

	if _, err := service.Prompt(ctx, tuiThread.ID, "wrong surface"); errorCode(err) != "wrong_thread_kind" {
		t.Fatalf("TUI prompt error = %v", err)
	}
	if _, err := service.OpenTerminal(ctx, chatThread.ID, OpenTerminal{}); errorCode(err) != "wrong_thread_kind" {
		t.Fatalf("chat terminal error = %v", err)
	}

	turn, err := service.Prompt(ctx, chatThread.ID, "hello")
	if err != nil {
		t.Fatal(err)
	}
	userMessageFound := false
	for _, event := range service.EventsAfter(0, chatThread.ID) {
		if event.Type == "user.message" && event.TurnID == turn.ID && event.Text == "hello" {
			userMessageFound = true
		}
	}
	if !userMessageFound {
		t.Fatal("prompt was not projected into the canonical conversation")
	}
	session := chat.sessions[0]
	if started := <-session.started; started != turn.ID {
		t.Fatalf("started turn = %s", started)
	}
	working, _ := service.Thread(chatThread.ID)
	if working.Activity != domain.ActivityWorking || working.ActiveTurn.ID != turn.ID {
		t.Fatalf("working thread = %#v", working)
	}
	if err := service.Interrupt(ctx, chatThread.ID, "different-turn"); errorCode(err) != "turn_mismatch" {
		t.Fatalf("mismatched interrupt = %v", err)
	}
	if err := service.Interrupt(ctx, chatThread.ID, turn.ID); err != nil {
		t.Fatal(err)
	}
	session.result <- promptResult{outcome: domain.TurnInterrupted}
	waitForEvent(t, service, chatThread.ID, "turn.ended", 0)

	// ACP v1 may deliver agent-owned tool evidence after the foreground prompt
	// has returned. It must keep the durable Thread working.
	session.open.Events.BackgroundActivity(chatThread.ID, domain.ActivityWorking)
	afterLateTool, _ := service.Thread(chatThread.ID)
	if afterLateTool.Activity != domain.ActivityWorking || afterLateTool.ActiveTurn != nil || afterLateTool.LastTurn.Outcome != domain.TurnInterrupted {
		t.Fatalf("late-tool thread = %#v", afterLateTool)
	}
	session.open.Events.Request(chatThread.ID, "", "provider-request-1", domain.RequestApproval, "Allow edit?", []domain.RequestOption{{ID: "option_1", Label: "Allow"}})
	needsInput, _ := service.Thread(chatThread.ID)
	if needsInput.Activity != domain.ActivityNeedsInput || needsInput.PendingCount != 1 {
		t.Fatalf("pending background request = %#v", needsInput)
	}
	requests, err := service.PendingRequests(chatThread.ID)
	if err != nil || len(requests) != 1 || requests[0].TurnID != "" {
		t.Fatalf("requests = %#v, %v", requests, err)
	}
	if err := service.AnswerRequest(ctx, chatThread.ID, "not-the-request", Answer{OptionID: "option_1"}); errorCode(err) != "request_not_found" {
		t.Fatalf("wrong request answer = %v", err)
	}
	if err := service.AnswerRequest(ctx, chatThread.ID, requests[0].ID, Answer{OptionID: "option_1"}); err != nil {
		t.Fatal(err)
	}
	if session.answers["provider-request-1"] != "option_1" {
		t.Fatalf("provider answers = %#v", session.answers)
	}

	// Multi-turn uses the one materialized writer.
	session.open.Events.BackgroundActivity(chatThread.ID, domain.ActivityIdle)
	second, err := service.Prompt(ctx, chatThread.ID, "again")
	if err != nil {
		t.Fatal(err)
	}
	if <-session.started != second.ID {
		t.Fatal("second prompt used a different turn")
	}
	session.result <- promptResult{outcome: domain.TurnCompleted}
	waitForEvent(t, service, chatThread.ID, "turn.ended", turnEventSequence(service, chatThread.ID, turn.ID))
	if len(chat.opens) != 1 {
		t.Fatalf("chat writers opened = %d", len(chat.opens))
	}
}

func TestRestartUsesExactProviderIdentityWithoutCreateFallback(t *testing.T) {
	ctx := context.Background()
	repository := store.NewMemory()
	firstChat := &fakeChat{}
	first := newTestService(t, repository, firstChat, newFakeTerminal(), nil)
	thread := createThread(t, first, domain.ThreadChat, domain.AgentCodex)
	turn, err := first.Prompt(ctx, thread.ID, "materialize")
	if err != nil {
		t.Fatal(err)
	}
	session := firstChat.sessions[0]
	<-session.started
	session.result <- promptResult{outcome: domain.TurnCompleted}
	waitForEvent(t, first, thread.ID, "turn.ended", turnEventSequence(first, thread.ID, turn.ID)-1)
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}

	secondChat := &fakeChat{failResume: true}
	second := newTestService(t, repository, secondChat, newFakeTerminal(), nil)
	if _, err := second.Prompt(ctx, thread.ID, "must fail closed"); err == nil {
		t.Fatal("exact resume unexpectedly succeeded")
	}
	if len(secondChat.opens) != 1 || secondChat.opens[0].ProviderSession != "private-provider-session" {
		t.Fatalf("resume opens = %#v", secondChat.opens)
	}
	failed, _ := second.Thread(thread.ID)
	if failed.LastTurn == nil || failed.LastTurn.Outcome != domain.TurnFailed {
		t.Fatalf("failed resume thread = %#v", failed)
	}
}

func TestRestartReconnectsWriterBeforeAnsweringBackgroundRequest(t *testing.T) {
	ctx := context.Background()
	repository := store.NewMemory()
	firstChat := &fakeChat{}
	first := newTestService(t, repository, firstChat, newFakeTerminal(), nil)
	thread := createThread(t, first, domain.ThreadChat, domain.AgentClaude)
	turn, err := first.Prompt(ctx, thread.ID, "materialize")
	if err != nil {
		t.Fatal(err)
	}
	firstSession := firstChat.sessions[0]
	<-firstSession.started
	firstSession.result <- promptResult{outcome: domain.TurnCompleted}
	waitForEvent(t, first, thread.ID, "turn.ended", turnEventSequence(first, thread.ID, turn.ID)-1)
	firstSession.open.Events.Request(thread.ID, "", "late-provider-request", domain.RequestQuestion, "Continue?", nil)
	requests, _ := first.PendingRequests(thread.ID)
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}

	secondChat := &fakeChat{}
	second := newTestService(t, repository, secondChat, newFakeTerminal(), nil)
	if err := second.RecoverChatSessions(ctx); err != nil {
		t.Fatal(err)
	}
	if len(secondChat.opens) != 1 || secondChat.opens[0].ProviderSession != "private-provider-session" {
		t.Fatalf("recovery opens = %#v", secondChat.opens)
	}
	if err := second.AnswerRequest(ctx, thread.ID, requests[0].ID, Answer{Text: "yes"}); err != nil {
		t.Fatal(err)
	}
	if secondChat.sessions[0].answers["late-provider-request"] != "yes" {
		t.Fatalf("recovered answers = %#v", secondChat.sessions[0].answers)
	}
}

func TestPendingRequestIsIsolatedFromAnotherThread(t *testing.T) {
	ctx := context.Background()
	chat := &fakeChat{}
	service := newTestService(t, store.NewMemory(), chat, newFakeTerminal(), nil)
	blocked := createThread(t, service, domain.ThreadChat, domain.AgentCodex)
	independent := createThread(t, service, domain.ThreadChat, domain.AgentClaude)

	blockedTurn, err := service.Prompt(ctx, blocked.ID, "materialize blocked thread")
	if err != nil {
		t.Fatal(err)
	}
	blockedSession := chat.sessions[0]
	<-blockedSession.started
	blockedSession.result <- promptResult{outcome: domain.TurnCompleted}
	waitForEvent(t, service, blocked.ID, "turn.ended", turnEventSequence(service, blocked.ID, blockedTurn.ID)-1)
	blockedSession.open.Events.Request(blocked.ID, "", "provider-request", domain.RequestApproval, "Allow?", []domain.RequestOption{{ID: "option_1", Label: "Allow"}})

	independentTurn, err := service.Prompt(ctx, independent.ID, "continue independently")
	if err != nil {
		t.Fatal(err)
	}
	independentSession := chat.sessions[1]
	<-independentSession.started
	blockedState, _ := service.Thread(blocked.ID)
	independentState, _ := service.Thread(independent.ID)
	if blockedState.Activity != domain.ActivityNeedsInput || blockedState.PendingCount != 1 {
		t.Fatalf("blocked thread = %#v", blockedState)
	}
	if independentState.Activity != domain.ActivityWorking || independentState.PendingCount != 0 {
		t.Fatalf("independent thread = %#v", independentState)
	}
	independentSession.result <- promptResult{outcome: domain.TurnCompleted}
	waitForEvent(t, service, independent.ID, "turn.ended", turnEventSequence(service, independent.ID, independentTurn.ID)-1)
	blockedState, _ = service.Thread(blocked.ID)
	independentState, _ = service.Thread(independent.ID)
	if blockedState.Activity != domain.ActivityNeedsInput || blockedState.PendingCount != 1 {
		t.Fatalf("blocked thread after independent completion = %#v", blockedState)
	}
	if independentState.Activity != domain.ActivityIdle || independentState.PendingCount != 0 {
		t.Fatalf("completed independent thread = %#v", independentState)
	}
}

func TestTerminalEvidenceMatrixAndScopedCleanup(t *testing.T) {
	ctx := context.Background()
	clock := time.Date(2026, 8, 22, 12, 0, 0, 0, time.UTC)
	terminal := newFakeTerminal()
	stateDir := t.TempDir()
	service := newTestService(t, store.NewMemory(), &fakeChat{}, terminal, &testClock{value: &clock, stateDir: stateDir})
	thread := createThread(t, service, domain.ThreadTUI, domain.AgentClaude)
	opened, err := service.OpenTerminal(ctx, thread.ID, OpenTerminal{})
	if err != nil {
		t.Fatal(err)
	}
	if !opened.Reachable || opened.Lifecycle != domain.TerminalLive {
		t.Fatalf("opened terminal = %#v", opened)
	}
	name := terminal.opened[0].TerminalID
	if name != opened.ID || len(name) > 31 || !strings.HasPrefix(name, "term_") {
		t.Fatalf("zmx terminal name = %q", name)
	}

	entry := terminal.entries[name]
	entry.Reachable = false
	terminal.entries[name] = entry
	disconnected := reconcileOne(t, service)
	if disconnected.Lifecycle != domain.TerminalLive || disconnected.Reachable {
		t.Fatalf("unreachable terminal = %#v", disconnected)
	}
	delete(terminal.entries, name)
	missing := reconcileOne(t, service)
	if missing.Lifecycle != domain.TerminalLive || missing.Reason == "" {
		t.Fatalf("missing terminal = %#v", missing)
	}
	clock = clock.Add(31 * time.Second)
	stale := reconcileOne(t, service)
	if stale.Lifecycle != domain.TerminalLive || !stringsContains(stale.Reason, "stayed absent") {
		t.Fatalf("stale terminal = %#v", stale)
	}

	terminal.listErr = errors.New("inventory offline")
	if _, err := service.ReconcileTerminals(ctx); errorCode(err) != "terminal_inventory_unavailable" {
		t.Fatalf("inventory error = %v", err)
	}
	if _, err := service.CleanupTerminals(ctx); errorCode(err) != "terminal_inventory_unavailable" {
		t.Fatalf("cleanup inventory error = %v", err)
	}
	if len(terminal.killed) != 0 {
		t.Fatalf("cleanup killed with failed inventory: %#v", terminal.killed)
	}

	terminal.listErr = nil
	terminal.entries["term_orphan_live"] = ports.TerminalEntry{Name: "term_orphan_live", Reachable: true}
	terminal.entries["term_orphan_down"] = ports.TerminalEntry{Name: "term_orphan_down", Reachable: false}
	cleanup, err := service.CleanupTerminals(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cleanup.TerminatedOrphans, []string{"term_orphan_live"}) {
		t.Fatalf("cleanup = %#v", cleanup)
	}
	if _, ok := terminal.entries["term_orphan_down"]; !ok {
		t.Fatal("cleanup removed unreachable orphan")
	}

	// An atomic child exit marker is authoritative when the session disappears.
	markerPath := filepath.Join(stateDir, "exits", opened.ID+".json")
	exitCode := 7
	exitedAt := clock.Add(time.Second)
	if err := child.WriteMarker(markerPath, child.Marker{TerminalID: opened.ID, ExitedAt: &exitedAt, ExitCode: &exitCode}); err != nil {
		t.Fatal(err)
	}
	exited := reconcileOne(t, service)
	if exited.Lifecycle != domain.TerminalEnded || !stringsContains(exited.Reason, "code 7") {
		t.Fatalf("exited terminal = %#v", exited)
	}
}

func TestOpenTerminalMigratesPersistedPrivateNameToPublicID(t *testing.T) {
	ctx := context.Background()
	repository := store.NewMemory()
	firstTerminal := newFakeTerminal()
	first := newTestService(t, repository, &fakeChat{}, firstTerminal, nil)
	thread := createThread(t, first, domain.ThreadTUI, domain.AgentClaude)
	opened, err := first.OpenTerminal(ctx, thread.ID, OpenTerminal{})
	if err != nil {
		t.Fatal(err)
	}

	legacyName := "atc-unified-" + opened.ID
	repository.State.Terminals[0].LegacyName = legacyName
	repository.State.Terminals[0].Terminal.Reachable = false
	repository.State.Terminals[0].State = store.TerminalMissing

	retryTerminal := newFakeTerminal()
	restarted := newTestService(t, repository, &fakeChat{}, retryTerminal, nil)
	if _, err := restarted.OpenTerminal(ctx, thread.ID, OpenTerminal{}); err != nil {
		t.Fatal(err)
	}
	if len(retryTerminal.opened) != 1 {
		t.Fatalf("opens = %#v", retryTerminal.opened)
	}
	retried := retryTerminal.opened[0]
	if retried.TerminalID != opened.ID || len(retried.TerminalID) > 31 {
		t.Fatalf("retried terminal = %#v", retried)
	}
	if !slices.Contains(retryTerminal.killed, legacyName) {
		t.Fatalf("legacy session was not removed: %#v", retryTerminal.killed)
	}
}

func TestStopIntentAndVerifiedTermination(t *testing.T) {
	ctx := context.Background()
	terminal := newFakeTerminal()
	service := newTestService(t, store.NewMemory(), &fakeChat{}, terminal, nil)
	thread := createThread(t, service, domain.ThreadTUI, domain.AgentCodex)
	opened, err := service.OpenTerminal(ctx, thread.ID, OpenTerminal{})
	if err != nil {
		t.Fatal(err)
	}
	if err := service.TerminateTerminal(ctx, opened.ID); err != nil {
		t.Fatal(err)
	}
	stopped, _ := service.Terminal(opened.ID)
	if stopped.Lifecycle != domain.TerminalEnded || stopped.Reason != "terminated deliberately" {
		t.Fatalf("stopped terminal = %#v", stopped)
	}
	if len(terminal.killed) != 1 {
		t.Fatalf("verified kills = %#v", terminal.killed)
	}
	reopened, err := service.OpenTerminal(ctx, thread.ID, OpenTerminal{})
	if err != nil {
		t.Fatal(err)
	}
	if reopened.ID != opened.ID || reopened.Lifecycle != domain.TerminalLive || !reopened.Reachable {
		t.Fatalf("reopened terminal = %#v, original = %#v", reopened, opened)
	}
}

func TestRestartReconstructionDoesNotDuplicateTerminalEvents(t *testing.T) {
	ctx := context.Background()
	repository := store.NewMemory()
	terminal := newFakeTerminal()
	first := newTestService(t, repository, &fakeChat{}, terminal, nil)
	thread := createThread(t, first, domain.ThreadTUI, domain.AgentClaude)
	if _, err := first.OpenTerminal(ctx, thread.ID, OpenTerminal{}); err != nil {
		t.Fatal(err)
	}
	events := first.EventsAfter(0, "")
	cursor := events[len(events)-1].Sequence
	second := newTestService(t, repository, &fakeChat{}, terminal, nil)
	if _, err := second.ReconcileTerminals(ctx); err != nil {
		t.Fatal(err)
	}
	if duplicated := second.EventsAfter(cursor, ""); len(duplicated) != 0 {
		t.Fatalf("restart duplicated terminal events: %#v", duplicated)
	}
}

func TestTUIProviderThreadsMapExactlyAndTerminalFollowsSelectionAcrossRestart(t *testing.T) {
	ctx := context.Background()
	repository := store.NewMemory()
	terminalAdapter := newFakeTerminal()
	first, err := New(Config{
		Repository: repository, Chat: &fakeChat{}, Terminal: terminalAdapter,
		Status: status.New(nil), StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	launchThread := createThread(t, first, domain.ThreadTUI, domain.AgentCodex)
	terminal, err := first.OpenTerminal(ctx, launchThread.ID, OpenTerminal{})
	if err != nil {
		t.Fatal(err)
	}

	rootOne := []byte(`{"method":"thread/started","atcExactRoot":"root-one","atcThreadTransition":"start","params":{"thread":{"id":"root-one","status":{"type":"idle"}}}}`)
	if err := first.ApplyTerminalEvidence(terminal.ID, domain.AgentCodex, rootOne); err != nil {
		t.Fatal(err)
	}
	bound, _ := first.Terminal(terminal.ID)
	if bound.ActiveThreadID != launchThread.ID || len(first.Threads()) != 1 {
		t.Fatalf("initial binding = %#v, threads = %#v", bound, first.Threads())
	}

	rootTwo := []byte(`{"method":"thread/started","atcExactRoot":"root-two","atcThreadTransition":"fork","params":{"thread":{"id":"root-two","status":{"type":"idle"}}}}`)
	if err := first.ApplyTerminalEvidence(terminal.ID, domain.AgentCodex, rootTwo); err != nil {
		t.Fatal(err)
	}
	threads := first.Threads()
	if len(threads) != 2 {
		t.Fatalf("threads after fork = %#v", threads)
	}
	discovered := threads[1]
	if discovered.ID == launchThread.ID || discovered.TerminalID != terminal.ID {
		t.Fatalf("discovered thread = %#v", discovered)
	}
	forked, _ := first.Terminal(terminal.ID)
	if forked.ActiveThreadID != discovered.ID {
		t.Fatalf("terminal after fork = %#v", forked)
	}

	rootOneResume := []byte(`{"method":"thread/started","atcExactRoot":"root-one","atcThreadTransition":"resume","params":{"thread":{"id":"root-one","status":{"type":"idle"}}}}`)
	if err := first.ApplyTerminalEvidence(terminal.ID, domain.AgentCodex, rootOneResume); err != nil {
		t.Fatal(err)
	}
	resumed, _ := first.Terminal(terminal.ID)
	if resumed.ActiveThreadID != launchThread.ID || len(first.Threads()) != 2 {
		t.Fatalf("terminal after resume = %#v, threads = %#v", resumed, first.Threads())
	}

	persisted, err := repository.Load()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.Threads[0].ProviderRoot != "root-one" || persisted.Threads[1].ProviderRoot != "root-two" {
		t.Fatalf("private identity map = %#v", persisted.Threads)
	}
	restarted, err := New(Config{
		Repository: repository, Chat: &fakeChat{}, Terminal: terminalAdapter,
		Status: status.New(nil), StateDir: t.TempDir(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := restarted.ApplyTerminalEvidence(terminal.ID, domain.AgentCodex, rootTwo); err != nil {
		t.Fatal(err)
	}
	recovered, _ := restarted.Terminal(terminal.ID)
	if recovered.ActiveThreadID != discovered.ID || len(restarted.Threads()) != 2 {
		t.Fatalf("restart mapping = %#v, threads = %#v", recovered, restarted.Threads())
	}

	changes := 0
	for _, event := range restarted.EventsAfter(0, "") {
		if event.Type == "terminal.active_thread_changed" {
			changes++
			if event.Terminal == nil || event.Terminal.ActiveThreadID != event.ThreadID {
				t.Fatalf("active-thread event = %#v", event)
			}
		}
	}
	if changes != 3 {
		t.Fatalf("active-thread changes = %d", changes)
	}
}

func TestUncleanRestartEndsForegroundTurnWithoutClaimingIdle(t *testing.T) {
	repository := store.NewMemory()
	first := newTestService(t, repository, &fakeChat{}, newFakeTerminal(), nil)
	thread := createThread(t, first, domain.ThreadChat, domain.AgentCodex)
	state, err := repository.Load()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	state.Threads[0].Thread.ActiveTurn = &domain.Turn{ID: "interrupted-by-crash", State: domain.TurnRunning, StartedAt: now}
	state.Threads[0].Foreground = domain.ActivityWorking
	state.Threads[0].Thread.Activity = domain.ActivityWorking
	if err := repository.Save(state); err != nil {
		t.Fatal(err)
	}
	restarted := newTestService(t, repository, &fakeChat{}, newFakeTerminal(), nil)
	recovered, err := restarted.Thread(thread.ID)
	if err != nil {
		t.Fatal(err)
	}
	if recovered.ActiveTurn != nil || recovered.LastTurn == nil || recovered.LastTurn.Outcome != domain.TurnFailed || recovered.Activity != domain.ActivityUnknown {
		t.Fatalf("unclean recovery = %#v", recovered)
	}
}

type testClock struct {
	value    *time.Time
	stateDir string
}

func newTestService(t *testing.T, repository store.Repository, chat ports.ChatAdapter, terminal ports.TerminalAdapter, clock *testClock) *Service {
	t.Helper()
	now := time.Now
	stateDir := t.TempDir()
	if clock != nil {
		now = func() time.Time { return *clock.value }
		stateDir = clock.stateDir
	}
	service, err := New(Config{Repository: repository, Chat: chat, Terminal: terminal, StateDir: stateDir, Now: now, StaleAfter: 30 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func createThread(t *testing.T, service *Service, kind domain.ThreadKind, agent domain.Agent) domain.Thread {
	t.Helper()
	thread, err := service.CreateThread(CreateThread{Kind: kind, Agent: agent, CWD: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return thread
}

func waitForEvent(t *testing.T, service *Service, threadID, kind string, after uint64) domain.Event {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		events, err := service.WaitEvents(ctx, after, threadID)
		if err != nil {
			t.Fatal(err)
		}
		for _, event := range events {
			after = event.Sequence
			if event.Type == kind {
				return event
			}
		}
	}
}

func turnEventSequence(service *Service, threadID, turnID string) uint64 {
	var result uint64
	for _, event := range service.EventsAfter(0, threadID) {
		if event.Turn != nil && event.Turn.ID == turnID {
			result = event.Sequence
		}
	}
	return result
}

func reconcileOne(t *testing.T, service *Service) domain.Terminal {
	t.Helper()
	terminals, err := service.ReconcileTerminals(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(terminals) != 1 {
		t.Fatalf("terminals = %#v", terminals)
	}
	return terminals[0]
}

func errorCode(err error) string {
	var domainError *domain.Error
	if errors.As(err, &domainError) {
		return domainError.Code
	}
	return ""
}

func stringsContains(value, substring string) bool {
	return strings.Contains(value, substring)
}
