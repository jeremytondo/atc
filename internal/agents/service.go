package agents

import (
	"context"
	"errors"
	"fmt"
	"os/exec"

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
// with a resolved command and the agent label (terminals.Service in
// production).
type TerminalCreator interface {
	CreateForAgent(ctx context.Context, params api.TerminalCreateParams, agent string) (api.Terminal, error)
}

// Options wires a Service.
type Options struct {
	Catalog   *Catalog
	Terminals TerminalCreator
	// LookPath resolves a binary name on the server's PATH — the
	// availability probe's injectable seam, so tests control which binaries
	// exist. Nil defaults to exec.LookPath.
	LookPath func(name string) (string, error)
}

// Service serves the catalog and owns the launch composition: resolve the
// entry, probe its binary, compose the command, and hand the terminals
// domain a normal create. Reads re-probe availability on every call — no
// cache, no version probing.
type Service struct {
	catalog   *Catalog
	terminals TerminalCreator
	lookPath  func(name string) (string, error)
}

func NewService(opts Options) *Service {
	if opts.Catalog == nil || opts.Terminals == nil {
		// Either nil would panic on the first request; fail at construction
		// instead (the server.NewHandler Verify precedent).
		panic("agents.NewService: Catalog and Terminals must not be nil")
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	return &Service{catalog: opts.Catalog, terminals: opts.Terminals, lookPath: opts.LookPath}
}

// List returns every catalog entry in registration order, availability
// freshly probed.
func (s *Service) List() []api.Agent {
	entries := s.catalog.Entries()
	agents := make([]api.Agent, 0, len(entries))
	for _, entry := range entries {
		agents = append(agents, s.agent(entry))
	}
	return agents
}

// Get returns one catalog entry, availability freshly probed.
func (s *Service) Get(id string) (api.Agent, error) {
	entry, ok := s.catalog.Get(id)
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
	entry, ok := s.catalog.Get(id)
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
	return s.terminals.CreateForAgent(ctx, api.TerminalCreateParams{
		ProjectID: params.ProjectID,
		Name:      name,
		Command:   entry.TUI.Command(),
	}, entry.ID)
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
