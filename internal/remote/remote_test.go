package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// scriptedExit stands in for *exec.ExitError: only the exit code matters
// to the classification.
type scriptedExit int

func (e scriptedExit) Error() string { return fmt.Sprintf("exit status %d", int(e)) }
func (e scriptedExit) ExitCode() int { return int(e) }

// scriptedSSH records the child it was asked to run, and what it was
// fed, and answers with the scripted output and exit.
type scriptedSSH struct {
	stdout, stderr string
	exit           error
	cmd            *exec.Cmd
	stdin          string
}

func (s *scriptedSSH) run(cmd *exec.Cmd) error {
	s.cmd = cmd
	if cmd.Stdin != nil {
		in, _ := io.ReadAll(cmd.Stdin)
		s.stdin = string(in)
	}
	_, _ = io.WriteString(cmd.Stdout, s.stdout)
	_, _ = io.WriteString(cmd.Stderr, s.stderr)
	return s.exit
}

func TestBootstrapDecodesStrictly(t *testing.T) {
	const good = `{"url":"https://ws.tail.ts.net:7331","token":"atc_secret","version":"v1.2.3","protocol":1}` + "\n"
	for name, tc := range map[string]struct {
		stdout, stderr string
		exit           error
		want           Bootstrap
		wantErr        string
	}{
		"valid":            {stdout: good, want: Bootstrap{URL: "https://ws.tail.ts.net:7331", Token: "atc_secret", Version: "v1.2.3", Protocol: 1}},
		"remote refusal":   {stderr: "tailnet exposure is not enabled; set tailscale = true\n", exit: scriptedExit(1), wantErr: "set tailscale = true"},
		"ssh failure":      {stderr: "ssh: connect to host ws port 22: Connection refused\n", exit: scriptedExit(255), wantErr: "ssh to ws failed"},
		"no atc":           {stderr: "bash: atc: command not found\n", exit: scriptedExit(127), wantErr: "could not be run"},
		"old atc":          {stderr: `atc: unknown command "__remote" for "atc"` + "\n", exit: scriptedExit(1), wantErr: "predates guided setup"},
		"newer field":      {stdout: `{"url":"https://h:1","token":"atc_secret","version":"v","protocol":1,"port":7331}`, want: Bootstrap{URL: "https://h:1", Token: "atc_secret", Version: "v", Protocol: 1}},
		"missing field":    {stdout: `{"url":"https://h:1","version":"v","protocol":1}`, wantErr: "returned no token"},
		"missing protocol": {stdout: `{"url":"https://h:1","token":"t","version":"v"}`, wantErr: "returned no protocol"},
		"not https":        {stdout: `{"url":"http://h:1","token":"t","version":"v","protocol":1}`, wantErr: "non-HTTPS url"},
		"trailing garbage": {stdout: good + `{"more":true}`, wantErr: "more than one JSON value"},
		"trailing bracket": {stdout: good + `]`, wantErr: "more than one JSON value"},
		"trailing brace":   {stdout: good + `}`, wantErr: "more than one JSON value"},
		"empty hostname":   {stdout: `{"url":"https://:443","token":"t","version":"v","protocol":1}`, wantErr: "non-HTTPS url"},
		"oversized":        {stdout: strings.Repeat(" ", maxBootstrapOutput) + good, wantErr: "more than"},
		"empty":            {stdout: "", wantErr: "unexpected output"},
	} {
		t.Run(name, func(t *testing.T) {
			script := &scriptedSSH{stdout: tc.stdout, stderr: tc.stderr, exit: tc.exit}
			ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir(), run: script.run}
			var stderr strings.Builder
			got, err := ssh.Bootstrap(context.Background(), "/home/u/.local/bin/atc", true, true, strings.NewReader(""), &stderr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				if stderr.String() != tc.stderr {
					t.Errorf("remote stderr shown = %q, want %q", stderr.String(), tc.stderr)
				}
				if strings.Contains(err.Error(), "atc_leaked") || strings.Contains(err.Error(), "atc_secret") {
					t.Errorf("error carries the token: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Bootstrap (-want +got):\n%s", diff)
			}
			if printed := fmt.Sprintf("%v %+v %s %#v", got, got, got, got); strings.Contains(printed, got.Token) {
				t.Errorf("formatting a Bootstrap prints the token: %s", printed)
			}
			wantArgs := []string{"/usr/bin/ssh", "-S", ssh.controlPath(), "-o", "ControlMaster=auto", "-o", "ControlPersist=60",
				"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-T", "--", "ws", "/home/u/.local/bin/atc", "__remote", "bootstrap", "--restart", "--tailscale"}
			if diff := cmp.Diff(wantArgs, script.cmd.Args); diff != "" {
				t.Errorf("argv (-want +got):\n%s", diff)
			}
			if script.cmd.Env != nil {
				t.Errorf("bootstrap child got an explicit environment: %v", script.cmd.Env)
			}
		})
	}
}

// Every other remote command is one argv over the shared connection,
// with the executable discovery named quoted for the login shell.
func TestRemoteCommandShapes(t *testing.T) {
	prefix := func(ssh *SSH) []string {
		return []string{"/usr/bin/ssh", "-S", ssh.controlPath(), "-o", "ControlMaster=auto", "-o", "ControlPersist=60",
			"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-T", "--", "ws"}
	}
	ctx := context.Background()
	var stderr strings.Builder

	t.Run("discover", func(t *testing.T) {
		script := &scriptedSSH{stdout: "os=Linux\narch=x86_64\nhome=/home/u\npath=/usr/local/bin/atc\ncandidate=/home/u/.local/bin/atc\nunit=/srv/atc/atc\n"}
		ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir(), run: script.run}
		got, err := ssh.Discover(ctx, &stderr)
		if err != nil {
			t.Fatal(err)
		}
		want := Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Path: "/usr/local/bin/atc", Candidate: "/home/u/.local/bin/atc", Unit: "/srv/atc/atc"}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Discovery (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(append(prefix(ssh), "sh"), script.cmd.Args); diff != "" {
			t.Errorf("argv (-want +got):\n%s", diff)
		}
		if script.stdin != discoverScript {
			t.Errorf("stdin = %q, want the discovery script", script.stdin)
		}
	})
	t.Run("inspect", func(t *testing.T) {
		script := &scriptedSSH{stdout: `{"executable":{"path":"/home/u/my bin/atc","version":"v1.0.0","channel":"stable","protocol":1,"writable":true,"future":true},"server":{},"tailnet":{}}`}
		ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir(), run: script.run}
		got, err := ssh.Inspect(ctx, "/home/u/my bin/atc", &stderr)
		if err != nil {
			t.Fatal(err)
		}
		want := Inspection{Executable: Executable{Path: "/home/u/my bin/atc", Version: "v1.0.0", Channel: "stable", Protocol: 1, Writable: true}}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("Inspection (-want +got):\n%s", diff)
		}
		if diff := cmp.Diff(append(prefix(ssh), "'/home/u/my bin/atc'", "__remote", "inspect"), script.cmd.Args); diff != "" {
			t.Errorf("argv (-want +got):\n%s", diff)
		}
		script.stdout = `{"executable":{"path":"","protocol":0}}`
		if _, err := ssh.Inspect(ctx, "/x/atc", &stderr); err == nil || !strings.Contains(err.Error(), "did not identify") {
			t.Errorf("unidentified inspection = %v", err)
		}
		script.stdout, script.stderr, script.exit = "", `atc: unknown command "__remote" for "atc"`+"\n", scriptedExit(1)
		if _, err := ssh.Inspect(ctx, "/x/atc", &stderr); !errors.Is(err, ErrPredatesSetup) {
			t.Errorf("old atc = %v, want ErrPredatesSetup", err)
		}
	})
	t.Run("stage promote remove", func(t *testing.T) {
		script := &scriptedSSH{stdout: "/home/u/.local/bin/.atc-setup-Ab12Cd\n"}
		ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir(), run: script.run}
		staged, err := ssh.Stage(ctx, "/home/u/.local/bin", []byte("ELF bytes"), &stderr)
		if err != nil || staged != "/home/u/.local/bin/.atc-setup-Ab12Cd" {
			t.Fatalf("Stage = %q, %v", staged, err)
		}
		if script.stdin != "ELF bytes" {
			t.Errorf("stdin = %q, want the executable bytes", script.stdin)
		}
		want := append(prefix(ssh), "sh", "-c", "'"+stageScript+"'", "sh", "/home/u/.local/bin")
		if diff := cmp.Diff(want, script.cmd.Args); diff != "" {
			t.Errorf("stage argv (-want +got):\n%s", diff)
		}
		if strings.Contains(stageScript, "'") {
			t.Error("the stage script must contain no single quote: it rides inside one pair")
		}
		if strings.Contains(stageScript, "rm ") {
			t.Error("staging must never remove another run's file")
		}
		script.stdout = "/elsewhere/.atc-setup-x\n"
		if _, err := ssh.Stage(ctx, "/home/u/.local/bin", nil, &stderr); err == nil || !strings.Contains(err.Error(), "unexpected output") {
			t.Errorf("staged outside the directory = %v", err)
		}
		script.stdout = ""
		if err := ssh.Promote(ctx, staged, "/home/u/.local/bin/atc", &stderr); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(append(prefix(ssh), "mv", "-f", staged, "/home/u/.local/bin/atc"), script.cmd.Args); diff != "" {
			t.Errorf("promote argv (-want +got):\n%s", diff)
		}
		if err := ssh.Remove(ctx, staged, &stderr); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(append(prefix(ssh), "rm", "-f", staged), script.cmd.Args); diff != "" {
			t.Errorf("remove argv (-want +got):\n%s", diff)
		}
	})
	t.Run("configure", func(t *testing.T) {
		script := &scriptedSSH{}
		ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir(), run: script.run}
		if err := ssh.Configure(ctx, "/home/u/.local/bin/atc", &stderr); err != nil {
			t.Fatal(err)
		}
		if diff := cmp.Diff(append(prefix(ssh), "/home/u/.local/bin/atc", "__remote", "configure", "--tailscale"), script.cmd.Args); diff != "" {
			t.Errorf("configure argv (-want +got):\n%s", diff)
		}
	})
}

func TestShellQuote(t *testing.T) {
	for arg, want := range map[string]string{
		"atc":                    "atc",
		"/home/u/.local/bin/atc": "/home/u/.local/bin/atc",
		"/home/u/my bin/atc":     "'/home/u/my bin/atc'",
		"it's":                   `'it'\''s'`,
		"-flag":                  "'-flag'",
		"":                       "''",
		"$HOME/atc":              "'$HOME/atc'",
		"~/atc":                  "'~/atc'",
	} {
		if got := shellQuote(arg); got != want {
			t.Errorf("shellQuote(%q) = %s, want %s", arg, got, want)
		}
	}
}

func TestParseDiscovery(t *testing.T) {
	for name, tc := range map[string]struct {
		out     string
		want    Discovery
		wantErr bool
	}{
		"nothing installed": {out: "os=Darwin\narch=arm64\nhome=/Users/u\n", want: Discovery{OS: "Darwin", Arch: "arm64", Home: "/Users/u"}},
		"off-path install":  {out: "os=Linux\narch=aarch64\nhome=/home/u\ncandidate=/home/u/.local/bin/atc\ncandidate=/opt/homebrew/bin/atc\n", want: Discovery{OS: "Linux", Arch: "aarch64", Home: "/home/u", Candidate: "/home/u/.local/bin/atc"}},
		"unit only":         {out: "os=Linux\narch=x86_64\nhome=/home/u\nunit=/srv/atc/atc\n", want: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Unit: "/srv/atc/atc"}},
		"relative ignored":  {out: "os=Linux\narch=x86_64\nhome=/home/u\npath=atc\ncandidate=bin/atc\nunit=atc\n", want: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u"}},
		"noise ignored":     {out: "Welcome!\nos=Linux\narch=x86_64\nhome=/home/u\n", want: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u"}},
		"no platform":       {out: "home=/home/u\n", wantErr: true},
		"no home":           {out: "os=Linux\narch=x86_64\nhome=\n", wantErr: true},
		"empty":             {out: "", wantErr: true},
	} {
		got, err := parseDiscovery("ws", []byte(tc.out))
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: err = %v", name, err)
			continue
		}
		if diff := cmp.Diff(tc.want, got); diff != "" {
			t.Errorf("%s: Discovery (-want +got):\n%s", name, diff)
		}
	}
	d := Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Path: "/usr/local/bin/atc", Candidate: "/home/u/.local/bin/atc"}
	if d.Executable() != "/usr/local/bin/atc" || d.InstallDir() != "/home/u/.local/bin" {
		t.Errorf("Executable %q InstallDir %q", d.Executable(), d.InstallDir())
	}
	d.Path = ""
	if d.Executable() != "/home/u/.local/bin/atc" {
		t.Errorf("Executable without PATH hit = %q", d.Executable())
	}
	for _, tc := range []struct{ os, arch, goos, goarch string }{
		{"Darwin", "arm64", "darwin", "arm64"}, {"Linux", "x86_64", "linux", "amd64"}, {"Linux", "aarch64", "linux", "arm64"}, {"Linux", "arm64", "linux", "arm64"},
	} {
		goos, goarch, err := (Discovery{OS: tc.os, Arch: tc.arch}).Platform()
		if err != nil || goos != tc.goos || goarch != tc.goarch {
			t.Errorf("Platform(%s/%s) = %s/%s, %v", tc.os, tc.arch, goos, goarch, err)
		}
	}
	if _, _, err := (Discovery{OS: "Darwin", Arch: "x86_64"}).Platform(); err == nil || !strings.Contains(err.Error(), "Darwin/x86_64") {
		t.Errorf("unsupported platform = %v", err)
	}
}

func TestBootstrapRejectsBadTargets(t *testing.T) {
	for _, target := range []string{"", "ws\n", "ws\x1b[2J"} {
		if _, err := NewSSH(target); err == nil {
			t.Errorf("target %q accepted", target)
		}
	}
	if err := validateTarget("user@ws.example:2222"); err != nil {
		t.Errorf("ordinary target refused: %v", err)
	}
}

func TestAttachCommandAndTransportLoss(t *testing.T) {
	ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir()}
	cmd := ssh.AttachCommand(context.Background(), "atc", "term-abcde")
	want := []string{"/usr/bin/ssh", "-S", ssh.controlPath(), "-o", "ControlMaster=auto", "-o", "ControlPersist=60",
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-tt", "-o", "LogLevel=ERROR", "--", "ws", "atc", "terminal", "attach", "term-abcde"}
	if diff := cmp.Diff(want, cmd.Args); diff != "" {
		t.Errorf("argv (-want +got):\n%s", diff)
	}
	// The attach runs whatever executable setup settled on, quoted.
	if got := ssh.AttachCommand(context.Background(), "/home/u/my bin/atc", "term-abcde").Args[16]; got != "'/home/u/my bin/atc'" {
		t.Errorf("attach executable = %s, want the discovered one quoted", got)
	}
	if cmd.Env != nil {
		t.Errorf("attach child got an explicit environment: %v", cmd.Env)
	}
	if !IsTransportLoss(scriptedExit(255)) || IsTransportLoss(scriptedExit(1)) || IsTransportLoss(nil) || IsTransportLoss(errors.New("x")) {
		t.Error("transport loss is exit 255 and nothing else")
	}
}

func TestPrivateSSHDirectories(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	first, err := NewSSH("ws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Close() })
	second, err := NewSSH("ws")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = second.Close() })
	if first.controlDir == second.controlDir {
		t.Fatal("pickers share a control directory")
	}
	for _, ssh := range []*SSH{first, second} {
		info, err := os.Stat(ssh.controlDir)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o700 || len(ssh.controlPath()) >= 104 {
			t.Errorf("control directory permissions %v or path length %d", info.Mode().Perm(), len(ssh.controlPath()))
		}
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(first.controlDir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("closed directory remains: %v", err)
	}
	if _, err := os.Stat(second.controlDir); err != nil {
		t.Errorf("closing one picker affected another: %v", err)
	}
}

func TestClosePrivateSSH(t *testing.T) {
	for _, name := range []string{"active", "absent", "expired during close", "shutdown failed"} {
		t.Run(name, func(t *testing.T) {
			ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir()}
			if name != "absent" {
				if err := os.WriteFile(ssh.controlPath(), nil, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			calls := 0
			ssh.run = func(cmd *exec.Cmd) error {
				calls++
				want := []string{"/usr/bin/ssh", "-S", ssh.controlPath(), "-O", "exit", "--", "ws"}
				if diff := cmp.Diff(want, cmd.Args); diff != "" {
					t.Errorf("shutdown command (-want +got):\n%s", diff)
				}
				if name == "expired during close" {
					if err := os.Remove(ssh.controlPath()); err != nil {
						t.Fatal(err)
					}
					return scriptedExit(255)
				}
				if name == "shutdown failed" {
					return context.DeadlineExceeded
				}
				return nil
			}
			err := ssh.Close()
			if (err != nil) != (name == "shutdown failed") {
				t.Errorf("Close = %v", err)
			}
			if _, err := os.Stat(ssh.controlDir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("control directory remains: %v", err)
			}
			if err := ssh.Close(); err != nil {
				t.Errorf("second Close = %v", err)
			}
			wantCalls := 1
			if name == "absent" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Errorf("shutdown calls = %d, want %d", calls, wantCalls)
			}
		})
	}
}

// The discovery script runs under a real sh with a private HOME: the
// systemd unit's ExecStart yields the server's executable, and the usual
// places are searched in order.
func TestDiscoverScriptUnderSh(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	unitDir := filepath.Join(home, ".config", "systemd", "user")
	if err := os.MkdirAll(unitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	unit := "[Unit]\nDescription=ATC\n\n[Service]\nExecStart=\"/srv/atc/atc\" \"server\" \"run\" \"--tailscale\"\n"
	if err := os.WriteFile(filepath.Join(unitDir, "atc.server.service"), []byte(unit), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, ".local", "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "atc"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh")
	cmd.Stdin = strings.NewReader(discoverScript)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("sh: %v", err)
	}
	got, err := parseDiscovery("ws", out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if got.Path != "" || got.Candidate != filepath.Join(home, ".local", "bin", "atc") || got.Unit != "/srv/atc/atc" || got.Home != home {
		t.Errorf("Discovery = %+v\n%s", got, out)
	}
}
