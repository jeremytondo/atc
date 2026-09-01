// Package remote implements the SSH-managed client transport used by the TUI.
// One short bootstrap command starts or finds the remote ATC server and returns
// its in-memory connection material. A separately supervised SSH process
// forwards the authenticated HTTP API over a private Unix socket; interactive
// terminal attachment uses another SSH process so terminal bytes never enter
// the control API.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
)

const (
	maxBootstrapOutput  = 16 << 10
	forwardReadyTimeout = 10 * time.Second
	forwardStopTimeout  = 2 * time.Second
	maxSSHErrorOutput   = 16 << 10
)

var resilienceOptions = []string{
	"-o", "ConnectTimeout=10",
	"-o", "ServerAliveInterval=5",
	"-o", "ServerAliveCountMax=3",
}

var controlOptions = append(append([]string(nil), resilienceOptions...), "-o", "BatchMode=yes")

const terminalIDSuffixAlphabet = "23456789bcdfghjkmnpqrstvwxyz"

// Bootstrap is the deliberately small stdout protocol emitted by
// `atc __remote prepare` over an authenticated SSH connection.
type Bootstrap struct {
	Host    string `json:"host"`
	Port    int    `json:"port"`
	Token   string `json:"token"`
	Version string `json:"version"`
}

// StableError reports remote configuration that retrying cannot heal, such as
// a missing remote ATC binary or an exact-version mismatch.
type StableError struct{ Err error }

func (e *StableError) Error() string { return e.Err.Error() }
func (e *StableError) Unwrap() error { return e.Err }

// IsStable reports whether reconnect loops should stop until the user changes
// configuration.
func IsStable(err error) bool {
	var stable *StableError
	return errors.As(err, &stable)
}

type command struct {
	path           string
	args           []string
	stdin          io.Reader
	stdout, stderr io.Writer
}

type child interface {
	Wait() error
}

type runner interface {
	Run(context.Context, command) error
	Start(context.Context, command) (child, error)
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, spec command) error {
	cmd := exec.CommandContext(ctx, spec.path, spec.args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = spec.stdin, spec.stdout, spec.stderr
	return cmd.Run()
}

func (execRunner) Start(ctx context.Context, spec command) (child, error) {
	cmd := exec.CommandContext(ctx, spec.path, spec.args...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = spec.stdin, spec.stdout, spec.stderr
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return cmd.Process.Signal(os.Interrupt)
	}
	cmd.WaitDelay = forwardStopTimeout
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd, nil
}

// SSH owns the OpenSSH executable and the local build identity used for exact
// client/server version matching.
type SSH struct {
	executable string
	version    string
	runner     runner
	mkdirTemp  func(string, string) (string, error)

	mu          sync.Mutex
	closed      bool
	operations  sync.WaitGroup
	sessions    map[*Session]struct{}
	shutdown    sync.Once
	shutdownErr error
}

// NewSSH resolves OpenSSH and returns the production remote transport.
func NewSSH(localVersion string) (*SSH, error) {
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return nil, errors.New("OpenSSH client not found on PATH")
	}
	return &SSH{
		executable: executable,
		version:    localVersion,
		runner:     execRunner{},
		mkdirTemp:  os.MkdirTemp,
	}, nil
}

// Connect starts or finds ATC on target, verifies an exact version match, and
// establishes a private Unix-socket HTTP forward. The bearer token remains in
// this process and is never written to local configuration.
func (s *SSH) Connect(ctx context.Context, target string) (*Session, error) {
	if err := s.beginConnect(); err != nil {
		return nil, err
	}
	defer s.operations.Done()
	if err := validateTarget(target); err != nil {
		return nil, &StableError{Err: err}
	}
	bootstrap, err := s.bootstrap(ctx, target)
	if err != nil {
		return nil, err
	}
	if bootstrap.Version != s.version {
		return nil, &StableError{Err: fmt.Errorf(
			"ATC version mismatch: local %s, remote %s; upgrade one side so they match",
			s.version, bootstrap.Version)}
	}

	// /tmp keeps the Unix-socket path under Darwin's short sun_path limit;
	// MkdirTemp creates the launcher-owned directory at 0700.
	dir, err := s.mkdirTemp("/tmp", "atc-ssh-")
	if err != nil {
		return nil, fmt.Errorf("creating private SSH state: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("securing private SSH state: %w", err)
	}
	socket := filepath.Join(dir, "control.sock")
	forwardCtx, cancel := context.WithCancel(ctx)
	stderr := cappedBuffer{limit: maxSSHErrorOutput}
	forwardArgs := append([]string{"-N"}, controlOptions...)
	forwardArgs = append(forwardArgs,
		"-o", "ExitOnForwardFailure=yes",
		"-o", "StreamLocalBindUnlink=yes",
		"-L", socket+":"+net.JoinHostPort(bootstrap.Host, strconv.Itoa(bootstrap.Port)),
		"--", target,
	)
	process, err := s.runner.Start(forwardCtx, command{
		path:   s.executable,
		args:   forwardArgs,
		stderr: &stderr,
	})
	if err != nil {
		cancel()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("starting SSH control forward: %w", err)
	}

	session := newSession(cancel, process, dir, socket, bootstrap.Token, s.version, &stderr, s.unregister)
	if err := waitForSocket(ctx, socket, session.done); err != nil {
		_ = session.Close()
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return nil, fmt.Errorf("SSH control forward: %w (%s)", err, detail)
		}
		return nil, fmt.Errorf("SSH control forward: %w", err)
	}
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		_ = session.Close()
		return nil, context.Canceled
	}
	if s.sessions == nil {
		s.sessions = make(map[*Session]struct{})
	}
	s.sessions[session] = struct{}{}
	s.mu.Unlock()
	return session, nil
}

func (s *SSH) beginConnect() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("SSH transport is closed")
	}
	s.operations.Add(1)
	return nil
}

func (s *SSH) unregister(session *Session) {
	s.mu.Lock()
	delete(s.sessions, session)
	s.mu.Unlock()
}

// Close prevents new connection attempts, closes every session the transport
// created, and waits for in-flight Connect calls to finish their cleanup.
func (s *SSH) Close() error {
	s.shutdown.Do(func() {
		s.mu.Lock()
		s.closed = true
		sessions := make([]*Session, 0, len(s.sessions))
		for session := range s.sessions {
			sessions = append(sessions, session)
		}
		s.mu.Unlock()
		var errs []error
		for _, session := range sessions {
			if err := session.Close(); err != nil {
				errs = append(errs, err)
			}
		}
		s.operations.Wait()
		s.shutdownErr = errors.Join(errs...)
	})
	return s.shutdownErr
}

func (s *SSH) bootstrap(ctx context.Context, target string) (Bootstrap, error) {
	stdout := cappedBuffer{limit: maxBootstrapOutput + 1}
	stderr := cappedBuffer{limit: maxSSHErrorOutput}
	bootstrapArgs := append([]string(nil), controlOptions...)
	bootstrapArgs = append(bootstrapArgs, "--", target, "atc", "__remote", "prepare")
	err := s.runner.Run(ctx, command{
		path:   s.executable,
		args:   bootstrapArgs,
		stdout: &stdout,
		stderr: &stderr,
	})
	if err != nil {
		detail := strings.TrimSpace(stderr.String())
		if exitCode(err) == 127 {
			return Bootstrap{}, &StableError{Err: fmt.Errorf(
				"remote ATC executable not found on %s; install ATC there and ensure it is on the non-interactive SSH PATH", target)}
		}
		if detail != "" {
			return Bootstrap{}, fmt.Errorf("preparing remote ATC on %s: %w (%s)", target, err, detail)
		}
		return Bootstrap{}, fmt.Errorf("preparing remote ATC on %s: %w", target, err)
	}
	if stdout.Len() > maxBootstrapOutput {
		return Bootstrap{}, &StableError{Err: errors.New("remote ATC returned an oversized bootstrap response")}
	}
	var response Bootstrap
	decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return Bootstrap{}, &StableError{Err: fmt.Errorf("decoding remote ATC bootstrap response: %w", err)}
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Bootstrap{}, &StableError{Err: err}
	}
	if response.Host == "" || response.Port < 1 || response.Port > 65535 || response.Token == "" || response.Version == "" {
		return Bootstrap{}, &StableError{Err: errors.New("remote ATC returned incomplete connection details")}
	}
	return response, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); errors.Is(err, io.EOF) {
		return nil
	} else if err != nil {
		return fmt.Errorf("decoding trailing remote ATC bootstrap data: %w", err)
	}
	return errors.New("remote ATC returned more than one bootstrap value")
}

// AttachmentCommand returns the separate interactive SSH process that owns
// the caller's real TTY while attached. -tt is intentional: the command may be
// launched from a renderer that temporarily relinquishes its terminal.
func (s *SSH) AttachmentCommand(target, terminalID string) (*exec.Cmd, error) {
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	if !validTerminalID(terminalID) {
		return nil, fmt.Errorf("invalid terminal ID %q", terminalID)
	}
	args := append([]string{"-tt"}, resilienceOptions...)
	args = append(args, "--", target, "atc", "terminal", "attach", terminalID)
	return exec.Command(s.executable, args...), nil
}

func validTerminalID(id string) bool {
	const prefix = "term-"
	if len(id) != len(prefix)+5 || !strings.HasPrefix(id, prefix) {
		return false
	}
	for _, char := range id[len(prefix):] {
		if !strings.ContainsRune(terminalIDSuffixAlphabet, char) {
			return false
		}
	}
	return true
}

// IsTransportFailure distinguishes OpenSSH's reserved transport/configuration
// exit status from a normal remote detach (zero) or a remote command failure.
func IsTransportFailure(err error) bool { return exitCode(err) == 255 }

func validateTarget(target string) error {
	if strings.TrimSpace(target) == "" {
		return errors.New("SSH target is empty")
	}
	if strings.ContainsAny(target, "\r\n\x00") {
		return errors.New("SSH target contains invalid control characters")
	}
	return nil
}

func exitCode(err error) int {
	var exit interface{ ExitCode() int }
	if errors.As(err, &exit) {
		return exit.ExitCode()
	}
	return -1
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		_, _ = b.Buffer.Write(p[:min(len(p), remaining)])
	}
	return n, nil
}

func waitForSocket(ctx context.Context, path string, processDone <-chan struct{}) error {
	deadline := time.NewTimer(forwardReadyTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if info, err := os.Stat(path); err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-processDone:
			return errors.New("SSH exited before its control socket became ready")
		case <-deadline.C:
			return errors.New("timed out waiting for the SSH control socket")
		case <-ticker.C:
		}
	}
}

// Session is one supervised SSH control forward and its authenticated API
// client. Done closes when the forward exits; WaitError then reports why.
type Session struct {
	cancel      context.CancelFunc
	dir, socket string
	client      *api.Client
	httpClient  *http.Client
	done        chan struct{}

	mu       sync.Mutex
	waitErr  error
	close    sync.Once
	closeErr error
	stderr   *cappedBuffer
	onClose  func(*Session)
}

func newSession(cancel context.CancelFunc, process child, dir, socket, token, version string, stderr *cappedBuffer, onClose func(*Session)) *Session {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", socket)
		},
	}
	httpClient := &http.Client{Transport: transport}
	session := &Session{
		cancel: cancel, dir: dir, socket: socket,
		httpClient: httpClient, done: make(chan struct{}), stderr: stderr, onClose: onClose,
	}
	session.client = api.NewClient("http://atc", token, version, httpClient, nil)
	go func() {
		err := process.Wait()
		session.mu.Lock()
		session.waitErr = err
		session.mu.Unlock()
		close(session.done)
	}()
	return session
}

// Client returns the authenticated public API client carried by this forward.
func (s *Session) Client() *api.Client { return s.client }

// Done closes when the SSH control process exits.
func (s *Session) Done() <-chan struct{} { return s.done }

// WaitError reports the control process result after Done closes. SSH stderr is
// appended when present because it usually contains the actionable cause.
func (s *Session) WaitError() error {
	<-s.done
	s.mu.Lock()
	err := s.waitErr
	s.mu.Unlock()
	if err == nil {
		err = errors.New("SSH control forward exited")
	}
	if detail := strings.TrimSpace(s.stderr.String()); detail != "" {
		return fmt.Errorf("%w (%s)", err, detail)
	}
	return err
}

// Close stops only this launcher-owned SSH child, closes its idle HTTP
// connections, and removes its private forwarding state.
func (s *Session) Close() error {
	s.close.Do(func() {
		s.cancel()
		<-s.done
		s.httpClient.CloseIdleConnections()
		if err := os.RemoveAll(s.dir); err != nil {
			s.closeErr = err
		}
		if s.onClose != nil {
			s.onClose(s)
		}
	})
	return s.closeErr
}
