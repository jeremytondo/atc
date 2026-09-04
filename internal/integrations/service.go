package integrations

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"slices"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
)

// The typed refusals. Each maps to one Problem code in the API layer; no
// terminal record exists after any of them.
var (
	// ErrNotFound reports an Integration id with no catalog entry.
	ErrNotFound = errors.New("integration not found")
	// ErrAppNotFound reports a qualified App id with no catalog entry.
	ErrAppNotFound = errors.New("app not found")
	// ErrAppNotTerminal refuses launching an App that does not run in an
	// ATC terminal (a web or desktop handoff App).
	ErrAppNotTerminal = errors.New("app does not run in a terminal")
	// ErrUnavailable refuses a launch the App cannot perform right now: the
	// Integration's executable does not resolve on the server's PATH (the
	// message names the missing command and its install hint), or the
	// App's launch preparation failed. The probe is a best-effort early
	// gate — the daemon's PATH and the login shell's can disagree — so past
	// it, launch failures surface as exit evidence through the normal
	// terminal status machinery, never a separate error path.
	ErrUnavailable = errors.New("app unavailable")
	// ErrNotResumable refuses resuming a thread with no terminal-capable
	// App provenance: it was not started in an ATC terminal App, or its
	// App is a handoff, so it opens only through its links.
	ErrNotResumable = errors.New("thread cannot be resumed in a terminal")
	// ErrOriginUnavailable refuses resuming a thread whose recorded App
	// the catalog no longer has, or that lies outside the thread's own
	// Integration — provenance ATC cannot act on.
	ErrOriginUnavailable = errors.New("thread's app is no longer available")
	// ErrThreadCreationUnsupported refuses creating a thread in an
	// Integration that has no creation seam.
	ErrThreadCreationUnsupported = errors.New("integration does not support thread creation")
	// ErrAgentNotFound refuses an agent id the Integration does not list.
	ErrAgentNotFound = errors.New("agent not found")
	// ErrNotConnected refuses a thread create while the Integration's
	// connection to its program is not up; the message names the state and
	// its detail.
	ErrNotConnected = errors.New("integration not connected")
	// ErrProjectNotRegistered refuses a thread create in a directory the
	// Integration's program does not know as a project; ATC never
	// registers one there.
	ErrProjectNotRegistered = errors.New("project not registered")
	// ErrThreadCreationFailed reports the program refusing or failing a
	// dispatched creation; the message is the program's own.
	ErrThreadCreationFailed = errors.New("thread creation failed")
)

// Options wires a Service.
type Options struct {
	// Integrations is the compiled-in catalog, one registration per
	// built-in Integration, listed in registration order.
	Integrations []Integration
	// LookPath resolves a binary name on the server's PATH — the
	// availability probe's injectable seam, so tests control which binaries
	// exist. Nil defaults to exec.LookPath.
	LookPath func(name string) (string, error)
}

// Service is the read-only catalog plus the resolution of the actions
// Integrations perform: turn an App reference, or a thread's provenance,
// into the opaque launch input the terminals domain takes, and a thread
// create into its Integration's creation seam. Reads re-probe
// availability on every call — no cache, no version probing.
type Service struct {
	integrations []Integration
	index        map[string]int
	lookPath     func(name string) (string, error)
}

// NewService assembles the catalog. A duplicate Integration id, an agent
// or App declared twice by one Integration, an Integration or App id that
// is empty or contains the qualifier separator, or a thread-creation seam
// declared without its capability (or the reverse) is an error — the
// composition root fails the boot.
func NewService(opts Options) (*Service, error) {
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	service := &Service{
		integrations: make([]Integration, 0, len(opts.Integrations)),
		index:        make(map[string]int, len(opts.Integrations)),
		lookPath:     opts.LookPath,
	}
	for _, integration := range opts.Integrations {
		if !validSegment(integration.ID) {
			return nil, fmt.Errorf("integration id %q: ids are one non-empty segment without %q", integration.ID, "/")
		}
		if _, taken := service.index[integration.ID]; taken {
			return nil, fmt.Errorf("duplicate integration id %q", integration.ID)
		}
		if (integration.PrepareThread != nil) != slices.Contains(integration.Capabilities, api.CapabilityThreadCreation) {
			return nil, fmt.Errorf("integration %q: the %s capability and the creation seam must be declared together", integration.ID, api.CapabilityThreadCreation)
		}
		agents := make(map[string]bool, len(integration.Agents))
		for _, agent := range integration.Agents {
			if agents[agent.ID] {
				return nil, fmt.Errorf("integration %q declares agent %q twice", integration.ID, agent.ID)
			}
			agents[agent.ID] = true
		}
		apps := make(map[string]bool, len(integration.Apps))
		for _, app := range integration.Apps {
			if !validSegment(app.ID) {
				return nil, fmt.Errorf("integration %q declares app %q: ids are one non-empty segment without %q", integration.ID, app.ID, "/")
			}
			if apps[app.ID] {
				return nil, fmt.Errorf("integration %q declares app %q twice", integration.ID, app.ID)
			}
			apps[app.ID] = true
		}
		service.index[integration.ID] = len(service.integrations)
		service.integrations = append(service.integrations, frozen(integration))
	}
	return service, nil
}

func validSegment(id string) bool {
	return id != "" && !strings.Contains(id, "/")
}

// frozen deep-copies a registration's descriptor data, so no caller-held
// slice can mutate the catalog after the checks above; the stateful
// references (an App's terminal implementation, a connection reporter)
// are shared by design.
func frozen(integration Integration) Integration {
	integration.Capabilities = slices.Clone(integration.Capabilities)
	integration.Agents = slices.Clone(integration.Agents)
	integration.Apps = slices.Clone(integration.Apps)
	for i := range integration.Apps {
		integration.Apps[i].Agents = slices.Clone(integration.Apps[i].Agents)
	}
	if integration.Executable != nil {
		executable := *integration.Executable
		integration.Executable = &executable
	}
	return integration
}

// List returns every Integration in registration order, availability
// freshly probed.
func (s *Service) List() []api.Integration {
	list := make([]api.Integration, 0, len(s.integrations))
	for _, integration := range s.integrations {
		list = append(list, s.integration(integration))
	}
	return list
}

// Get returns one Integration, availability freshly probed.
func (s *Service) Get(id string) (api.Integration, error) {
	i, ok := s.index[id]
	if !ok {
		return api.Integration{}, ErrNotFound
	}
	return s.integration(s.integrations[i]), nil
}

// availability is the one rule for whether an Integration can act right
// now, for the catalog and for launches alike: a connection-backed
// Integration when connected, an executable-backed one when its binary
// resolves on the server's PATH, and one with neither always. It returns
// the connection it consulted, if any, and the refusal a launch would
// carry.
func (s *Service) availability(integration Integration) (available bool, connection *api.IntegrationConnection, reason error) {
	if integration.Connection != nil {
		c := integration.Connection()
		if c.State != api.IntegrationConnected {
			return false, &c, fmt.Errorf("%w: %s is %s: %s", ErrUnavailable, integration.Name, c.State, c.Detail)
		}
		return true, &c, nil
	}
	if integration.Executable != nil && !s.resolves(integration.Executable) {
		return false, nil, fmt.Errorf("%w: command %q not found on the server's PATH; install with: %s",
			ErrUnavailable, integration.Executable.Binary, integration.Executable.InstallHint)
	}
	return true, nil, nil
}

// integration converts a registration to its wire shape. It carries the
// install hint whenever an executable backs it. Terminal Apps inherit an
// executable-backed Integration's availability; handoff Apps make no
// claim.
func (s *Service) integration(integration Integration) api.Integration {
	out := api.Integration{
		ID:           integration.ID,
		Name:         integration.Name,
		Capabilities: slices.Clone(integration.Capabilities),
		Agents:       slices.Clone(integration.Agents),
		Apps:         make([]api.App, 0, len(integration.Apps)),
	}
	if out.Capabilities == nil {
		out.Capabilities = []api.IntegrationCapability{}
	}
	if out.Agents == nil {
		out.Agents = []api.IntegrationAgent{}
	}
	if integration.Executable != nil {
		out.InstallHint = integration.Executable.InstallHint
	}
	out.Available, out.Connection, _ = s.availability(integration)
	var executableResolves *bool
	if integration.Executable != nil {
		resolves := s.resolves(integration.Executable)
		executableResolves = &resolves
	}
	for _, app := range integration.Apps {
		wire := api.App{
			ID:           QualifiedAppID(integration.ID, app.ID),
			Name:         app.Name,
			Agents:       slices.Clone(app.Agents),
			Interactions: []api.AppInteraction{},
		}
		if wire.Agents == nil {
			wire.Agents = []string{}
		}
		if app.Terminal != nil {
			wire.Interactions = append(wire.Interactions, api.AppTerminalStart, api.AppTerminalResume)
			// Evidence only: an executable-backed terminal App is available
			// when the binary resolves; without an executable there is
			// nothing to probe and the App claims nothing.
			if executableResolves != nil {
				available := *executableResolves
				wire.Available = &available
			}
		}
		if app.Handoff {
			wire.Interactions = append(wire.Interactions, api.AppHandoff)
		}
		out.Apps = append(out.Apps, wire)
	}
	return out
}

func (s *Service) resolves(executable *Executable) bool {
	_, err := s.lookPath(executable.Binary)
	return err == nil
}

func (s *Service) app(qualified string) (Integration, App, error) {
	integrationID, appID, ok := strings.Cut(qualified, "/")
	if !ok {
		return Integration{}, App{}, fmt.Errorf("%w: %q is not integration/app", ErrAppNotFound, qualified)
	}
	i, known := s.index[integrationID]
	if !known {
		return Integration{}, App{}, fmt.Errorf("%w: %q", ErrAppNotFound, qualified)
	}
	integration := s.integrations[i]
	for _, app := range integration.Apps {
		if app.ID == appID {
			return integration, app, nil
		}
	}
	return Integration{}, App{}, fmt.Errorf("%w: %q", ErrAppNotFound, qualified)
}

// ResolveLaunch turns a qualified App id into the launch input a
// terminal create takes: the App must run in a terminal and its
// Integration must be available now. Every refusal lands before a record
// exists; no thread exists until the Integration observes one.
func (s *Service) ResolveLaunch(ctx context.Context, appID string) (terminals.AppLaunch, error) {
	integration, app, err := s.app(appID)
	if err != nil {
		return terminals.AppLaunch{}, err
	}
	if app.Terminal == nil {
		return terminals.AppLaunch{}, fmt.Errorf("%w: %s", ErrAppNotTerminal, appID)
	}
	return s.launch(ctx, integration, app, "")
}

// ResolveResume turns a thread's provenance into the launch input for the
// provider's exact resume (ATC-282), composed through the App that
// produced the thread with its private identity. A thread with no App
// provenance, or whose App is a handoff, is refused as not resumable: it
// opens only in its own program. An App the catalog no longer has, or one
// outside the thread's own Integration, is refused as unavailable origin.
func (s *Service) ResolveResume(ctx context.Context, req threads.ResumeRequest) (terminals.AppLaunch, error) {
	if req.AppID == "" {
		return terminals.AppLaunch{}, fmt.Errorf("%w: the thread was not started in an ATC terminal", ErrNotResumable)
	}
	integration, app, err := s.app(req.AppID)
	if err != nil {
		// Not wrapped: the outcome is the origin's absence, one category,
		// whatever the lookup's own reason.
		return terminals.AppLaunch{}, fmt.Errorf("%w: %s", ErrOriginUnavailable, req.AppID)
	}
	if integration.ID != req.IntegrationID {
		// Provenance is Integration-scoped: an App outside the thread's
		// origin Integration cannot have produced it.
		return terminals.AppLaunch{}, fmt.Errorf("%w: app %s does not belong to integration %s", ErrOriginUnavailable, req.AppID, req.IntegrationID)
	}
	if app.Terminal == nil {
		return terminals.AppLaunch{}, fmt.Errorf("%w: %s conversations open in %s, not in an ATC terminal", ErrNotResumable, integration.Name, integration.Name)
	}
	return s.launch(ctx, integration, app, req.ProviderID)
}

// ResolveThreadCreation routes a thread create to its Integration's
// creation seam (ATC-289): the Integration must exist, implement
// creation, and list the agent. The program's live state is not consulted
// here — the seam gates on it.
func (s *Service) ResolveThreadCreation(integrationID, agentID string) (func(context.Context, ThreadCreation) (PreparedThread, error), error) {
	i, ok := s.index[integrationID]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrNotFound, integrationID)
	}
	integration := s.integrations[i]
	if integration.PrepareThread == nil {
		return nil, fmt.Errorf("%w: %s", ErrThreadCreationUnsupported, integration.Name)
	}
	if !slices.ContainsFunc(integration.Agents, func(agent api.IntegrationAgent) bool { return agent.ID == agentID }) {
		return nil, fmt.Errorf("%w: %s lists no agent %q", ErrAgentNotFound, integration.Name, agentID)
	}
	return integration.PrepareThread, nil
}

// launch composes the launch input: the App's command once the id is
// minted — under the terminals commit lock, so it must be quick — and the
// App's optional preparation before it. The context is the request's;
// the closures run within it.
func (s *Service) launch(ctx context.Context, integration Integration, app App, resumeID string) (terminals.AppLaunch, error) {
	if _, _, err := s.availability(integration); err != nil {
		return terminals.AppLaunch{}, err
	}
	launch := terminals.AppLaunch{
		AppID: QualifiedAppID(integration.ID, app.ID),
		Compose: func(terminalID, directory string) (string, error) {
			return app.Terminal.Command(ctx, LaunchContext{
				TerminalID: terminalID, Directory: directory, ResumeConversationID: resumeID,
			})
		},
	}
	if preparer, ok := app.Terminal.(LaunchPreparer); ok {
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
	return launch, nil
}
