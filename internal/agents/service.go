package agents

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"

	"github.com/jeremytondo/atc/internal/api"
)

// ErrNotFound reports an id with no catalog entry; the API layer maps it
// to 404.
var ErrNotFound = errors.New("agent not found")

// ErrUnavailable refuses a launch whose tui binary does not resolve on
// the server's PATH; the message names the missing command and its
// install hint, and no terminal record is created. The probe is a
// best-effort early gate — the daemon's PATH and the login shell's can
// disagree — so past it, launch failures surface as exit evidence through
// the normal terminal status machinery, never a separate error path.
var ErrUnavailable = errors.New("agent unavailable")

// TerminalCreator is the seam into the terminals domain: CreateForAgent
// with the agent label and a command factory (terminals.Service in
// production). The factory runs once the terminal identity is minted —
// per-launch context like hook settings needs the id before the session
// starts — and stays an opaque function there, so the terminals domain
// never learns agent vocabulary.
type TerminalCreator interface {
	CreateForAgent(ctx context.Context, params api.TerminalCreateParams, agent string,
		compose func(terminalID, directory string) (string, error)) (api.Terminal, error)
}

// Options wires a Service.
type Options struct {
	// Entries is the compiled-in catalog, one registration per built-in
	// agent, listed in registration order.
	Entries   []Entry
	Terminals TerminalCreator
	// LookPath resolves a binary name on the server's PATH — the
	// availability probe's injectable seam, so tests control which binaries
	// exist. Nil defaults to exec.LookPath.
	LookPath func(name string) (string, error)
}

// Service is the read-only catalog plus the launch composition: resolve
// the entry, probe its binary, compose the command, and hand the
// terminals domain a normal create. Reads re-probe availability on every
// call — no cache, no version probing.
type Service struct {
	entries   []Entry
	index     map[string]int
	terminals TerminalCreator
	lookPath  func(name string) (string, error)
}

// NewService assembles the catalog. A duplicate id is an error — the
// composition root fails the boot.
func NewService(opts Options) (*Service, error) {
	if opts.Terminals == nil {
		// A nil terminals service would panic on the first launch request;
		// fail at construction instead (the server.NewHandler Verify
		// precedent).
		panic("agents.NewService: Terminals must not be nil")
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	service := &Service{
		// Cloned so no caller-held slice can mutate the catalog after the
		// duplicate check.
		entries:   slices.Clone(opts.Entries),
		index:     make(map[string]int, len(opts.Entries)),
		terminals: opts.Terminals,
		lookPath:  opts.LookPath,
	}
	for i, entry := range service.entries {
		if _, taken := service.index[entry.ID]; taken {
			return nil, fmt.Errorf("duplicate agent id %q", entry.ID)
		}
		service.index[entry.ID] = i
	}
	return service, nil
}

// List returns every catalog entry in registration order, availability
// freshly probed.
func (s *Service) List() []api.Agent {
	agents := make([]api.Agent, 0, len(s.entries))
	for _, entry := range s.entries {
		agents = append(agents, s.agent(entry))
	}
	return agents
}

// Get returns one catalog entry, availability freshly probed.
func (s *Service) Get(id string) (api.Agent, error) {
	entry, ok := s.entry(id)
	if !ok {
		return api.Agent{}, ErrNotFound
	}
	return s.agent(entry), nil
}

// Launch resolves the agent, probes its binary, and creates the terminal
// that runs its TUI: the adapter supplies the command, the entry's display
// name is the default terminal name, and everything from the record on is
// the normal terminal create path — persistence, wrapper, verification
// window, status, and events all belong to the terminals domain.
func (s *Service) Launch(ctx context.Context, id string, params api.AgentLaunchParams) (api.Terminal, error) {
	entry, ok := s.entry(id)
	if !ok {
		return api.Terminal{}, ErrNotFound
	}
	if entry.TUI == nil {
		return api.Terminal{}, fmt.Errorf("%w: %s has no tui capability", ErrUnavailable, entry.ID)
	}
	if _, err := s.lookPath(entry.TUI.Binary()); err != nil {
		return api.Terminal{}, fmt.Errorf("%w: command %q not found on the server's PATH; install with: %s",
			ErrUnavailable, entry.TUI.Binary(), entry.TUI.InstallHint())
	}
	name := params.Name
	if name == "" {
		name = entry.Name
	}
	// Prewarm runs slow launch-time work (starting and connecting the
	// shared Codex app-server) before the terminals domain takes its
	// commit lock — compose runs under that lock, and a cold provider
	// start there would block every terminal mutation server-wide.
	if warmer, ok := entry.TUI.(interface {
		Prewarm(ctx context.Context) error
	}); ok {
		if err := warmer.Prewarm(ctx); err != nil {
			return api.Terminal{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
		}
	}
	return s.terminals.CreateForAgent(ctx, api.TerminalCreateParams{
		ProjectID: params.ProjectID,
		Name:      name,
	}, entry.ID, func(terminalID, directory string) (string, error) {
		return entry.TUI.Command(ctx, LaunchContext{TerminalID: terminalID, Directory: directory})
	})
}

func (s *Service) entry(id string) (Entry, bool) {
	i, ok := s.index[id]
	if !ok {
		return Entry{}, false
	}
	return s.entries[i], true
}

// agent converts an entry to its wire shape, probing availability per
// capability. The install hint is present whether or not the binary is.
func (s *Service) agent(entry Entry) api.Agent {
	agent := api.Agent{ID: entry.ID, Name: entry.Name, Capabilities: []api.AgentCapability{}}
	if entry.TUI != nil {
		_, err := s.lookPath(entry.TUI.Binary())
		agent.Capabilities = append(agent.Capabilities, api.AgentCapability{
			Capability:  CapabilityTUI,
			Available:   err == nil,
			InstallHint: entry.TUI.InstallHint(),
		})
	}
	return agent
}
