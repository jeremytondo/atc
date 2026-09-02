package agents

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
)

// ErrNotFound reports an agent or adapter id with no catalog entry; the
// API layer maps it to 404.
var ErrNotFound = errors.New("agent not found")

// ErrUnavailable refuses a launch no adapter can perform right now: the
// agent's tui binary does not resolve on the server's PATH (the message
// names the missing command and its install hint), or the thread belongs
// to a program only it can open. No terminal record is created. The
// probe is a best-effort early gate — the daemon's PATH and the login
// shell's can disagree — so past it, launch failures surface as exit
// evidence through the normal terminal status machinery, never a
// separate error path.
var ErrUnavailable = errors.New("agent unavailable")

// TerminalCreator is the seam into the terminals domain: CreateForAgent
// with the agent label, an optional working-directory override, and the
// adapter's hooks (terminals.Service in production). Prepare runs once
// the directory is resolved and before the commit lock; Compose runs once
// the terminal identity is minted — per-launch context like hook settings
// needs the id before the session starts. Both stay opaque functions
// there, so the terminals domain never learns agent vocabulary.
type TerminalCreator interface {
	CreateForAgent(ctx context.Context, params api.TerminalCreateParams, launch terminals.AgentLaunch) (api.Terminal, error)
}

// Options wires a Service.
type Options struct {
	// Adapters is the compiled-in catalog, one registration per built-in
	// adapter, listed in registration order.
	Adapters  []Adapter
	Terminals TerminalCreator
	// LookPath resolves a binary name on the server's PATH — the
	// availability probe's injectable seam, so tests control which binaries
	// exist. Nil defaults to exec.LookPath.
	LookPath func(name string) (string, error)
}

// Service is the read-only catalog plus the launch composition: resolve
// the agent to an adapter that launches it, probe its binary, compose the
// command, and hand the terminals domain a normal create. Reads re-probe
// availability on every call — no cache, no version probing.
type Service struct {
	adapters  []Adapter
	index     map[string]int
	terminals TerminalCreator
	lookPath  func(name string) (string, error)
}

// NewService assembles the catalog. A duplicate adapter id, or an agent
// declared twice by one adapter, is an error — the composition root
// fails the boot.
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
		adapters:  slices.Clone(opts.Adapters),
		index:     make(map[string]int, len(opts.Adapters)),
		terminals: opts.Terminals,
		lookPath:  opts.LookPath,
	}
	for i, adapter := range service.adapters {
		if _, taken := service.index[adapter.ID]; taken {
			return nil, fmt.Errorf("duplicate adapter id %q", adapter.ID)
		}
		service.index[adapter.ID] = i
		seen := make(map[string]bool, len(adapter.Agents))
		for _, agent := range adapter.Agents {
			if seen[agent.ID] {
				return nil, fmt.Errorf("adapter %q declares agent %q twice", adapter.ID, agent.ID)
			}
			seen[agent.ID] = true
		}
	}
	return service, nil
}

// List returns every agent some adapter declares, in first-declaration
// order, availability freshly probed. An agent's name is its first
// declarer's; its adapters are every declarer in registration order.
func (s *Service) List() []api.Agent {
	var agents []api.Agent
	index := map[string]int{}
	for _, adapter := range s.adapters {
		for _, spec := range adapter.Agents {
			i, ok := index[spec.ID]
			if !ok {
				i = len(agents)
				index[spec.ID] = i
				agents = append(agents, api.Agent{ID: spec.ID, Name: spec.Name, Adapters: []string{}})
			}
			agents[i].Adapters = append(agents[i].Adapters, adapter.ID)
			if spec.TUI != nil && s.resolves(spec.TUI) {
				agents[i].Available = true
			}
		}
	}
	if agents == nil {
		agents = []api.Agent{}
	}
	return agents
}

// Get returns one agent, availability freshly probed.
func (s *Service) Get(id string) (api.Agent, error) {
	for _, agent := range s.List() {
		if agent.ID == id {
			return agent, nil
		}
	}
	return api.Agent{}, ErrNotFound
}

// Adapters returns every adapter in registration order, availability
// freshly probed.
func (s *Service) Adapters() []api.AgentAdapter {
	adapters := make([]api.AgentAdapter, 0, len(s.adapters))
	for _, adapter := range s.adapters {
		adapters = append(adapters, s.adapter(adapter))
	}
	return adapters
}

// Adapter returns one adapter, availability freshly probed.
func (s *Service) Adapter(id string) (api.AgentAdapter, error) {
	i, ok := s.index[id]
	if !ok {
		return api.AgentAdapter{}, ErrNotFound
	}
	return s.adapter(s.adapters[i]), nil
}

// adapter converts a registration to its wire shape. A launcher is
// available when every TUI it declares resolves, and carries the install
// hint whether or not the binary is; an observer is available when
// connected.
func (s *Service) adapter(adapter Adapter) api.AgentAdapter {
	out := api.AgentAdapter{ID: adapter.ID, Name: adapter.Name, Agents: make([]string, 0, len(adapter.Agents))}
	for _, spec := range adapter.Agents {
		out.Agents = append(out.Agents, spec.ID)
	}
	if adapter.Connection != nil {
		connection := adapter.Connection()
		out.Connection = &connection
		out.Available = connection.State == api.AdapterConnected
		return out
	}
	out.Available = true
	for _, spec := range adapter.Agents {
		if spec.TUI == nil {
			continue
		}
		out.InstallHint = spec.TUI.InstallHint()
		if !s.resolves(spec.TUI) {
			out.Available = false
		}
	}
	return out
}

func (s *Service) resolves(tui TUI) bool {
	_, err := s.lookPath(tui.Binary())
	return err == nil
}

// Launch resolves the agent to the first adapter that launches it,
// probes its binary, and creates the terminal that runs its TUI: the
// adapter supplies the command, the agent's display name is the default
// terminal name, and everything from the record on is the normal
// terminal create path — persistence, wrapper, verification window,
// status, and events all belong to the terminals domain.
func (s *Service) Launch(ctx context.Context, agentID, projectID, name string) (api.Terminal, error) {
	var launcher *AgentSpec
	known := false
	for _, adapter := range s.adapters {
		for i := range adapter.Agents {
			if adapter.Agents[i].ID != agentID {
				continue
			}
			known = true
			if adapter.Agents[i].TUI != nil {
				launcher = &adapter.Agents[i]
				break
			}
		}
		if launcher != nil {
			break
		}
	}
	if !known {
		return api.Terminal{}, ErrNotFound
	}
	if launcher == nil {
		return api.Terminal{}, fmt.Errorf("%w: no adapter can launch %s", ErrUnavailable, agentID)
	}
	return s.launch(ctx, *launcher, projectID, name, "", "")
}

// Resume is the launch's second form (ATC-282): the terminal runs the
// provider's exact resume of a dormant conversation, composed through the
// adapter that produced the thread with the thread's private identity. A
// thread produced by an adapter that cannot launch its agent — an
// external program's conversation — is refused: it opens only in that
// program. The session starts in the conversation's recorded working
// directory when it still exists, otherwise the project directory — a
// resumed conversation can have run from a subdirectory the user since
// removed. The threads domain calls this inside its open decision;
// everything else is the normal launch.
func (s *Service) Resume(ctx context.Context, req threads.ResumeRequest) (api.Terminal, error) {
	i, ok := s.index[req.Adapter]
	if !ok {
		return api.Terminal{}, fmt.Errorf("%w: adapter %q", ErrNotFound, req.Adapter)
	}
	adapter := s.adapters[i]
	var launcher *AgentSpec
	for j := range adapter.Agents {
		if adapter.Agents[j].ID == req.Agent && adapter.Agents[j].TUI != nil {
			launcher = &adapter.Agents[j]
		}
	}
	if launcher == nil {
		return api.Terminal{}, fmt.Errorf("%w: %s conversations open in %s, not in an ATC terminal", ErrUnavailable, adapter.Name, adapter.Name)
	}
	directory := req.Directory
	if directory != "" {
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			directory = ""
		}
	}
	return s.launch(ctx, *launcher, req.ProjectID, "", directory, req.ProviderID)
}

// launch is the shared composition: probe the binary and hand the
// terminals domain a create whose command the TUI composes once the id
// is minted.
func (s *Service) launch(ctx context.Context, spec AgentSpec, projectID, name, directory, resumeID string) (api.Terminal, error) {
	if _, err := s.lookPath(spec.TUI.Binary()); err != nil {
		return api.Terminal{}, fmt.Errorf("%w: command %q not found on the server's PATH; install with: %s",
			ErrUnavailable, spec.TUI.Binary(), spec.TUI.InstallHint())
	}
	if name == "" {
		name = spec.Name
	}
	launch := terminals.AgentLaunch{
		Agent:     spec.ID,
		Directory: directory,
		Compose: func(terminalID, directory string) (string, error) {
			return spec.TUI.Command(ctx, LaunchContext{
				TerminalID: terminalID, Directory: directory, ResumeConversationID: resumeID,
			})
		},
	}
	if preparer, ok := spec.TUI.(LaunchPreparer); ok {
		launch.Prepare = func(ctx context.Context, directory string) (func(), error) {
			abort, err := preparer.PrepareLaunch(ctx, LaunchContext{Directory: directory, ResumeConversationID: resumeID})
			if err != nil {
				// A provider that cannot be observed is unavailable for
				// launch — the same refusal as a missing binary.
				return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
			}
			return abort, nil
		}
	}
	return s.terminals.CreateForAgent(ctx, api.TerminalCreateParams{
		ProjectID: projectID,
		Name:      name,
	}, launch)
}
