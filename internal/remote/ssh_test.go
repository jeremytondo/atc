package remote

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

type fakeExit int

func (e fakeExit) Error() string { return "exit" }
func (e fakeExit) ExitCode() int { return int(e) }

type fakeChild struct {
	done <-chan struct{}
	err  error
}

func (c fakeChild) Wait() error {
	<-c.done
	return c.err
}

type fakeRunner struct {
	run   func(context.Context, command) error
	start func(context.Context, command) (child, error)
}

func (r fakeRunner) Run(ctx context.Context, cmd command) error { return r.run(ctx, cmd) }
func (r fakeRunner) Start(ctx context.Context, cmd command) (child, error) {
	return r.start(ctx, cmd)
}

func TestConnectCarriesAuthenticatedAPIOverPrivateForward(t *testing.T) {
	var runCommand, startCommand command
	var socketPath string
	var sawAuthorization, sawVersion string
	runner := fakeRunner{
		run: func(_ context.Context, cmd command) error {
			runCommand = cmd
			return json.NewEncoder(cmd.stdout).Encode(Bootstrap{Host: "127.0.0.1", Port: 7331, Token: "secret", Version: "v-test"})
		},
		start: func(ctx context.Context, cmd command) (child, error) {
			startCommand = cmd
			for i, arg := range cmd.args {
				if arg == "-L" && i+1 < len(cmd.args) {
					socketPath = strings.SplitN(cmd.args[i+1], ":127.0.0.1:", 2)[0]
				}
			}
			listener, err := net.Listen("unix", socketPath)
			if err != nil {
				return nil, err
			}
			server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				sawAuthorization = r.Header.Get("Authorization")
				sawVersion = r.Header.Get(api.ClientVersionHeader)
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"terminals":[]}`)
			})}
			done := make(chan struct{})
			go func() {
				<-ctx.Done()
				_ = server.Close()
				_ = listener.Close()
				close(done)
			}()
			go func() { _ = server.Serve(listener) }()
			return fakeChild{done: done, err: context.Canceled}, nil
		},
	}
	ssh := &SSH{
		executable: "/usr/bin/ssh", version: "v-test", runner: runner,
		mkdirTemp: os.MkdirTemp,
	}
	session, err := ssh.Connect(context.Background(), "workstation")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Client().Terminals(context.Background(), ""); err != nil {
		t.Fatal(err)
	}
	if sawAuthorization != "Bearer secret" || sawVersion != "v-test" {
		t.Errorf("API headers = Authorization:%q version:%q", sawAuthorization, sawVersion)
	}
	if !containsSequence(runCommand.args, "-o", "ConnectTimeout=10") ||
		!containsSequence(runCommand.args, "-o", "ServerAliveInterval=5") ||
		!containsSequence(runCommand.args, "-o", "BatchMode=yes") ||
		!containsSequence(runCommand.args, "--", "workstation", "atc", "__remote", "prepare") {
		t.Errorf("bootstrap argv = %q", runCommand.args)
	}
	if !containsSequence(startCommand.args, "-o", "ExitOnForwardFailure=yes") ||
		!containsSequence(startCommand.args, "-o", "StreamLocalBindUnlink=yes") ||
		!containsSequence(startCommand.args, "-o", "BatchMode=yes") ||
		!containsSequence(startCommand.args, "--", "workstation") {
		t.Errorf("forward argv = %q", startCommand.args)
	}
	dir := filepath.Dir(socketPath)
	if info, err := os.Stat(dir); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("private forward dir = %v, %v", info, err)
	}
	if err := ssh.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("forward state survived Close: %v", err)
	}
	if _, err := ssh.Connect(context.Background(), "workstation"); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Connect after Close = %v", err)
	}
}

func TestConnectStableFailuresDoNotStartForward(t *testing.T) {
	for name, tc := range map[string]struct {
		run  func(command) error
		want string
	}{
		"missing remote binary": {
			run:  func(command) error { return fakeExit(127) },
			want: "remote ATC executable not found",
		},
		"version mismatch": {
			run: func(cmd command) error {
				return json.NewEncoder(cmd.stdout).Encode(Bootstrap{Host: "::1", Port: 7331, Token: "x", Version: "old"})
			},
			want: "version mismatch",
		},
		"malformed response": {
			run: func(cmd command) error {
				_, _ = io.WriteString(cmd.stdout, "not json")
				return nil
			},
			want: "decoding",
		},
	} {
		t.Run(name, func(t *testing.T) {
			started := false
			ssh := &SSH{
				executable: "ssh", version: "new", mkdirTemp: os.MkdirTemp,
				runner: fakeRunner{
					run: func(_ context.Context, cmd command) error { return tc.run(cmd) },
					start: func(context.Context, command) (child, error) {
						started = true
						return nil, errors.New("unexpected")
					},
				},
			}
			_, err := ssh.Connect(context.Background(), "host")
			if !IsStable(err) || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Connect = %v; want stable %q", err, tc.want)
			}
			if started {
				t.Fatal("control forward started after stable bootstrap failure")
			}
		})
	}
}

func TestAttachmentCommandAndTransportClassification(t *testing.T) {
	ssh := &SSH{executable: "/usr/bin/ssh"}
	cmd, err := ssh.AttachmentCommand("dev-box", "term-bcdfg")
	if err != nil {
		t.Fatal(err)
	}
	if !containsSequence(cmd.Args, "-tt", "-o", "ConnectTimeout=10") ||
		!containsSequence(cmd.Args, "-o", "ServerAliveCountMax=3") ||
		!containsSequence(cmd.Args, "--", "dev-box", "atc", "terminal", "attach", "term-bcdfg") {
		t.Errorf("attach argv = %q", cmd.Args)
	}
	if containsSequence(cmd.Args, "-o", "BatchMode=yes") {
		t.Errorf("interactive attachment unexpectedly disabled authentication prompts: %q", cmd.Args)
	}
	if _, err := ssh.AttachmentCommand("dev-box", "term-ok;touch /tmp/pwn"); err == nil {
		t.Error("shell-active terminal ID was accepted")
	}
	if !IsTransportFailure(fakeExit(255)) || IsTransportFailure(fakeExit(1)) || IsTransportFailure(nil) {
		t.Error("OpenSSH exit 255 was not the only classified transport failure")
	}
}

func containsSequence(values []string, sequence ...string) bool {
	for i := 0; i+len(sequence) <= len(values); i++ {
		if cmp.Equal(values[i:i+len(sequence)], sequence) {
			return true
		}
	}
	return false
}
