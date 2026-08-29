package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// The shared app-server lifecycle follows the proven legacy playbook:
// adopt whatever answers on Codex's well-known control socket, start one
// only if nothing does, spawn detached so the server survives ATC
// restarts, re-verify a persisted pid against its command line before
// ever trusting or signaling it, and never signal a server ATC did not
// start. A codex TUI launched with --remote dies the instant its
// app-server dies, so ATC shutdown deliberately leaves the server
// running.

const (
	// probeTimeout bounds one "does anything answer" WebSocket upgrade.
	probeTimeout = time.Second
	// readyTimeout is how long a freshly spawned server gets to answer.
	readyTimeout = 15 * time.Second
	// adoptTimeout is how long our own already-running but silent server
	// gets — deliberately generous: wrongly declaring it dead would kill
	// every attached TUI.
	adoptTimeout = 10 * time.Second
	// awaitInterval paces probes while waiting on a pid.
	awaitInterval = 250 * time.Millisecond
	// argvToken is the exact whitespace-delimited word a trusted pid's
	// command line must contain (substring matching would hit any path
	// containing it).
	argvToken = "app-server"
)

// errServerExited reports a pid that died while we waited on its socket.
var errServerExited = errors.New("codex app-server exited before answering")

// ControlSocketPath is Codex's own well-known control socket, one per
// CODEX_HOME. ATC never binds any other address and never runs a private
// server.
func ControlSocketPath(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control", "app-server-control.sock")
}

// CodexHome resolves the CODEX_HOME the spawned server (and every codex
// TUI) will use: codex's own variable, never an ATC setting.
func CodexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return home, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// identity is the persisted claim "we started pid at startedAt". Absent
// or corrupt means "no server of ours" — never trusted.
type identity struct {
	PID       int    `json:"pid"`
	StartedAt string `json:"startedAt"`
}

// SupervisorOptions wires a Supervisor. The function fields are seams for
// tests; nil selects the real implementation.
type SupervisorOptions struct {
	// CodexHome locates the well-known control socket.
	CodexHome string
	// IdentityFile persists {pid, startedAt} (paths.CodexServerFile).
	IdentityFile string
	// LogFile captures the detached server's output (paths.CodexServerLogFile).
	LogFile string
	// SpawnDir is the neutral cwd the server starts in: codex stamps
	// threads whose client sent no cwd with the server's own cwd, so an
	// inherited launch directory would leak into threads.
	SpawnDir string
	// Executable is the codex binary name, resolved on PATH at spawn.
	Executable string
	Logger     *slog.Logger

	Probe        func(ctx context.Context, socketPath string) bool
	Spawn        func(ctx context.Context) (int, error)
	ProcessAlive func(pid int) bool
	HasArgvToken func(pid int, token string) bool
	Terminate    func(pid int) error
}

// Supervisor owns adopt-or-start over the shared app-server. All entry
// points serialize on mu, so concurrent launches cannot race two spawns.
type Supervisor struct {
	opts SupervisorOptions
	// readyWindow and adoptWindow are readyTimeout/adoptTimeout in
	// production; tests shrink them.
	readyWindow, adoptWindow time.Duration

	mu sync.Mutex
	// socket is the adopted socket path once any acquire succeeded; pid is
	// nonzero only for a server we started (the only pid ever signaled).
	socket string
	pid    int
}

func NewSupervisor(opts SupervisorOptions) *Supervisor {
	if opts.Executable == "" {
		opts.Executable = "codex"
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	s := &Supervisor{opts: opts, readyWindow: readyTimeout, adoptWindow: adoptTimeout}
	if s.opts.Probe == nil {
		s.opts.Probe = probeSocket
	}
	if s.opts.Spawn == nil {
		s.opts.Spawn = s.spawnDetached
	}
	if s.opts.ProcessAlive == nil {
		s.opts.ProcessAlive = processAlive
	}
	if s.opts.HasArgvToken == nil {
		s.opts.HasArgvToken = hasArgvToken
	}
	if s.opts.Terminate == nil {
		s.opts.Terminate = terminate
	}
	return s
}

func (s *Supervisor) socketPath() string {
	return ControlSocketPath(s.opts.CodexHome)
}

// Ensure adopts or starts the shared server and returns its socket path —
// the first codex launch's lazy start, and every later launch's fast
// path.
func (s *Supervisor) Ensure(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.socket != "" && (s.pid == 0 || s.opts.ProcessAlive(s.pid)) && s.opts.Probe(ctx, s.socket) {
		return s.socket, nil
	}
	s.socket, s.pid = "", 0
	return s.acquire(ctx, true)
}

// Adopt is the boot-time re-adoption: connect to whatever is there so
// live work returns to observation, but never start a server no demand
// asked for. False means nothing answered.
func (s *Supervisor) Adopt(ctx context.Context) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	socket, err := s.acquire(ctx, false)
	if err != nil {
		s.opts.Logger.Debug("no codex app-server to adopt", "error", err)
		return "", false
	}
	return socket, true
}

// Socket reports the last acquired socket path, empty when no server has
// ever been acquired this process. The observer keys its connection loop
// on it.
func (s *Supervisor) Socket() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.socket
}

// OwnedPID reports the pid of a server this process started, zero for an
// adopted (foreign) server.
func (s *Supervisor) OwnedPID() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.pid
}

// acquire implements adopt-or-start. Caller holds mu.
func (s *Supervisor) acquire(ctx context.Context, allowSpawn bool) (string, error) {
	socketPath := s.socketPath()

	// Something answers: adopt it. It is "ours" only when the persisted
	// identity's pid re-verifies against its command line — pid recycling
	// defense before the pid is ever trusted.
	if s.opts.Probe(ctx, socketPath) {
		if pid, ok := s.readIdentity(); ok && s.isOurServer(pid) {
			s.socket, s.pid = socketPath, pid
			s.opts.Logger.Info("codex app-server re-adopted", "pid", pid)
		} else {
			s.clearIdentity()
			s.socket, s.pid = socketPath, 0
			s.opts.Logger.Info("codex app-server adopted (not started by atc)")
		}
		return s.socket, nil
	}

	// Silent socket but a persisted pid of ours: it may still be booting.
	if pid, ok := s.readIdentity(); ok {
		if s.isOurServer(pid) {
			answered, err := s.awaitAnswer(ctx, socketPath, pid, s.adoptWindow)
			if answered {
				s.socket, s.pid = socketPath, pid
				return s.socket, nil
			}
			// Our own pid, alive or not, that will not answer: reap it so
			// a fresh start can take the socket. This is the only signal
			// path besides spawn cleanup; the argv token is re-verified
			// right here — the wait window is exactly where a recycled
			// pid could otherwise inherit the earlier blessing.
			if err == nil || errors.Is(err, errServerExited) {
				if s.opts.ProcessAlive(pid) && s.isOurServerPID(pid) {
					if err := s.opts.Terminate(pid); err != nil {
						return "", fmt.Errorf("codex app-server pid %d not answering and not terminable: %w", pid, err)
					}
				}
			} else {
				return "", err
			}
		}
		s.clearIdentity()
	}

	if !allowSpawn {
		return "", errors.New("no codex app-server answering")
	}
	return s.start(ctx, socketPath)
}

// start spawns the detached server and waits for it to answer. Caller
// holds mu.
func (s *Supervisor) start(ctx context.Context, socketPath string) (string, error) {
	pid, err := s.opts.Spawn(ctx)
	if err != nil {
		return "", err
	}
	// Identity first: a crash between spawn and readiness must still leave
	// the pid re-verifiable at the next boot.
	if err := s.writeIdentity(pid); err != nil {
		s.reap(pid)
		return "", err
	}
	answered, waitErr := s.awaitAnswer(ctx, socketPath, pid, s.readyWindow)
	switch {
	case answered && s.opts.ProcessAlive(pid):
		s.socket, s.pid = socketPath, pid
		s.opts.Logger.Info("codex app-server started", "pid", pid)
		return s.socket, nil
	case answered:
		// Raced: another client's server took the socket and ours exited
		// "already in use". Adopt the winner.
		s.clearIdentity()
		s.socket, s.pid = socketPath, 0
		s.opts.Logger.Info("codex app-server adopted (another client started it first)")
		return s.socket, nil
	default:
		s.reap(pid)
		detail := s.logTail()
		if waitErr == nil {
			waitErr = fmt.Errorf("codex app-server pid %d never answered on %s", pid, socketPath)
		}
		if detail != "" {
			waitErr = fmt.Errorf("%w (%s)", waitErr, detail)
		}
		return "", waitErr
	}
}

// awaitAnswer probes until the socket answers, the pid dies, or the
// window closes. (false, nil) means the window closed with the pid alive.
func (s *Supervisor) awaitAnswer(ctx context.Context, socketPath string, pid int, window time.Duration) (bool, error) {
	deadline := time.Now().Add(window)
	for {
		if s.opts.Probe(ctx, socketPath) {
			return true, nil
		}
		if !s.opts.ProcessAlive(pid) {
			// Fail immediately rather than burning the window: the probe
			// answering right after is the "raced" case the caller checks.
			if s.opts.Probe(ctx, socketPath) {
				return true, nil
			}
			return false, fmt.Errorf("%w: pid %d", errServerExited, pid)
		}
		if time.Now().After(deadline) {
			return false, nil
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-time.After(awaitInterval):
		}
	}
}

// reap terminates a pid we spawned but could not use, after the argv
// re-verification — otherwise failed starts accumulate servers.
func (s *Supervisor) reap(pid int) {
	if s.isOurServerPID(pid) {
		if err := s.opts.Terminate(pid); err != nil {
			s.opts.Logger.Warn("reaping failed codex app-server", "pid", pid, "error", err)
		}
	}
	s.clearIdentity()
}

// isOurServer re-verifies a persisted pid against its live command line.
func (s *Supervisor) isOurServer(pid int) bool {
	return s.isOurServerPID(pid)
}

func (s *Supervisor) isOurServerPID(pid int) bool {
	return pid > 0 && s.opts.HasArgvToken(pid, argvToken)
}

func (s *Supervisor) readIdentity() (int, bool) {
	content, err := os.ReadFile(s.opts.IdentityFile)
	if err != nil {
		return 0, false
	}
	var id identity
	if err := json.Unmarshal(content, &id); err != nil || id.PID <= 0 {
		return 0, false
	}
	return id.PID, true
}

func (s *Supervisor) writeIdentity(pid int) error {
	content, err := json.MarshalIndent(identity{PID: pid, StartedAt: time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.opts.IdentityFile), 0o700); err != nil {
		return err
	}
	return os.WriteFile(s.opts.IdentityFile, content, 0o600)
}

func (s *Supervisor) clearIdentity() {
	if err := os.Remove(s.opts.IdentityFile); err != nil && !errors.Is(err, os.ErrNotExist) {
		s.opts.Logger.Warn("clearing codex app-server identity", "error", err)
	}
}

// logTail is the last few log lines for failure diagnostics.
func (s *Supervisor) logTail() string {
	content, err := os.ReadFile(s.opts.LogFile)
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(content)), "\n")
	if len(lines) > 10 {
		lines = lines[len(lines)-10:]
	}
	return strings.Join(lines, " · ")
}

// spawnDetached starts the server through an intermediate /bin/sh so it
// reparents to init: nohup, output to the log file, stdin from /dev/null
// (the server must never see EOF when ATC exits), and the child's single
// stdout line is the detached pid. `--listen unix://` carries no path —
// codex derives the well-known socket from the CODEX_HOME it inherits.
func (s *Supervisor) spawnDetached(ctx context.Context) (int, error) {
	executable, err := exec.LookPath(s.opts.Executable)
	if err != nil {
		return 0, fmt.Errorf("codex executable not found on PATH: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.opts.LogFile), 0o700); err != nil {
		return 0, err
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c",
		`nohup "$1" app-server --listen unix:// >"$2" 2>&1 </dev/null & echo $!`,
		"sh", executable, s.opts.LogFile)
	cmd.Dir = s.opts.SpawnDir
	if env := os.Getenv("CODEX_HOME"); env == "" && s.opts.CodexHome != "" {
		// Tests point CODEX_HOME at a private directory; production
		// inherits the user's own.
		cmd.Env = append(os.Environ(), "CODEX_HOME="+s.opts.CodexHome)
	}
	output, err := cmd.Output()
	if err != nil {
		return 0, fmt.Errorf("spawning codex app-server: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(output)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("codex app-server spawn reported no pid: %q", output)
	}
	return pid, nil
}

func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}

// hasArgvToken requires an exact whitespace-delimited argv word. Any
// failure means "not ours": the pid is never trusted, never signaled.
func hasArgvToken(pid int, token string) bool {
	output, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=").Output()
	if err != nil {
		return false
	}
	return slices.Contains(strings.Fields(string(output)), token)
}

// terminate is SIGTERM, wait, SIGKILL, wait. Callers gate it behind the
// argv re-verification.
func terminate(pid int) error {
	_ = syscall.Kill(pid, syscall.SIGTERM)
	if waitForExit(pid, 5*time.Second) {
		return nil
	}
	_ = syscall.Kill(pid, syscall.SIGKILL)
	if waitForExit(pid, 5*time.Second) {
		return nil
	}
	return fmt.Errorf("process %d survived SIGTERM and SIGKILL", pid)
}

func waitForExit(pid int, window time.Duration) bool {
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		if !processAlive(pid) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return !processAlive(pid)
}
