package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/config"
)

func TestFirstRunNotice(t *testing.T) {
	linux := firstRunNotice("linux", "/home/ab/.config/systemd/user/atc.server.service")
	wantLinux := "registered atc.server (/home/ab/.config/systemd/user/atc.server.service)\n" +
		"the server now starts automatically when this machine boots and restarts if it exits\n" +
		"undo at any time with `atc server uninstall`\n"
	if diff := cmp.Diff(wantLinux, linux); diff != "" {
		t.Errorf("linux notice mismatch (-want +got):\n%s", diff)
	}

	darwin := firstRunNotice("darwin", "/Users/ab/Library/LaunchAgents/atc.server.plist")
	wantDarwin := "registered atc.server (/Users/ab/Library/LaunchAgents/atc.server.plist)\n" +
		"the server now starts automatically at every login and restarts if it exits\n" +
		"undo at any time with `atc server uninstall`\n"
	if diff := cmp.Diff(wantDarwin, darwin); diff != "" {
		t.Errorf("darwin notice mismatch (-want +got):\n%s", diff)
	}
}

func TestUninstallReport(t *testing.T) {
	got := uninstallReport(true, []remainingFile{
		{"config", "/home/ab/.config/atc/config.toml"},
		{"token", "/home/ab/.local/share/atc/auth-token"},
	})
	want := "uninstalled atc.server\n" +
		"left in place (uninstall never deletes data):\n" +
		"  config: /home/ab/.config/atc/config.toml\n" +
		"  token: /home/ab/.local/share/atc/auth-token\n"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("uninstallReport mismatch (-want +got):\n%s", diff)
	}

	got = uninstallReport(false, nil)
	want = "atc.server was not installed; nothing removed\n"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("empty uninstallReport mismatch (-want +got):\n%s", diff)
	}

	got = lingerUninstallNotice(1000)
	want = "ATC did not disable systemd lingering because other user services may use it\n" +
		"disable it, if no longer needed, with `sudo loginctl disable-linger 1000`\n"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("lingerUninstallNotice mismatch (-want +got):\n%s", diff)
	}
}

// seamStub swaps every host-touching seam so the lifecycle decision path
// runs hermetically: supervisor commands are recorded instead of executed,
// the probe answers from the healthy field (the real awaitHealthy gate
// runs against it), and tailscale executable resolution is scripted.
// Linux command lines only — the darwin branch stays untested by design
// (the dev and CI hosts are Linux).
type seamStub struct {
	commands       [][]string // state-changing supervisor invocations, in order
	active         bool       // systemctl --user is-active answer
	healthy        bool       // probe answer
	resolves       int        // tailscale executable resolution calls
	resolveErr     error
	commandErr     error
	lingering      bool
	lingerCheckErr error
	tailnetURL     string
	tailnetProblem string
	tailnetExec    string
	webhooks       api.Webhooks // the server's scripted webhook report
	webhookProbes  int
}

func installSeams(t *testing.T, s *seamStub) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("lifecycle seam tests exercise the linux supervisor branch")
	}
	origRun, origExit, origProbe, origResolve, origRequire, origLinger, origTailnet, origWebhooks := runSupervisor, exitCode, probeOnce, resolveTailscaleExecutable, requireSystemctl, userLingering, inspectTailnetEndpoint, probeWebhooks
	t.Cleanup(func() {
		runSupervisor, exitCode, probeOnce, resolveTailscaleExecutable, requireSystemctl, userLingering, inspectTailnetEndpoint, probeWebhooks = origRun, origExit, origProbe, origResolve, origRequire, origLinger, origTailnet, origWebhooks
	})
	requireSystemctl = func() error { return nil }
	userLingering = func(context.Context, int) (bool, error) {
		return s.lingering, s.lingerCheckErr
	}
	runSupervisor = func(_ context.Context, name string, args ...string) error {
		s.commands = append(s.commands, append([]string{name}, args...))
		if name == "loginctl" && s.commandErr != nil {
			return s.commandErr
		}
		return nil
	}
	exitCode = func(_ context.Context, name string, args ...string) int {
		if strings.Contains(strings.Join(args, " "), "is-active") {
			if s.active {
				return 0
			}
			return 3
		}
		s.commands = append(s.commands, append([]string{name}, args...))
		return 0
	}
	probeOnce = func(context.Context, Options, string) probeOutcome {
		return probeOutcome{responding: s.healthy, healthy: s.healthy}
	}
	resolveTailscaleExecutable = func(string) (string, error) {
		s.resolves++
		if s.resolveErr != nil {
			return "", s.resolveErr
		}
		return "/usr/bin/tailscale", nil
	}
	inspectTailnetEndpoint = func(_ context.Context, _ config.Config, executable string) (string, string) {
		s.tailnetExec = executable
		return s.tailnetURL, s.tailnetProblem
	}
	probeWebhooks = func(context.Context, Options, string) (api.Webhooks, error) {
		s.webhookProbes++
		return s.webhooks, nil
	}
}

func TestUserLingeringReadsStdoutOnlyAndWrapsExecFailure(t *testing.T) {
	t.Run("stderr noise does not poison state", func(t *testing.T) {
		dir := t.TempDir()
		path := filepath.Join(dir, "loginctl")
		if err := os.WriteFile(path, []byte("#!/bin/sh\necho warning >&2\necho Linger=yes\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", dir)
		enabled, err := userLingering(context.Background(), 1000)
		if err != nil || !enabled {
			t.Fatalf("userLingering = %v, %v; want true, nil", enabled, err)
		}
	})

	t.Run("missing executable keeps cause", func(t *testing.T) {
		t.Setenv("PATH", t.TempDir())
		_, err := userLingering(context.Background(), 1000)
		if err == nil || !strings.Contains(err.Error(), "executable file not found") {
			t.Fatalf("userLingering error = %v, want the exec cause", err)
		}
	})
}

// seamEnv isolates every path the lifecycle touches (unit file, token,
// state) under a temp home; port 1 is reserved and closed, so the real
// pre-flight TCP dial fails fast.
func seamEnv(t *testing.T) (Options, string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(dir, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(dir, "state"))
	unitFile, err := UnitPath()
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	opts := Options{
		Config:  config.Config{Port: 1, Bind: "127.0.0.1", TailscaleExecutable: "tailscale"},
		Version: "test",
		Stdout:  &out,
		Stderr:  &out,
	}
	return opts, unitFile
}

func writeInstalledUnit(t *testing.T, unitFile string, flags LaunchFlags) string {
	t.Helper()
	content, err := renderUnit(flags)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeUnit(unitFile, content); err != nil {
		t.Fatal(err)
	}
	return content
}

func readUnitFlags(t *testing.T, unitFile string) LaunchFlags {
	t.Helper()
	content, err := os.ReadFile(unitFile)
	if err != nil {
		t.Fatal(err)
	}
	flags, err := unitLaunchFlags(runtime.GOOS, string(content))
	if err != nil {
		t.Fatalf("installed unit unreadable: %v", err)
	}
	return flags
}

func wantUnitFlags(t *testing.T, unitFile string, want LaunchFlags) {
	t.Helper()
	if diff := cmp.Diff(want, readUnitFlags(t, unitFile)); diff != "" {
		t.Errorf("installed unit flags mismatch (-want +got):\n%s", diff)
	}
}

// A logged-out or unreachable tailnet cannot fail this path — the
// lifecycle consults nothing beyond executable resolution.
func TestStartInstallsTailscaleOverride(t *testing.T) {
	s := &seamStub{healthy: true, tailnetURL: "https://host.tailnet.ts.net:1"}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	opts.Flags.Tailscale = boolPtr(true)

	if err := Start(context.Background(), opts); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	wantUnitFlags(t, unitFile, LaunchFlags{Tailscale: boolPtr(true)})
	if s.resolves != 1 {
		t.Errorf("resolve calls = %d, want 1 (preflight)", s.resolves)
	}
	if s.tailnetExec != "/usr/bin/tailscale" {
		t.Errorf("tailnet inspection executable = %q, want the preflight result", s.tailnetExec)
	}
	want := [][]string{
		{"loginctl", "enable-linger"},
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", UnitName},
		{"systemctl", "--user", "start", UnitName},
	}
	if diff := cmp.Diff(want, s.commands); diff != "" {
		t.Errorf("supervisor commands mismatch (-want +got):\n%s", diff)
	}
	got := opts.Stdout.(*strings.Builder).String()
	for _, wantLine := range []string{
		"started atc.server\n",
		"  api: http://127.0.0.1:1\n",
		"  api (tailnet): https://host.tailnet.ts.net:1\n",
	} {
		if !strings.Contains(got, wantLine) {
			t.Errorf("start output %q does not contain %q", got, wantLine)
		}
	}
}

func TestStartReportsPendingTailnetEndpoint(t *testing.T) {
	s := &seamStub{
		healthy:        true,
		tailnetURL:     "https://host.tailnet.ts.net:1",
		tailnetProblem: "tailscale serve has not exposed the route yet",
	}
	installSeams(t, s)
	opts, _ := seamEnv(t)
	opts.Flags.Tailscale = boolPtr(true)

	if err := Start(context.Background(), opts); err != nil {
		t.Fatal(err)
	}
	got := opts.Stdout.(*strings.Builder).String()
	want := "api (tailnet): pending at https://host.tailnet.ts.net:1 (tailscale serve has not exposed the route yet)"
	if !strings.Contains(got, want) {
		t.Errorf("start output = %q, want %q", got, want)
	}
}

func TestStartFailsBeforeInstallWhenLingeringCannotBeEnabled(t *testing.T) {
	lingerErr := errors.New("authorization required")
	s := &seamStub{healthy: true, commandErr: lingerErr}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)

	err := Start(context.Background(), opts)
	if !errors.Is(err, lingerErr) || !strings.Contains(err.Error(), "sudo loginctl enable-linger") {
		t.Fatalf("Start = %v, want the linger error and corrective command", err)
	}
	if _, statErr := os.Stat(unitFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("failed linger setup installed the unit")
	}
	wantCommands := [][]string{{"loginctl", "enable-linger"}}
	if diff := cmp.Diff(wantCommands, s.commands); diff != "" {
		t.Errorf("commands after failed linger setup mismatch (-want +got):\n%s", diff)
	}
}

func TestStartRollsBackNewLingeringWhenUnitWriteFails(t *testing.T) {
	s := &seamStub{healthy: true}
	installSeams(t, s)
	opts, _ := seamEnv(t)
	blocked := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("XDG_CONFIG_HOME", blocked)
	opts.Flags.Tailscale = boolPtr(false)

	err := Start(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Start = %v, want the unit write failure", err)
	}
	want := [][]string{
		{"loginctl", "enable-linger"},
		{"loginctl", "disable-linger", strconv.Itoa(os.Getuid())},
	}
	if diff := cmp.Diff(want, s.commands); diff != "" {
		t.Errorf("linger rollback commands mismatch (-want +got):\n%s", diff)
	}
}

func TestLifecycleSuccessPropagatesCancellation(t *testing.T) {
	s := &seamStub{tailnetProblem: "context canceled"}
	installSeams(t, s)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := lifecycleSuccess(ctx, "started atc.server", Options{Config: config.Config{Port: 7331}}, "", exposure{tailnet: true})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("lifecycleSuccess = %v, want context.Canceled", err)
	}
}

func TestFlaglessStartPreservesRunningLaunchFlags(t *testing.T) {
	s := &seamStub{active: true, healthy: true, lingering: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	installed := writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(true), Webhooks: boolPtr(false)})

	if err := Start(context.Background(), opts); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	want := [][]string{
		{"systemctl", "--user", "enable", UnitName},
	}
	if diff := cmp.Diff(want, s.commands); diff != "" {
		t.Errorf("supervisor commands mismatch (-want +got):\n%s", diff)
	}
	if content, _ := os.ReadFile(unitFile); string(content) != installed {
		t.Error("idempotent start rewrote the unit")
	}
	if s.resolves != 0 {
		t.Errorf("resolve calls = %d, want 0 (no mutation, no preflight)", s.resolves)
	}
}

// The flagless restart is also the plain and upgrade restart path: the
// running launch's flags — both of them, explicit false included — carry
// over, and the preflight runs because the restarted daemon will boot
// with tailscale on.
func TestFlaglessRestartPreservesRunningLaunchFlags(t *testing.T) {
	s := &seamStub{active: true, healthy: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	flags := LaunchFlags{Tailscale: boolPtr(true), Webhooks: boolPtr(false)}
	writeInstalledUnit(t, unitFile, flags)

	if err := Restart(context.Background(), opts); err != nil {
		t.Fatalf("Restart = %v, want nil", err)
	}
	wantUnitFlags(t, unitFile, flags)
	if s.resolves != 1 {
		t.Errorf("resolve calls = %d, want 1 (effective tailscale preflight)", s.resolves)
	}
	last := s.commands[len(s.commands)-1]
	if diff := cmp.Diff([]string{"systemctl", "--user", "restart", UnitName}, last); diff != "" {
		t.Errorf("final supervisor command mismatch (-want +got):\n%s", diff)
	}
}

// A flag supplied to a start against a running server replaces that one
// flag; the running launch's other flag is preserved, and the changed
// unit bounces the process.
func TestStartReplacesOneFlagOnRunningService(t *testing.T) {
	s := &seamStub{active: true, healthy: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	writeInstalledUnit(t, unitFile, LaunchFlags{Webhooks: boolPtr(true)})
	opts.Flags.Tailscale = boolPtr(true)

	if err := Start(context.Background(), opts); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	wantUnitFlags(t, unitFile, LaunchFlags{Tailscale: boolPtr(true), Webhooks: boolPtr(true)})
	last := s.commands[len(s.commands)-1]
	if diff := cmp.Diff([]string{"systemctl", "--user", "restart", UnitName}, last); diff != "" {
		t.Errorf("final supervisor command mismatch (-want +got):\n%s", diff)
	}
}

// An explicit =false is an ordinary override for that launch: it disables
// the exposure even over a configured true, and the unit records it so a
// later flagless restart keeps it off. Disabling needs no tailscale
// install, so no preflight runs.
func TestRestartExplicitFalseOverridesConfiguration(t *testing.T) {
	s := &seamStub{active: true, healthy: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	opts.Config.Tailscale = true
	opts.Config.Webhooks = true
	writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(true)})
	opts.Flags = LaunchFlags{Tailscale: boolPtr(false), Webhooks: boolPtr(false)}

	if err := Restart(context.Background(), opts); err != nil {
		t.Fatalf("Restart = %v, want nil", err)
	}
	wantUnitFlags(t, unitFile, LaunchFlags{Tailscale: boolPtr(false), Webhooks: boolPtr(false)})
	if s.resolves != 0 {
		t.Errorf("resolve calls = %d, want 0 (both exposures disabled for this launch)", s.resolves)
	}
	if got := opts.Stdout.(*strings.Builder).String(); strings.Contains(got, "tailnet") || strings.Contains(got, "webhooks") {
		t.Errorf("restart output %q reports exposure that was disabled", got)
	}
}

// Stop then start begins fresh: the stopped launch's recorded flags are
// not inherited, so configuration alone decides, and the unit is
// re-rendered without them.
func TestStartAfterStopIgnoresStoppedLaunchFlags(t *testing.T) {
	for name, tc := range map[string]struct {
		configTailscale bool
		wantResolves    int
	}{
		"config quiet":   {configTailscale: false, wantResolves: 0},
		"config enabled": {configTailscale: true, wantResolves: 1},
	} {
		t.Run(name, func(t *testing.T) {
			s := &seamStub{active: false, healthy: true, tailnetURL: "https://host.tailnet.ts.net:1"}
			installSeams(t, s)
			opts, unitFile := seamEnv(t)
			opts.Config.Tailscale = tc.configTailscale
			writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(true), Webhooks: boolPtr(true)})

			if err := Start(context.Background(), opts); err != nil {
				t.Fatalf("Start = %v, want nil", err)
			}
			wantUnitFlags(t, unitFile, LaunchFlags{})
			if s.resolves != tc.wantResolves {
				t.Errorf("resolve calls = %d, want %d", s.resolves, tc.wantResolves)
			}
			if s.webhookProbes != 0 {
				t.Errorf("webhook probes = %d, want 0 (webhooks off by configuration)", s.webhookProbes)
			}
		})
	}
}

// Enabling webhooks preflights the tailscale executable (Funnel needs it)
// and the success report carries the server's own webhook state.
func TestStartWithWebhooksReportsServerState(t *testing.T) {
	s := &seamStub{healthy: true, webhooks: api.Webhooks{
		State: api.WebhooksStarting, URL: "https://host.tailnet.ts.net",
		Reason: "tailscale funnel exited: Funnel not available",
		Action: "To approve, visit: https://login.tailscale.com/f/funnel?node=abc",
	}}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	opts.Flags.Webhooks = boolPtr(true)

	if err := Start(context.Background(), opts); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	wantUnitFlags(t, unitFile, LaunchFlags{Webhooks: boolPtr(true)})
	if s.resolves != 1 {
		t.Errorf("resolve calls = %d, want 1 (Funnel preflight)", s.resolves)
	}
	got := opts.Stdout.(*strings.Builder).String()
	for _, want := range []string{
		"  webhooks: starting at https://host.tailnet.ts.net (tailscale funnel exited: Funnel not available)\n",
		"  webhooks action: To approve, visit: https://login.tailscale.com/f/funnel?node=abc\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("start output %q does not contain %q", got, want)
		}
	}
	if strings.Contains(got, "tailnet)") {
		t.Errorf("start output %q reports the private tailnet API, which --webhooks does not enable", got)
	}
}

// A running launch whose unit cannot be read cannot have its flags
// preserved, so the lifecycle refuses rather than guessing — naming the
// fresh start that never consults the old unit as the remedy.
func TestStartFailsOnUnrecognizedUnitOfRunningService(t *testing.T) {
	s := &seamStub{active: true, healthy: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	if err := writeUnit(unitFile, "garbage"); err != nil {
		t.Fatal(err)
	}

	err := Start(context.Background(), opts)
	if err == nil || !strings.Contains(err.Error(), "`atc server stop`, then `atc server start`") {
		t.Fatalf("Start = %v, want an error naming stop then start as the remedy", err)
	}
	if content, _ := os.ReadFile(unitFile); string(content) != "garbage" {
		t.Error("failed start modified the unit")
	}
	if len(s.commands) != 0 {
		t.Errorf("supervisor commands = %v, want none", s.commands)
	}
}

// With no launch running there is nothing to preserve: a fresh start
// replaces whatever the unit held.
func TestFreshStartReplacesUnrecognizedUnit(t *testing.T) {
	s := &seamStub{healthy: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	if err := writeUnit(unitFile, "garbage"); err != nil {
		t.Fatal(err)
	}
	opts.Flags.Tailscale = boolPtr(true)

	if err := Start(context.Background(), opts); err != nil {
		t.Fatalf("Start = %v, want nil", err)
	}
	wantUnitFlags(t, unitFile, LaunchFlags{Tailscale: boolPtr(true)})
}

func TestTailscalePreflightFailureLeavesEverythingAlone(t *testing.T) {
	s := &seamStub{active: true, healthy: true, resolveErr: errors.New("tailscale executable \"tailscale\" not found")}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	installed := writeInstalledUnit(t, unitFile, LaunchFlags{})
	opts.Flags.Tailscale = boolPtr(true)

	err := Start(context.Background(), opts)
	if !errors.Is(err, s.resolveErr) {
		t.Fatalf("Start = %v, want the resolution error", err)
	}
	if content, _ := os.ReadFile(unitFile); string(content) != installed {
		t.Error("failed preflight modified the unit")
	}
	if len(s.commands) != 0 {
		t.Errorf("supervisor commands = %v, want none", s.commands)
	}
}

// The preflight covers declarative tailscale too, instead of installing
// a daemon that would crash-loop on boot.
func TestDeclarativePreflightBlocksFirstStart(t *testing.T) {
	s := &seamStub{healthy: true, resolveErr: errors.New("tailscale executable \"tailscale\" not found")}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	opts.Config.Tailscale = true

	err := Start(context.Background(), opts)
	if !errors.Is(err, s.resolveErr) {
		t.Fatalf("Start = %v, want the resolution error", err)
	}
	if _, statErr := os.Stat(unitFile); !errors.Is(statErr, os.ErrNotExist) {
		t.Error("failed preflight installed a unit")
	}
}

// Stop leaves the unit (and its recorded flags) for supervised recovery;
// uninstall removing it is the whole cleanup.
func TestStopPreservesAndUninstallRemovesUnit(t *testing.T) {
	s := &seamStub{}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(true)})

	if err := Stop(context.Background(), opts); err != nil {
		t.Fatalf("Stop = %v, want nil", err)
	}
	if got := opts.Stdout.(*strings.Builder).String(); !strings.Contains(got, "returns at next boot") {
		t.Errorf("Stop output = %q, want Linux boot semantics", got)
	}
	wantUnitFlags(t, unitFile, LaunchFlags{Tailscale: boolPtr(true)})

	if err := Uninstall(context.Background(), opts); err != nil {
		t.Fatalf("Uninstall = %v, want nil", err)
	}
	if _, err := os.Stat(unitFile); !errors.Is(err, os.ErrNotExist) {
		t.Error("uninstall left the unit installed")
	}
}

func TestProbeAddr(t *testing.T) {
	for name, tc := range map[string]struct {
		bind string
		want string
	}{
		"loopback":      {"127.0.0.1", "127.0.0.1:7331"},
		"wildcard v4":   {"0.0.0.0", "127.0.0.1:7331"},
		"wildcard v6":   {"::", "127.0.0.1:7331"},
		"specific bind": {"192.168.1.20", "192.168.1.20:7331"},
	} {
		got := probeAddr(config.Config{Port: 7331, Bind: tc.bind})
		if got != tc.want {
			t.Errorf("%s: probeAddr = %q, want %q", name, got, tc.want)
		}
	}
}

func TestTailLines(t *testing.T) {
	for name, tc := range map[string]struct {
		text  string
		count int
		want  string
	}{
		"shorter than count":  {"a\nb\n", 5, "a\nb"},
		"trimmed to count":    {"a\nb\nc\nd\n", 2, "c\nd"},
		"no trailing newline": {"a\nb", 1, "b"},
	} {
		if got := tailLines(tc.text, tc.count); got != tc.want {
			t.Errorf("%s: tailLines = %q, want %q", name, got, tc.want)
		}
	}
}
