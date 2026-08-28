// Package zmx is the complete zmx-specific boundary (ATC-251): only this
// package knows zmx commands, inventory parsing, environment traps, PTY
// creation, and attach mechanics. It implements terminals.Adapter; future
// terminal backends implement the same interface.
//
// Environment contract (every invocation, learned the hard way in
// experiments/zmx-supervisor and the legacy product): ZMX_DIR is forced to
// ATC's private socket directory; ZMX_SESSION is scrubbed (inherited, it
// turns attach into a session switch of the operator's own terminal);
// ZMX_SESSION_PREFIX is scrubbed (it silently rewrites every session
// name); TERM is pinned for spawned sessions, since zmx only inherits it.
//
// Sessions are born through zmx's attach-auto-creates behavior: a
// short-lived attach client on a fresh PTY starts the daemon, and closing
// the PTY (stdin EOF) detaches the client while the daemon persists.
// `zmx kill` returns before the session is gone and its exit code proves
// nothing; absence is verified only by polling complete inventories.
package zmx

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"

	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/terminals/exitmarker"
)

// commandTimeout bounds every zmx invocation: a hung zmx must not hang its
// caller — slowness is unavailability.
const commandTimeout = 10 * time.Second

// sessionTerm is the TERM spawned sessions inherit. zmx never sets TERM,
// and a supervised server's own environment may not have a usable one.
const sessionTerm = "xterm-256color"

// maxSocketPathBytes is sun_path minus the NUL terminator.
func maxSocketPathBytes() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}

// Options wires an Adapter. Cadence values come from the terminals
// package, the one place cadence lives.
type Options struct {
	// SocketDir is ATC's private zmx socket directory (paths.TerminalSocketDir).
	SocketDir string
	// MarkerDir is where wrappers record exit evidence (paths.ExitMarkerDir).
	MarkerDir string
	// WrapperExecutable is the atc binary, re-exec'd as `atc __child`.
	WrapperExecutable string
	Logger            *slog.Logger
}

// Adapter implements terminals.Adapter over the zmx CLI.
type Adapter struct {
	socketDir string
	markerDir string
	wrapper   string
	logger    *slog.Logger

	mu       sync.Mutex
	resolved string // memoized successful LookPath result
}

// New validates and prepares the private namespace. A socket directory too
// deep for the OS socket-path limit is a boot error with the remedy in the
// message (legacy's proven guard); a missing zmx binary deliberately is
// not — delete must keep working when zmx is unhealthy.
func New(opts Options) (*Adapter, error) {
	socketDir, err := filepath.Abs(opts.SocketDir)
	if err != nil {
		return nil, err
	}
	longestPath := filepath.Join(socketDir, strings.Repeat("x", terminals.IDLength))
	if len(longestPath) > maxSocketPathBytes() {
		return nil, fmt.Errorf(
			"terminal socket directory %s is too deep: socket paths would exceed %d bytes; move your state dir (XDG_STATE_HOME)",
			socketDir, maxSocketPathBytes())
	}
	// Private: sockets grant full read/write on the user's terminals. zmx's
	// own default mode is 0750, and MkdirAll's mode only applies when it
	// creates the directory, so a pre-existing permissive one is tightened.
	if err := os.MkdirAll(socketDir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(socketDir, 0o700); err != nil {
		return nil, err
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	return &Adapter{
		socketDir: socketDir,
		markerDir: opts.MarkerDir,
		wrapper:   opts.WrapperExecutable,
		logger:    opts.Logger,
	}, nil
}

// zmx resolves the executable on PATH, memoizing success so a transiently
// missing binary heals on a later call.
func (a *Adapter) zmx() (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.resolved != "" {
		return a.resolved, nil
	}
	path, err := exec.LookPath("zmx")
	if err != nil {
		return "", errors.New("zmx executable not found on PATH")
	}
	a.resolved = path
	return path, nil
}

// lookupSession scans one inventory for a name: present reports whether
// the session exists at all, reachable whether it answered its probe.
func lookupSession(sessions []terminals.Session, name string) (present, reachable bool) {
	for _, session := range sessions {
		if session.Name == name {
			return true, session.Reachable
		}
	}
	return false, false
}

// Env returns the environment for zmx child processes: the parent
// environment with the traps scrubbed and the private namespace forced.
// forceTerm pins TERM (session spawning); attach from a real TTY keeps the
// user's TERM.
func Env(socketDir string, forceTerm bool) []string {
	scrubbed := make([]string, 0, len(os.Environ())+2)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		switch name {
		case "ZMX_DIR", "ZMX_SESSION", "ZMX_SESSION_PREFIX":
			continue
		case "TERM":
			if forceTerm {
				continue
			}
		}
		scrubbed = append(scrubbed, entry)
	}
	scrubbed = append(scrubbed, "ZMX_DIR="+socketDir)
	if forceTerm {
		scrubbed = append(scrubbed, "TERM="+sessionTerm)
	}
	return scrubbed
}

// Inventory runs one complete `zmx list`. Only exit 0 is trustworthy ("no
// sessions" is exit 0 with empty stdout); any failure means the inventory
// is unavailable, never empty.
func (a *Adapter) Inventory(ctx context.Context) ([]terminals.Session, error) {
	executable, err := a.zmx()
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executable, "list")
	cmd.Env = Env(a.socketDir, true)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("zmx list: %w (%s)", err, printableTail(stderr.String()))
	}
	return parseList(stdout.String()), nil
}

// parseList parses `zmx list` output: tab-separated key=value fields per
// line, a possible leading "→ " current-session marker, and err= marking a
// present-but-unreachable session. Tolerant by design — unknown fields and
// unparsable lines are skipped, absence of err is reachability.
func parseList(output string) []terminals.Session {
	var sessions []terminals.Session
	for line := range strings.SplitSeq(output, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "→"))
		if line == "" {
			continue
		}
		var session terminals.Session
		hasName, hasErr := false, false
		for field := range strings.SplitSeq(line, "\t") {
			key, value, ok := strings.Cut(field, "=")
			if !ok {
				continue
			}
			switch key {
			case "name":
				session.Name = value
				hasName = true
			case "err":
				hasErr = true
			}
		}
		if !hasName {
			continue
		}
		session.Reachable = !hasErr
		sessions = append(sessions, session)
	}
	return sessions
}

// Create births the session: record already persisted by the caller, the
// wrapper as root task, a fresh PTY for the short-lived attach client, and
// complete inventories as the only settle authority — the client's exit
// code is not one.
func (a *Adapter) Create(ctx context.Context, id string, spec terminals.CreateSpec) error {
	executable, err := a.zmx()
	if err != nil {
		return err
	}
	// zmx attach silently attaches to an existing name, ignoring the
	// command entirely; creating must never be silently attaching. An
	// unreachable leftover blocks creation the same way — it holds the
	// socket path.
	inventory, err := a.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("create %s: %w", id, err)
	}
	if present, _ := lookupSession(inventory, id); present {
		return fmt.Errorf("create %s: session already exists", id)
	}

	argv := []string{"attach", id, a.wrapper, "__child",
		"--marker", exitmarker.Path(a.markerDir, id), "--id", id, "--dir", spec.Directory}
	if spec.Command != "" {
		argv = append(argv, "--command", spec.Command)
	}
	cmd := exec.Command(executable, argv...)
	cmd.Env = Env(a.socketDir, true)
	// A real PTY, sized sanely: the forked daemon reads its initial
	// winsize from the creator (falling back to 24x160 for a bare pipe),
	// and the attach client's raw-mode setup wants a terminal.
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 24, Cols: 120})
	if err != nil {
		return fmt.Errorf("create %s: %w", id, err)
	}
	tail := newTailBuffer()
	go tail.consume(ptmx)
	clientExited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(clientExited)
	}()

	settled, err := a.pollInventory(ctx, clientExited, func(sessions []terminals.Session) bool {
		_, reachable := lookupSession(sessions, id)
		return reachable
	})
	// Detach: closing the PTY is stdin EOF to the attach client, which
	// detaches cleanly; the session daemon persists.
	_ = ptmx.Close()
	a.reap(cmd, clientExited)
	if settled {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("session never settled (%s)", tail.printable())
	}
	return fmt.Errorf("create %s: %w", id, err)
}

// Kill terminates the session and verifies absence. An absent session is
// success; `zmx kill`'s own exit code and output are deliberately ignored
// (it returns before death and exits 0 on unmatched names).
func (a *Adapter) Kill(ctx context.Context, id string) error {
	executable, err := a.zmx()
	if err != nil {
		return err
	}
	inventory, err := a.Inventory(ctx)
	if err != nil {
		return fmt.Errorf("kill %s: %w", id, err)
	}
	if present, _ := lookupSession(inventory, id); !present {
		return nil
	}

	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(runCtx, executable, "kill", id)
	cmd.Env = Env(a.socketDir, true)
	_ = cmd.Run()

	gone, err := a.pollInventory(ctx, nil, func(sessions []terminals.Session) bool {
		present, _ := lookupSession(sessions, id)
		return !present
	})
	if gone {
		return nil
	}
	if err == nil {
		err = fmt.Errorf("session still present after %d inventory passes", terminals.VerifyPasses)
	}
	return fmt.Errorf("kill %s: %w", id, err)
}

// pollInventory re-checks complete inventories at the verification cadence
// until done reports true. Complete passes are counted rather than wall
// time; consecutive failures are capped so an inventory outage does not
// stall mutations. A closed earlyStop channel triggers one final
// authoritative pass — the creation client may exit as the daemon settles.
func (a *Adapter) pollInventory(ctx context.Context, earlyStop <-chan struct{}, done func([]terminals.Session) bool) (bool, error) {
	passes, failures := 0, 0
	var lastErr error
	for passes < terminals.VerifyPasses && failures < terminals.VerifyFailureCap {
		sessions, err := a.Inventory(ctx)
		if err != nil {
			failures++
			lastErr = err
		} else {
			passes++
			failures = 0 // the cap is on consecutive failures
			if done(sessions) {
				return true, nil
			}
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-earlyStop:
			sessions, err := a.Inventory(ctx)
			if err != nil {
				return false, err
			}
			return done(sessions), nil
		case <-time.After(terminals.VerifyInterval):
		}
	}
	return false, lastErr
}

// reap waits briefly for the detached client to exit, then kills it — a
// stuck attach client must not leak.
func (a *Adapter) reap(cmd *exec.Cmd, exited <-chan struct{}) {
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		a.logger.Warn("attach client did not exit after detach; killing", "pid", cmd.Process.Pid)
		_ = cmd.Process.Kill()
		<-exited
	}
}

// Attacher is the client-side half of the zmx boundary: how a local
// process hands its real TTY to a session. It implements
// cli.SessionAttacher — an interface the cli package owns — so only this
// package and the composition root know the adapter is zmx.
type Attacher struct {
	socketDir string
}

// NewAttacher returns an Attacher over ATC's private socket directory
// (paths.TerminalSocketDir), the same namespace the server's Adapter uses.
func NewAttacher(socketDir string) Attacher {
	return Attacher{socketDir: socketDir}
}

// Preflight reports whether attach can possibly succeed on this machine:
// zmx must be on PATH. Callers use it to fail before creating a terminal
// they could never attach.
func (a Attacher) Preflight() error {
	_, err := lookPathZmx()
	return err
}

// AttachCommand resolves the argv and environment for handing the caller's
// real TTY to the session — what `atc terminal attach` execs. The user's
// TERM is kept (the client renders on their terminal); the namespace env
// contract is the same as every other invocation.
//
// A loopback API URL does not prove the server shares this process's
// namespace (an SSH-forwarded remote server, a different XDG_STATE_HOME).
// A session the API calls running must have its socket here; anything
// else would hand the TTY to zmx's attach-auto-creates behavior.
func (a Attacher) AttachCommand(id string) (executable string, argv, env []string, err error) {
	if _, statErr := os.Stat(filepath.Join(a.socketDir, id)); statErr != nil {
		return "", nil, nil, fmt.Errorf(
			"terminal %s has no session socket under %s — the server appears to be remote (an SSH-forwarded port?) or running with a different state directory",
			id, a.socketDir)
	}
	executable, err = lookPathZmx()
	if err != nil {
		return "", nil, nil, err
	}
	return executable, []string{"zmx", "attach", id}, Env(a.socketDir, false), nil
}

func lookPathZmx() (string, error) {
	path, err := exec.LookPath("zmx")
	if err != nil {
		return "", errors.New("zmx executable not found on PATH; install zmx to attach")
	}
	return path, nil
}

// tailBuffer keeps the last window of client PTY output for diagnostics.
type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func newTailBuffer() *tailBuffer {
	return &tailBuffer{}
}

func (t *tailBuffer) consume(f *os.File) {
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if n > 0 {
			t.mu.Lock()
			t.data = append(t.data, buf[:n]...)
			if len(t.data) > 8192 {
				t.data = t.data[len(t.data)-8192:]
			}
			t.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// printable strips control sequences so the tail can ride an error line.
func (t *tailBuffer) printable() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return printableTail(string(t.data))
}

func printableTail(s string) string {
	var b strings.Builder
	skipCSI := false
	for _, r := range s {
		switch {
		case skipCSI:
			if (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') {
				skipCSI = false
			}
		case r == 0x1b:
			skipCSI = true
		case r >= 0x20 || r == '\n' || r == '\t':
			b.WriteRune(r)
		}
	}
	tail := strings.TrimSpace(b.String())
	if len(tail) > 300 {
		tail = tail[len(tail)-300:]
	}
	return strings.ReplaceAll(tail, "\n", " · ")
}
