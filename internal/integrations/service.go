package integrations

import (
	"context"
	"errors"
	"fmt"
	"os"
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
	// App provenance: it was not started in an ATC terminal App, so it
	// opens only through its links.
	ErrNotResumable = errors.New("thread cannot be resumed in a terminal")
)

// TerminalCreator is the seam into the terminals domain: CreateForApp
// with the qualified App id, an optional working-directory override, and
// the App's hooks (terminals.Service in production). Prepare runs once
// the directory is resolved and before the commit lock; Compose runs
// once the terminal identity is minted — per-launch context like hook
// settings needs the id before the session starts. Both stay opaque
// functions there, so the terminals domain never learns App vocabulary.
type TerminalCreator interface {
	CreateForApp(ctx context.Context, params api.TerminalCreateParams, launch terminals.AppLaunch) (api.Terminal, error)
}

// Options wires a Service.
type Options struct {
	// Integrations is the compiled-in catalog, one registration per
	// built-in Integration, listed in registration order.
	Integrations []Integration
	Terminals    TerminalCreator
	// LookPath resolves a binary name on the server's PATH — the
	// availability probe's injectable seam, so tests control which binaries
	// exist. Nil defaults to exec.LookPath.
	LookPath func(name string) (string, error)
}

// Service is the read-only catalog plus the launch composition: resolve
// the App, probe its Integration's executable, compose the command, and
// hand the terminals domain a normal create. Reads re-probe availability
// on every call — no cache, no version probing.
type Service struct {
	integrations []Integration
	index        map[string]int
	terminals    TerminalCreator
	lookPath     func(name string) (string, error)
}

// NewService assembles the catalog. A duplicate Integration id, an agent
// or App declared twice by one Integration, or an Integration or App id
// that is empty or contains the qualifier separator is an error — the
// composition root fails the boot.
func NewService(opts Options) (*Service, error) {
	if opts.Terminals == nil {
		// A nil terminals service would panic on the first launch request;
		// fail at construction instead (the server.NewHandler Verify
		// precedent).
		panic("integrations.NewService: Terminals must not be nil")
	}
	if opts.LookPath == nil {
		opts.LookPath = exec.LookPath
	}
	service := &Service{
		integrations: make([]Integration, 0, len(opts.Integrations)),
		index:        make(map[string]int, len(opts.Integrations)),
		terminals:    opts.Terminals,
		lookPath:     opts.LookPath,
	}
	for _, integration := range opts.Integrations {
		if !validSegment(integration.ID) {
			return nil, fmt.Errorf("integration id %q: ids are one non-empty segment without %q", integration.ID, "/")
		}
		if _, taken := service.index[integration.ID]; taken {
			return nil, fmt.Errorf("duplicate integration id %q", integration.ID)
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

// integration converts a registration to its wire shape. A
// connection-backed Integration is available when connected; an
// executable-backed one when its binary resolves, and it carries the
// install hint either way. Terminal Apps inherit the executable's
// availability; handoff Apps make no claim.
func (s *Service) integration(integration Integration) api.Integration {
	out := api.Integration{
		ID:           integration.ID,
		Name:         integration.Name,
		Capabilities: slices.Clone(integration.Capabilities),
		Agents:       slices.Clone(integration.Agents),
		Apps:         make([]api.App, 0, len(integration.Apps)),
		Available:    true,
	}
	if out.Capabilities == nil {
		out.Capabilities = []api.IntegrationCapability{}
	}
	if out.Agents == nil {
		out.Agents = []api.IntegrationAgent{}
	}
	var executableResolves *bool
	if integration.Executable != nil {
		out.InstallHint = integration.Executable.InstallHint
		resolves := s.resolves(integration.Executable)
		executableResolves = &resolves
		out.Available = resolves
	}
	if integration.Connection != nil {
		connection := integration.Connection()
		out.Connection = &connection
		out.Available = connection.State == api.IntegrationConnected
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

// Launch creates the terminal that runs the App: the App composes the
// command, its display name is the default terminal name, and everything
// from the record on is the normal terminal create path — persistence,
// wrapper, verification window, status, and events all belong to the
// terminals domain. Every refusal lands before a record exists, and no
// thread exists until the Integration observes one.
func (s *Service) Launch(ctx context.Context, appID, projectID, name string) (api.Terminal, error) {
	integration, app, err := s.app(appID)
	if err != nil {
		return api.Terminal{}, err
	}
	if app.Terminal == nil {
		return api.Terminal{}, fmt.Errorf("%w: %s", ErrAppNotTerminal, appID)
	}
	return s.launch(ctx, integration, app, projectID, name, "", "")
}

// Resume is the launch's second form (ATC-282): the terminal runs the
// provider's exact resume of a dormant conversation, composed through the
// App that produced the thread with the thread's private identity. A
// thread with no App provenance, or whose App does not run in a
// terminal, is refused: it opens only in its own program. The session
// starts in the conversation's recorded working directory when it still
// exists, otherwise the project directory — a resumed conversation can
// have run from a subdirectory the user since removed. The threads
// domain calls this inside its open decision; everything else is the
// normal launch.
func (s *Service) Resume(ctx context.Context, req threads.ResumeRequest) (api.Terminal, error) {
	if req.AppID == "" {
		return api.Terminal{}, fmt.Errorf("%w: the thread was not started in an ATC terminal", ErrNotResumable)
	}
	integration, app, err := s.app(req.AppID)
	if err != nil {
		return api.Terminal{}, fmt.Errorf("%w: %w", ErrNotResumable, err)
	}
	if integration.ID != req.IntegrationID {
		// Provenance is Integration-scoped: an App outside the thread's
		// origin Integration cannot have produced it.
		return api.Terminal{}, fmt.Errorf("%w: app %s does not belong to integration %s", ErrNotResumable, req.AppID, req.IntegrationID)
	}
	if app.Terminal == nil {
		return api.Terminal{}, fmt.Errorf("%w: %s conversations open in %s, not in an ATC terminal", ErrNotResumable, integration.Name, integration.Name)
	}
	directory := req.Directory
	if directory != "" {
		if info, err := os.Stat(directory); err != nil || !info.IsDir() {
			directory = ""
		}
	}
	return s.launch(ctx, integration, app, req.ProjectID, "", directory, req.ProviderID)
}

// launch hands the terminals domain a create whose command the App
// composes once the id is minted — under the commit lock, so it must be
// quick — with the App's optional preparation run before it.
func (s *Service) launch(ctx context.Context, integration Integration, app App, projectID, name, directory, resumeID string) (api.Terminal, error) {
	if integration.Executable != nil && !s.resolves(integration.Executable) {
		return api.Terminal{}, fmt.Errorf("%w: command %q not found on the server's PATH; install with: %s",
			ErrUnavailable, integration.Executable.Binary, integration.Executable.InstallHint)
	}
	if name == "" {
		name = app.Name
	}
	launch := terminals.AppLaunch{
		AppID:     QualifiedAppID(integration.ID, app.ID),
		Directory: directory,
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
	return s.terminals.CreateForApp(ctx, api.TerminalCreateParams{
		ProjectID: projectID,
		Name:      name,
	}, launch)
}
