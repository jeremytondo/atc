// Package remote is the picker's side of `atc --remote <target>`
// (ATC-316): the launch-time bootstrap over plain OpenSSH that returns
// the remote server's tailnet URL, bearer token, and version, and the
// interactive attach command that hands the caller's TTY to a remote
// terminal. SSH is used for exactly those two things; control traffic
// rides the server's HTTPS tailnet exposure, nothing is forwarded, and
// nothing is cached on disk. The target is any OpenSSH target and is
// passed through unmodified beyond a control-character check.
package remote

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os/exec"
	"strings"
	"unicode"
)

// Bootstrap is the one JSON object `atc __bootstrap` prints on the
// remote: the wire contract between an `atc --remote` launch and the
// remote binary. Decoded strictly — no unknown fields, every field
// present, a fixed size cap.
type Bootstrap struct {
	URL     string `json:"url"`
	Token   string `json:"token"`
	Version string `json:"version"`
}

// String redacts the token so no format verb can print it.
func (b Bootstrap) String() string {
	return fmt.Sprintf("Bootstrap{URL:%s Token:[redacted] Version:%s}", b.URL, b.Version)
}

// maxBootstrapOutput caps what the local side will read from the remote
// command's stdout: the object is a few hundred bytes, so anything larger
// is not a bootstrap.
const maxBootstrapOutput = 16 << 10

// SSH runs OpenSSH children. run is the process seam: (*exec.Cmd).Run in
// production, a scripted double in tests.
type SSH struct {
	executable string
	run        func(*exec.Cmd) error
}

// NewSSH resolves the OpenSSH client on PATH.
func NewSSH() (*SSH, error) {
	executable, err := exec.LookPath("ssh")
	if err != nil {
		return nil, errors.New("ssh executable not found on PATH; install an OpenSSH client to use --remote")
	}
	return &SSH{executable: executable, run: (*exec.Cmd).Run}, nil
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

// Bootstrap runs the remote bootstrap over `ssh <target>` with the
// caller's stdin and stderr, so passphrase, agent, and host-key prompts
// behave as for any ssh invocation and the remote's progress is visible
// live. Stdout is captured and decoded strictly. Any failure ends the
// launch: there is no retry, the user re-runs the command.
func (s *SSH) Bootstrap(ctx context.Context, target string, stdin io.Reader, stderr io.Writer) (Bootstrap, error) {
	if err := validateTarget(target); err != nil {
		return Bootstrap{}, err
	}
	cmd := exec.CommandContext(ctx, s.executable, "--", target, "atc", "__bootstrap")
	cmd.Stdin = stdin
	stdout := &cappedBuffer{limit: maxBootstrapOutput}
	tail := &cappedBuffer{limit: 4 << 10}
	cmd.Stdout = stdout
	cmd.Stderr = io.MultiWriter(stderr, tail)
	if err := s.run(cmd); err != nil {
		if ctx.Err() != nil {
			return Bootstrap{}, ctx.Err()
		}
		return Bootstrap{}, bootstrapFailure(target, err, tail.String())
	}
	if stdout.overflowed {
		return Bootstrap{}, fmt.Errorf("bootstrap on %s printed more than %d bytes; expected one small JSON object", target, maxBootstrapOutput)
	}
	return decodeBootstrap(target, stdout.Bytes())
}

// bootstrapFailure names the failure a non-zero exit most likely means:
// ssh itself (255), no atc on the remote's non-interactive PATH (127),
// an atc too old to know the command, or the bootstrap's own refusal.
func bootstrapFailure(target string, err error, stderr string) error {
	var exit interface{ ExitCode() int }
	if !errors.As(err, &exit) {
		return fmt.Errorf("cannot run ssh: %w", err)
	}
	code := exit.ExitCode()
	last := lastLine(stderr)
	switch {
	case code == 255:
		return fmt.Errorf("ssh to %s failed (exit 255): %s", target, last)
	case code == 127 || strings.Contains(stderr, "command not found"):
		return fmt.Errorf("atc is not installed on %s, or not on its non-interactive PATH: %s", target, last)
	case strings.Contains(stderr, "unknown command"):
		return fmt.Errorf("the atc on %s is too old to have the bootstrap command (%s); upgrade it with `atc upgrade` there", target, last)
	}
	return fmt.Errorf("bootstrap on %s failed (exit %d): %s", target, code, last)
}

// decodeBootstrap decodes exactly one object with exactly the known
// fields and rejects anything missing or trailing. The output itself
// never rides an error: a rejected object may still hold the token.
func decodeBootstrap(target string, out []byte) (Bootstrap, error) {
	dec := json.NewDecoder(bytes.NewReader(out))
	dec.DisallowUnknownFields()
	var b Bootstrap
	if err := dec.Decode(&b); err != nil {
		return Bootstrap{}, fmt.Errorf("bootstrap on %s returned unexpected output: %w", target, err)
	}
	if dec.More() {
		return Bootstrap{}, fmt.Errorf("bootstrap on %s returned more than one JSON value", target)
	}
	for name, value := range map[string]string{"url": b.URL, "token": b.Token, "version": b.Version} {
		if value == "" {
			return Bootstrap{}, fmt.Errorf("bootstrap on %s returned no %s", target, name)
		}
	}
	parsed, err := url.Parse(b.URL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return Bootstrap{}, fmt.Errorf("bootstrap on %s returned a non-HTTPS url %q", target, b.URL)
	}
	return b, nil
}

// AttachCommand is the interactive channel: `ssh -tt <target> atc
// terminal attach <id>`, run as a child so the picker takes the terminal
// back when it exits. The keepalives bound transport-loss detection to
// roughly fifteen seconds; connection sharing is deliberately left to the
// user's SSH configuration. The remote attach uses the remote's own token
// file; nothing secret rides argv or the environment.
func (s *SSH) AttachCommand(ctx context.Context, target, terminalID string) *exec.Cmd {
	return exec.CommandContext(ctx, s.executable, "-tt", "-o", "ServerAliveInterval=5", "-o", "ServerAliveCountMax=3",
		"--", target, "atc", "terminal", "attach", terminalID)
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
