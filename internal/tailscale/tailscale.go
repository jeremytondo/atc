// Package tailscale supervises exposure of loopback listeners through the
// machine's existing Tailscale node: a foreground `tailscale serve` child
// fronting the API on the tailnet (ATC-259, resolving ATC-247 §4), and a
// foreground `tailscale funnel` child fronting the webhook receiver on the
// public internet (ATC-306). Both are owned by the server's lifetime, so
// the route disappears when ATC exits. The design is the legacy
// tailscale.ts contract (tag legacy-product-2026-08) ported to Go:
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
	"net"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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

// Report is one observation of managed exposure, delivered to a
// Supervisor's observer on every transition so the owner can show setup
// state without parsing logs.
type Report struct {
	// Serving: the child printed this node's own URL, so exposure is
	// established. False while converging or after a failure.
	Serving bool
	// URL is the endpoint: the live one while Serving, the expected one
	// once the node's DNS name is known, "" before that.
	URL string
	// Problem is why the last attempt ended; "" while serving.
	Problem string
	// Action is Tailscale's own instruction when an operator must act — an
	// approval or enable link and its explanation — captured from the
	// child's output; "" when it printed none.
	Action string
}

// Supervisor runs one foreground tailscale exposure child for the
// server's lifetime: `tailscale serve` fronting the API on the tailnet, or
// `tailscale funnel` fronting the webhook receiver on the public internet
// (ATC-306). Both share the preflight, banner-gated readiness, retry, and
// teardown contract; they differ in the subcommand and in the ports.
type Supervisor struct {
	// executable is the resolved tailscale CLI path (ResolveExecutable).
	executable string
	// funnel selects `tailscale funnel` (public) over `tailscale serve`
	// (tailnet only).
	funnel bool
	// publicPort is the --https port; targetPort the localhost port the
	// child proxies to. Serve uses one port for both.
	publicPort int
	targetPort int
	// observe receives every state transition; nil discards them.
	observe func(Report)
	logger  *slog.Logger

	// readyTimeout and waitDelay shrink the readiness and kill delays in
	// tests; zero means the production values.
	readyTimeout time.Duration
	waitDelay    time.Duration
}

// NewSupervisor builds a Supervisor for the resolved tailscale CLI path
// (ResolveExecutable) fronting the bound API listener port on the tailnet:
// same port, same API, the bearer token doing the auth work. A nil logger
// discards supervision events.
func NewSupervisor(executable string, port int, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Supervisor{executable: executable, publicPort: port, targetPort: port, logger: logger}
}

// NewFunnelSupervisor builds a Supervisor that exposes localhost:targetPort
// to the public internet at https://<node>:publicPort through Tailscale
// Funnel, reporting every transition to observe (nil discards). Funnel
// serves only 443, 8443, and 10000; the caller validates. A nil logger
// discards supervision events.
func NewFunnelSupervisor(executable string, publicPort, targetPort int, observe func(Report), logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	return &Supervisor{executable: executable, funnel: true, publicPort: publicPort, targetPort: targetPort, observe: observe, logger: logger}
}

func (s *Supervisor) report(r Report) {
	if s.observe != nil {
		s.observe(r)
	}
}

// subcommand names the exposure the child runs, for command lines and
// messages.
func (s *Supervisor) subcommand() string {
	if s.funnel {
		return "funnel"
	}
	return "serve"
}

// url is the endpoint this Supervisor establishes for the node.
func (s *Supervisor) url(dnsName string) string {
	if s.funnel {
		return PublicURL(dnsName, s.publicPort)
	}
	return HTTPSURL(dnsName, s.publicPort)
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
			s.report(Report{Problem: "stopped"})
			return
		}
		s.logger.Warn("tailscale exposure failed", "mode", s.subcommand(), "error", err)
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

// attempt performs one preflight-then-serve cycle and returns why it
// ended, reporting the failure (with the expected URL once the node is
// known, and any operator action the child printed) to the observer.
func (s *Supervisor) attempt(ctx context.Context) error {
	dnsName, err := s.preflight(ctx)
	if err != nil {
		s.report(Report{Problem: err.Error()})
		return err
	}
	var action string
	err = s.serve(ctx, dnsName, &action)
	if err != nil && ctx.Err() == nil {
		s.report(Report{URL: s.url(dnsName), Problem: err.Error(), Action: action})
	}
	return err
}

// preflight asks tailscaled for this node's DNS name; serve is only
// attempted against a running backend, so the retry loop reports "logged
// out" instead of an opaque serve failure.
func (s *Supervisor) preflight(ctx context.Context) (string, error) {
	return DNSName(ctx, s.executable)
}

// DNSName asks tailscaled for its state and this node's DNS name, reported
// only for a running backend so callers see "logged out" instead of an
// opaque failure. The serve supervisor preflights with it, and `atc server
// status` uses it to print the paste-ready tailnet URL.
func DNSName(ctx context.Context, executable string) (string, error) {
	statusCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	cmd := exec.CommandContext(statusCtx, executable, "status", "--json")
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

type serveConfig struct {
	TCP map[string]struct {
		HTTPS bool
	}
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string
		}
	}
}

type serveStatus struct {
	serveConfig
	// Foreground routes are keyed by the owning CLI process. Background
	// routes occupy the promoted top-level TCP/Web fields.
	Foreground map[string]serveConfig
}

// HTTPSURL formats the one public endpoint shape shared by Serve supervision
// and lifecycle/status reporting.
func HTTPSURL(dnsName string, port int) string {
	return "https://" + net.JoinHostPort(dnsName, strconv.Itoa(port))
}

// PublicURL is HTTPSURL with the default HTTPS port left implicit — the
// form Tailscale prints for a Funnel on 443 and the one people paste into
// a webhook sender.
func PublicURL(dnsName string, port int) string {
	if port == 443 {
		return "https://" + dnsName
	}
	return HTTPSURL(dnsName, port)
}

// ServeURL reports the HTTPS URL only when tailscaled's live Serve
// configuration contains the exact route ATC requested. DNSName alone proves
// node connectivity, not that `tailscale serve` obtained approval and exposed
// this backend.
func ServeURL(ctx context.Context, executable, dnsName string, port int) (string, error) {
	statusCtx, cancel := context.WithTimeout(ctx, statusTimeout)
	defer cancel()
	cmd := exec.CommandContext(statusCtx, executable, "serve", "status", "--json")
	cmd.Env = cliEnv()
	out, err := cmd.Output()
	if statusCtx.Err() != nil && ctx.Err() == nil {
		return "", errors.New("tailscale serve status timed out")
	}
	if err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return "", fmt.Errorf("tailscale serve status failed: %s", detail(exit.Stderr, err.Error()))
		}
		return "", fmt.Errorf("cannot run tailscale serve status: %w", err)
	}
	var status serveStatus
	if err := json.Unmarshal(out, &status); err != nil {
		return "", fmt.Errorf("tailscale serve status returned unexpected output: %s", detail(out, "(empty)"))
	}
	portText := strconv.Itoa(port)
	endpoint := HTTPSURL(dnsName, port)
	authority := strings.TrimPrefix(endpoint, "https://")
	configs := []serveConfig{status.serveConfig}
	for _, foreground := range status.Foreground {
		configs = append(configs, foreground)
	}
	for _, cfg := range configs {
		tcp, tcpOK := cfg.TCP[portText]
		web, webOK := cfg.Web[authority]
		handler, handlerOK := web.Handlers["/"]
		proxy, proxyErr := url.Parse(handler.Proxy)
		if proxyErr != nil {
			continue
		}
		proxyHost := net.ParseIP(proxy.Hostname())
		proxyLoopback := proxy.Hostname() == "localhost" || (proxyHost != nil && proxyHost.IsLoopback())
		if tcpOK && tcp.HTTPS && webOK && handlerOK && proxy.Scheme == "http" && proxyLoopback && proxy.Port() == portText {
			return endpoint, nil
		}
	}
	return "", fmt.Errorf("tailscale serve has not exposed https://%s yet", authority)
}

// serve runs the foreground exposure child until it exits or ctx is
// cancelled. Exposure is logged as serving only once the child's own-URL
// banner is observed on either output stream. When the child ends without
// that banner, *action receives any operator instruction it printed (an
// approval or enable link with its explanation).
func (s *Supervisor) serve(ctx context.Context, dnsName string, action *string) error {
	// The parent-death signal is tied to the thread that forks the child,
	// so that thread stays alive — locked to this goroutine — until the
	// child has exited.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	// Attempt-scoped context: cancelling it drives the same
	// interrupt → WaitDelay → kill teardown as parent cancellation. The
	// readiness-timeout path depends on that — see below.
	serveCtx, cancelServe := context.WithCancel(ctx)
	defer cancelServe()
	cmd := exec.CommandContext(serveCtx, s.executable,
		s.subcommand(), fmt.Sprintf("--https=%d", s.publicPort), fmt.Sprintf("localhost:%d", s.targetPort))
	cmd.Env = cliEnv()
	// Graceful stop: interrupt on ctx cancel so the child tears its route
	// down, SIGKILL only if it lingers past WaitDelay. On Linux the kernel
	// also kills the child if this process dies without running any of
	// this (a forced termination): a foreground exposure lives exactly as
	// long as the CLI process holding it, so no orphan route survives.
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	if s.waitDelay != 0 {
		cmd.WaitDelay = s.waitDelay
	}
	cmd.SysProcAttr = childAttr()

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot run tailscale %s: %w", s.subcommand(), err)
	}

	// Only this node's own URL is the banner. Tailscale also prints
	// https:// consent/login URLs when Serve, Funnel, or tailnet HTTPS
	// still needs enabling; counting those as ready would log "serving"
	// while exposure is still blocked on setup (fail-closed rule). The
	// port is matched loosely because the banner omits :443.
	banner := "https://" + dnsName
	ready := make(chan struct{})
	var readyOnce sync.Once
	var errTail, outTail tail
	var readers sync.WaitGroup
	watch := func(r io.Reader, keep *tail) {
		defer readers.Done()
		scanner := bufio.NewScanner(r)
		for scanner.Scan() {
			line := scanner.Text()
			keep.add(line)
			if strings.Contains(line, banner) {
				readyOnce.Do(func() { close(ready) })
			}
		}
	}
	readers.Add(2)
	go watch(stdout, &outTail)
	go watch(stderr, &errTail)

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
		*action = actionFrom(&outTail, &errTail, banner)
		return fmt.Errorf("tailscale %s exited: %s", s.subcommand(), exitReason(err, &errTail))
	case <-bannerTimeout.C:
		// Cancel the attempt context instead of calling cmd.Cancel
		// directly: only context cancellation arms WaitDelay's kill, and
		// without it a child that ignores the interrupt and keeps its
		// pipes open would block <-exited forever.
		cancelServe()
		<-exited
		*action = actionFrom(&outTail, &errTail, banner)
		return fmt.Errorf("tailscale %s did not become ready within %s", s.subcommand(), readyTimeout)
	}

	url := s.url(dnsName)
	s.logger.Info("tailscale serving", "mode", s.subcommand(), "url", url)
	s.report(Report{Serving: true, URL: url})
	err = <-exited
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return fmt.Errorf("tailscale %s exited: %s", s.subcommand(), exitReason(err, &errTail))
}

// actionFrom extracts an operator instruction from a failed child's
// output: Tailscale prints approval and enable links (login.tailscale.com,
// tailscale.com/s/...) with a sentence of explanation when Serve, Funnel,
// or HTTPS needs enabling. Any output line carrying a URL other than this
// node's own banner marks the output as such an instruction, which is
// returned whole so the explanation travels with the link.
func actionFrom(outTail, errTail *tail, banner string) string {
	for _, text := range []string{outTail.String(), errTail.String()} {
		for _, line := range strings.Fields(text) {
			if strings.HasPrefix(line, "https://") && !strings.HasPrefix(line, banner) {
				return text
			}
		}
	}
	return ""
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
