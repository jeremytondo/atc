package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
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
		"unknown field":    {stdout: `{"url":"https://h:1","token":"t","version":"v","port":7331}`, wantErr: "unexpected output"},
		"missing field":    {stdout: `{"url":"https://h:1","version":"v"}`, wantErr: "returned no token"},
		"not https":        {stdout: `{"url":"http://h:1","token":"t","version":"v"}`, wantErr: "non-HTTPS url"},
		"trailing garbage": {stdout: good + `{"more":true}`, wantErr: "more than one JSON value"},
		"oversized":        {stdout: strings.Repeat(" ", maxBootstrapOutput) + good, wantErr: "more than"},
		"empty":            {stdout: "", wantErr: "unexpected output"},
	} {
		t.Run(name, func(t *testing.T) {
			script := &scriptedSSH{stdout: tc.stdout, stderr: tc.stderr, exit: tc.exit}
			ssh := &SSH{executable: "/usr/bin/ssh", run: script.run}
			var stderr strings.Builder
			got, err := ssh.Bootstrap(context.Background(), "ws", strings.NewReader(""), &stderr)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				if stderr.String() != tc.stderr {
					t.Errorf("remote stderr shown = %q, want %q", stderr.String(), tc.stderr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Bootstrap (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff([]string{"/usr/bin/ssh", "--", "ws", "atc", "__bootstrap"}, script.cmd.Args); diff != "" {
				t.Errorf("argv (-want +got):\n%s", diff)
			}
			if script.cmd.Env != nil {
				t.Errorf("bootstrap child got an explicit environment: %v", script.cmd.Env)
			}
		})
	}
}

func TestBootstrapRejectsBadTargets(t *testing.T) {
	ssh := &SSH{executable: "ssh", run: func(*exec.Cmd) error { t.Fatal("ssh ran"); return nil }}
	for _, target := range []string{"", "ws\n", "ws\x1b[2J"} {
		if _, err := ssh.Bootstrap(context.Background(), target, strings.NewReader(""), io.Discard); err == nil {
			t.Errorf("target %q accepted", target)
		}
	}
	if err := validateTarget("user@ws.example:2222"); err != nil {
		t.Errorf("ordinary target refused: %v", err)
	}
}

func TestAttachCommandAndTransportLoss(t *testing.T) {
	ssh := &SSH{executable: "/usr/bin/ssh"}
	cmd := ssh.AttachCommand("ws", "term-abcde")
	want := []string{"/usr/bin/ssh", "-tt", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3", "--", "ws", "atc", "terminal", "attach", "term-abcde"}
	if diff := cmp.Diff(want, cmd.Args); diff != "" {
		t.Errorf("argv (-want +got):\n%s", diff)
	}
	for _, arg := range cmd.Args {
		if strings.Contains(arg, "ControlMaster") || strings.Contains(arg, "ControlPath") || strings.Contains(arg, "ControlPersist") {
			t.Errorf("attach sets connection sharing: %v", cmd.Args)
		}
	}
	if cmd.Env != nil {
		t.Errorf("attach child got an explicit environment: %v", cmd.Env)
	}
	if !IsTransportLoss(scriptedExit(255)) || IsTransportLoss(scriptedExit(1)) || IsTransportLoss(nil) || IsTransportLoss(errors.New("x")) {
		t.Error("transport loss is exit 255 and nothing else")
	}
}
