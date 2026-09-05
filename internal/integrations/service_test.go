package integrations

import (
	"context"
	"errors"
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

// run drives the launch input the way the terminals domain does: the
// optional preparation with the resolved directory, then the command
// composed once the identity is minted. abort undoes the preparation
// when the create fails afterwards.
func run(t *testing.T, launch terminals.AppLaunch, directory string) (command string, abort func()) {
	t.Helper()
	abort = func() {}
	if launch.Prepare != nil {
		prepared, err := launch.Prepare(context.Background(), directory)
		if err != nil {
			t.Fatalf("Prepare = %v", err)
		}
		abort = prepared
	}
	command, err := launch.Compose("term-aaaaa", directory)
	if err != nil {
		abort()
		t.Fatalf("Compose = %v", err)
	}
	return command, abort
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
// observes a provider's own program, exposes several agents (one sharing
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
		{ID: "watcher", Name: "Watcher", Capabilities: []api.IntegrationCapability{api.CapabilityThreadObservation, api.CapabilityThreadCreation},
			Agents: []api.IntegrationAgent{{ID: "alpha", Name: "Alpha (as Watcher names it)"}, {ID: "gamma", Name: "Gamma"}},
			Apps: []App{
				{ID: "web", Name: "Watcher (web)", Agents: []string{"alpha", "gamma"}, Handoff: true},
				{ID: "desktop", Name: "Watcher (desktop)", Handoff: true},
			},
			Connection:    func() api.IntegrationConnection { return connection },
			PrepareThread: fakePrepareThread},
		{ID: "mux", Name: "Mux", Capabilities: []api.IntegrationCapability{api.CapabilityTerminalDriver},
			Executable: &Executable{Binary: "mux", InstallHint: "install mux"}},
	}
}

func newTestService(t *testing.T, available ...string) *Service {
	t.Helper()
	service, err := NewService(Options{
		Integrations: testIntegrations(api.IntegrationConnection{State: api.IntegrationConnected, Since: observedSince, Detail: "live"}),
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
	return service
}

func TestNewServiceRejectsDuplicates(t *testing.T) {
	cases := map[string]struct {
		integrations []Integration
		want         string
	}{
		"integration id":              {[]Integration{{ID: "alpha"}, {ID: "alpha"}}, `duplicate integration id "alpha"`},
		"agent id":                    {[]Integration{{ID: "alpha", Agents: []api.IntegrationAgent{{ID: "x"}, {ID: "x"}}}}, `declares agent "x" twice`},
		"app id":                      {[]Integration{{ID: "alpha", Apps: []App{{ID: "tui"}, {ID: "tui"}}}}, `declares app "tui" twice`},
		"qualified app":               {[]Integration{{ID: "alpha", Apps: []App{{ID: "alpha/tui"}}}}, `ids are one non-empty segment`},
		"empty app":                   {[]Integration{{ID: "alpha", Apps: []App{{ID: ""}}}}, `ids are one non-empty segment`},
		"qualified id":                {[]Integration{{ID: "alpha/beta"}}, `ids are one non-empty segment`},
		"empty id":                    {[]Integration{{ID: ""}}, `ids are one non-empty segment`},
		"creation without capability": {[]Integration{{ID: "alpha", PrepareThread: fakePrepareThread}}, `threads.create capability and the creation seam must be declared together`},
		"capability without creation": {[]Integration{{ID: "alpha", Capabilities: []api.IntegrationCapability{api.CapabilityThreadCreation}}}, `must be declared together`},
	}
	for name, tc := range cases {
		_, err := NewService(Options{Integrations: tc.integrations})
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: NewService = %v, want %q", name, err, tc.want)
		}
	}
	// The same agent id under two Integrations is fine: ids are scoped.
	if _, err := NewService(Options{Integrations: testIntegrations(api.IntegrationConnection{})}); err != nil {
		t.Errorf("NewService(fixture) = %v", err)
	}
}

// The catalog is the service's own: mutating the registration slices
// after construction changes nothing.
func TestCatalogIsCopiedFromRegistrations(t *testing.T) {
	registrations := testIntegrations(api.IntegrationConnection{State: api.IntegrationConnected})
	service, err := NewService(Options{Integrations: registrations, LookPath: func(string) (string, error) { return "", errors.New("no") }})
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
	service := newTestService(t, "alpha", "mux")
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
		{ID: "watcher", Name: "Watcher", Capabilities: []api.IntegrationCapability{api.CapabilityThreadObservation, api.CapabilityThreadCreation},
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
		LookPath:     func(string) (string, error) { return "", errors.New("nope") },
	})
	if err != nil {
		t.Fatal(err)
	}
	if watcher, _ := disconnected.Get("watcher"); watcher.Available || watcher.Connection.Detail != "not installed" {
		t.Errorf("disconnected watcher = %+v", watcher)
	}
}

// ResolveLaunch turns an App into launch input: the qualified App id as
// the recorded intent, and the App's command composed with the minted
// identity — placement is the caller's, not the catalog's business.
func TestResolveLaunchComposesTheCommand(t *testing.T) {
	service := newTestService(t, "alpha")
	launch, err := service.ResolveLaunch(context.Background(), "alpha/tui")
	if err != nil {
		t.Fatal(err)
	}
	if launch.AppID != "alpha/tui" || launch.Prepare != nil {
		t.Errorf("launch = %+v; want alpha/tui without preparation", launch)
	}
	if command, _ := run(t, launch, "/spaces/default"); command != "alpha --tui --for term-aaaaa" {
		t.Errorf("composed command = %q", command)
	}
}

func TestResolveLaunchRefusals(t *testing.T) {
	service := newTestService(t, "alpha")
	ctx := context.Background()

	for _, appID := range []string{"nonexistent/tui", "alpha/desktop", "alpha", "", "alpha/tui/extra"} {
		if _, err := service.ResolveLaunch(ctx, appID); !errors.Is(err, ErrAppNotFound) {
			t.Errorf("ResolveLaunch(%q) = %v, want ErrAppNotFound", appID, err)
		}
	}

	// A missing binary refuses before any terminal exists, naming the
	// command and its install hint.
	_, err := service.ResolveLaunch(ctx, "beta/tui")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ResolveLaunch(missing binary) = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), `"beta-bin"`) || !strings.Contains(err.Error(), "install beta") {
		t.Errorf("refusal names neither command nor hint: %v", err)
	}

	// A handoff App does not run in a terminal.
	if _, err := service.ResolveLaunch(ctx, "watcher/web"); !errors.Is(err, ErrAppNotTerminal) {
		t.Errorf("ResolveLaunch(handoff app) = %v, want ErrAppNotTerminal", err)
	}

	// Availability is one rule for the catalog and for launches: a
	// connection-backed Integration with a terminal App launches only
	// while connected.
	disconnected, err := NewService(Options{
		Integrations: []Integration{{ID: "remote", Name: "Remote",
			Apps: []App{{ID: "tui", Name: "Remote", Terminal: fakeTerminalApp{command: "remote"}}},
			Connection: func() api.IntegrationConnection {
				return api.IntegrationConnection{State: api.IntegrationConnecting, Detail: "dialing"}
			}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := disconnected.ResolveLaunch(ctx, "remote/tui"); !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "dialing") {
		t.Errorf("ResolveLaunch(disconnected) = %v, want ErrUnavailable with the connection detail", err)
	}
	if got := disconnected.List()[0]; got.Available {
		t.Errorf("catalog says available while disconnected: %+v", got)
	}
}

// ResolveResume is the launch's second form: the thread's App composes
// the provider's exact resume from the thread's private identity.
func TestResolveResumeComposesTheExactResume(t *testing.T) {
	service := newTestService(t, "alpha")
	launch, err := service.ResolveResume(context.Background(), threads.ResumeRequest{IntegrationID: "alpha", AppID: "alpha/tui", ProviderID: "sess-1"})
	if err != nil {
		t.Fatal(err)
	}
	if command, _ := run(t, launch, "/work"); command != "alpha --tui --for term-aaaaa --resume sess-1" {
		t.Errorf("composed command = %q", command)
	}
	if launch.AppID != "alpha/tui" {
		t.Errorf("app = %q, want alpha/tui", launch.AppID)
	}
}

// Resume refusals: the same availability gate as launch; a thread with no
// App or a handoff App is not resumable (it opens only in its own
// program); an App the catalog lacks, or one outside the thread's own
// Integration, is an unavailable origin.
func TestResolveResumeRefusals(t *testing.T) {
	service := newTestService(t, "alpha")
	ctx := context.Background()
	if _, err := service.ResolveResume(ctx, threads.ResumeRequest{IntegrationID: "beta", AppID: "beta/tui"}); !errors.Is(err, ErrUnavailable) {
		t.Errorf("ResolveResume(missing binary) = %v, want ErrUnavailable", err)
	}
	cases := map[string]struct {
		request threads.ResumeRequest
		want    error
	}{
		"no app":              {threads.ResumeRequest{IntegrationID: "watcher", ProviderID: "t1"}, ErrNotResumable},
		"handoff app":         {threads.ResumeRequest{IntegrationID: "watcher", AppID: "watcher/web"}, ErrNotResumable},
		"foreign app":         {threads.ResumeRequest{IntegrationID: "watcher", AppID: "alpha/tui"}, ErrOriginUnavailable},
		"unknown app":         {threads.ResumeRequest{IntegrationID: "alpha", AppID: "alpha/gone"}, ErrOriginUnavailable},
		"unknown integration": {threads.ResumeRequest{IntegrationID: "nonexistent", AppID: "nonexistent/tui"}, ErrOriginUnavailable},
	}
	for name, tc := range cases {
		if _, err := service.ResolveResume(ctx, tc.request); !errors.Is(err, tc.want) {
			t.Errorf("%s: ResolveResume = %v, want %v", name, err, tc.want)
		}
	}
	if _, err := service.ResolveResume(ctx, cases["handoff app"].request); !strings.Contains(err.Error(), "open in Watcher") {
		t.Errorf("handoff refusal does not name the program: %v", err)
	}
}

// The prepare seam rides the launch input: it runs with the resolved
// directory before the create, its abort undoes it when the create fails
// afterwards, and a refusal there is an unavailable App — no command is
// ever composed.
func TestResolveLaunchCarriesThePreparation(t *testing.T) {
	app := &fakePreparingApp{fakeTerminalApp: fakeTerminalApp{command: "delta"}}
	service, err := NewService(Options{
		Integrations: []Integration{{ID: "delta", Name: "Delta",
			Apps:       []App{{ID: "tui", Name: "Delta", Terminal: app}},
			Executable: &Executable{Binary: "delta", InstallHint: "install delta"}}},
		LookPath: func(string) (string, error) { return "/bin/delta", nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	launch, err := service.ResolveLaunch(ctx, "delta/tui")
	if err != nil {
		t.Fatal(err)
	}
	command, abort := run(t, launch, "/spaces/default")
	if app.prepared != "/spaces/default" || command != "delta --for term-aaaaa" || app.aborted {
		t.Errorf("prepared = %q, command = %q, aborted = %v", app.prepared, command, app.aborted)
	}
	abort()
	if !app.aborted {
		t.Error("abort did not undo the preparation")
	}

	app.prepareErr = errors.New("no server answering")
	launch, err = service.ResolveLaunch(ctx, "delta/tui")
	if err != nil {
		t.Fatal(err)
	}
	_, err = launch.Prepare(ctx, "/spaces/default")
	if !errors.Is(err, ErrUnavailable) || !strings.Contains(err.Error(), "no server answering") {
		t.Fatalf("preparation refusal = %v, want ErrUnavailable carrying the cause", err)
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
