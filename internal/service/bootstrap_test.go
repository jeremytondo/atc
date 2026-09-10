package service

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/remote"
)

// The bootstrap over the lifecycle seams and a private state directory:
// a stopped server is started through Start, a healthy one is left alone,
// the token is the store's own, and the URL is the live tailnet endpoint.
func TestBootstrapStartsStoppedServerAndReportsEndpoint(t *testing.T) {
	s := &seamStub{tailnetURL: "https://host.tailnet.ts.net:1"}
	installSeams(t, s)
	opts, _ := seamEnv(t)
	opts.Config.Tailscale = true
	tailnetPollInterval = time.Millisecond
	// Unhealthy until the supervisor has been asked to start it.
	probeOnce = func(context.Context, Options, string) probeOutcome {
		for _, command := range s.commands {
			if strings.Join(command, " ") == "systemctl --user start "+UnitName {
				return probeOutcome{responding: true, healthy: true, serverVersion: "v9.9.9"}
			}
		}
		return probeOutcome{}
	}

	got, err := Bootstrap(context.Background(), opts, false)
	if err != nil {
		t.Fatalf("Bootstrap = %v", err)
	}
	token, err := ensureToken()
	if err != nil {
		t.Fatal(err)
	}
	want := remote.Bootstrap{URL: "https://host.tailnet.ts.net:1", Token: token, Version: "v9.9.9", Protocol: api.Protocol}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Bootstrap (-want +got):\n%s", diff)
	}
	wantCommands := [][]string{
		{"loginctl", "enable-linger"},
		{"systemctl", "--user", "daemon-reload"},
		{"systemctl", "--user", "enable", UnitName},
		{"systemctl", "--user", "start", UnitName},
	}
	if diff := cmp.Diff(wantCommands, s.commands); diff != "" {
		t.Errorf("supervisor commands (-want +got):\n%s", diff)
	}
	if out := opts.Stdout.(*strings.Builder).String(); strings.Contains(out, token) {
		t.Error("lifecycle output leaked the token")
	}

	// Healthy already: nothing is started, the version is the server's.
	s.commands = nil
	probeOnce = func(context.Context, Options, string) probeOutcome {
		return probeOutcome{responding: true, healthy: true, serverVersion: "v1.0.0"}
	}
	if got, err = Bootstrap(context.Background(), opts, false); err != nil || got.Version != "v1.0.0" || got.Protocol != api.Protocol || len(s.commands) != 0 {
		t.Errorf("healthy bootstrap = %+v, %v; commands %v", got, err, s.commands)
	}
}

// A running server on another protocol is never bounced by an ordinary
// bootstrap — that is the approved restart's job, after which the
// server's answer is read again and must be on this build's protocol.
func TestBootstrapRestartsOnlyWhenApproved(t *testing.T) {
	s := &seamStub{tailnetURL: "https://host.tailnet.ts.net:1", active: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	opts.Config.Tailscale = true
	writeInstalledUnit(t, unitFile, LaunchFlags{})
	tailnetPollInterval = time.Millisecond
	restarted := func() bool {
		for _, command := range s.commands {
			if strings.Join(command, " ") == "systemctl --user restart "+UnitName {
				return true
			}
		}
		return false
	}
	probeOnce = func(context.Context, Options, string) probeOutcome {
		if restarted() {
			return probeOutcome{responding: true, healthy: true, serverVersion: "v2.0.0", serverProtocol: api.Protocol}
		}
		return probeOutcome{responding: true, incompatible: true, serverVersion: "v1.0.0", serverProtocol: 0}
	}

	_, err := Bootstrap(context.Background(), opts, false)
	if err == nil || !strings.Contains(err.Error(), "no protocol") || !strings.Contains(err.Error(), "rerun") {
		t.Fatalf("unapproved bootstrap against an incompatible server = %v, want a refusal naming the protocol", err)
	}
	if len(s.commands) != 0 {
		t.Fatalf("unapproved bootstrap touched the supervisor: %v", s.commands)
	}

	got, err := Bootstrap(context.Background(), opts, true)
	if err != nil {
		t.Fatalf("approved restart = %v", err)
	}
	if !restarted() || got.Version != "v2.0.0" || got.Protocol != api.Protocol {
		t.Errorf("after restart: commands %v, bootstrap %+v", s.commands, got)
	}

	// Approved, but by now the server is healthy, on this protocol, and
	// serving on the tailnet: the restart is skipped, never a bounce for
	// its release.
	s.commands = nil
	probeOnce = func(context.Context, Options, string) probeOutcome {
		return probeOutcome{responding: true, healthy: true, serverVersion: "v1.5.0", serverProtocol: api.Protocol}
	}
	got, err = Bootstrap(context.Background(), opts, true)
	if err != nil || restarted() || got.Version != "v1.5.0" {
		t.Errorf("approved restart of a ready server: %+v, %v; commands %v", got, err, s.commands)
	}
	if !strings.Contains(opts.Stderr.(*strings.Builder).String(), "not restarted") {
		t.Error("the skipped restart was not reported")
	}
}

// With a restart approved, the exposure judged is the launch the restart
// will render: an explicit --tailscale replaces the running launch's
// disabling flag, and an omitted one inherits it.
func TestBootstrapRestartJudgesTheLaunchItRenders(t *testing.T) {
	s := &seamStub{tailnetURL: "https://host.tailnet.ts.net:1", active: true, healthy: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	opts.Config.Tailscale = true
	writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(false)})
	tailnetPollInterval = time.Millisecond
	// The route is not serving until the restart has happened.
	inspectTailnetEndpoint = func(context.Context, config.Config, string) (string, string) {
		for _, command := range s.commands {
			if strings.Join(command, " ") == "systemctl --user restart "+UnitName {
				return s.tailnetURL, ""
			}
		}
		return s.tailnetURL, "tailscale serve has not exposed the route yet"
	}

	if _, err := Bootstrap(context.Background(), opts, true); err == nil || !strings.Contains(err.Error(), "--tailscale") {
		t.Errorf("restart inheriting --tailscale=false: err = %v, want the flag remedy", err)
	}
	if len(s.commands) != 0 {
		t.Errorf("refusal touched the supervisor: %v", s.commands)
	}
	opts.Flags.Tailscale = boolPtr(true)
	if _, err := Bootstrap(context.Background(), opts, true); err != nil {
		t.Errorf("restart with --tailscale = %v, want nil", err)
	}
	wantUnitFlags(t, unitFile, LaunchFlags{Tailscale: boolPtr(true)})
}

// Inspect reports the executable, the unit's own executable (not this
// one), what answered the probe, and the tailnet setting and node.
func TestInspectReportsExecutableServerAndTailnet(t *testing.T) {
	s := &seamStub{active: true}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	opts.Version = "v1.2.3-dev.abc1234"
	opts.Config.Tailscale = true
	// The unit runs a different executable than the one inspecting.
	unit := writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(false)})
	unit = regexp.MustCompile(`ExecStart="[^"]*"`).ReplaceAllString(unit, `ExecStart="/opt/elsewhere/atc"`)
	if err := os.WriteFile(unitFile, []byte(unit), 0o600); err != nil {
		t.Fatal(err)
	}
	probeOnce = func(context.Context, Options, string) probeOutcome {
		return probeOutcome{responding: true, incompatible: true, serverVersion: "v0.9.0", serverProtocol: 0}
	}
	origProblem := inspectTailnetProblem
	t.Cleanup(func() { inspectTailnetProblem = origProblem })
	inspectTailnetProblem = func(context.Context, config.Config) string { return "" }

	got, err := Inspect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Inspect = %v", err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	want := remote.Inspection{
		Executable: remote.Executable{Path: self, Version: "v1.2.3-dev.abc1234", Channel: "dev", Protocol: api.Protocol, Writable: true},
		Server:     remote.Server{Executable: "/opt/elsewhere/atc", Supervised: true, Responding: true, Version: "v0.9.0"},
		Tailnet:    remote.Tailnet{Configured: true, Launch: boolPtr(false)},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Inspection (-want +got):\n%s", diff)
	}

	// No unit, nothing answering, Tailscale absent: every fact degrades
	// to its absence rather than a guess.
	if err := os.Remove(unitFile); err != nil {
		t.Fatal(err)
	}
	s.active = false
	probeOnce = func(context.Context, Options, string) probeOutcome { return probeOutcome{} }
	inspectTailnetProblem = func(context.Context, config.Config) string {
		return "tailscale executable not found; install Tailscale"
	}
	got, err = Inspect(context.Background(), opts)
	if err != nil {
		t.Fatalf("Inspect = %v", err)
	}
	want.Server = remote.Server{}
	want.Tailnet = remote.Tailnet{Configured: true, Problem: "tailscale executable not found; install Tailscale"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Inspection without unit or node (-want +got):\n%s", diff)
	}
	// Inspection changes nothing: no token was minted.
	tokenPath, err := paths.AuthTokenFile()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(tokenPath); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("inspection minted a token: %v", err)
	}
}

func TestPackageManaged(t *testing.T) {
	for path, want := range map[string]bool{
		"/opt/homebrew/Cellar/atc/0.1.0/bin/atc": true,
		"/usr/local/Cellar/atc/0.1.0/bin/atc":    true,
		"/home/linuxbrew/.linuxbrew/bin/atc":     true,
		"/nix/store/abc-atc/bin/atc":             true,
		"/usr/bin/atc":                           true,
		"/usr/local/bin/atc":                     false,
		"/home/u/.local/bin/atc":                 false,
		"/opt/atc/atc":                           false,
	} {
		if got := packageManaged(path); got != want {
			t.Errorf("packageManaged(%q) = %v, want %v", path, got, want)
		}
	}
}

// The tailnet node query names the remedy for each prerequisite failure.
func TestTailnetNodeNamesRemedies(t *testing.T) {
	origResolve := resolveTailscaleExecutable
	t.Cleanup(func() { resolveTailscaleExecutable = origResolve })
	resolveTailscaleExecutable = func(string) (string, error) { return "", errors.New("tailscale executable not found") }
	problem := tailnetProblem(context.Background(), config.Config{TailscaleExecutable: "tailscale"})
	if !strings.Contains(problem, "tailscale.com/download") || !strings.Contains(problem, "tailscale up") {
		t.Errorf("missing executable: problem %q", problem)
	}
	dir := t.TempDir()
	fake := filepath.Join(dir, "tailscale")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho '{\"BackendState\":\"NeedsLogin\",\"Self\":{\"DNSName\":\"\"}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolveTailscaleExecutable = func(string) (string, error) { return fake, nil }
	problem = tailnetProblem(context.Background(), config.Config{TailscaleExecutable: "tailscale"})
	if !strings.Contains(problem, "logged out") || !strings.Contains(problem, "tailscale up") {
		t.Errorf("logged out: problem %q", problem)
	}
	up := filepath.Join(dir, "tailscale-up")
	if err := os.WriteFile(up, []byte("#!/bin/sh\necho '{\"BackendState\":\"Running\",\"Self\":{\"DNSName\":\"ws.tailnet.ts.net.\"}}'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	resolveTailscaleExecutable = func(string) (string, error) { return up, nil }
	if problem := tailnetProblem(context.Background(), config.Config{TailscaleExecutable: "tailscale"}); problem != "" {
		t.Errorf("running node: problem %q, want none", problem)
	}
}

func TestBootstrapRefusesWithoutTailnetAndBoundsTheWait(t *testing.T) {
	s := &seamStub{healthy: true, tailnetProblem: "tailscale serve has not exposed the route yet"}
	installSeams(t, s)
	opts, unitFile := seamEnv(t)
	tailnetPollInterval = time.Millisecond
	origTimeout := tailnetServingTimeout
	tailnetServingTimeout = 20 * time.Millisecond
	t.Cleanup(func() { tailnetServingTimeout = origTimeout })

	_, err := Bootstrap(context.Background(), opts, false)
	if err == nil || !strings.Contains(err.Error(), "`tailscale = true`") || !strings.Contains(err.Error(), "config.toml") {
		t.Errorf("config without tailnet: err = %v, want the config remedy", err)
	}
	if len(s.commands) != 0 {
		t.Errorf("refusal touched the supervisor: %v", s.commands)
	}

	// Configuration enables it but the running launch was started with
	// --tailscale=false: the remedy is the restart flag.
	opts.Config.Tailscale = true
	s.active = true
	writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(false)})
	if _, err := Bootstrap(context.Background(), opts, false); err == nil || !strings.Contains(err.Error(), "--tailscale") {
		t.Errorf("launch without tailnet: err = %v, want the restart remedy", err)
	}

	// Enabled but never serving: the wait is bounded and names the reason.
	writeInstalledUnit(t, unitFile, LaunchFlags{Tailscale: boolPtr(true)})
	_, err = Bootstrap(context.Background(), opts, false)
	if err == nil || !strings.Contains(err.Error(), "did not reach serving") || !strings.Contains(err.Error(), s.tailnetProblem) {
		t.Errorf("never serving: err = %v", err)
	}

	// An inspection that hangs is cut off by the same bound.
	inspectTailnetEndpoint = func(ctx context.Context, _ config.Config, _ string) (string, string) {
		<-ctx.Done()
		return "", ""
	}
	started := time.Now()
	_, err = Bootstrap(context.Background(), opts, false)
	if err == nil || !strings.Contains(err.Error(), "did not reach serving") || time.Since(started) > time.Second {
		t.Errorf("hung inspection: err = %v after %s", err, time.Since(started))
	}
}
