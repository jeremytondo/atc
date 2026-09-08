package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/tui"
	"github.com/jeremytondo/atc/internal/version"
)

// capturePicker replaces the picker with a recorder so the launch can be
// checked without a terminal.
func capturePicker(t *testing.T) *tui.Options {
	t.Helper()
	captured := &tui.Options{}
	prev := runPicker
	runPicker = func(_ context.Context, opts tui.Options) error {
		*captured = opts
		return nil
	}
	t.Cleanup(func() { runPicker = prev })
	return captured
}

func TestRootWithoutTTYPrintsUsageAndFails(t *testing.T) {
	captured := capturePicker(t)
	stdout, _, err := runCLI(t)
	if err == nil || !strings.Contains(err.Error(), "interactive terminal") {
		t.Fatalf("bare atc without a TTY = %v, want the TTY refusal", err)
	}
	if !strings.Contains(stdout, "Usage:") {
		t.Errorf("stdout = %q, want usage", stdout)
	}
	if captured.Client != nil {
		t.Error("picker opened without a TTY")
	}
}

func TestRootOpensLocalPicker(t *testing.T) {
	startTestServer(t)
	forceTTY(t)
	captured := capturePicker(t)
	started := false
	prev := startLocalServer
	startLocalServer = func(context.Context, service.Options) error { started = true; return nil }
	t.Cleanup(func() { startLocalServer = prev })

	if _, _, err := runCLI(t); err != nil {
		t.Fatal(err)
	}
	if started {
		t.Error("a healthy server was started again")
	}
	if captured.Target != "" || captured.ServerVersion != "v0.0.0-test" || captured.ClientVersion != version.String() || captured.TransportLoss != nil {
		t.Errorf("options = target %q server %q client %q transportLoss set %v", captured.Target, captured.ServerVersion, captured.ClientVersion, captured.TransportLoss != nil)
	}
	spaces, err := captured.Client.Spaces(context.Background())
	if err != nil || len(spaces) != 1 || !spaces[0].IsDefault {
		t.Errorf("picker client Spaces = %+v, %v; want the Default space", spaces, err)
	}
	// The local attach runs the zmx preflight: with no zmx on PATH the
	// picker learns why, per attempt, instead of the launch failing.
	t.Setenv("PATH", t.TempDir())
	if _, err := captured.Attach(api.Terminal{ID: "term-abcde", Status: api.TerminalRunning}); err == nil || !strings.Contains(err.Error(), "zmx") {
		t.Errorf("attach without zmx = %v", err)
	}
}

func TestRootRefusesRemoteATCServerWithoutRemoteFlag(t *testing.T) {
	forceTTY(t)
	capturePicker(t)
	t.Setenv("ATC_SERVER", "https://ws.tailnet.ts.net:7331")
	t.Setenv("ATC_TOKEN", cliTestToken)
	if _, _, err := runCLI(t); err == nil || !strings.Contains(err.Error(), "--remote") {
		t.Errorf("bare atc against a remote ATC_SERVER = %v, want the --remote hint", err)
	}
}

// A stopped local server is started through the lifecycle, and the
// picker opens once it answers.
func TestRootStartsStoppedLocalServer(t *testing.T) {
	forceTTY(t)
	captured := capturePicker(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("ATC_SERVER", "")
	t.Setenv("ATC_TOKEN", cliTestToken)
	// Reserve a port, release it, and configure the server on it; the
	// start stub brings a server up there.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	configDir := filepath.Join(home, "config", "atc")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte("port = "+strconv.Itoa(port)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(api.ServerVersionHeader, "v0.0.0-started")
		_ = json.NewEncoder(w).Encode(api.Health{Status: "ok", Version: "v0.0.0-started"})
	})
	var startedWith service.Options
	prev := startLocalServer
	startLocalServer = func(_ context.Context, opts service.Options) error {
		startedWith = opts
		l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
		if err != nil {
			return err
		}
		srv := httptest.NewUnstartedServer(handler)
		_ = srv.Listener.Close()
		srv.Listener = l
		srv.Start()
		t.Cleanup(srv.Close)
		return nil
	}
	t.Cleanup(func() { startLocalServer = prev })

	if _, _, err := runCLI(t); err != nil {
		t.Fatal(err)
	}
	if startedWith.Config.Port != port {
		t.Errorf("lifecycle started with port %d, want %d", startedWith.Config.Port, port)
	}
	if captured.ServerVersion != "v0.0.0-started" {
		t.Errorf("picker opened against %q", captured.ServerVersion)
	}
}

// installFakeSSH puts an ssh on PATH that records its arguments, prints
// the scripted stdout and stderr, and exits with the scripted code.
func installFakeSSH(t *testing.T, stdout, stderr string, exit int) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	// The scripted output lives in files, not the environment: the test
	// checks that the token never reaches this process's environment.
	for name, content := range map[string]string{"stdout": stdout, "stderr": stderr} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" > \"$FAKE_SSH_DIR/args\"\ncat \"$FAKE_SSH_DIR/stdout\"\ncat \"$FAKE_SSH_DIR/stderr\" >&2\nexit \"$FAKE_SSH_EXIT\"\n"
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SSH_DIR", dir)
	t.Setenv("FAKE_SSH_EXIT", strconv.Itoa(exit))
	return argsFile
}

func TestRootRemoteBootstrapsOverSSH(t *testing.T) {
	forceTTY(t)
	captured := capturePicker(t)
	const token = "atc_remote-secret-token"
	argsFile := installFakeSSH(t, `{"url":"https://ws.tailnet.ts.net:7331","token":"`+token+`","version":"v9.9.9"}`+"\n", "starting atc.server\n", 0)

	_, stderr, err := runCLI(t, "--remote", "ws")
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "-- ws atc __bootstrap" {
		t.Errorf("bootstrap ssh args = %q", got)
	}
	if !strings.Contains(stderr, "starting atc.server") {
		t.Errorf("remote stderr not shown live: %q", stderr)
	}
	if captured.Target != "ws" || captured.ServerVersion != "v9.9.9" || captured.TransportLoss == nil || captured.Client == nil {
		t.Errorf("options = target %q server %q transportLoss set %v client %v", captured.Target, captured.ServerVersion, captured.TransportLoss != nil, captured.Client)
	}
	cmd, err := captured.Attach(api.Terminal{ID: "term-abcde", Status: api.TerminalRunning})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cmd.Path, "/ssh") {
		t.Errorf("attach executable = %q", cmd.Path)
	}
	want := []string{cmd.Args[0], "-tt", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "--", "ws", "atc", "terminal", "attach", "term-abcde"}
	if diff := cmp.Diff(want, cmd.Args); diff != "" {
		t.Errorf("attach args (-want +got):\n%s", diff)
	}
	// The token stays in picker memory: not in the attach child's argv or
	// environment, and not in this process's environment either.
	for _, arg := range cmd.Args {
		if strings.Contains(arg, token) {
			t.Errorf("token in attach argv: %v", cmd.Args)
		}
	}
	for _, kv := range append(cmd.Env, os.Environ()...) {
		if strings.Contains(kv, token) {
			t.Errorf("token in environment: %s", kv)
		}
	}
}

func TestRootRemoteBootstrapFailureEndsLaunch(t *testing.T) {
	forceTTY(t)
	captured := capturePicker(t)
	installFakeSSH(t, "", "tailnet exposure is not enabled on this machine: add `tailscale = true` to /home/u/.config/atc/config.toml\n", 1)
	_, stderr, err := runCLI(t, "--remote", "ws")
	if err == nil || !strings.Contains(err.Error(), "bootstrap on ws failed") || !strings.Contains(err.Error(), "tailscale = true") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(stderr, "tailnet exposure is not enabled") {
		t.Errorf("remote stderr not shown: %q", stderr)
	}
	if captured.Client != nil {
		t.Error("picker opened after a failed bootstrap")
	}
	if _, _, err := runCLI(t, "--remote", "ws\x07"); err == nil {
		t.Error("target with a control character accepted")
	}
}

func TestRemoteFlagIsRootOnlyAndBootstrapIsHidden(t *testing.T) {
	startTestServer(t)
	if _, _, err := runCLI(t, "terminal", "list", "--remote", "ws"); err == nil || !strings.Contains(err.Error(), "unknown flag") {
		t.Errorf("subcommand accepted --remote: %v", err)
	}
	stdout, _, err := runCLI(t, "--help")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stdout, "__bootstrap") || !strings.Contains(stdout, "--remote") {
		t.Errorf("root help:\n%s", stdout)
	}
}
