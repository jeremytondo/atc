// Package remote is the picker's side of `atc --remote <target>`
// (ATC-316, ATC-325): the launch-time discovery, guided setup, and
// bootstrap over plain OpenSSH that end with the remote server's tailnet
// URL, bearer token, and identity, and the interactive attach command
// that hands the caller's TTY to a remote terminal. Control traffic
// rides the server's HTTPS tailnet exposure; credentials stay in memory.
// Every remote command shares a private OpenSSH control socket, so
// switching sessions need not authenticate again. Close shuts down only
// this picker's connection and removes its socket directory. The target
// is any OpenSSH target and is passed through unmodified beyond a
// control-character check.
//
// This file is the transport: one method per remote command, each a
// deterministic operation the setup in setup.go sequences. Commands
// name the remote atc by the executable discovery found, quoted for the
// login shell, so an installation off the non-interactive PATH works
// without changing that PATH.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"
)

// Bootstrap is the one JSON object `atc __remote bootstrap` prints on
// the remote: the wire contract between an `atc --remote` launch and the
// remote binary. Decoded additively — every field here present, fields a
// newer remote adds ignored (equal protocols connect across releases),
// exactly one object, a fixed size cap. Version and Protocol are the
// running server's, read off its health answer after any start or
// restart.
type Bootstrap struct {
	URL      string `json:"url"`
	Token    string `json:"token"`
	Version  string `json:"version"`
	Protocol int    `json:"protocol"`
}

// String and GoString redact the token so no format verb can print it.
func (b Bootstrap) String() string {
	return fmt.Sprintf("Bootstrap{URL:%s Token:[redacted] Version:%s Protocol:%d}", b.URL, b.Version, b.Protocol)
}

func (b Bootstrap) GoString() string {
	return fmt.Sprintf("remote.Bootstrap{URL:%q, Token:%q, Version:%q, Protocol:%d}", b.URL, "[redacted]", b.Version, b.Protocol)
}

// maxBootstrapOutput caps what the local side will read from the remote
// command's stdout: the object is a few hundred bytes, so anything larger
// is not a bootstrap.
const maxBootstrapOutput = 16 << 10

// SSH runs OpenSSH children. run is the process seam: (*exec.Cmd).Run in
// production, a scripted double in tests.
type SSH struct {
	executable string
	target     string
	controlDir string
	run        func(*exec.Cmd) error
}

// NewSSH resolves OpenSSH and reserves a private control socket directory
// for one target. The caller must Close it, including after bootstrap fails.
func NewSSH(target string) (*SSH, error) {
	if err := validateTarget(target); err != nil {
		return nil, err
	}
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return nil, errors.New("ssh executable not found on PATH; install an OpenSSH client to use --remote")
	}
	// Both supported OS families provide /tmp. Keep the socket path short:
	// macOS's per-user TMPDIR and nested test directories can exceed the
	// Unix socket path limit. MkdirTemp creates an owner-only directory.
	dir, err := os.MkdirTemp("/tmp", "atc-ssh-")
	if err != nil {
		return nil, fmt.Errorf("create private ssh socket directory: %w", err)
	}
	return &SSH{executable: executable, target: target, controlDir: dir, run: (*exec.Cmd).Run}, nil
}

func (s *SSH) controlPath() string { return filepath.Join(s.controlDir, "control") }

// command reuses the bootstrap connection, or creates a new one if it
// expired or the transport failed. Keepalives belong on the master too.
// The idle lease bounds a leftover master's life if ATC is killed before
// Close can run; an active attachment does not count as idle.
func (s *SSH) command(ctx context.Context, options, command []string) *exec.Cmd {
	args := []string{"-S", s.controlPath(), "-o", "ControlMaster=auto", "-o", "ControlPersist=60",
		"-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3"}
	args = append(args, options...)
	args = append(args, "--", s.target)
	args = append(args, command...)
	return exec.CommandContext(ctx, s.executable, args...)
}

// Close stops this picker's master even when the run context was cancelled.
// An expired or never-started master needs no shutdown. Repeated calls are
// harmless, and neither the user's control sockets nor another picker is
// addressed. The bounded idle lease also covers a failed shutdown.
func (s *SSH) Close() error {
	var stopErr error
	if _, err := os.Stat(s.controlPath()); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, s.executable, "-S", s.controlPath(), "-O", "exit", "--", s.target)
		cmd.Stdout, cmd.Stderr = io.Discard, io.Discard
		cmd.WaitDelay = time.Second
		if err := s.run(cmd); err != nil {
			// The master can expire between Stat and the control request.
			if _, statErr := os.Stat(s.controlPath()); !errors.Is(statErr, os.ErrNotExist) {
				stopErr = fmt.Errorf("close private ssh connection: %w", err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		stopErr = err
	}
	return errors.Join(stopErr, os.RemoveAll(s.controlDir))
}

// validateTarget rejects only control characters; anything else is
// ssh's to judge, and the command line's "--" keeps an option-shaped
// target from being read as one.
func validateTarget(target string) error {
	if target == "" {
		return errors.New("--remote needs an ssh target")
	}
	if strings.IndexFunc(target, unicode.IsControl) >= 0 {
		return fmt.Errorf("invalid ssh target %q: contains a control character", target)
	}
	return nil
}

// output runs one remote command with stdin as its input and the caller's
// stderr shown live (passphrase, agent, and host-key prompts behave as for
// any ssh invocation; the remote's progress is visible), capturing stdout
// up to limit. what names the operation in failures. Any failure ends the
// launch: there is no retry, the user re-runs the command.
func (s *SSH) output(ctx context.Context, what string, limit int, stdin io.Reader, stderr io.Writer, command []string) ([]byte, error) {
	cmd := s.command(ctx, []string{"-T"}, command)
	cmd.Stdin = stdin
	stdout := &cappedBuffer{limit: limit}
	tail := &cappedBuffer{limit: 4 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, tail)
	if err := s.run(cmd); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, remoteFailure(s.target, what, err, tail.String())
	}
	if stdout.overflowed {
		return nil, fmt.Errorf("%s on %s printed more than %d bytes", what, s.target, limit)
	}
	return stdout.Bytes(), nil
}

// ErrPredatesSetup marks an atc that does not know the `__remote`
// commands: a build from before guided setup, which the rollout replaces
// by hand (ATC-325 records the prerequisite; nothing infers its
// protocol).
var ErrPredatesSetup = errors.New("predates guided setup")

// remoteFailure names the failure a non-zero exit most likely means:
// ssh itself (255), a command the remote shell could not find (127), an
// atc too old to know the command, or the command's own refusal.
func remoteFailure(target, what string, err error, stderr string) error {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) {
		return fmt.Errorf("cannot run ssh: %w", err)
	}
	code := exit.ExitCode()
	last := lastLine(stderr)
	switch {
	case code == 255:
		return fmt.Errorf("ssh to %s failed (exit 255): %s", target, last)
	case code == 127 || strings.Contains(stderr, "command not found") || strings.Contains(stderr, "No such file or directory"):
		return fmt.Errorf("%s on %s: the command could not be run: %s", what, target, last)
	case strings.Contains(stderr, "unknown command"):
		return fmt.Errorf("%s on %s: the atc there %w (%s)", what, target, ErrPredatesSetup, last)
	}
	return fmt.Errorf("%s on %s failed (exit %d): %s", what, target, code, last)
}

// Discover runs the discovery script under `sh` on the target — the
// script rides stdin, so the login shell, whatever it is, parses only the
// word "sh" — and reports the platform and where atc is.
func (s *SSH) Discover(ctx context.Context, stderr io.Writer) (Discovery, error) {
	out, err := s.output(ctx, "discovery", maxBootstrapOutput, strings.NewReader(discoverScript), stderr, []string{"sh"})
	if err != nil {
		return Discovery{}, err
	}
	return parseDiscovery(s.target, out)
}

// Inspect runs `__remote inspect` with the named executable and decodes
// what it reports about itself, the server, and the tailnet.
func (s *SSH) Inspect(ctx context.Context, executable string, stderr io.Writer) (Inspection, error) {
	out, err := s.output(ctx, "inspection", maxBootstrapOutput, strings.NewReader(""), stderr, []string{shellQuote(executable), "__remote", "inspect"})
	if err != nil {
		return Inspection{}, err
	}
	var insp Inspection
	if err := json.Unmarshal(out, &insp); err != nil {
		return Inspection{}, fmt.Errorf("inspection on %s returned unexpected output: %w", s.target, err)
	}
	if insp.Executable.Path == "" || insp.Executable.Protocol == 0 {
		return Inspection{}, fmt.Errorf("inspection on %s did not identify the executable", s.target)
	}
	return insp, nil
}

// stageScript receives an executable's bytes on stdin and publishes
// nothing: it writes them to a fresh private file beside the target and
// prints that path. Each run owns its own file, so concurrent runs never
// touch each other's. Written without single quotes so the login shell
// passes it through unchanged inside one pair of them.
const stageScript = `set -e; dir=$1; mkdir -p "$dir"; staged=$(mktemp "$dir/.atc-setup-XXXXXX"); cat > "$staged"; chmod 755 "$staged"; printf "%s\n" "$staged"`

// Stage copies an executable's bytes to a private file in dir on the
// target and returns its path. Nothing is replaced: Promote does that,
// after the staged file has proven itself.
func (s *SSH) Stage(ctx context.Context, dir string, executable []byte, stderr io.Writer) (string, error) {
	out, err := s.output(ctx, "staging", 4<<10, bytes.NewReader(executable), stderr, []string{"sh", "-c", shellQuote(stageScript), "sh", shellQuote(dir)})
	if err != nil {
		return "", err
	}
	staged := strings.TrimSpace(string(out))
	if !strings.HasPrefix(staged, filepath.Join(dir, ".atc-setup-")) {
		return "", fmt.Errorf("staging on %s returned unexpected output: %q", s.target, staged)
	}
	return staged, nil
}

// Promote renames the staged file over target: one atomic rename on the
// same filesystem, never a write into the existing executable.
func (s *SSH) Promote(ctx context.Context, staged, target string, stderr io.Writer) error {
	_, err := s.output(ctx, "installation", 4<<10, strings.NewReader(""), stderr, []string{"mv", "-f", shellQuote(staged), shellQuote(target)})
	return err
}

// Remove deletes a staged file that will not be promoted.
func (s *SSH) Remove(ctx context.Context, staged string, stderr io.Writer) error {
	_, err := s.output(ctx, "cleanup", 4<<10, strings.NewReader(""), stderr, []string{"rm", "-f", shellQuote(staged)})
	return err
}

// Configure enables ATC's tailnet exposure setting in the target's
// config.toml through the named executable.
func (s *SSH) Configure(ctx context.Context, executable string, stderr io.Writer) error {
	_, err := s.output(ctx, "configuration", 4<<10, strings.NewReader(""), stderr, []string{shellQuote(executable), "__remote", "configure", "--tailscale"})
	return err
}

// Bootstrap runs `__remote bootstrap` with the named executable: start
// the server if it is stopped, restart it when approved (with --tailscale
// when the running launch disabled exposure by flag), wait for the
// tailnet route, and return the connection material. stdin is the
// caller's so the remote's prompts, if any, reach a person.
func (s *SSH) Bootstrap(ctx context.Context, executable string, restart, tailscaleFlag bool, stdin io.Reader, stderr io.Writer) (Bootstrap, error) {
	command := []string{shellQuote(executable), "__remote", "bootstrap"}
	if restart {
		command = append(command, "--restart")
	}
	if tailscaleFlag {
		command = append(command, "--tailscale")
	}
	out, err := s.output(ctx, "bootstrap", maxBootstrapOutput, stdin, stderr, command)
	if err != nil {
		return Bootstrap{}, err
	}
	return decodeBootstrap(s.target, out)
}

// shellQuote renders one argument for the remote login shell: plain
// words pass as they are; anything else is single-quoted, the one form
// POSIX shells and fish read identically (an embedded quote closes,
// escapes, and reopens).
func shellQuote(arg string) string {
	plain := func(r rune) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_./:@%+=,-", r)
	}
	if arg != "" && !strings.HasPrefix(arg, "-") && strings.IndexFunc(arg, func(r rune) bool { return !plain(r) }) < 0 {
		return arg
	}
	return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'"
}

// decodeBootstrap decodes exactly one object, requiring the known fields
// and rejecting anything trailing. The output itself never rides an
// error: a rejected object may still hold the token.
func decodeBootstrap(target string, out []byte) (Bootstrap, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	var b Bootstrap
	if err := dec.Decode(&b); err != nil {
		return Bootstrap{}, fmt.Errorf("bootstrap on %s returned unexpected output: %w", target, err)
	}
	// Decoder.More is false at a stray ']' or '}' too; only EOF proves the
	// object stood alone.
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return Bootstrap{}, fmt.Errorf("bootstrap on %s returned more than one JSON value", target)
	}
	for name, value := range map[string]string{"url": b.URL, "token": b.Token, "version": b.Version} {
		if value == "" {
			return Bootstrap{}, fmt.Errorf("bootstrap on %s returned no %s", target, name)
		}
	}
	if b.Protocol <= 0 {
		return Bootstrap{}, fmt.Errorf("bootstrap on %s returned no protocol", target)
	}
	parsed, err := url.Parse(b.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" {
		return Bootstrap{}, fmt.Errorf("bootstrap on %s returned a non-HTTPS url %q", target, b.URL)
	}
	return b, nil
}

// AttachCommand is the interactive channel: `ssh -tt <target> <atc>
// terminal attach <id>`, run as a child so the picker takes the terminal
// back when it exits, through the executable setup settled on. The
// keepalives bound transport-loss detection to roughly fifteen seconds.
// The remote attach uses the remote's own token file; nothing secret
// rides argv or the environment. LogLevel=ERROR keeps the routine
// "Connection to ... closed" line from flashing on detach, while leaving
// errors and authentication prompts visible.
func (s *SSH) AttachCommand(ctx context.Context, executable, terminalID string) *exec.Cmd {
	return s.command(ctx, []string{"-tt", "-o", "LogLevel=ERROR"}, []string{shellQuote(executable), "terminal", "attach", terminalID})
}

// IsTransportLoss reports whether an attach child ended because the SSH
// transport failed (OpenSSH's exit 255) rather than because the remote
// command ended. Detach versus loss is decided solely by this status.
func IsTransportLoss(err error) bool {
	var exit interface{ ExitCode() int }
	return errors.As(err, &exit) && exit.ExitCode() == 255
}

// cappedBuffer keeps the first limit bytes and remembers overflow. The
// buffer is a field, not embedded, so no promoted WriteString can bypass
// the cap.
type cappedBuffer struct {
	buf        bytes.Buffer
	limit      int
	overflowed bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	room := b.limit - b.buf.Len()
	if len(p) > room {
		b.overflowed = true
		if room > 0 {
			b.buf.Write(p[:room])
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *cappedBuffer) Bytes() []byte  { return b.buf.Bytes() }
func (b *cappedBuffer) String() string { return b.buf.String() }

func lastLine(text string) string {
	lines := strings.Split(strings.TrimSpace(text), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return "(no output)"
	}
	return last
}
