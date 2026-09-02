package agents

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
)

type fakeTUI struct{ command, binary, hint string }

// Command echoes the launch context so tests can assert the composition
// forwarded the minted identity and, for a resume, the conversation.
func (a fakeTUI) Command(_ context.Context, launch LaunchContext) (string, error) {
	command := a.command + " --for " + launch.TerminalID
	if launch.ResumeConversationID != "" {
		command += " --resume " + launch.ResumeConversationID
	}
	return command, nil
}
func (a fakeTUI) Binary() string      { return a.binary }
func (a fakeTUI) InstallHint() string { return a.hint }

// fakeCreator records the create the launch composition hands the
// terminals domain, running the compose factory the way the real service
// does — after minting the terminal identity.
type fakeCreator struct {
	params    api.TerminalCreateParams
	agent     string
	directory string
	command   string
	// prepared records that the launch's Prepare hook ran; failCreate
	// makes the create fail right after it, exercising abort.
	prepared   bool
	failCreate bool
}

func (c *fakeCreator) CreateForAgent(ctx context.Context, params api.TerminalCreateParams, launch terminals.AgentLaunch) (api.Terminal, error) {
	c.params, c.agent, c.directory = params, launch.Agent, launch.Directory
	directory := launch.Directory
	if directory == "" {
		directory = "/projects/alpha"
	}
	abort := func() {}
	if launch.Prepare != nil {
		prepared, err := launch.Prepare(ctx, directory)
		if err != nil {
			return api.Terminal{}, err
		}
		c.prepared = true
		abort = prepared
	}
	if c.failCreate {
		abort()
		return api.Terminal{}, errors.New("create failed")
	}
	command, err := launch.Compose("term-aaaaa", directory)
	if err != nil {
		abort()
		return api.Terminal{}, err
	}
	c.command = command
	return api.Terminal{ID: "term-aaaaa", Name: params.Name, Command: command, Agent: launch.Agent}, nil
}

// fakePreparingTUI is a TUI with the optional prepare seam: it records
// the directory it was prepared for and whether the launch was aborted,
// and can refuse.
type fakePreparingTUI struct {
	fakeTUI
	prepareErr error
	prepared   string
	aborted    bool
}

func (a *fakePreparingTUI) PrepareLaunch(_ context.Context, launch LaunchContext) (func(), error) {
	if a.prepareErr != nil {
		return nil, a.prepareErr
	}
	a.prepared = launch.Directory
	return func() { a.aborted = true }, nil
}

var observedSince = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// testAdapters is the fixture catalog: two launchers, plus an observer of
// an external program that produces threads for alpha and for an agent
// nothing launches.
func testAdapters(connection api.AgentAdapterConnection) []Adapter {
	return []Adapter{
		{ID: "alpha", Name: "Alpha", Agents: []AgentSpec{
			{ID: "alpha", Name: "Alpha", TUI: fakeTUI{command: "alpha --tui", binary: "alpha", hint: "install alpha"}},
		}},
		{ID: "beta", Name: "Beta", Agents: []AgentSpec{
			{ID: "beta", Name: "Beta", TUI: fakeTUI{command: "beta", binary: "beta-bin", hint: "install beta"}},
		}},
		{ID: "watcher", Name: "Watcher", Agents: []AgentSpec{
			{ID: "alpha", Name: "Alpha (as Watcher names it)"},
			{ID: "gamma", Name: "Gamma"},
		}, Connection: func() api.AgentAdapterConnection { return connection }},
	}
}

func newTestService(t *testing.T, available ...string) (*Service, *fakeCreator) {
	t.Helper()
	creator := &fakeCreator{}
	service, err := NewService(Options{
		Adapters:  testAdapters(api.AgentAdapterConnection{State: api.AdapterConnected, Since: observedSince, Detail: "live"}),
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

func TestNewServiceRejectsDuplicates(t *testing.T) {
	_, err := NewService(Options{
		Adapters:  []Adapter{{ID: "alpha"}, {ID: "alpha"}},
		Terminals: &fakeCreator{},
	})
	if err == nil || !strings.Contains(err.Error(), `duplicate adapter id "alpha"`) {
		t.Errorf("NewService(duplicate adapter) = %v, want the duplicate-id error", err)
	}
	_, err = NewService(Options{
		Adapters:  []Adapter{{ID: "alpha", Agents: []AgentSpec{{ID: "x"}, {ID: "x"}}}},
		Terminals: &fakeCreator{},
	})
	if err == nil || !strings.Contains(err.Error(), `declares agent "x" twice`) {
		t.Errorf("NewService(duplicate agent) = %v, want the duplicate-agent error", err)
	}
	_, err = NewService(Options{
		Adapters: []Adapter{{ID: "hybrid", Agents: []AgentSpec{{ID: "x", TUI: fakeTUI{}}},
			Connection: func() api.AgentAdapterConnection { return api.AgentAdapterConnection{} }}},
		Terminals: &fakeCreator{},
	})
	if err == nil || !strings.Contains(err.Error(), "both launches") {
		t.Errorf("NewService(hybrid) = %v, want the hybrid error", err)
	}
}

// The agent catalog is derived: first declaration names the agent, every
// declarer lists as a provider, and availability means some declarer
// can launch it right now.
func TestListDerivesAgentsFromAdapters(t *testing.T) {
	service, _ := newTestService(t, "alpha")
	want := []api.Agent{
		{ID: "alpha", Name: "Alpha", Available: true, Adapters: []string{"alpha", "watcher"}},
		{ID: "beta", Name: "Beta", Available: false, Adapters: []string{"beta"}},
		{ID: "gamma", Name: "Gamma", Available: false, Adapters: []string{"watcher"}},
	}
	if diff := cmp.Diff(want, service.List()); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}
	got, err := service.Get("gamma")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want[2], got); diff != "" {
		t.Errorf("Get(gamma) mismatch (-want +got):\n%s", diff)
	}
	if _, err := service.Get("nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(unknown) = %v, want ErrNotFound", err)
	}
}

// Adapters report their own health: a launcher through its binary and
// install hint, an observer through its connection.
func TestAdaptersReportAvailability(t *testing.T) {
	service, _ := newTestService(t, "alpha")
	connection := api.AgentAdapterConnection{State: api.AdapterConnected, Since: observedSince, Detail: "live"}
	want := []api.AgentAdapter{
		{ID: "alpha", Name: "Alpha", Agents: []string{"alpha"}, Available: true, InstallHint: "install alpha"},
		{ID: "beta", Name: "Beta", Agents: []string{"beta"}, Available: false, InstallHint: "install beta"},
		{ID: "watcher", Name: "Watcher", Agents: []string{"alpha", "gamma"}, Available: true, Connection: &connection},
	}
	if diff := cmp.Diff(want, service.Adapters()); diff != "" {
		t.Errorf("Adapters() mismatch (-want +got):\n%s", diff)
	}
	got, err := service.Adapter("beta")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want[1], got); diff != "" {
		t.Errorf("Adapter(beta) mismatch (-want +got):\n%s", diff)
	}
	if _, err := service.Adapter("nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Adapter(unknown) = %v, want ErrNotFound", err)
	}

	// An observer that is not connected is unavailable, whatever the
	// reason.
	disconnected, err := NewService(Options{
		Adapters:  testAdapters(api.AgentAdapterConnection{State: api.AdapterUnavailable, Since: observedSince, Detail: "not installed"}),
		Terminals: &fakeCreator{},
		LookPath:  func(string) (string, error) { return "", errors.New("nope") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if adapter, _ := disconnected.Adapter("watcher"); adapter.Available || adapter.Connection.Detail != "not installed" {
		t.Errorf("disconnected watcher = %+v", adapter)
	}
}

// Launch composes the create: the TUI's command, the agent's display
// name as the default, and the agent id as the recorded label.
func TestLaunchComposesTheTerminalCreate(t *testing.T) {
	service, creator := newTestService(t, "alpha")
	terminal, err := service.Launch(context.Background(), "alpha", "proj-aaaaa", "")
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
	if _, err := service.Launch(context.Background(), "alpha", "proj-aaaaa", "pair session"); err != nil {
		t.Fatal(err)
	}
	if creator.params.Name != "pair session" {
		t.Errorf("name = %q, want the caller's", creator.params.Name)
	}
}

func TestLaunchRefusals(t *testing.T) {
	service, creator := newTestService(t, "alpha")

	if _, err := service.Launch(context.Background(), "nonexistent", "proj-aaaaa", ""); !errors.Is(err, ErrNotFound) {
		t.Errorf("Launch(unknown) = %v, want ErrNotFound", err)
	}

	// A missing binary refuses before any terminal exists, naming the
	// command and its install hint.
	_, err := service.Launch(context.Background(), "beta", "proj-aaaaa", "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Launch(missing binary) = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), `"beta-bin"`) || !strings.Contains(err.Error(), "install beta") {
		t.Errorf("refusal names neither command nor hint: %v", err)
	}

	// An agent only an observer declares is known but not launchable.
	if _, err := service.Launch(context.Background(), "gamma", "proj-aaaaa", ""); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Launch(no launcher) = %v, want ErrUnavailable", err)
	}

	if creator.agent != "" || creator.params.ProjectID != "" {
		t.Errorf("a refused launch reached the terminals domain: %+v", creator)
	}
}

// Resume is the launch's second form: the producing adapter composes the
// provider's exact resume from the thread's private identity, the
// terminal joins the thread's project, and the working directory is the
// conversation's recorded one — or the project's when that directory is
// gone.
func TestResumeComposesTheExactResume(t *testing.T) {
	service, creator := newTestService(t, "alpha")
	cwd := t.TempDir()
	request := threads.ResumeRequest{Adapter: "alpha", Agent: "alpha", ProviderID: "sess-1", ProjectID: "proj-aaaaa", Directory: cwd}
	terminal, err := service.Resume(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if creator.command != "alpha --tui --for term-aaaaa --resume sess-1" {
		t.Errorf("composed command = %q", creator.command)
	}
	wantParams := api.TerminalCreateParams{ProjectID: "proj-aaaaa", Name: "Alpha"}
	if diff := cmp.Diff(wantParams, creator.params); diff != "" {
		t.Errorf("create params (-want +got):\n%s", diff)
	}
	if creator.directory != cwd || creator.agent != "alpha" || terminal.Agent != "alpha" {
		t.Errorf("directory = %q, agent = %q; want %q, alpha", creator.directory, creator.agent, cwd)
	}

	// A recorded directory that no longer exists (or never was observed)
	// falls back to the project directory: the terminals domain decides
	// that when the override is empty.
	for _, directory := range []string{filepath.Join(cwd, "gone"), ""} {
		request.Directory = directory
		if _, err := service.Resume(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if creator.directory != "" {
			t.Errorf("directory for %q = %q, want the project fallback", directory, creator.directory)
		}
	}

	// The same refusals as launch, before anything is created — and a
	// thread an observer produced opens only in its own program.
	creator.agent = ""
	if _, err := service.Resume(context.Background(), threads.ResumeRequest{Adapter: "beta", Agent: "beta", ProjectID: "proj-aaaaa"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Resume(missing binary) = %v, want ErrUnavailable", err)
	}
	if _, err := service.Resume(context.Background(), threads.ResumeRequest{Adapter: "nonexistent", Agent: "alpha", ProjectID: "proj-aaaaa"}); !errors.Is(err, ErrNotFound) {
		t.Errorf("Resume(unknown adapter) = %v, want ErrNotFound", err)
	}
	_, err = service.Resume(context.Background(), threads.ResumeRequest{Adapter: "watcher", Agent: "alpha", ProjectID: "proj-aaaaa"})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "open in Watcher") {
		t.Errorf("Resume(observer's thread) = %v, want ErrUnavailable naming the program", err)
	}
	if creator.agent != "" {
		t.Errorf("a refused resume reached the terminals domain: %+v", creator)
	}
}

// The prepare seam runs before the create with the resolved directory; a
// refusal there is an unavailable agent — no command is ever composed —
// and a create that fails after it aborts the preparation.
func TestLaunchPreparesBeforeTheCreate(t *testing.T) {
	tui := &fakePreparingTUI{fakeTUI: fakeTUI{command: "delta", binary: "delta", hint: "install delta"}}
	creator := &fakeCreator{}
	service, err := NewService(Options{
		Adapters:  []Adapter{{ID: "delta", Name: "Delta", Agents: []AgentSpec{{ID: "delta", Name: "Delta", TUI: tui}}}},
		Terminals: creator,
		LookPath:  func(string) (string, error) { return "/bin/delta", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.Launch(ctx, "delta", "proj-aaaaa", ""); err != nil {
		t.Fatal(err)
	}
	if tui.prepared != "/projects/alpha" || !creator.prepared || tui.aborted {
		t.Errorf("prepared = %q (creator saw %v), aborted = %v", tui.prepared, creator.prepared, tui.aborted)
	}

	creator.failCreate = true
	if _, err := service.Launch(ctx, "delta", "proj-aaaaa", ""); err == nil {
		t.Fatal("launch succeeded though the create failed")
	}
	if !tui.aborted {
		t.Error("a failed create did not abort the preparation")
	}

	tui.prepareErr = errors.New("no server answering")
	creator.command = ""
	_, err = service.Launch(ctx, "delta", "proj-aaaaa", "")
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "no server answering") {
		t.Fatalf("launch with a failed preparation = %v, want ErrUnavailable carrying the cause", err)
	}
	if creator.command != "" {
		t.Error("a command was composed for a launch whose preparation failed")
	}
}
