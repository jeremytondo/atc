package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/jeremytondo/atc/internal/remote"
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

func connectionNames(opts tui.Options) []string {
	names := make([]string, len(opts.Connections))
	for i, c := range opts.Connections {
		names[i] = c.Name
	}
	return names
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
	if captured.Connections != nil {
		t.Error("picker opened without a TTY")
	}
}

func TestRootOpensLocalPicker(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
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
	if diff := cmp.Diff([]string{"Local"}, connectionNames(*captured)); diff != "" {
		t.Fatalf("connections (-want +got):\n%s", diff)
	}
	if captured.ClientVersion != version.String() || !captured.Connections[0].Local || captured.Open == nil || captured.Save == nil {
		t.Errorf("options = %+v", *captured)
	}
	session, err := captured.Connections[0].Connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if started {
		t.Error("a healthy server was started again")
	}
	if session.ServerVersion != "v0.0.0-test" || session.TransportLoss != nil {
		t.Errorf("session = server %q transportLoss set %v", session.ServerVersion, session.TransportLoss != nil)
	}
	spaces, err := session.Client.Spaces(context.Background())
	if err != nil || len(spaces) != 1 || !spaces[0].IsDefault {
		t.Errorf("picker client Spaces = %+v, %v; want the Default space", spaces, err)
	}
	// The private state has no managed zmx runtime selected. The picker
	// learns why per attach attempt, instead of the launch failing.
	if _, err := session.Attach(context.Background(), api.Terminal{ID: "term-abcde", Status: api.TerminalRunning}); err == nil || !strings.Contains(err.Error(), "zmx") {
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

// A stopped local server is started through the lifecycle when Local
// connects, and the session opens once it answers. The lifecycle's
// report never reaches the terminal — the picker owns it by then — and
// its first-run notice rides on the session as one line.
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
		w.Header().Set(api.ProtocolHeader, strconv.Itoa(api.Protocol))
		_ = json.NewEncoder(w).Encode(api.Health{Status: "ok", Version: "v0.0.0-started", Protocol: api.Protocol})
	})
	var startedWith service.Options
	prev := startLocalServer
	startLocalServer = func(_ context.Context, opts service.Options) error {
		startedWith = opts
		_, _ = fmt.Fprintf(opts.Stderr, "registered atc.server (unit)\nundo at any time with `atc server uninstall`\n")
		_, _ = fmt.Fprintf(opts.Stdout, "started atc.server\n  api: http://127.0.0.1:%d\n", port)
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

	var stdout, stderr strings.Builder
	if err := run(context.Background(), nil, strings.NewReader(""), &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	if startedWith.Config.Port != 0 {
		t.Fatal("the server was started before the picker opened")
	}
	session, err := captured.Connections[0].Connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if startedWith.Config.Port != port {
		t.Errorf("lifecycle started with port %d, want %d", startedWith.Config.Port, port)
	}
	if session.ServerVersion != "v0.0.0-started" {
		t.Errorf("session opened against %q", session.ServerVersion)
	}
	if stdout.String() != "" || stderr.String() != "" {
		t.Errorf("the lifecycle wrote to the picker's terminal:\nstdout: %q\nstderr: %q", stdout.String(), stderr.String())
	}
	if want := "registered atc.server (unit); undo at any time with `atc server uninstall`"; session.Notice != want {
		t.Errorf("session notice = %q, want %q", session.Notice, want)
	}
}

// When the start fails, what the lifecycle said on stderr (the last
// logs) follows the error so the Connections screen can show it.
func TestRootReportsFailedLocalStart(t *testing.T) {
	forceTTY(t)
	captured := capturePicker(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	t.Setenv("ATC_SERVER", "")
	t.Setenv("ATC_TOKEN", cliTestToken)
	// A reserved, released port: nothing answers there, so Local starts.
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
	prev := startLocalServer
	startLocalServer = func(_ context.Context, opts service.Options) error {
		_, _ = fmt.Fprintln(opts.Stderr, "last log lines:")
		_, _ = fmt.Fprintln(opts.Stderr, "  bind: address already in use")
		return errors.New("atc.server did not become healthy")
	}
	t.Cleanup(func() { startLocalServer = prev })

	if _, _, err := runCLI(t); err != nil {
		t.Fatal(err)
	}
	_, err = captured.Connections[0].Connector.Connect(context.Background())
	want := "atc.server did not become healthy\nlast log lines:\n  bind: address already in use"
	if err == nil || err.Error() != want {
		t.Errorf("Connect error = %v, want %q", err, want)
	}
}

// A plain launch opens every saved connection beside Local, offers the
// ssh configuration's aliases to Add, and saves choices to the
// connections file. Nothing is saved by a launch itself.
func TestRootOpensSavedConnections(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	configHome := filepath.Join(home, "config")
	t.Setenv("XDG_CONFIG_HOME", configHome)
	startTestServer(t)
	forceTTY(t)
	captured := capturePicker(t)
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host ws\n  HostName ws.example\nHost devbox\nHost *.example\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	connectionsFile := filepath.Join(configHome, "atc", "connections.json")

	if _, _, err := runCLI(t); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"Local"}, connectionNames(*captured)); diff != "" {
		t.Errorf("first launch (-want +got):\n%s", diff)
	}
	if diff := cmp.Diff([]string{"ws", "devbox"}, captured.Aliases); diff != "" {
		t.Errorf("aliases (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(connectionsFile); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a launch wrote the connections file: %v", err)
	}
	// The picker saves a choice; the next launch opens it.
	if err := captured.Save([]string{"ws"}); err != nil {
		t.Fatal(err)
	}
	if saved, err := remote.LoadSaved(connectionsFile); err != nil || !cmp.Equal(saved, []string{"ws"}) {
		t.Errorf("saved = %v, %v", saved, err)
	}
	if _, _, err := runCLI(t); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff([]string{"Local", "ws"}, connectionNames(*captured)); diff != "" {
		t.Errorf("second launch (-want +got):\n%s", diff)
	}
	if captured.Connections[1].Local {
		t.Error("a saved remote is marked Local")
	}
	// Opened connectors are the same kind as the saved ones; nothing
	// touches ssh until a connection is attempted.
	if c, ok := captured.Open("devbox").(*remoteConnector); !ok || c.target != "devbox" || c.ssh != nil {
		t.Errorf("Open(devbox) = %#v", captured.Open("devbox"))
	}
	if err := captured.Connections[1].Connector.Close(); err != nil {
		t.Errorf("closing an unopened connection: %v", err)
	}
}

// fakeRemote scripts the target of a remote launch: what discovery
// prints, what inspection reports, and what the bootstrap answers.
type fakeRemote struct {
	discovery  string
	inspection remote.Inspection
	bootstrap  string // stdout of the bootstrap command
	stderr     string // stderr of the bootstrap command
	exit       int    // exit code of the bootstrap command
	// sshErr and sshExit make every command fail the way ssh itself
	// does, before anything runs on the target.
	sshErr  string
	sshExit int
}

// readyRemote is a target that needs nothing: compatible executable on
// PATH, healthy exposed server.
func readyRemote(token string) fakeRemote {
	return fakeRemote{
		discovery: "os=Linux\narch=x86_64\nhome=/home/u\npath=/home/u/.local/bin/atc\n",
		inspection: remote.Inspection{
			Executable: remote.Executable{Path: "/home/u/.local/bin/atc", Version: "v9.9.9", Channel: "stable", Protocol: api.Protocol, Writable: true},
			Server:     remote.Server{Executable: "/home/u/.local/bin/atc", Supervised: true, Responding: true, Healthy: true, Version: "v9.9.9", Protocol: api.Protocol},
			Tailnet:    remote.Tailnet{Configured: true},
		},
		bootstrap: fmt.Sprintf(`{"url":"https://ws.tailnet.ts.net:7331","token":%q,"version":"v9.9.9","protocol":%d}`+"\n", token, api.Protocol),
		stderr:    "starting atc.server\n",
	}
}

// installFakeSSH puts an ssh on PATH that records its arguments and
// answers each remote command from the scripted target: the discovery
// script on stdin, inspection, and the bootstrap. The bootstrap's
// arguments are what the test reads back.
func installFakeSSH(t *testing.T, target fakeRemote) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	inspection, err := json.Marshal(target.inspection)
	if err != nil {
		t.Fatal(err)
	}
	// The scripted output lives in files, not the environment: the test
	// checks that the token never reaches this process's environment.
	for name, content := range map[string]string{"discovery": target.discovery, "inspection": string(inspection), "stdout": target.bootstrap, "stderr": target.stderr, "ssh-err": target.sshErr} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	script := `#!/bin/sh
if [ "$3" = "-O" ]; then
  printf '%s\n' "$*" > "$FAKE_SSH_DIR/close-args"
  rm -f "$2"
  exit 0
fi
if [ "$FAKE_SSH_EXIT_ALL" != "0" ]; then
  cat "$FAKE_SSH_DIR/ssh-err" >&2
  exit "$FAKE_SSH_EXIT_ALL"
fi
: > "$2"
case "$*" in
  *" -- ws sh") cat >/dev/null; cat "$FAKE_SSH_DIR/discovery"; exit 0 ;;
  *" __remote inspect") cat "$FAKE_SSH_DIR/inspection"; exit 0 ;;
  *" __remote bootstrap"*)
    printf '%s\n' "$*" > "$FAKE_SSH_DIR/args"
    cat "$FAKE_SSH_DIR/stdout"
    cat "$FAKE_SSH_DIR/stderr" >&2
    exit "$FAKE_SSH_EXIT" ;;
esac
echo "unexpected: $*" >&2
exit 2
`
	if err := os.WriteFile(filepath.Join(dir, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_SSH_DIR", dir)
	t.Setenv("FAKE_SSH_EXIT", strconv.Itoa(target.exit))
	t.Setenv("FAKE_SSH_EXIT_ALL", strconv.Itoa(target.sshExit))
	return argsFile
}

// verifyRemote stands in for the live readiness check over the tailnet.
func verifyRemote(t *testing.T, err error) {
	t.Helper()
	prev := verifyRemoteHealth
	verifyRemoteHealth = func(context.Context, remote.Bootstrap) (api.Health, error) { return api.Health{Status: "ok"}, err }
	t.Cleanup(func() { verifyRemoteHealth = prev })
}

// `atc --remote` keeps its single-machine behavior: the guided setup runs
// on the plain terminal, the picker opens on that one proven machine, and
// nothing is saved.
func TestRootRemoteBootstrapsOverSSH(t *testing.T) {
	forceTTY(t)
	captured := capturePicker(t)
	verifyRemote(t, nil)
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	const token = "atc_remote-secret-token"
	argsFile := installFakeSSH(t, readyRemote(token))

	stdout, stderr, err := runCLI(t, "--remote", "ws")
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	bootstrapArgs := strings.Fields(string(args))
	if len(bootstrapArgs) != 16 || bootstrapArgs[0] != "-S" || !strings.HasSuffix(string(args), "-T -- ws /home/u/.local/bin/atc __remote bootstrap\n") {
		t.Fatalf("bootstrap ssh args = %q", args)
	}
	if !strings.Contains(stderr, "starting atc.server") {
		t.Errorf("remote stderr not shown live: %q", stderr)
	}
	if strings.Contains(stdout, "[y/N]") {
		t.Errorf("a ready target was asked to approve changes:\n%s", stdout)
	}
	if diff := cmp.Diff([]string{"ws"}, connectionNames(*captured)); diff != "" {
		t.Fatalf("connections (-want +got):\n%s", diff)
	}
	if captured.Aliases != nil || captured.Save != nil || captured.Open != nil || captured.Connections[0].Local {
		t.Errorf("single-machine picker manages connections: %+v", *captured)
	}
	// The picker's first attempt gets the session setup proved; no
	// second probe runs.
	session, err := captured.Connections[0].Connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if session.ServerVersion != "v9.9.9" || session.TransportLoss == nil || session.Client == nil {
		t.Errorf("session = server %q transportLoss set %v client %v", session.ServerVersion, session.TransportLoss != nil, session.Client)
	}
	cmd, err := session.Attach(context.Background(), api.Terminal{ID: "term-abcde", Status: api.TerminalRunning})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(cmd.Path, "/ssh") {
		t.Errorf("attach executable = %q", cmd.Path)
	}
	controlPath := bootstrapArgs[1]
	// The attach runs the executable discovery found, not a PATH lookup.
	want := []string{cmd.Args[0], "-S", controlPath, "-o", "ControlMaster=auto", "-o", "ControlPersist=8h",
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-tt", "-o", "LogLevel=ERROR", "--", "ws", "/home/u/.local/bin/atc", "terminal", "attach", "term-abcde"}
	if diff := cmp.Diff(want, cmd.Args); diff != "" {
		t.Errorf("attach args (-want +got):\n%s", diff)
	}
	checkSSHClosed(t, argsFile, controlPath)
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
	if saved, err := remote.LoadSaved(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "atc", "connections.json")); err != nil || saved != nil {
		t.Errorf("--remote saved %v, %v", saved, err)
	}
}

func TestRootRemoteBootstrapFailureEndsLaunch(t *testing.T) {
	forceTTY(t)
	captured := capturePicker(t)
	target := readyRemote("t")
	target.bootstrap, target.stderr, target.exit = "", "tailnet exposure is not enabled on this machine: add `tailscale = true` to /home/u/.config/atc/config.toml\n", 1
	argsFile := installFakeSSH(t, target)
	_, stderr, err := runCLI(t, "--remote", "ws")
	if err == nil || !strings.Contains(err.Error(), "bootstrap on ws failed") || !strings.Contains(err.Error(), "tailscale = true") {
		t.Fatalf("err = %v", err)
	}
	if !strings.Contains(stderr, "tailnet exposure is not enabled") {
		t.Errorf("remote stderr not shown: %q", stderr)
	}
	if captured.Connections != nil {
		t.Error("picker opened after a failed bootstrap")
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	checkSSHClosed(t, argsFile, strings.Fields(string(args))[1])
	if _, _, err := runCLI(t, "--remote", "ws\x07"); err == nil {
		t.Error("target with a control character accepted")
	}
}

func checkSSHClosed(t *testing.T, argsFile, controlPath string) {
	t.Helper()
	args, err := os.ReadFile(filepath.Join(filepath.Dir(argsFile), "close-args"))
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-S", controlPath, "-O", "exit", "--", "ws"}
	if diff := cmp.Diff(want, strings.Fields(string(args))); diff != "" {
		t.Errorf("close args (-want +got):\n%s", diff)
	}
	if _, err := os.Stat(filepath.Dir(controlPath)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("private control directory remains: %v", err)
	}
}

func TestRootRemoteClosesSSHAfterCancelledPicker(t *testing.T) {
	forceTTY(t)
	verifyRemote(t, nil)
	argsFile := installFakeSSH(t, readyRemote("test"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	previous := runPicker
	t.Cleanup(func() { runPicker = previous })
	controlPath := ""
	runPicker = func(ctx context.Context, opts tui.Options) error {
		session, err := opts.Connections[0].Connector.Connect(ctx)
		if err != nil {
			t.Fatal(err)
		}
		cmd, err := session.Attach(ctx, api.Terminal{ID: "term-test"})
		if err != nil {
			t.Fatal(err)
		}
		controlPath = cmd.Args[2]
		if _, err := os.Stat(controlPath); err != nil {
			t.Fatalf("connection closed before picker returned: %v", err)
		}
		cancel()
		return ctx.Err()
	}
	root := newRootCmd()
	root.SetArgs([]string{"--remote", "ws"})
	if err := root.ExecuteContext(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled picker = %v", err)
	}
	checkSSHClosed(t, argsFile, controlPath)
}

// A saved connection's unattended attempt runs the same discovery and
// bootstrap over batch-mode ssh, applying nothing; the interactive setup
// then applies what the machine needs on the streams the picker hands it.
func TestRemoteConnectorConnectsWithoutAPerson(t *testing.T) {
	verifyRemote(t, nil)
	argsFile := installFakeSSH(t, readyRemote("atc_secret"))
	connector := newRemoteConnector("ws")
	session, err := connector.Connect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	args, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), " -o BatchMode=yes -o ConnectTimeout=15 -T -- ws /home/u/.local/bin/atc __remote bootstrap") {
		t.Errorf("unattended bootstrap args = %q", args)
	}
	if session.ServerVersion != "v9.9.9" || session.TransportLoss == nil {
		t.Errorf("session = %+v", session)
	}
	cmd, err := session.Attach(context.Background(), api.Terminal{ID: "term-1"})
	if err != nil || strings.Contains(strings.Join(cmd.Args, " "), "BatchMode") {
		t.Errorf("attach = %v, %v; want an interactive command", cmd.Args, err)
	}
	if err := connector.Close(); err != nil {
		t.Fatal(err)
	}
	checkSSHClosed(t, argsFile, cmd.Args[2])

	// A target needing changes is reported with its plan, never changed;
	// the interactive setup applies them after the person's answer.
	stale := readyRemote("atc_secret")
	stale.inspection.Server.Protocol, stale.inspection.Server.Healthy = api.Protocol+1, false
	installFakeSSH(t, stale)
	connector = newRemoteConnector("ws")
	_, err = connector.Connect(context.Background())
	if !errors.Is(err, tui.ErrSetupRequired) || !strings.Contains(err.Error(), "restart it") || strings.Contains(err.Error(), "setup required: setup required") {
		t.Errorf("stale server = %v", err)
	}
	var stdout, stderr strings.Builder
	session, err = connector.Setup(context.Background(), strings.NewReader("y\n"), &stdout, &stderr)
	if err != nil || session.ServerVersion != "v9.9.9" {
		t.Fatalf("Setup = %+v, %v\n%s", session, err, stdout.String())
	}
	if !strings.Contains(stdout.String(), "[y/N]") || !strings.Contains(stderr.String(), "starting atc.server") {
		t.Errorf("setup streams: stdout %q stderr %q", stdout.String(), stderr.String())
	}
	_ = connector.Close()
}

// classify maps the remote package's outcomes onto the picker's actions
// and keeps the remote package's words.
func TestClassifyRemoteOutcomes(t *testing.T) {
	for name, tc := range map[string]struct {
		err    error
		login  bool
		update bool
	}{
		"login":       {err: fmt.Errorf("ssh to ws %w: Permission denied", remote.ErrLoginRequired), login: true},
		"setup":       {err: fmt.Errorf("%w:\nws needs setup", remote.ErrSetupRequired), update: true},
		"declined":    {err: fmt.Errorf("%w; nothing was changed on ws", remote.ErrDeclined), update: true},
		"unreachable": {err: errors.New("ssh to ws failed (exit 255): Connection refused")},
	} {
		t.Run(name, func(t *testing.T) {
			got := classify(tc.err)
			if errors.Is(got, tui.ErrLoginRequired) != tc.login || errors.Is(got, tui.ErrSetupRequired) != tc.update || !errors.Is(got, tc.err) {
				t.Errorf("classify(%v) = %v", tc.err, got)
			}
			if got.Error() != tc.err.Error() {
				t.Errorf("classify changed the words: %q", got.Error())
			}
		})
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
	if strings.Contains(stdout, "__remote") || !strings.Contains(stdout, "--remote") {
		t.Errorf("root help:\n%s", stdout)
	}
}
