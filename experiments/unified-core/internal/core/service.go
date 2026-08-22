// Package core owns orchestration, exact writer/control correlation, normalized
// events, and restart recovery. HTTP handlers only translate these operations.
package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/elevenideas/atc/experiments/unified-core/internal/domain"
	"github.com/elevenideas/atc/experiments/unified-core/internal/ports"
	"github.com/elevenideas/atc/experiments/unified-core/internal/store"
)

type Config struct {
	Repository store.Repository
	Chat       ports.ChatAdapter
	Terminal   ports.TerminalAdapter
	Status     ports.StatusAdapter
	StateDir   string
	StaleAfter time.Duration
	Now        func() time.Time
}

type Service struct {
	mu sync.Mutex

	repository store.Repository
	chat       ports.ChatAdapter
	terminal   ports.TerminalAdapter
	status     ports.StatusAdapter
	stateDir   string
	staleAfter time.Duration
	now        func() time.Time
	state      store.State
	sessions   map[string]ports.ChatSession
	wake       chan struct{}
}

type CreateThread struct {
	Kind  domain.ThreadKind `json:"kind"`
	Agent domain.Agent      `json:"agent"`
	CWD   string            `json:"cwd"`
}

type Prompt struct {
	Text string `json:"text"`
}

type Answer struct {
	OptionID string `json:"optionId"`
	Text     string `json:"text,omitempty"`
}

type OpenTerminal struct{}

type CleanupResult struct {
	TerminatedOrphans []string `json:"terminatedOrphans"`
}

func New(config Config) (*Service, error) {
	if config.Repository == nil || config.Chat == nil || config.Terminal == nil {
		return nil, errors.New("repository, chat adapter, and terminal adapter are required")
	}
	state, err := config.Repository.Load()
	if err != nil {
		return nil, err
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	staleAfter := config.StaleAfter
	if staleAfter <= 0 {
		staleAfter = 30 * time.Second
	}
	stateDir := config.StateDir
	if stateDir == "" {
		stateDir = "."
	}
	service := &Service{
		repository: config.Repository, chat: config.Chat, terminal: config.Terminal,
		status: config.Status, stateDir: stateDir, staleAfter: staleAfter, now: now,
		state: state, sessions: make(map[string]ports.ChatSession), wake: make(chan struct{}),
	}
	if err := service.recoverInterruptedTurns(); err != nil {
		return nil, err
	}
	return service, nil
}

func (s *Service) CreateThread(request CreateThread) (domain.Thread, error) {
	if request.Kind != domain.ThreadChat && request.Kind != domain.ThreadTUI {
		return domain.Thread{}, domain.NewError("invalid_thread_kind", "kind must be chat or tui")
	}
	if request.Agent != domain.AgentClaude && request.Agent != domain.AgentCodex {
		return domain.Thread{}, domain.NewError("invalid_agent", "agent must be claude or codex")
	}
	if strings.TrimSpace(request.CWD) == "" {
		return domain.Thread{}, domain.NewError("invalid_working_directory", "cwd is required")
	}
	cwd, err := filepath.Abs(request.CWD)
	if err != nil {
		return domain.Thread{}, domain.NewError("invalid_working_directory", err.Error())
	}
	cwd, err = filepath.EvalSymlinks(cwd)
	if err != nil {
		return domain.Thread{}, domain.NewError("invalid_working_directory", err.Error())
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return domain.Thread{}, domain.NewError("invalid_working_directory", "cwd must be an existing directory")
	}
	id, err := randomID("thr")
	if err != nil {
		return domain.Thread{}, err
	}
	now := s.now().UTC()
	thread := domain.Thread{
		ID: id, Kind: request.Kind, Agent: request.Agent, CWD: cwd,
		Activity: domain.ActivityIdle, Background: domain.ActivityIdle,
		CreatedAt: now, UpdatedAt: now,
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.state.Threads = append(s.state.Threads, store.ThreadRecord{Thread: thread, Foreground: domain.ActivityIdle})
	s.emitLocked(domain.Event{ThreadID: id, Resource: "thread", Type: "thread.created", Activity: thread.Activity})
	if err := s.saveLocked(); err != nil {
		return domain.Thread{}, err
	}
	return thread, nil
}

func (s *Service) Threads() []domain.Thread {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]domain.Thread, 0, len(s.state.Threads))
	for _, record := range s.state.Threads {
		result = append(result, cloneThread(record.Thread))
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.Before(result[j].CreatedAt) })
	return result
}

func (s *Service) Thread(id string) (domain.Thread, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.threadLocked(id)
	if record == nil {
		return domain.Thread{}, notFound("thread", id)
	}
	return cloneThread(record.Thread), nil
}

func (s *Service) Prompt(ctx context.Context, threadID, text string) (domain.Turn, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return domain.Turn{}, domain.NewError("invalid_prompt", "prompt text is required")
	}
	turnID, err := randomID("turn")
	if err != nil {
		return domain.Turn{}, err
	}
	now := s.now().UTC()
	turn := domain.Turn{ID: turnID, State: domain.TurnRunning, StartedAt: now}

	s.mu.Lock()
	record := s.threadLocked(threadID)
	if record == nil {
		s.mu.Unlock()
		return domain.Turn{}, notFound("thread", threadID)
	}
	if record.Thread.Kind != domain.ThreadChat {
		s.mu.Unlock()
		return domain.Turn{}, wrongKind(threadID, domain.ThreadChat, record.Thread.Kind)
	}
	if record.Thread.ActiveTurn != nil {
		s.mu.Unlock()
		return domain.Turn{}, domain.NewError("turn_in_progress", "thread already has an active foreground turn")
	}
	record.Thread.ActiveTurn = &turn
	record.Foreground = domain.ActivityWorking
	s.recomputeActivityLocked(record)
	s.emitLocked(domain.Event{ThreadID: threadID, Resource: "turn", Type: "turn.started", Activity: record.Thread.Activity, Turn: cloneTurn(&turn)})
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return domain.Turn{}, err
	}
	session := s.sessions[threadID]
	providerSession := record.ProviderSession
	agent, cwd := record.Thread.Agent, record.Thread.CWD
	s.mu.Unlock()

	if session == nil {
		opened, identity, openErr := s.chat.Open(ctx, ports.ChatOpen{
			ThreadID: threadID, Agent: agent, CWD: cwd,
			ProviderSession: providerSession, Events: s,
		})
		if openErr != nil {
			s.finishTurn(threadID, turnID, domain.TurnFailed, openErr)
			return turn, openErr
		}
		if providerSession != "" && identity != providerSession {
			_ = opened.Close(context.Background())
			err := domain.NewError("exact_resume_failed", "adapter returned a different provider session during exact resume")
			s.finishTurn(threadID, turnID, domain.TurnFailed, err)
			return turn, err
		}
		s.mu.Lock()
		record = s.threadLocked(threadID)
		if record == nil || record.Thread.ActiveTurn == nil || record.Thread.ActiveTurn.ID != turnID {
			s.mu.Unlock()
			_ = opened.Close(context.Background())
			return turn, domain.NewError("turn_lost", "turn changed while the chat adapter opened")
		}
		if existing := s.sessions[threadID]; existing != nil {
			s.mu.Unlock()
			_ = opened.Close(context.Background())
			return turn, domain.NewError("writer_conflict", "a chat writer already exists")
		}
		record.ProviderSession = identity
		s.sessions[threadID] = opened
		s.rawLocked(threadID, "chat.materialized", mustJSON(map[string]any{"resumed": providerSession != ""}))
		if err := s.saveLocked(); err != nil {
			delete(s.sessions, threadID)
			s.mu.Unlock()
			_ = opened.Close(context.Background())
			return turn, err
		}
		session = opened
		s.mu.Unlock()
	}

	go func() {
		outcome, promptErr := session.Prompt(context.Background(), turnID, text)
		if outcome == "" {
			if promptErr != nil {
				outcome = domain.TurnFailed
			} else {
				outcome = domain.TurnCompleted
			}
		}
		s.finishTurn(threadID, turnID, outcome, promptErr)
	}()
	return turn, nil
}

// RecoverChatSessions eagerly resumes exact persisted ACP identities so a
// background request can still be answered before another foreground prompt.
// Failed resumes remain failed closed and do not prevent unrelated Threads
// from recovering.
func (s *Service) RecoverChatSessions(ctx context.Context) error {
	type candidate struct {
		threadID string
		agent    domain.Agent
		cwd      string
		identity string
	}
	s.mu.Lock()
	candidates := make([]candidate, 0)
	for _, record := range s.state.Threads {
		if record.Thread.Kind == domain.ThreadChat && record.ProviderSession != "" && s.sessions[record.Thread.ID] == nil {
			candidates = append(candidates, candidate{
				threadID: record.Thread.ID, agent: record.Thread.Agent,
				cwd: record.Thread.CWD, identity: record.ProviderSession,
			})
		}
	}
	s.mu.Unlock()
	var result error
	for _, item := range candidates {
		openContext, cancelOpen := context.WithTimeout(ctx, 30*time.Second)
		session, identity, err := s.chat.Open(openContext, ports.ChatOpen{
			ThreadID: item.threadID, Agent: item.agent, CWD: item.cwd,
			ProviderSession: item.identity, Events: s,
		})
		cancelOpen()
		if err == nil && identity != item.identity {
			_ = session.Close(context.Background())
			err = domain.NewError("exact_resume_failed", "adapter returned a different provider session during recovery")
		}
		if err != nil {
			s.mu.Lock()
			if record := s.threadLocked(item.threadID); record != nil {
				record.Foreground = domain.ActivityUnknown
				record.Thread.Background = domain.ActivityUnknown
				s.recomputeActivityLocked(record)
				s.rawLocked(item.threadID, "chat.recovery_failed", mustJSON(map[string]string{"error": err.Error()}))
				_ = s.saveLocked()
			}
			s.mu.Unlock()
			result = errors.Join(result, fmt.Errorf("recover chat thread %s: %w", item.threadID, err))
			continue
		}
		s.mu.Lock()
		if existing := s.sessions[item.threadID]; existing != nil {
			s.mu.Unlock()
			_ = session.Close(context.Background())
			continue
		}
		s.sessions[item.threadID] = session
		s.rawLocked(item.threadID, "chat.recovered", mustJSON(map[string]bool{"exact": true}))
		_ = s.saveLocked()
		s.mu.Unlock()
	}
	return result
}

func (s *Service) Interrupt(ctx context.Context, threadID, turnID string) error {
	s.mu.Lock()
	record := s.threadLocked(threadID)
	if record == nil {
		s.mu.Unlock()
		return notFound("thread", threadID)
	}
	if record.Thread.Kind != domain.ThreadChat {
		s.mu.Unlock()
		return wrongKind(threadID, domain.ThreadChat, record.Thread.Kind)
	}
	if record.Thread.ActiveTurn == nil || record.Thread.ActiveTurn.ID != turnID {
		s.mu.Unlock()
		return domain.NewError("turn_mismatch", "interrupt must name the exact active foreground turn")
	}
	session := s.sessions[threadID]
	s.mu.Unlock()
	if session == nil {
		return domain.NewError("writer_unavailable", "the exact chat writer is unavailable")
	}
	if err := session.Interrupt(ctx, turnID); err != nil {
		return err
	}
	s.mu.Lock()
	s.emitLocked(domain.Event{ThreadID: threadID, Resource: "turn", Type: "turn.interrupt_requested", Text: "foreground only; background work may continue"})
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

func (s *Service) PendingRequests(threadID string) ([]domain.PendingRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.threadLocked(threadID)
	if record == nil {
		return nil, notFound("thread", threadID)
	}
	if record.Thread.Kind != domain.ThreadChat {
		return nil, wrongKind(threadID, domain.ThreadChat, record.Thread.Kind)
	}
	result := make([]domain.PendingRequest, 0, len(record.Requests))
	for _, request := range record.Requests {
		result = append(result, request.Request)
	}
	return result, nil
}

func (s *Service) AnswerRequest(ctx context.Context, threadID, requestID string, answer Answer) error {
	s.mu.Lock()
	record := s.threadLocked(threadID)
	if record == nil {
		s.mu.Unlock()
		return notFound("thread", threadID)
	}
	if record.Thread.Kind != domain.ThreadChat {
		s.mu.Unlock()
		return wrongKind(threadID, domain.ThreadChat, record.Thread.Kind)
	}
	index := -1
	for i := range record.Requests {
		if record.Requests[i].Request.ID == requestID {
			index = i
			break
		}
	}
	if index < 0 {
		s.mu.Unlock()
		return notFound("request", requestID)
	}
	request := record.Requests[index]
	session := s.sessions[threadID]
	s.mu.Unlock()
	if session == nil {
		return domain.NewError("writer_unavailable", "the exact chat writer is unavailable; restart recovery must reconnect it before answering")
	}
	value := answer.OptionID
	if value == "" {
		value = answer.Text
	}
	if value == "" {
		return domain.NewError("invalid_answer", "optionId or text is required")
	}
	if err := session.Answer(ctx, request.ProviderRef, value); err != nil {
		return err
	}
	s.mu.Lock()
	record = s.threadLocked(threadID)
	for i := range record.Requests {
		if record.Requests[i].Request.ID == requestID {
			record.Requests = append(record.Requests[:i], record.Requests[i+1:]...)
			break
		}
	}
	record.Thread.PendingCount = len(record.Requests)
	s.recomputeActivityLocked(record)
	s.emitLocked(domain.Event{ThreadID: threadID, Resource: "request", Type: "request.answered", Activity: record.Thread.Activity})
	err := s.saveLocked()
	s.mu.Unlock()
	return err
}

func (s *Service) OpenTerminal(ctx context.Context, threadID string, _ OpenTerminal) (domain.Terminal, error) {
	s.mu.Lock()
	record := s.threadLocked(threadID)
	if record == nil {
		s.mu.Unlock()
		return domain.Terminal{}, notFound("thread", threadID)
	}
	if record.Thread.Kind != domain.ThreadTUI {
		s.mu.Unlock()
		return domain.Terminal{}, wrongKind(threadID, domain.ThreadTUI, record.Thread.Kind)
	}
	terminalRecord := s.terminalForThreadLocked(threadID)
	if terminalRecord != nil && terminalRecord.Terminal.Lifecycle == domain.TerminalLive && terminalRecord.Terminal.Reachable {
		result := terminalRecord.Terminal
		s.mu.Unlock()
		return result, nil
	}
	if terminalRecord == nil {
		id, err := randomID("term")
		if err != nil {
			s.mu.Unlock()
			return domain.Terminal{}, err
		}
		now := s.now().UTC()
		terminal := domain.Terminal{ID: id, ThreadID: threadID, Lifecycle: domain.TerminalLive, CreatedAt: now}
		exitPath := filepath.Join(s.stateDir, "exits", id+".json")
		s.state.Terminals = append(s.state.Terminals, store.TerminalRecord{
			Terminal: terminal, PrivateName: "atc-unified-" + id,
			State: store.TerminalMissing, ExitPath: exitPath,
		})
		record.Thread.TerminalID = id
		terminalRecord = &s.state.Terminals[len(s.state.Terminals)-1]
	} else {
		terminalRecord.Terminal.Lifecycle = domain.TerminalLive
		terminalRecord.Terminal.EndedAt = nil
		terminalRecord.Terminal.Reason = ""
		terminalRecord.State = store.TerminalMissing
		terminalRecord.MissingSince = nil
		terminalRecord.StopRequestedAt = nil
	}
	open := ports.TerminalOpen{
		TerminalID: terminalRecord.PrivateName, Agent: record.Thread.Agent,
		CWD: record.Thread.CWD, ExitPath: terminalRecord.ExitPath,
	}
	public := terminalRecord.Terminal
	s.emitLocked(domain.Event{ThreadID: threadID, Resource: "terminal", Type: "terminal.opening", Terminal: cloneTerminal(&public)})
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return domain.Terminal{}, err
	}
	s.mu.Unlock()

	if err := s.terminal.Open(ctx, open); err != nil {
		return domain.Terminal{}, err
	}
	if _, err := s.ReconcileTerminals(ctx); err != nil {
		return domain.Terminal{}, err
	}
	return s.Terminal(public.ID)
}

func (s *Service) Terminals() []domain.Terminal {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]domain.Terminal, 0, len(s.state.Terminals))
	for _, record := range s.state.Terminals {
		result = append(result, record.Terminal)
	}
	return result
}

func (s *Service) Terminal(id string) (domain.Terminal, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.terminalLocked(id)
	if record == nil {
		return domain.Terminal{}, notFound("terminal", id)
	}
	return record.Terminal, nil
}

func (s *Service) SendTerminal(ctx context.Context, id string, input []byte) error {
	private, err := s.liveTerminalName(id)
	if err != nil {
		return err
	}
	return s.terminal.Send(ctx, private, input)
}

func (s *Service) TerminalOutput(ctx context.Context, id string) ([]byte, error) {
	private, err := s.liveTerminalName(id)
	if err != nil {
		return nil, err
	}
	return s.terminal.Output(ctx, private)
}

func (s *Service) AttachTerminal(ctx context.Context, id string, input interface{ Read([]byte) (int, error) }, output interface{ Write([]byte) (int, error) }) error {
	private, err := s.liveTerminalName(id)
	if err != nil {
		return err
	}
	return s.terminal.Attach(ctx, private, input, output)
}

func (s *Service) TerminateTerminal(ctx context.Context, id string) error {
	s.mu.Lock()
	record := s.terminalLocked(id)
	if record == nil {
		s.mu.Unlock()
		return notFound("terminal", id)
	}
	now := s.now().UTC()
	record.StopRequestedAt = &now
	private := record.PrivateName
	if err := s.saveLocked(); err != nil {
		s.mu.Unlock()
		return err
	}
	s.mu.Unlock()
	if err := s.terminal.Terminate(ctx, private); err != nil {
		return err
	}
	_, err := s.ReconcileTerminals(ctx)
	return err
}

func (s *Service) ReconcileTerminals(ctx context.Context) ([]domain.Terminal, error) {
	inventory, inventoryErr := s.terminal.Inventory(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	if inventoryErr != nil {
		for i := range s.state.Terminals {
			record := &s.state.Terminals[i]
			if record.Terminal.Lifecycle == domain.TerminalEnded {
				continue
			}
			record.State = store.TerminalDisconnected
			record.Terminal.Reachable = false
			record.Terminal.Reason = "terminal inventory unavailable"
		}
		_ = s.saveLocked()
		return publicTerminals(s.state.Terminals), domain.NewError("terminal_inventory_unavailable", inventoryErr.Error())
	}
	entries := make(map[string]ports.TerminalEntry, len(inventory))
	for _, entry := range inventory {
		entries[entry.Name] = entry
	}
	now := s.now().UTC()
	for i := range s.state.Terminals {
		record := &s.state.Terminals[i]
		entry, present := entries[record.PrivateName]
		exit, exitErr := readExit(record.ExitPath, record.Terminal.ID)
		if exitErr != nil {
			return nil, exitErr
		}
		previousLifecycle := record.Terminal.Lifecycle
		switch {
		case present && entry.Reachable:
			record.State = store.TerminalRunning
			record.DaemonPID = entry.DaemonPID
			record.LastSeenAt = &now
			record.MissingSince = nil
			record.Terminal.Lifecycle = domain.TerminalLive
			record.Terminal.Reachable = true
			record.Terminal.Reason = ""
		case present:
			record.State = store.TerminalDisconnected
			record.DaemonPID = entry.DaemonPID
			record.Terminal.Reachable = false
			record.Terminal.Reason = "terminal is present but unreachable"
		case exit != nil || record.StopRequestedAt != nil:
			record.State = store.TerminalExited
			record.Terminal.Lifecycle = domain.TerminalEnded
			record.Terminal.Reachable = false
			record.Terminal.EndedAt = &now
			if record.StopRequestedAt != nil {
				record.Terminal.Reason = "terminated deliberately"
			} else {
				record.Terminal.Reason = exit.reason()
			}
		default:
			if record.MissingSince == nil {
				record.MissingSince = &now
			}
			record.Terminal.Reachable = false
			if now.Sub(*record.MissingSince) >= s.staleAfter {
				record.State = store.TerminalStale
				record.Terminal.Reason = "terminal stayed absent without exit evidence"
			} else {
				record.State = store.TerminalMissing
				record.Terminal.Reason = "terminal is absent without exit evidence"
			}
		}
		if record.Terminal.Lifecycle == domain.TerminalEnded {
			thread := s.threadLocked(record.Terminal.ThreadID)
			if thread != nil {
				thread.Foreground = domain.ActivityIdle
				thread.Thread.Background = domain.ActivityIdle
				s.recomputeActivityLocked(thread)
			}
		}
		if previousLifecycle != record.Terminal.Lifecycle {
			typeName := "terminal.live"
			if record.Terminal.Lifecycle == domain.TerminalEnded {
				typeName = "terminal.ended"
			}
			s.emitLocked(domain.Event{ThreadID: record.Terminal.ThreadID, Resource: "terminal", Type: typeName, Terminal: cloneTerminal(&record.Terminal)})
		}
	}
	if err := s.saveLocked(); err != nil {
		return nil, err
	}
	return publicTerminals(s.state.Terminals), nil
}

// CleanupTerminals acts only on reachable sessions in the adapter's private
// inventory that have no persisted ATC Terminal. Incomplete inventory and
// unreachable entries always fail closed.
func (s *Service) CleanupTerminals(ctx context.Context) (CleanupResult, error) {
	inventory, err := s.terminal.Inventory(ctx)
	if err != nil {
		return CleanupResult{}, domain.NewError("terminal_inventory_unavailable", "refusing cleanup without complete inventory: "+err.Error())
	}
	s.mu.Lock()
	managed := make(map[string]bool, len(s.state.Terminals))
	for _, record := range s.state.Terminals {
		managed[record.PrivateName] = true
	}
	s.mu.Unlock()
	result := CleanupResult{TerminatedOrphans: []string{}}
	for _, entry := range inventory {
		if managed[entry.Name] || !entry.Reachable {
			continue
		}
		if err := s.terminal.Terminate(ctx, entry.Name); err != nil {
			return result, err
		}
		result.TerminatedOrphans = append(result.TerminatedOrphans, entry.Name)
	}
	sort.Strings(result.TerminatedOrphans)
	return result, nil
}

func (s *Service) EventsAfter(sequence uint64, threadID string) []domain.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.eventsAfterLocked(sequence, threadID)
}

func (s *Service) WaitEvents(ctx context.Context, sequence uint64, threadID string) ([]domain.Event, error) {
	for {
		s.mu.Lock()
		events := s.eventsAfterLocked(sequence, threadID)
		wake := s.wake
		s.mu.Unlock()
		if len(events) > 0 {
			return events, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-wake:
		}
	}
}

func (s *Service) DiagnosticsAfter(sequence uint64) []domain.Diagnostic {
	s.mu.Lock()
	defer s.mu.Unlock()
	result := make([]domain.Diagnostic, 0)
	for _, diagnostic := range s.state.Diagnostics {
		if diagnostic.Sequence > sequence {
			result = append(result, diagnostic)
		}
	}
	return result
}

func (s *Service) ApplyStatus(threadID string, provider domain.Agent, raw []byte) error {
	if s.status == nil {
		return domain.NewError("status_unavailable", "no provider status adapter is configured")
	}
	observation, recognized := s.status.Observe(threadID, provider, raw)
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.threadLocked(threadID)
	if record == nil {
		return notFound("thread", threadID)
	}
	if record.Thread.Kind != domain.ThreadTUI {
		return wrongKind(threadID, domain.ThreadTUI, record.Thread.Kind)
	}
	if record.Thread.Agent != provider {
		return domain.NewError("provider_mismatch", "status evidence does not match the thread agent")
	}
	s.rawLocked(threadID, "status."+string(provider), raw)
	if !recognized {
		return s.saveLocked()
	}
	if terminal := s.terminalForThreadLocked(threadID); terminal != nil && terminal.Terminal.Lifecycle == domain.TerminalEnded {
		return s.saveLocked()
	}
	record.Thread.Background = observation.Activity
	s.recomputeActivityLocked(record)
	s.emitLocked(domain.Event{ThreadID: threadID, Resource: "thread", Type: "thread.activity", Activity: record.Thread.Activity})
	return s.saveLocked()
}

// ChatEvents implementation. Adapter callbacks may arrive after Prompt has
// returned; they remain valid session evidence and never depend on active turn.
func (s *Service) AssistantText(threadID, turnID, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emitLocked(domain.Event{ThreadID: threadID, TurnID: turnID, Resource: "message", Type: "assistant.delta", Text: text})
	_ = s.saveLocked()
}

func (s *Service) BackgroundActivity(threadID string, activity domain.Activity) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.threadLocked(threadID)
	if record == nil {
		return
	}
	record.Thread.Background = activity
	s.recomputeActivityLocked(record)
	s.emitLocked(domain.Event{ThreadID: threadID, Resource: "thread", Type: "thread.activity", Activity: record.Thread.Activity})
	_ = s.saveLocked()
}

func (s *Service) Request(threadID, turnID, providerRef string, kind domain.RequestKind, prompt string, options []domain.RequestOption) {
	id, err := randomID("req")
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.threadLocked(threadID)
	if record == nil {
		return
	}
	for _, pending := range record.Requests {
		if pending.ProviderRef == providerRef {
			return
		}
	}
	request := domain.PendingRequest{
		ID: id, ThreadID: threadID, TurnID: turnID, Kind: kind,
		Prompt: prompt, Options: append([]domain.RequestOption(nil), options...), CreatedAt: s.now().UTC(),
	}
	record.Requests = append(record.Requests, store.RequestRecord{Request: request, ProviderRef: providerRef})
	record.Thread.PendingCount = len(record.Requests)
	s.recomputeActivityLocked(record)
	s.emitLocked(domain.Event{ThreadID: threadID, Resource: "request", Type: "request.opened", Activity: record.Thread.Activity, Request: &request})
	_ = s.saveLocked()
}

func (s *Service) Raw(threadID, kind string, raw []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rawLocked(threadID, kind, raw)
	_ = s.saveLocked()
}

func (s *Service) Close(ctx context.Context) error {
	s.mu.Lock()
	sessions := make([]ports.ChatSession, 0, len(s.sessions))
	for _, session := range s.sessions {
		sessions = append(sessions, session)
	}
	s.sessions = make(map[string]ports.ChatSession)
	s.mu.Unlock()
	var result error
	for _, session := range sessions {
		result = errors.Join(result, session.Close(ctx))
	}
	return result
}

func (s *Service) finishTurn(threadID, turnID string, outcome domain.TurnOutcome, turnErr error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.threadLocked(threadID)
	if record == nil || record.Thread.ActiveTurn == nil || record.Thread.ActiveTurn.ID != turnID {
		return
	}
	now := s.now().UTC()
	turn := *record.Thread.ActiveTurn
	turn.State = domain.TurnEnded
	turn.Outcome = outcome
	turn.EndedAt = &now
	if turnErr != nil {
		turn.Error = turnErr.Error()
	}
	record.Thread.ActiveTurn = nil
	record.Thread.LastTurn = &turn
	record.Foreground = domain.ActivityIdle
	s.recomputeActivityLocked(record)
	s.emitLocked(domain.Event{ThreadID: threadID, Resource: "turn", Type: "turn.ended", Activity: record.Thread.Activity, Turn: &turn})
	_ = s.saveLocked()
}

func (s *Service) recoverInterruptedTurns() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	changed := false
	for i := range s.state.Threads {
		record := &s.state.Threads[i]
		if record.Thread.ActiveTurn == nil {
			continue
		}
		now := s.now().UTC()
		turn := *record.Thread.ActiveTurn
		turn.State = domain.TurnEnded
		turn.Outcome = domain.TurnFailed
		turn.EndedAt = &now
		turn.Error = "core restarted before the foreground turn produced an outcome"
		record.Thread.ActiveTurn = nil
		record.Thread.LastTurn = &turn
		record.Foreground = domain.ActivityUnknown
		record.Thread.Background = domain.ActivityUnknown
		s.recomputeActivityLocked(record)
		s.emitLocked(domain.Event{ThreadID: record.Thread.ID, Resource: "turn", Type: "turn.recovery_failed", Activity: record.Thread.Activity, Turn: &turn})
		changed = true
	}
	if changed {
		return s.saveLocked()
	}
	return nil
}

func (s *Service) recomputeActivityLocked(record *store.ThreadRecord) {
	record.Thread.PendingCount = len(record.Requests)
	record.Thread.Activity = domain.CombineActivity(record.Foreground, record.Thread.Background, len(record.Requests) > 0)
	record.Thread.UpdatedAt = s.now().UTC()
}

func (s *Service) emitLocked(event domain.Event) {
	event.Sequence = s.state.NextEvent
	s.state.NextEvent++
	event.CreatedAt = s.now().UTC()
	s.state.Events = append(s.state.Events, event)
	normalized := event
	s.state.Diagnostics = append(s.state.Diagnostics, domain.Diagnostic{
		Sequence: s.state.NextDiag, ThreadID: event.ThreadID, Layer: "normalized",
		Kind: event.Type, Normalized: &normalized, CreatedAt: event.CreatedAt,
	})
	s.state.NextDiag++
	close(s.wake)
	s.wake = make(chan struct{})
}

func (s *Service) rawLocked(threadID, kind string, raw []byte) {
	evidence := append(json.RawMessage(nil), raw...)
	if !json.Valid(evidence) {
		encoded, _ := json.Marshal(string(raw))
		evidence = encoded
	}
	s.state.Diagnostics = append(s.state.Diagnostics, domain.Diagnostic{
		Sequence: s.state.NextDiag, ThreadID: threadID, Layer: "raw", Kind: kind,
		Raw: evidence, CreatedAt: s.now().UTC(),
	})
	s.state.NextDiag++
}

func (s *Service) saveLocked() error { return s.repository.Save(s.state) }

func (s *Service) eventsAfterLocked(sequence uint64, threadID string) []domain.Event {
	result := make([]domain.Event, 0)
	for _, event := range s.state.Events {
		if event.Sequence <= sequence || (threadID != "" && event.ThreadID != threadID) {
			continue
		}
		result = append(result, event)
	}
	return result
}

func (s *Service) threadLocked(id string) *store.ThreadRecord {
	for i := range s.state.Threads {
		if s.state.Threads[i].Thread.ID == id {
			return &s.state.Threads[i]
		}
	}
	return nil
}

func (s *Service) terminalLocked(id string) *store.TerminalRecord {
	for i := range s.state.Terminals {
		if s.state.Terminals[i].Terminal.ID == id {
			return &s.state.Terminals[i]
		}
	}
	return nil
}

func (s *Service) terminalForThreadLocked(threadID string) *store.TerminalRecord {
	for i := range s.state.Terminals {
		if s.state.Terminals[i].Terminal.ThreadID == threadID {
			return &s.state.Terminals[i]
		}
	}
	return nil
}

func (s *Service) liveTerminalName(id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	record := s.terminalLocked(id)
	if record == nil {
		return "", notFound("terminal", id)
	}
	if record.Terminal.Lifecycle != domain.TerminalLive || !record.Terminal.Reachable {
		return "", domain.NewError("terminal_unavailable", "terminal is not live and reachable")
	}
	return record.PrivateName, nil
}

type exitMarker struct {
	Version    int        `json:"version"`
	TerminalID string     `json:"terminalId"`
	ExitedAt   *time.Time `json:"exitedAt"`
	ExitCode   *int       `json:"exitCode"`
	Signal     string     `json:"signal"`
	Error      string     `json:"error"`
}

func readExit(path, terminalID string) (*exitMarker, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var marker exitMarker
	if err := json.Unmarshal(contents, &marker); err != nil {
		return nil, err
	}
	if marker.Version != 1 || marker.TerminalID != terminalID {
		return nil, fmt.Errorf("invalid terminal exit marker for %s", terminalID)
	}
	if marker.ExitedAt == nil {
		return nil, nil
	}
	return &marker, nil
}

func (m *exitMarker) reason() string {
	if m.Error != "" {
		return m.Error
	}
	if m.Signal != "" {
		return "terminal process exited from signal " + m.Signal
	}
	if m.ExitCode != nil {
		return fmt.Sprintf("terminal process exited with code %d", *m.ExitCode)
	}
	return "terminal process exited"
}

func publicTerminals(records []store.TerminalRecord) []domain.Terminal {
	result := make([]domain.Terminal, 0, len(records))
	for _, record := range records {
		result = append(result, record.Terminal)
	}
	return result
}

func wrongKind(id string, expected, actual domain.ThreadKind) error {
	return domain.NewError("wrong_thread_kind", fmt.Sprintf("thread %s is %s; operation requires %s", id, actual, expected))
}

func notFound(resource, id string) error {
	return domain.NewError(resource+"_not_found", fmt.Sprintf("%s %s was not found", resource, id))
}

func randomID(prefix string) (string, error) {
	data := make([]byte, 12)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return prefix + "_" + hex.EncodeToString(data), nil
}

func cloneThread(thread domain.Thread) domain.Thread {
	copy := thread
	copy.ActiveTurn = cloneTurn(thread.ActiveTurn)
	copy.LastTurn = cloneTurn(thread.LastTurn)
	return copy
}

func cloneTurn(turn *domain.Turn) *domain.Turn {
	if turn == nil {
		return nil
	}
	copy := *turn
	return &copy
}

func cloneTerminal(terminal *domain.Terminal) *domain.Terminal {
	if terminal == nil {
		return nil
	}
	copy := *terminal
	return &copy
}

func mustJSON(value any) []byte {
	encoded, _ := json.Marshal(value)
	return encoded
}
