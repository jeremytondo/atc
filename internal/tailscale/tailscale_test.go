package tailscale

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const statusRunning = `{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."}}`

// fake writes a shell script standing in for the tailscale CLI.
func fake(t *testing.T, script string, logs io.Writer) *Supervisor {
	t.Helper()
	path := filepath.Join(t.TempDir(), "tailscale")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return NewSupervisor(path, 7331, slog.New(slog.NewTextHandler(logs, nil)))
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestResolveExecutableNotFound(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := ResolveExecutable("tailscale-definitely-absent")
	if err == nil || !strings.Contains(err.Error(), "tailscale_executable") {
		t.Errorf("err = %v, want message naming the config key", err)
	}
}

func TestResolveExecutableCustomPath(t *testing.T) {
	s := fake(t, "exit 0", io.Discard)
	resolved, err := ResolveExecutable(s.executable)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != s.executable {
		t.Errorf("resolved %q, want %q", resolved, s.executable)
	}
}

func TestPreflightRunning(t *testing.T) {
	s := fake(t, `printf '%s' '`+statusRunning+`'`, io.Discard)
	dnsName, err := s.preflight(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if dnsName != "host.tailnet.ts.net" {
		t.Errorf("dnsName = %q, want trailing dot stripped", dnsName)
	}
}

func TestPreflightNeedsLogin(t *testing.T) {
	s := fake(t, `printf '%s' '{"BackendState":"NeedsLogin","Self":{"DNSName":""}}'`, io.Discard)
	if _, err := s.preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "logged out") {
		t.Errorf("err = %v, want logged-out reason", err)
	}
}

// The macOS GUI-bundled CLI reports some failures as plain text with exit
// code 0; the diagnostic must be quoted, not hidden behind "invalid JSON".
func TestPreflightQuotesUnexpectedOutput(t *testing.T) {
	s := fake(t, `echo "please run tailscale up"`, io.Discard)
	if _, err := s.preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "please run tailscale up") {
		t.Errorf("err = %v, want the CLI's own diagnostic quoted", err)
	}
}

func TestPreflightReportsStderrOnFailure(t *testing.T) {
	s := fake(t, `echo "no tailscaled socket" >&2; exit 1`, io.Discard)
	if _, err := s.preflight(context.Background()); err == nil || !strings.Contains(err.Error(), "no tailscaled socket") {
		t.Errorf("err = %v, want stderr detail", err)
	}
}

func TestServeURLVerifiesExactRoute(t *testing.T) {
	for name, output := range map[string]string{
		"background route": `{"TCP":{"7331":{"HTTPS":true}},"Web":{"host.tailnet.ts.net:7331":{"Handlers":{"/":{"Proxy":"http://localhost:7331"}}}}}`,
		"foreground route": `{"Foreground":{"owner-id":{"TCP":{"7331":{"HTTPS":true}},"Web":{"host.tailnet.ts.net:7331":{"Handlers":{"/":{"Proxy":"http://localhost:7331"}}}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := fake(t, `printf '%s' '`+output+`'`, io.Discard)
			got, err := ServeURL(context.Background(), s.executable, "host.tailnet.ts.net", 7331)
			if err != nil {
				t.Fatal(err)
			}
			if got != "https://host.tailnet.ts.net:7331" {
				t.Errorf("ServeURL = %q, want the verified HTTPS endpoint", got)
			}
		})
	}
}

func TestServeURLRejectsAbsentOrWrongRoute(t *testing.T) {
	for name, output := range map[string]string{
		"empty config":  `{}`,
		"wrong backend": `{"TCP":{"7331":{"HTTPS":true}},"Web":{"host.tailnet.ts.net:7331":{"Handlers":{"/":{"Proxy":"http://localhost:9000"}}}}}`,
		"plain http":    `{"TCP":{"7331":{"HTTP":true}},"Web":{"host.tailnet.ts.net:7331":{"Handlers":{"/":{"Proxy":"http://localhost:7331"}}}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := fake(t, `printf '%s' '`+output+`'`, io.Discard)
			if _, err := ServeURL(context.Background(), s.executable, "host.tailnet.ts.net", 7331); err == nil || !strings.Contains(err.Error(), "has not exposed") {
				t.Errorf("ServeURL error = %v, want a pending-route diagnostic", err)
			}
		})
	}
}

func TestServeURLCarriesStatusFailure(t *testing.T) {
	s := fake(t, `echo "serve status unavailable" >&2; exit 1`, io.Discard)
	if _, err := ServeURL(context.Background(), s.executable, "host.tailnet.ts.net", 7331); err == nil || !strings.Contains(err.Error(), "serve status unavailable") {
		t.Errorf("ServeURL error = %v, want stderr detail", err)
	}
}

func TestServeReadyThenReapedOnCancel(t *testing.T) {
	var logs lockedBuffer
	s := fake(t, `echo "Available within your tailnet: https://host.tailnet.ts.net:7331/"; exec sleep 30`, &logs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, "host.tailnet.ts.net", new(string)) }()
	waitFor(t, "serving log line", func() bool {
		return strings.Contains(logs.String(), "tailscale serving") &&
			strings.Contains(logs.String(), "https://host.tailnet.ts.net:7331")
	})
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("serve after cancel = %v, want context.Canceled", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serve child was not reaped after cancellation")
	}
}

// Tailscale prints https:// consent/login URLs when Serve or tailnet HTTPS
// still needs enabling; those must not be mistaken for the Serve banner.
func TestServeIgnoresUnrelatedHTTPSURLs(t *testing.T) {
	var logs lockedBuffer
	s := fake(t, `echo "To approve, visit: https://login.tailscale.com/f/serve?node=x"; exec sleep 30`, &logs)
	s.readyTimeout = 200 * time.Millisecond
	err := s.serve(context.Background(), "host.tailnet.ts.net", new(string))
	if err == nil || !strings.Contains(err.Error(), "did not become ready") {
		t.Errorf("err = %v, want readiness timeout", err)
	}
	if strings.Contains(logs.String(), "tailscale serving") {
		t.Error("consent URL was logged as serving")
	}
}

// A child that ignores the interrupt and keeps its pipes open must still be
// killed after the readiness timeout, or the retry loop never resumes.
func TestServeTimeoutKillsHungChild(t *testing.T) {
	s := fake(t, `trap '' INT TERM
while :; do sleep 0.05; done`, io.Discard)
	s.readyTimeout = 200 * time.Millisecond
	s.waitDelay = 200 * time.Millisecond
	done := make(chan error, 1)
	go func() { done <- s.serve(context.Background(), "host.tailnet.ts.net", new(string)) }()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "did not become ready") {
			t.Errorf("err = %v, want readiness timeout", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("hung serve child was never killed after the readiness timeout")
	}
}

func TestServeFailureCarriesStderr(t *testing.T) {
	s := fake(t, `echo "serve not permitted" >&2; exit 1`, io.Discard)
	err := s.serve(context.Background(), "host.tailnet.ts.net", new(string))
	if err == nil || !strings.Contains(err.Error(), "serve not permitted") {
		t.Errorf("err = %v, want stderr detail", err)
	}
}

// A nil logger must default to discard: Run logs every failed attempt and
// previously dereferenced the logger unguarded.
func TestNilLoggerDefaultsToDiscard(t *testing.T) {
	s := NewSupervisor(fake(t, "exit 1", io.Discard).executable, 7331, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

func TestRunRetriesAndStopsOnCancel(t *testing.T) {
	var logs lockedBuffer
	s := fake(t, `echo "transient failure" >&2; exit 1`, &logs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, "two retry attempts", func() bool {
		return strings.Count(logs.String(), "tailscale exposure failed") >= 2
	})
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
}

type lockedBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// A Funnel child runs the funnel subcommand against the receiver's port,
// reports the public URL (443 implicit) once its banner appears, and
// reports "stopped" on cancellation.
func TestFunnelReportsServingAndStop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tailscale")
	script := "#!/bin/sh\nif [ \"$1\" = status ]; then printf '%s' '" + statusRunning + "'; exit 0; fi\n" +
		"echo \"$@\" > " + dir + "/args\n" +
		"echo 'Available on the internet:'; echo; echo 'https://host.tailnet.ts.net/'; exec sleep 30\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var reports lockedReports
	s := NewFunnelSupervisor(path, 443, 39999, reports.add, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, "serving report", func() bool {
		last, ok := reports.last()
		return ok && last.Serving
	})
	last, _ := reports.last()
	if last.URL != "https://host.tailnet.ts.net" {
		t.Errorf("serving URL = %q, want the 443-implicit public URL", last.URL)
	}
	args, err := os.ReadFile(filepath.Join(dir, "args"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "funnel --https=443 localhost:39999" {
		t.Errorf("funnel args = %q", got)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not stop after cancellation")
	}
	if last, _ := reports.last(); last.Serving || last.Problem != "stopped" {
		t.Errorf("final report = %+v, want stopped", last)
	}
}

// When Funnel needs enabling, Tailscale prints an instruction with a link
// and exits; the report carries the expected URL, the failure, and the
// whole instruction so status can show it. The retry loop then keeps
// trying, which is how approval activates exposure without a restart.
func TestFunnelReportsOperatorAction(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tailscale")
	script := "#!/bin/sh\nif [ \"$1\" = status ]; then printf '%s' '" + statusRunning + "'; exit 0; fi\n" +
		"echo 'Funnel not available; \"funnel\" node attribute not set. See https://tailscale.com/s/no-funnel.' >&2; exit 1\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	var reports lockedReports
	s := NewFunnelSupervisor(path, 8443, 39999, reports.add, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	waitFor(t, "two failure reports", func() bool { return reports.count() >= 2 })
	cancel()
	<-done
	first := reports.all()[0]
	if first.Serving || first.URL != "https://host.tailnet.ts.net:8443" {
		t.Errorf("report = %+v, want the expected URL without serving", first)
	}
	if !strings.Contains(first.Problem, "tailscale funnel exited") || !strings.Contains(first.Action, "https://tailscale.com/s/no-funnel") {
		t.Errorf("report = %+v, want the exit reason and the enable link as the action", first)
	}
}

// A failure that prints no link is not an operator action.
func TestActionFromIgnoresPlainFailures(t *testing.T) {
	var out, errs tail
	errs.add("connection refused")
	if got := actionFrom(&out, &errs, "https://host.tailnet.ts.net"); got != "" {
		t.Errorf("actionFrom = %q, want empty", got)
	}
	out.add("Available within your tailnet: https://host.tailnet.ts.net:7331/")
	if got := actionFrom(&out, &errs, "https://host.tailnet.ts.net"); got != "" {
		t.Errorf("actionFrom = %q, want empty for the node's own banner", got)
	}
}

type lockedReports struct {
	mu      sync.Mutex
	reports []Report
}

func (l *lockedReports) add(r Report) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.reports = append(l.reports, r)
}

func (l *lockedReports) last() (Report, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.reports) == 0 {
		return Report{}, false
	}
	return l.reports[len(l.reports)-1], true
}

func (l *lockedReports) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.reports)
}

func (l *lockedReports) all() []Report {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]Report(nil), l.reports...)
}
