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

func (a fakeTUI) Command() string     { return a.command }
func (a fakeTUI) Binary() string      { return a.binary }
func (a fakeTUI) InstallHint() string { return a.hint }

// fakeCreator records the create the launch composition hands the
// terminals domain.
type fakeCreator struct {
	params api.TerminalCreateParams
	agent  string
}

func (c *fakeCreator) CreateForAgent(_ context.Context, params api.TerminalCreateParams, agent string) (api.Terminal, error) {
	c.params, c.agent = params, agent
	return api.Terminal{ID: "term-aaaaa", Name: params.Name, Command: params.Command, Agent: agent}, nil
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
	catalog, err := NewCatalog(testEntries()...)
	if err != nil {
		t.Fatal(err)
	}
	creator := &fakeCreator{}
	return NewService(Options{
		Catalog:   catalog,
		Terminals: creator,
		LookPath: func(name string) (string, error) {
			if slices.Contains(available, name) {
				return "/bin/" + name, nil
			}
			return "", errors.New("executable file not found in $PATH")
		},
	}), creator
}

func TestNewCatalogRejectsDuplicateIDs(t *testing.T) {
	_, err := NewCatalog(Entry{ID: "alpha"}, Entry{ID: "alpha"})
	if err == nil || !strings.Contains(err.Error(), `duplicate agent id "alpha"`) {
		t.Errorf("NewCatalog(duplicate) = %v, want the duplicate-id error", err)
	}
}

// List is registration order with capabilities derived from the adapters
// each entry registers, availability probed per capability.
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
	wantParams := api.TerminalCreateParams{ProjectID: "proj-aaaaa", Name: "Alpha", Command: "alpha --tui"}
	if diff := cmp.Diff(wantParams, creator.params); diff != "" {
		t.Errorf("create params (-want +got):\n%s", diff)
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
