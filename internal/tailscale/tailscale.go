// Package tailscale supervises tailnet exposure of the loopback listener
// (ATC-259, resolving ATC-247 §4): a foreground `tailscale serve` child
// owned by the server's lifetime, so the route disappears when ATC exits.
// The design is the legacy tailscale.ts contract (tag
// legacy-product-2026-08) ported to Go:
//
//   - Trust is fail-closed against observation — exposure is reported
//     serving only after the child's https banner is seen.
//   - Runtime failures never take down the loopback server; the supervisor
//     logs one useful reason and retries forever with bounded jittered
//     backoff, so a logged-out or restarting tailscaled self-heals.
//   - Only boot-time configuration failure (executable not found) is a
//     startup error, surfaced by ResolveExecutable before the server binds.
package tailscale

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	statusTimeout     = 10 * time.Second
	serveReadyTimeout = 15 * time.Second
	retryBaseDelay    = 500 * time.Millisecond
	retryMaxDelay     = 30 * time.Second
	// A run that survived this long was healthy; its eventual failure
	// starts a fresh backoff curve instead of inheriting a saturated one.
	healthyRunReset = time.Minute

	macOSAppExecutable = "/Applications/Tailscale.app/Contents/MacOS/Tailscale"
)

// ResolveExecutable resolves the tailscale CLI following Tailscale's
// supported installation shapes: normal PATH lookup everywhere, then the
// CLI bundled in the macOS app. A custom configured name or path is exact
// user intent and gets no fallback.
func ResolveExecutable(configured string) (string, error) {
	candidates := []string{configured}
	if configured == "tailscale" && runtime.GOOS == "darwin" {
		candidates = append(candidates, macOSAppExecutable)
	}
	for _, candidate := range candidates {
		if path, err := exec.LookPath(candidate); err == nil {
			return path, nil
		}
	}
	message := fmt.Sprintf("tailscale executable %q not found", configured)
	if len(candidates) > 1 {
		message += fmt.Sprintf(" (also tried %s)", strings.Join(candidates[1:], ", "))
	}
	return "", errors.New(message + "; install Tailscale or set tailscale_executable in config.toml / ATC_TAILSCALE_EXECUTABLE")
}

// Supervisor exposes the loopback listener over the tailnet. Same port,
// same API, the bearer token doing the auth work.
type Supervisor struct {
	// Executable is the resolved tailscale CLI path (ResolveExecutable).
	Executable string
	// Port is the bound listener port; serve fronts localhost:Port at
	// https://<node>:Port.
	Port   int
	Logger *slog.Logger

	// readyTimeout and waitDelay shrink the readiness and kill delays in
	// tests; zero means the production values.
	readyTimeout time.Duration
	waitDelay    time.Duration
}

// Run supervises exposure until ctx is cancelled. It never returns an
// error: failures are logged and retried. It returns only after the serve
// child, if any, has been terminated and reaped, so callers can wait on it
// for a clean shutdown.
func (s *Supervisor) Run(ctx context.Context) {
	delay := retryBaseDelay
	for {
		started := time.Now()
		err := s.attempt(ctx)
		if ctx.Err() != nil {
			return
		}
		s.Logger.Warn("tailscale exposure failed", "error", err)
		if time.Since(started) > healthyRunReset {
			delay = retryBaseDelay
		}
		// Full jitter on the upper half keeps herds apart without ever
		// halving below the base delay.
		jittered := delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		select {
		case <-time.After(jittered):
		case <-ctx.Done():
			return
		}
		delay = min(delay*2, retryMaxDelay)
	}
}

// attempt performs one preflight-then-serve cycle and returns why it ended.
func (s *Supervisor) attempt(ctx context.Context) error {
	dnsName, err := s.preflight(ctx)
	if err != nil {
		return err
	}
	return s.serve(ctx, dnsName)
}

// preflight asks tailscaled for its state and this node's DNS name; serve
// is only attempted against a running backend, so the retry loop reports
// "logged out" instead of an opaque serve failure.
func (s *Supervisor) preflight(ctx context.Context) (string, error) {
	statusCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	cmd := exec.CommandContext(statusCtx, s.Executable, "status", "--json")
	cmd.Env = cliEnv()
	out, err := cmd.Output()
	if statusCtx.Err() != nil && ctx.Err() == nil {
		return "", errors.New("tailscale status timed out")
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("tailscale status failed: %s", detail(exit.Stderr, err.Error()))
		}
		return "", fmt.Errorf("cannot run tailscale status: %w", err)
	}
	var status struct {
		BackendState string
		Self         struct{ DNSName string }
	}
	if err := json.Unmarshal(out, &status); err != nil {
		// The macOS GUI-bundled CLI reports some failures as plain text on
		// stdout with exit code 0, so quote the unexpected output instead
		// of a bare "invalid JSON" that would hide the actual diagnostic.
		return "", fmt.Errorf("tailscale status returned unexpected output: %s", detail(out, "(empty)"))
	}
	switch status.BackendState {
	case "Running":
	case "NeedsLogin":
		return "", errors.New("tailscale is logged out (BackendState NeedsLogin)")
	case "Stopped":
		return "", errors.New("tailscale is not running")
	default:
		return "", fmt.Errorf("tailscale is not running (BackendState %s)", status.BackendState)
	}
	dnsName := strings.TrimSuffix(status.Self.DNSName, ".")
	if dnsName == "" {
		return "", errors.New("tailscale status did not report this node's DNS name")
	}
	return dnsName, nil
}

// serve runs the foreground `tailscale serve` child until it exits or ctx
// is cancelled. Exposure is logged as serving only once the child's
// https banner is observed on either output stream.
func (s *Supervisor) serve(ctx context.Context, dnsName string) error {
	// Attempt-scoped context: cancelling it drives the same
	// interrupt → WaitDelay → kill teardown as parent cancellation. The
	// readiness-timeout path depends on that — see below.
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	cmd := exec.CommandContext(serveCtx, s.Executable,
		"serve", fmt.Sprintf("--https=%d", s.Port), fmt.Sprintf("localhost:%d", s.Port))
	cmd.Env = cliEnv()
	// Graceful stop: interrupt on ctx cancel so serve tears its route
	// down, SIGKILL only if it lingers past WaitDelay.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	if s.waitDelay != 0 {
		cmd.WaitDelay = s.waitDelay
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot run tailscale serve: %w", err)
	}

	// Only this node's own URL is the Serve banner. Tailscale also prints
	// https:// consent/login URLs when Serve or tailnet HTTPS still needs
	// enabling; counting those as ready would log "tailscale serving"
	// while exposure is still blocked on setup (fail-closed rule). The
	// port is matched loosely because the banner omits :443.
	banner := "https://" + dnsName
	ready := make(chan struct{})
	var readyOnce sync.Once
	var errTail tail
	var readers sync.WaitGroup
	watch := func(r io.Reader, keep bool) {
		defer readers.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			if keep {
				errTail.add(line)
			}
			if strings.Contains(line, banner) {
				readyOnce.Do(func() { close(ready) })
			}
		}
	}
	readers.Add(2)
	go watch(stdout, false)
	go watch(stderr, true)

	// Wait must not run before the pipe readers finish (os/exec contract).
	exited := make(chan error, 1)
	go func() {
		readers.Wait()
		exited <- cmd.Wait()
	}()

	readyTimeout := serveReadyTimeout
	if s.readyTimeout != 0 {
		readyTimeout = s.readyTimeout
	}
	bannerTimeout := time.NewTimer(readyTimeout)
	defer bannerTimeout.Stop()
	select {
	case <-ready:
	case err := <-exited:
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return fmt.Errorf("tailscale serve exited: %s", exitReason(err, &errTail))
	case <-bannerTimeout.C:
		// Cancel the attempt context instead of calling cmd.Cancel
		// directly: only context cancellation arms WaitDelay's kill, and
		// without it a child that ignores the interrupt and keeps its
		// pipes open would block <-exited forever.
		cancelServe()
		<-exited
		return fmt.Errorf("tailscale serve did not become ready within %s", readyTimeout)
	}

	s.Logger.Info("tailscale serving", "url", fmt.Sprintf("https://%s:%d", dnsName, s.Port))
	err = <-exited
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("tailscale serve exited: %s", exitReason(err, &errTail))
}

// cliEnv forces CLI mode: the macOS app and CLI are one executable, and
// Tailscale otherwise guesses which mode to enter from shell variables
// that services need not have.
func cliEnv() []string {
	return append(os.Environ(), "TAILSCALE_BE_CLI=1")
}

func exitReason(waitErr error, errTail *tail) string {
	if text := errTail.String(); text != "" {
		return text
	}
	if waitErr != nil {
		return waitErr.Error()
	}
	return "exit code 0"
}

// detail joins non-empty trimmed lines, falling back when nothing useful
// was captured.
func detail(raw []byte, fallback string) string {
	var parts []string
	for _, line := range strings.Split(string(raw), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			parts = append(parts, line)
		}
	}
	if len(parts) == 0 {
		return fallback
	}
	return strings.Join(parts, " ")
}

// tail keeps the last few stderr lines for failure reports.
type tail struct {
	mu    sync.Mutex
	lines []string
}

func (t *tail) add(line string) {
	if line = strings.TrimSpace(line); line == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lines = append(t.lines, line)
	if len(t.lines) > 8 {
		t.lines = t.lines[1:]
	}
}

func (t *tail) String() string {
	t.mu.Lock()
	defer t.mu.Unlock()
	return strings.Join(t.lines, " ")
}
