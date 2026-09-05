package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/service"
)

func TestRunNoArgsPrintsUsage(t *testing.T) {
	var stdout, stderr strings.Builder
	if err := run(context.Background(), nil, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("stdout = %q, want usage text", stdout.String())
	}
}

func TestRootHelpScopesConfigurationPrecedence(t *testing.T) {
	var stdout, stderr strings.Builder
	if err := run(context.Background(), nil, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	text := strings.Join(strings.Fields(help), " ")
	for _, want := range []string{
		"For `atc server run`, configuration precedence is:",
		"flags > ATC_<KEY> environment > ~/.config/atc/config.toml > defaults",
		"Supervised server commands read config.toml and defaults",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("root help missing %q:\n%s", want, help)
		}
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr strings.Builder
	if err := run(context.Background(), []string{"version"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Error("version printed nothing")
	}
}

func TestRunRejectsBadInvocations(t *testing.T) {
	for _, args := range [][]string{
		{"frobnicate"},
		{"version", "extra"},
		{"upgrade", "extra"},
		{"upgrade", "--restart", "--no-restart"},
		{"server"},
		{"server", "frobnicate"},
		{"server", "run", "extra"},
		{"server", "token", "frobnicate"},
		{"server", "token", "rotate", "extra"},
		{"server", "start", "extra"},
		{"server", "stop", "extra"},
		{"server", "restart", "extra"},
		{"server", "status", "extra"},
		{"server", "logs", "extra"},
		{"server", "uninstall", "extra"},
	} {
		var stdout, stderr strings.Builder
		if err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err == nil {
			t.Errorf("run(%q) = nil, want error", args)
		}
	}
}

// isolateXDG points every ATC file location at a temp directory so tests
// never touch the developer's real state.
func isolateXDG(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_DATA_HOME", dir+"/data")
	t.Setenv("XDG_STATE_HOME", dir+"/state")
}

// syncBuffer is a strings.Builder safe to read while the server goroutine
// writes it.
type syncBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestServerRunStopsOnContextCancel(t *testing.T) {
	isolateXDG(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout strings.Builder
	var stderr syncBuffer
	done := make(chan error, 1)
	go func() {
		done <- run(ctx, []string{"server", "run", "--port", "0"}, strings.NewReader(""), &stdout, &stderr)
	}()

	// Boot now includes storage migration and the blocking startup
	// reconcile; wait for the serve log before requesting shutdown.
	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(stderr.String(), "server started") {
		if time.Now().After(deadline) {
			t.Fatalf("server never started; stderr:\n%s", stderr.String())
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server run = %v, want nil", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("server run did not stop after context cancellation")
	}
}

// A cancellation that lands during boot is a clean exit too, not an error.
func TestServerRunCancelledDuringBoot(t *testing.T) {
	isolateXDG(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var stdout, stderr strings.Builder
	if err := run(ctx, []string{"server", "run", "--port", "0"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatalf("server run with pre-cancelled context = %v, want nil", err)
	}
}

// Flags apply after config.Load's validation, so validation must run again
// on the settled values — an empty --bind would otherwise fall through to
// net.Listen and bind every interface.
func TestServerRunRejectsInvalidFlagValues(t *testing.T) {
	isolateXDG(t)
	for name, args := range map[string][]string{
		"empty bind":        {"server", "run", "--bind=", "--port", "0"},
		"port out of range": {"server", "run", "--port", "70000"},
	} {
		var stdout, stderr strings.Builder
		if err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err == nil {
			t.Errorf("%s: run(%q) = nil, want validation error", name, args)
		}
	}
}

// closedPort finds a port nothing is listening on, so the status probe
// cannot accidentally reach a real server running on the developer machine.
func closedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func TestServerStatusNotInstalled(t *testing.T) {
	isolateXDG(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "atc")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := fmt.Sprintf("port = %d\n", closedPort(t))
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr strings.Builder
	err := run(context.Background(), []string{"server", "status"}, strings.NewReader(""), &stdout, &stderr)
	var exit *service.ExitError
	if !errors.As(err, &exit) || exit.Code != 2 {
		t.Fatalf("server status = %v, want ExitError with code 2", err)
	}
	if !strings.Contains(stdout.String(), "not installed") {
		t.Errorf("stdout = %q, want a not-installed report", stdout.String())
	}
}

// A config.toml the server refuses to load must not block the recovery
// commands: stop, logs, and uninstall never consume configuration, and they
// are the diagnostics and the undo for exactly that broken state.
func TestRecoveryCommandsSurviveBrokenConfig(t *testing.T) {
	isolateXDG(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "atc")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("bogus_key = true\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// status settles config and must surface the parse error.
	var stdout, stderr strings.Builder
	if err := run(context.Background(), []string{"server", "status"}, strings.NewReader(""), &stdout, &stderr); err == nil || !strings.Contains(err.Error(), "bogus_key") {
		t.Errorf("server status = %v, want the config parse error", err)
	}

	// stop and uninstall proceed to their not-installed reports.
	for _, args := range [][]string{{"server", "stop"}, {"server", "uninstall"}} {
		var stdout, stderr strings.Builder
		if err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Errorf("run(%q) = %v, want nil despite broken config", args, err)
		}
		if !strings.Contains(stdout.String(), "not installed") {
			t.Errorf("run(%q) stdout = %q, want a not-installed report", args, stdout.String())
		}
	}

	// logs reports the uninstalled state rather than a config error (or an
	// empty journal).
	stdout.Reset()
	stderr.Reset()
	err := run(context.Background(), []string{"server", "logs"}, strings.NewReader(""), &stdout, &stderr)
	if err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Errorf("server logs = %v, want a not-installed explanation", err)
	}
}

// Help must document both exposure flags with one contract: restart keeps
// the running launch's flags, a fresh start after stop uses config.toml,
// and =false disables for this launch.
func TestServerStartRestartExposureHelp(t *testing.T) {
	for _, sub := range []string{"start", "restart"} {
		var stdout, stderr strings.Builder
		if err := run(context.Background(), []string{"server", sub, "--help"}, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("server %s --help = %v", sub, err)
		}
		help := stdout.String()
		for _, want := range []string{"--tailscale", "--webhooks", "restart keeps the running launch's flags", "after stop uses config.toml alone", "disables it for this launch", "never modify config.toml"} {
			if !strings.Contains(help, want) {
				t.Errorf("server %s help missing %q:\n%s", sub, want, help)
			}
		}
	}
}

func TestServerStopHelpUsesPlatformSpecificReturnPoints(t *testing.T) {
	var stdout, stderr strings.Builder
	if err := run(context.Background(), []string{"server", "stop", "--help"}, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	help := stdout.String()
	for _, want := range []string{"next boot on Linux", "next login on macOS"} {
		if !strings.Contains(help, want) {
			t.Errorf("server stop help missing %q:\n%s", want, help)
		}
	}
}

func TestExposureLifecycleOptionsTriState(t *testing.T) {
	isolateXDG(t)
	options := func(t *testing.T, set map[string]string) service.LaunchFlags {
		t.Helper()
		cmd := newServerStartCmd()
		for name, value := range set {
			if err := cmd.Flags().Set(name, value); err != nil {
				t.Fatal(err)
			}
		}
		opts, err := exposureLifecycleOptions(cmd)
		if err != nil {
			t.Fatal(err)
		}
		return opts.Flags
	}
	boolPtr := func(v bool) *bool { return &v }
	for name, tc := range map[string]struct {
		set  map[string]string
		want service.LaunchFlags
	}{
		"omitted":        {nil, service.LaunchFlags{}},
		"tailscale true": {map[string]string{"tailscale": "true"}, service.LaunchFlags{Tailscale: boolPtr(true)}},
		"webhooks false": {map[string]string{"webhooks": "false"}, service.LaunchFlags{Webhooks: boolPtr(false)}},
		"both":           {map[string]string{"tailscale": "false", "webhooks": "true"}, service.LaunchFlags{Tailscale: boolPtr(false), Webhooks: boolPtr(true)}},
	} {
		if diff := cmp.Diff(tc.want, options(t, tc.set)); diff != "" {
			t.Errorf("%s: flags mismatch (-want +got):\n%s", name, diff)
		}
	}
}

func TestServerTokenPrintsAndRotates(t *testing.T) {
	isolateXDG(t)
	tokenOut := func(args ...string) string {
		var stdout, stderr strings.Builder
		if err := run(context.Background(), args, strings.NewReader(""), &stdout, &stderr); err != nil {
			t.Fatalf("run(%q) = %v", args, err)
		}
		return strings.TrimSpace(stdout.String())
	}
	first := tokenOut("server", "token")
	if !strings.HasPrefix(first, "atc_") || len(first) != len("atc_")+43 {
		t.Fatalf("token %q does not match the contract format", first)
	}
	if again := tokenOut("server", "token"); again != first {
		t.Errorf("second print minted a new token")
	}
	if rotated := tokenOut("server", "token", "rotate"); rotated == first {
		t.Errorf("rotate returned the old token")
	}
}
