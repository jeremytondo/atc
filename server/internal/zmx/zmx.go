// Package zmx is the single, narrow seam through which atc talks to the zmx
// multiplexer. It builds and runs zmx as a child process and deals only in zmx
// session names and byte payloads. Confining zmx invocation and private name
// derivation to this package is what keeps the multiplexer swappable.
package zmx

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	"github.com/creack/pty"
)

// DefaultBin is the zmx binary name resolved from PATH when no override is set.
const DefaultBin = "zmx"

const (
	atcNamePrefix          = "atc-"
	defaultSessionTermType = "xterm-256color"
	zmxSessionEnv          = "ZMX_SESSION"
)

// Session is one entry reported by `zmx list`.
type Session struct {
	Name     string
	PID      int
	Created  int64
	StartDir string
	Cmd      string
}

// PTY is a live, bidirectional attach to a session: a stream of terminal bytes
// that can be read, written, resized, and closed. It is an interface so the
// session domain and its tests need not depend on a real OS pseudo-terminal.
type PTY interface {
	io.ReadWriteCloser
	// Resize informs the pseudo-terminal of a new window size.
	Resize(rows, cols uint16) error
}

// Default attach dimensions used when a client does not provide an initial
// terminal size before atc must spawn the attach PTY.
const (
	DefaultAttachRows uint16 = 24
	DefaultAttachCols uint16 = 80
)

// Wrapper builds and runs zmx child-process commands.
type Wrapper struct {
	bin    string
	run    runFunc
	attach attachFunc
}

// runFunc executes the zmx binary with args, optional process settings, and
// returns its stdout. It is the seam that lets the wrapper be tested without a
// live zmx daemon.
type runFunc func(ctx context.Context, bin string, opts runOptions, args ...string) (stdout []byte, err error)

type runOptions struct {
	dir   string
	env   []string
	stdin []byte
}

// attachFunc spawns `zmx attach <name>` under a pseudo-terminal and returns the
// live PTY. It is the seam that lets Attach be tested without a real zmx or tty;
// it is separate from runFunc because attach is a long-lived stream, not a
// run-to-completion command whose stdout is buffered.
type attachFunc func(ctx context.Context, bin, name string, rows, cols uint16) (PTY, error)

// New returns a Wrapper that runs the given zmx binary. An empty bin resolves
// zmx from PATH.
func New(bin string) *Wrapper {
	if bin == "" {
		bin = DefaultBin
	}
	return &Wrapper{bin: bin, run: execRun, attach: execAttach}
}

// Start launches argv in a new detached, persistent session named name, with
// the session's working directory set to dir. Start returns as soon as zmx
// returns and never waits on the spawned command.
func (w *Wrapper) Start(ctx context.Context, name, dir string, argv []string) error {
	// The -d (detached) flag MUST follow the name; a leading -d is parsed by
	// zmx as the session name.
	args := append([]string{"run", name, "-d"}, argv...)
	_, err := w.run(ctx, w.bin, runOptions{dir: dir, env: sessionRunEnv()}, args...)
	return err
}

// Send writes payload verbatim to the PTY of session name via zmx's stdin.
// Payloads go through stdin rather than arguments to avoid quoting defects.
func (w *Wrapper) Send(ctx context.Context, name string, payload []byte) error {
	_, err := w.run(ctx, w.bin, runOptions{stdin: payload}, "send", name)
	return err
}

// Attach starts `zmx attach <name>` under a pseudo-terminal and returns a live
// PTY bound to it. The returned PTY drives this one attach client; closing it
// ends the client, never the persistent session.
func (w *Wrapper) Attach(ctx context.Context, name string, rows, cols uint16) (PTY, error) {
	return w.attach(ctx, w.bin, name, rows, cols)
}

// Terminate asks zmx to stop the persistent session named name.
func (w *Wrapper) Terminate(ctx context.Context, name string) error {
	_, err := w.run(ctx, w.bin, runOptions{}, "kill", name)
	return err
}

// List returns one record per zmx session, tolerating malformed entries.
func (w *Wrapper) List(ctx context.Context) ([]Session, error) {
	out, err := w.run(ctx, w.bin, runOptions{}, "list")
	if err != nil {
		return nil, err
	}
	return parseList(out), nil
}

// NameForID derives the private zmx name for an atc-owned session id. The
// name is deterministic and recomputable, but intentionally does not reveal the
// public id.
func NameForID(id string) string {
	sum := sha256.Sum256([]byte(id))
	return atcNamePrefix + hex.EncodeToString(sum[:])[:32]
}

// parseList parses `zmx list` output. Each line is a tab-separated set of
// key=value fields prefixed by a reachability marker. Lines without a name are
// skipped so one broken entry never fails the whole listing.
func parseList(out []byte) []Session {
	var sessions []Session
	for line := range strings.SplitSeq(string(out), "\n") {
		// Drop the leading reachability marker ("→" for the active session,
		// spaces otherwise) before parsing fields.
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "→"))
		if line == "" {
			continue
		}
		s := parseSessionLine(line)
		if s.Name == "" {
			continue
		}
		sessions = append(sessions, s)
	}
	return sessions
}

func parseSessionLine(line string) Session {
	var s Session
	for field := range strings.SplitSeq(line, "\t") {
		key, value, ok := strings.Cut(field, "=")
		if !ok {
			continue
		}
		switch key {
		case "name":
			s.Name = value
		case "pid":
			s.PID, _ = strconv.Atoi(value)
		case "created":
			s.Created, _ = strconv.ParseInt(value, 10, 64)
		case "start_dir":
			s.StartDir = value
		case "cmd":
			s.Cmd = value
		}
	}
	return s
}

func sessionRunEnv() []string {
	return setEnv(os.Environ(), "TERM", defaultSessionTermType)
}

func setEnv(env []string, key, value string) []string {
	out := unsetEnv(env, key)
	return append(out, key+"="+value)
}

func unsetEnv(env []string, key string) []string {
	prefix := key + "="
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			continue
		}
		out = append(out, entry)
	}
	return out
}

// zmxCommand is the shared server-to-zmx subprocess boundary. ZMX_SESSION
// identifies a zmx client as nested inside an existing session; a long-running
// server must never pass its caller's identity to server-owned zmx commands.
func zmxCommand(ctx context.Context, bin string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = unsetEnv(os.Environ(), zmxSessionEnv)
	return cmd
}

// execRun is the production runFunc: it runs zmx as a child process.
func execRun(ctx context.Context, bin string, opts runOptions, args ...string) ([]byte, error) {
	cmd := zmxCommand(ctx, bin, args...)
	cmd.Dir = opts.dir
	if opts.env != nil {
		cmd.Env = unsetEnv(opts.env, zmxSessionEnv)
	}
	if opts.stdin != nil {
		cmd.Stdin = bytes.NewReader(opts.stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		// An *exec.Error means zmx could not be located or launched.
		if execErr, ok := errors.AsType[*exec.Error](err); ok {
			return nil, fmt.Errorf("cannot run zmx binary %q: %w; set zmx.bin in the config file or ATC_ZMX_BIN, or install zmx on PATH", bin, execErr.Err)
		}
		if msg := strings.TrimSpace(stderr.String()); msg != "" {
			return nil, fmt.Errorf("zmx %s failed: %s", args[0], msg)
		}
		return nil, fmt.Errorf("zmx %s failed: %w", args[0], err)
	}
	return stdout.Bytes(), nil
}

// ptySession is the production PTY: a pseudo-terminal master bound to a running
// `zmx attach` child process.
type ptySession struct {
	ptmx *os.File
	cmd  *exec.Cmd
}

func (p *ptySession) Read(b []byte) (int, error)  { return p.ptmx.Read(b) }
func (p *ptySession) Write(b []byte) (int, error) { return p.ptmx.Write(b) }

func (p *ptySession) Resize(rows, cols uint16) error {
	return pty.Setsize(p.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
}

// Close tears down this attach client: it closes the PTY master and kills then
// reaps the `zmx attach` process so no zombie is left behind. The underlying
// persistent session is unaffected — Close never runs `zmx kill`.
func (p *ptySession) Close() error {
	err := p.ptmx.Close()
	if p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
		_ = p.cmd.Wait()
	}
	return err
}

// execAttach is the production attachFunc: it spawns `zmx attach <name>` with a
// controlling PTY and returns it. The PTY is required because zmx attach drives
// an interactive login shell that expects a tty.
func execAttach(ctx context.Context, bin, name string, rows, cols uint16) (PTY, error) {
	if rows == 0 {
		rows = DefaultAttachRows
	}
	if cols == 0 {
		cols = DefaultAttachCols
	}
	cmd := zmxCommand(ctx, bin, "attach", name)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: rows, Cols: cols})
	if err != nil {
		if execErr, ok := errors.AsType[*exec.Error](err); ok {
			return nil, fmt.Errorf("cannot run zmx binary %q: %w; set zmx.bin in the config file or ATC_ZMX_BIN, or install zmx on PATH", bin, execErr.Err)
		}
		return nil, fmt.Errorf("zmx attach failed: %w", err)
	}
	return &ptySession{ptmx: ptmx, cmd: cmd}, nil
}
