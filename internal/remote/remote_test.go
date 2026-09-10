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

// scriptedSSH records the child it was asked to run and answers with the
// scripted output and exit.
type scriptedSSH struct {
	stdout, stderr string
	exit           error
	cmd            *exec.Cmd
}

func (s *scriptedSSH) run(cmd *exec.Cmd) error {
	s.cmd = cmd
	_, _ = io.WriteString(cmd.Stdout, s.stdout)
	_, _ = io.WriteString(cmd.Stderr, s.stderr)
	return s.exit
}

func TestBootstrapDecodesStrictly(t *testing.T) {
	const good = `{"url":"https://ws.tail.ts.net:7331","token":"atc_secret","version":"v1.2.3"}` + "\n"
	for name, tc := range map[string]struct {
		stdout, stderr string
		exit           error
		want           Bootstrap
		wantErr        string
	}{
		"valid":            {stdout: good, want: Bootstrap{URL: "https://ws.tail.ts.net:7331", Token: "atc_secret", Version: "v1.2.3"}},
		"remote refusal":   {stderr: "tailnet exposure is not enabled; set tailscale = true\n", exit: scriptedExit(1), wantErr: "set tailscale = true"},
		"ssh failure":      {stderr: "ssh: connect to host ws port 22: Connection refused\n", exit: scriptedExit(255), wantErr: "ssh to ws failed"},
		"no atc":           {stderr: "bash: atc: command not found\n", exit: scriptedExit(127), wantErr: "not installed on ws"},
		"old atc":          {stderr: `atc: unknown command "__bootstrap" for "atc"` + "\n", exit: scriptedExit(1), wantErr: "too old"},
		"unknown field":    {stdout: `{"url":"https://h:1","token":"atc_leaked","version":"v","port":7331}`, wantErr: "unexpected output"},
		"missing field":    {stdout: `{"url":"https://h:1","version":"v"}`, wantErr: "returned no token"},
		"not https":        {stdout: `{"url":"http://h:1","token":"t","version":"v"}`, wantErr: "non-HTTPS url"},
		"trailing garbage": {stdout: good + `{"more":true}`, wantErr: "more than one JSON value"},
		"trailing bracket": {stdout: good + `]`, wantErr: "more than one JSON value"},
		"trailing brace":   {stdout: good + `}`, wantErr: "more than one JSON value"},
		"empty hostname":   {stdout: `{"url":"https://:443","token":"t","version":"v"}`, wantErr: "non-HTTPS url"},
		"oversized":        {stdout: strings.Repeat(" ", maxBootstrapOutput) + good, wantErr: "more than"},
		"empty":            {stdout: "", wantErr: "unexpected output"},
	} {
		t.Run(name, func(t *testing.T) {
			script := &scriptedSSH{stdout: tc.stdout, stderr: tc.stderr, exit: tc.exit}
			ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir(), run: script.run}
			var stderr strings.Builder
			got, err := ssh.Bootstrap(context.Background(), strings.NewReader(""), &stderr)
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
				"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-T", "--", "ws", "atc", "__bootstrap"}
			if diff := cmp.Diff(wantArgs, script.cmd.Args); diff != "" {
				t.Errorf("argv (-want +got):\n%s", diff)
			}
			if script.cmd.Env != nil {
				t.Errorf("bootstrap child got an explicit environment: %v", script.cmd.Env)
			}
		})
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
	cmd := ssh.AttachCommand(context.Background(), "term-abcde")
	want := []string{"/usr/bin/ssh", "-S", ssh.controlPath(), "-o", "ControlMaster=auto", "-o", "ControlPersist=60",
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "-tt", "-o", "LogLevel=ERROR", "--", "ws", "atc", "terminal", "attach", "term-abcde"}
	if diff := cmp.Diff(want, cmd.Args); diff != "" {
		t.Errorf("argv (-want +got):\n%s", diff)
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
