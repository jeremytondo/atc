package integrations

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

type fakeTerminalApp struct{ command string }

// Command echoes the launch context so tests can assert the composition
// forwarded the minted identity and, for a resume, the conversation.
func (a fakeTerminalApp) Command(_ context.Context, launch LaunchContext) (string, error) {
	command := a.command + " --for " + launch.TerminalID
	if launch.ResumeConversationID != "" {
		command += " --resume " + launch.ResumeConversationID
	}
	return command, nil
}

// fakeCreator records the create the launch composition hands the
// terminals domain, running the compose factory the way the real service
// does — after minting the terminal identity.
type fakeCreator struct {
	params    api.TerminalCreateParams
	appID     string
	directory string
	command   string
	// prepared records that the launch's Prepare hook ran; failCreate
	// makes the create fail right after it, exercising abort.
	prepared   bool
	failCreate bool
}

func (c *fakeCreator) CreateForApp(ctx context.Context, params api.TerminalCreateParams, launch terminals.AppLaunch) (api.Terminal, error) {
	c.params, c.appID, c.directory = params, launch.AppID, launch.Directory
	directory := launch.Directory
	if directory == "" {
		directory = "/spaces/default"
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
	return api.Terminal{ID: "term-aaaaa", Name: params.Name, AppID: launch.AppID}, nil
}

// fakePreparingApp is a terminal App with the optional prepare seam: it
// records the directory it was prepared for and whether the launch was
// aborted, and can refuse.
type fakePreparingApp struct {
	fakeTerminalApp
	prepareErr error
	prepared   string
	aborted    bool
}

func (a *fakePreparingApp) PrepareLaunch(_ context.Context, launch LaunchContext) (func(), error) {
	if a.prepareErr != nil {
		return nil, a.prepareErr
	}
	a.prepared = launch.Directory
	return func() { a.aborted = true }, nil
}

var observedSince = time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

// testIntegrations is the fixture catalog: two executable-backed
// Integrations with one terminal App each; a connection-backed one that
// observes an external program, exposes several agents (one sharing
// alpha's id — implying nothing) and two handoff Apps; and an
// infrastructure Integration with neither Apps nor agents.
func testIntegrations(connection api.IntegrationConnection) []Integration {
	return []Integration{
		{ID: "alpha", Name: "Alpha", Capabilities: []api.IntegrationCapability{api.CapabilityThreadObservation},
			Agents:     []api.IntegrationAgent{{ID: "alpha", Name: "Alpha"}},
			Apps:       []App{{ID: "tui", Name: "Alpha", Agents: []string{"alpha"}, Terminal: fakeTerminalApp{command: "alpha --tui"}}},
			Executable: &Executable{Binary: "alpha", InstallHint: "install alpha"}},
		{ID: "beta", Name: "Beta",
			Apps:       []App{{ID: "tui", Name: "Beta", Terminal: fakeTerminalApp{command: "beta"}}},
			Executable: &Executable{Binary: "beta-bin", InstallHint: "install beta"}},
		{ID: "watcher", Name: "Watcher", Capabilities: []api.IntegrationCapability{api.CapabilityThreadObservation},
			Agents: []api.IntegrationAgent{{ID: "alpha", Name: "Alpha (as Watcher names it)"}, {ID: "gamma", Name: "Gamma"}},
			Apps: []App{
				{ID: "web", Name: "Watcher (web)", Agents: []string{"alpha", "gamma"}, Handoff: true},
				{ID: "desktop", Name: "Watcher (desktop)", Handoff: true},
			},
			Connection: func() api.IntegrationConnection { return connection }},
		{ID: "mux", Name: "Mux", Capabilities: []api.IntegrationCapability{api.CapabilityTerminalDriver},
			Executable: &Executable{Binary: "mux", InstallHint: "install mux"}},
	}
}

func newTestService(t *testing.T, available ...string) (*Service, *fakeCreator) {
	t.Helper()
	creator := &fakeCreator{}
	service, err := NewService(Options{
		Integrations: testIntegrations(api.IntegrationConnection{State: api.IntegrationConnected, Since: observedSince, Detail: "live"}),
		Terminals:    creator,
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
	cases := map[string]struct {
		integrations []Integration
		want         string
	}{
		"integration id": {[]Integration{{ID: "alpha"}, {ID: "alpha"}}, `duplicate integration id "alpha"`},
		"agent id":       {[]Integration{{ID: "alpha", Agents: []api.IntegrationAgent{{ID: "x"}, {ID: "x"}}}}, `declares agent "x" twice`},
		"app id":         {[]Integration{{ID: "alpha", Apps: []App{{ID: "tui"}, {ID: "tui"}}}}, `declares app "tui" twice`},
		"qualified app":  {[]Integration{{ID: "alpha", Apps: []App{{ID: "alpha/tui"}}}}, `ids are one non-empty segment`},
		"empty app":      {[]Integration{{ID: "alpha", Apps: []App{{ID: ""}}}}, `ids are one non-empty segment`},
		"qualified id":   {[]Integration{{ID: "alpha/beta"}}, `ids are one non-empty segment`},
		"empty id":       {[]Integration{{ID: ""}}, `ids are one non-empty segment`},
	}
	for name, tc := range cases {
		_, err := NewService(Options{Integrations: tc.integrations, Terminals: &fakeCreator{}})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: NewService = %v, want %q", name, err, tc.want)
		}
	}
	// The same agent id under two Integrations is fine: ids are scoped.
	if _, err := NewService(Options{Integrations: testIntegrations(api.IntegrationConnection{}), Terminals: &fakeCreator{}}); err != nil {
		t.Errorf("NewService(fixture) = %v", err)
	}
}

// The catalog is the service's own: mutating the registration slices
// after construction changes nothing.
func TestCatalogIsCopiedFromRegistrations(t *testing.T) {
	registrations := testIntegrations(api.IntegrationConnection{State: api.IntegrationConnected})
	service, err := NewService(Options{Integrations: registrations, Terminals: &fakeCreator{}, LookPath: func(string) (string, error) { return "", errors.New("no") }})
	if err != nil {
		t.Fatal(err)
	}
	registrations[0].Apps[0].ID = "mutated"
	registrations[0].Apps[0].Agents[0] = "mutated"
	registrations[0].Agents[0].ID = "mutated"
	registrations[0].Capabilities[0] = "mutated"
	registrations[0].Executable.InstallHint = "mutated"
	registrations[0].ID = "mutated"
	got := service.List()[0]
	if got.ID != "alpha" || got.Apps[0].ID != "alpha/tui" || got.Apps[0].Agents[0] != "alpha" || got.Agents[0].ID != "alpha" ||
		got.Capabilities[0] != api.CapabilityThreadObservation || got.InstallHint != "install alpha" {
		t.Errorf("first integration = %+v after mutating the registration; want alpha untouched", got)
	}
}

// Every Integration reports its own health: executable-backed ones
// through their binary (their terminal Apps with it), a connection-backed
// one through its connection, with handoff Apps claiming nothing.
func TestListAndGetReportAvailability(t *testing.T) {
	service, _ := newTestService(t, "alpha", "mux")
	connection := api.IntegrationConnection{State: api.IntegrationConnected, Since: observedSince, Detail: "live"}
	yes, no := true, false
	terminal := []api.AppInteraction{api.AppTerminalStart, api.AppTerminalResume}
	handoff := []api.AppInteraction{api.AppHandoff}
	want := []api.Integration{
		{ID: "alpha", Name: "Alpha", Capabilities: []api.IntegrationCapability{api.CapabilityThreadObservation},
			Agents:    []api.IntegrationAgent{{ID: "alpha", Name: "Alpha"}},
			Apps:      []api.App{{ID: "alpha/tui", Name: "Alpha", Agents: []string{"alpha"}, Interactions: terminal, Available: &yes}},
			Available: true, InstallHint: "install alpha"},
		{ID: "beta", Name: "Beta", Capabilities: []api.IntegrationCapability{}, Agents: []api.IntegrationAgent{},
			Apps:      []api.App{{ID: "beta/tui", Name: "Beta", Agents: []string{}, Interactions: terminal, Available: &no}},
			Available: false, InstallHint: "install beta"},
		{ID: "watcher", Name: "Watcher", Capabilities: []api.IntegrationCapability{api.CapabilityThreadObservation},
			Agents: []api.IntegrationAgent{{ID: "alpha", Name: "Alpha (as Watcher names it)"}, {ID: "gamma", Name: "Gamma"}},
			Apps: []api.App{
				{ID: "watcher/web", Name: "Watcher (web)", Agents: []string{"alpha", "gamma"}, Interactions: handoff},
				{ID: "watcher/desktop", Name: "Watcher (desktop)", Agents: []string{}, Interactions: handoff},
			},
			Available: true, Connection: &connection},
		{ID: "mux", Name: "Mux", Capabilities: []api.IntegrationCapability{api.CapabilityTerminalDriver},
			Agents: []api.IntegrationAgent{}, Apps: []api.App{}, Available: true, InstallHint: "install mux"},
	}
	if diff := cmp.Diff(want, service.List()); diff != "" {
		t.Errorf("List() mismatch (-want +got):\n%s", diff)
	}
	got, err := service.Get("beta")
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want[1], got); diff != "" {
		t.Errorf("Get(beta) mismatch (-want +got):\n%s", diff)
	}
	if _, err := service.Get("nonexistent"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get(unknown) = %v, want ErrNotFound", err)
	}

	// A connection-backed Integration that is not connected is
	// unavailable, whatever the reason.
	disconnected, err := NewService(Options{
		Integrations: testIntegrations(api.IntegrationConnection{State: api.IntegrationUnavailable, Since: observedSince, Detail: "not installed"}),
		Terminals:    &fakeCreator{},
		LookPath:     func(string) (string, error) { return "", errors.New("nope") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if watcher, _ := disconnected.Get("watcher"); watcher.Available || watcher.Connection.Detail != "not installed" {
		t.Errorf("disconnected watcher = %+v", watcher)
	}
}

// Launch composes the create: the App's command, the request's placement,
// and the qualified App id as the recorded intent.
func TestLaunchComposesTheTerminalCreate(t *testing.T) {
	service, creator := newTestService(t, "alpha")
	terminal, err := service.Launch(context.Background(), "alpha/tui", api.TerminalCreateParams{AppID: "alpha/tui", SpaceID: "spce-aaaaa"})
	if err != nil {
		t.Fatal(err)
	}
	// Placement passes through untouched; the App selectors stay the
	// catalog's — the terminals domain sees neither.
	wantParams := api.TerminalCreateParams{SpaceID: "spce-aaaaa"}
	if diff := cmp.Diff(wantParams, creator.params); diff != "" {
		t.Errorf("create params (-want +got):\n%s", diff)
	}
	// The composed command carries the per-launch context the terminals
	// domain fed the factory.
	if creator.command != "alpha --tui --for term-aaaaa" {
		t.Errorf("composed command = %q", creator.command)
	}
	if creator.appID != "alpha/tui" || terminal.AppID != "alpha/tui" {
		t.Errorf("app = %q on create, %q on terminal; want alpha/tui", creator.appID, terminal.AppID)
	}

	if _, err := service.Launch(context.Background(), "alpha/tui", api.TerminalCreateParams{Name: "pair session", Directory: "/work"}); err != nil {
		t.Fatal(err)
	}
	if creator.params.Name != "pair session" || creator.params.Directory != "/work" {
		t.Errorf("params = %+v, want the caller's name and directory", creator.params)
	}
}

func TestLaunchRefusals(t *testing.T) {
	service, creator := newTestService(t, "alpha")
	ctx := context.Background()

	for _, appID := range []string{"nonexistent/tui", "alpha/desktop", "alpha", "", "alpha/tui/extra"} {
		if _, err := service.Launch(ctx, appID, api.TerminalCreateParams{}); !errors.Is(err, ErrAppNotFound) {
			t.Errorf("Launch(%q) = %v, want ErrAppNotFound", appID, err)
		}
	}

	// A missing binary refuses before any terminal exists, naming the
	// command and its install hint.
	_, err := service.Launch(ctx, "beta/tui", api.TerminalCreateParams{})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Launch(missing binary) = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), `"beta-bin"`) || !strings.Contains(err.Error(), "install beta") {
		t.Errorf("refusal names neither command nor hint: %v", err)
	}

	// A handoff App does not run in a terminal.
	if _, err := service.Launch(ctx, "watcher/web", api.TerminalCreateParams{}); !errors.Is(err, ErrAppNotTerminal) {
		t.Errorf("Launch(handoff app) = %v, want ErrAppNotTerminal", err)
	}

	if creator.appID != "" {
		t.Errorf("a refused launch reached the terminals domain: %+v", creator)
	}
}

// Resume is the launch's second form: the thread's App composes the
// provider's exact resume from the thread's private identity, the
// terminal lands in the Default space, and the working directory is the
// conversation's recorded one — or the space's when that directory is
// gone.
func TestResumeComposesTheExactResume(t *testing.T) {
	service, creator := newTestService(t, "alpha")
	cwd := t.TempDir()
	request := threads.ResumeRequest{IntegrationID: "alpha", AppID: "alpha/tui", ProviderID: "sess-1", Directory: cwd}
	terminal, err := service.Resume(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if creator.command != "alpha --tui --for term-aaaaa --resume sess-1" {
		t.Errorf("composed command = %q", creator.command)
	}
	if diff := cmp.Diff(api.TerminalCreateParams{}, creator.params); diff != "" {
		t.Errorf("create params (-want +got):\n%s", diff)
	}
	if creator.directory != cwd || creator.appID != "alpha/tui" || terminal.AppID != "alpha/tui" {
		t.Errorf("directory = %q, app = %q; want %q, alpha/tui", creator.directory, creator.appID, cwd)
	}

	// A recorded directory that no longer exists (or never was observed)
	// falls back to the space's directory: the terminals domain decides
	// that when the override is empty.
	for _, directory := range []string{filepath.Join(cwd, "gone"), ""} {
		request.Directory = directory
		if _, err := service.Resume(context.Background(), request); err != nil {
			t.Fatal(err)
		}
		if creator.directory != "" {
			t.Errorf("directory for %q = %q, want the space fallback", directory, creator.directory)
		}
	}
}

// Resume refusals: the same executable gate as launch, and a thread
// without terminal-capable App provenance — no App, an App outside its
// origin Integration, an App the catalog no longer has, or a handoff App
// — opens only in its own program. Nothing is created.
func TestResumeRefusals(t *testing.T) {
	service, creator := newTestService(t, "alpha")
	ctx := context.Background()
	if _, err := service.Resume(ctx, threads.ResumeRequest{IntegrationID: "beta", AppID: "beta/tui"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("Resume(missing binary) = %v, want ErrUnavailable", err)
	}
	cases := map[string]threads.ResumeRequest{
		"no app":              {IntegrationID: "watcher", ProviderID: "t1"},
		"foreign app":         {IntegrationID: "watcher", AppID: "alpha/tui"},
		"unknown app":         {IntegrationID: "alpha", AppID: "alpha/gone"},
		"unknown integration": {IntegrationID: "nonexistent", AppID: "nonexistent/tui"},
		"handoff app":         {IntegrationID: "watcher", AppID: "watcher/web"},
	}
	for name, request := range cases {
		if _, err := service.Resume(ctx, request); !errors.Is(err, ErrNotResumable) {
			t.Errorf("%s: Resume = %v, want ErrNotResumable", name, err)
		}
	}
	if _, err := service.Resume(ctx, cases["handoff app"]); !strings.Contains(err.Error(), "open in Watcher") {
		t.Errorf("handoff refusal does not name the program: %v", err)
	}
	if creator.appID != "" {
		t.Errorf("a refused resume reached the terminals domain: %+v", creator)
	}
}

// The prepare seam runs before the create with the resolved directory; a
// refusal there is an unavailable App — no command is ever composed —
// and a create that fails after it aborts the preparation.
func TestLaunchPreparesBeforeTheCreate(t *testing.T) {
	app := &fakePreparingApp{fakeTerminalApp: fakeTerminalApp{command: "delta"}}
	creator := &fakeCreator{}
	service, err := NewService(Options{
		Integrations: []Integration{{ID: "delta", Name: "Delta",
			Apps:       []App{{ID: "tui", Name: "Delta", Terminal: app}},
			Executable: &Executable{Binary: "delta", InstallHint: "install delta"}}},
		Terminals: creator,
		LookPath:  func(string) (string, error) { return "/bin/delta", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := service.Launch(ctx, "delta/tui", api.TerminalCreateParams{}); err != nil {
		t.Fatal(err)
	}
	if app.prepared != "/spaces/default" || !creator.prepared || app.aborted {
		t.Errorf("prepared = %q (creator saw %v), aborted = %v", app.prepared, creator.prepared, app.aborted)
	}

	creator.failCreate = true
	if _, err := service.Launch(ctx, "delta/tui", api.TerminalCreateParams{}); err == nil {
		t.Fatal("launch succeeded though the create failed")
	}
	if !app.aborted {
		t.Error("a failed create did not abort the preparation")
	}

	app.prepareErr = errors.New("no server answering")
	creator.failCreate = false
	creator.command = ""
	_, err = service.Launch(ctx, "delta/tui", api.TerminalCreateParams{})
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "no server answering") {
		t.Fatalf("launch with a failed preparation = %v, want ErrUnavailable carrying the cause", err)
	}
	if creator.command != "" {
		t.Error("a command was composed for a launch whose preparation failed")
	}
}

func TestQuoteAndCondenseTitle(t *testing.T) {
	if got := Quote("it's a path"); got != `'it'\''s a path'` {
		t.Errorf("Quote = %s", got)
	}
	long := strings.Repeat("word ", 20)
	if got := CondenseTitle("  fix   the\nbuild  "); got != "fix the build" {
		t.Errorf("CondenseTitle(short) = %q", got)
	}
	if got := CondenseTitle(long); len(got) > 50 || strings.HasSuffix(got, " ") {
		t.Errorf("CondenseTitle(long) = %q", got)
	}
}
