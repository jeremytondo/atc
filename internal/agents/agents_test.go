package agents

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

type fakeTUI struct{ command, binary, hint string }

// Command echoes the launch context so tests can assert the composition
// forwarded the minted identity.
func (a fakeTUI) Command(_ context.Context, launch LaunchContext) (string, error) {
	return a.command + " --for " + launch.TerminalID, nil
}
func (a fakeTUI) Binary() string      { return a.binary }
func (a fakeTUI) InstallHint() string { return a.hint }

// fakeCreator records the create the launch composition hands the
// terminals domain, running the compose factory the way the real service
// does — after minting the terminal identity.
type fakeCreator struct {
	params  api.TerminalCreateParams
	agent   string
	command string
}

func (c *fakeCreator) CreateForAgent(_ context.Context, params api.TerminalCreateParams, agent string,
	compose func(terminalID string) (string, error)) (api.Terminal, error) {
	c.params, c.agent = params, agent
	command, err := compose("term-aaaaa")
	if err != nil {
		return api.Terminal{}, err
	}
	c.command = command
	return api.Terminal{ID: "term-aaaaa", Name: params.Name, Command: command, Agent: agent}, nil
}

func testEntries() []Entry {
	return []Entry{
		{ID: "alpha", Name: "Alpha", TUI: fakeTUI{command: "alpha --tui", binary: "alpha", hint: "install alpha"}},
		{ID: "beta", Name: "Beta", TUI: fakeTUI{command: "beta", binary: "beta-bin", hint: "install beta"}},
		// No adapters: no capabilities, launch refused.
		{ID: "gamma", Name: "Gamma"},
	}
}

func newTestService(t *testing.T, available ...string) (*Service, *fakeCreator) {
	t.Helper()
	creator := &fakeCreator{}
	service, err := NewService(Options{
		Entries:   testEntries(),
		Terminals: creator,
		LookPath: func(name string) (string, error) {
			if slices.Contains(available, name) {
				return "/bin/" + name, nil
			}
			return "", errors.New("executable file not found in $PATH")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return service, creator
}

func TestNewServiceRejectsDuplicateIDs(t *testing.T) {
	_, err := NewService(Options{
		Entries:   []Entry{{ID: "alpha"}, {ID: "alpha"}},
		Terminals: &fakeCreator{},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate agent id "alpha"`) {
		t.Errorf("NewService(duplicate) = %v, want the duplicate-id error", err)
	}
}

func TestListDerivesCapabilitiesInRegistrationOrder(t *testing.T) {
	service, _ := newTestService(t, "alpha")
	want := []api.Agent{
		{ID: "alpha", Name: "Alpha", Capabilities: []api.AgentCapability{
			{Capability: "tui", Available: true, InstallHint: "install alpha"},
		}},
		{ID: "beta", Name: "Beta", Capabilities: []api.AgentCapability{
			{Capability: "tui", Available: false, InstallHint: "install beta"},
		}},
		{ID: "gamma", Name: "Gamma", Capabilities: []api.AgentCapability{}},
	}
	if diff := cmp.Diff(want, service.List()); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}
}

func TestGetUnknownAgent(t *testing.T) {
	service, _ := newTestService(t)
	if _, err := service.Get("nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(unknown) = %v, want ErrNotFound", err)
	}
}

// Launch composes the create: the adapter's command, the entry's display
// name as the default, and the catalog id as the recorded agent label.
func TestLaunchComposesTheTerminalCreate(t *testing.T) {
	service, creator := newTestService(t, "alpha")
	terminal, err := service.Launch(context.Background(), "alpha", api.AgentLaunchParams{ProjectID: "proj-aaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	wantParams := api.TerminalCreateParams{ProjectID: "proj-aaaaa", Name: "Alpha"}
	if diff := cmp.Diff(wantParams, creator.params); diff != "" {
		t.Errorf("create params (-want +got):\n%s", diff)
	}
	// The composed command carries the per-launch context the terminals
	// domain fed the factory.
	if creator.command != "alpha --tui --for term-aaaaa" {
		t.Errorf("composed command = %q", creator.command)
	}
	if creator.agent != "alpha" || terminal.Agent != "alpha" {
		t.Errorf("agent label = %q on create, %q on terminal; want alpha", creator.agent, terminal.Agent)
	}

	// A caller-supplied name wins over the display-name default.
	if _, err := service.Launch(context.Background(), "alpha", api.AgentLaunchParams{ProjectID: "proj-aaaaa", Name: "pair session"}); err != nil {
		t.Fatal(err)
	}
	if creator.params.Name != "pair session" {
		t.Errorf("name = %q, want the caller's", creator.params.Name)
	}
}

func TestLaunchRefusals(t *testing.T) {
	service, creator := newTestService(t, "alpha")

	if _, err := service.Launch(context.Background(), "nonexistent", api.AgentLaunchParams{ProjectID: "proj-aaaaa"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Launch(unknown) = %v, want ErrNotFound", err)
	}

	// A missing binary refuses before any terminal exists, naming the
	// command and its install hint.
	_, err := service.Launch(context.Background(), "beta", api.AgentLaunchParams{ProjectID: "proj-aaaaa"})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Launch(missing binary) = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), `"beta-bin"`) || !strings.Contains(err.Error(), "install beta") {
		t.Errorf("refusal names neither command nor hint: %v", err)
	}

	if _, err := service.Launch(context.Background(), "gamma", api.AgentLaunchParams{ProjectID: "proj-aaaaa"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Launch(no tui adapter) = %v, want ErrUnavailable", err)
	}

	if creator.agent != "" || creator.params.ProjectID != "" {
		t.Errorf("a refused launch reached the terminals domain: %+v", creator)
	}
}
