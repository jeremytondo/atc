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
	return &Supervisor{
		Executable: path,
		Port:       7331,
		Logger:     slog.New(slog.NewTextHandler(logs, nil)),
	}
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
	resolved, err := ResolveExecutable(s.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if resolved != s.Executable {
		t.Errorf("resolved %q, want %q", resolved, s.Executable)
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

func TestServeReadyThenReapedOnCancel(t *testing.T) {
	var logs lockedBuffer
	s := fake(t, `echo "Available within your tailnet: https://host.tailnet.ts.net:7331/"; exec sleep 30`, &logs)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- s.serve(ctx, "host.tailnet.ts.net") }()
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

func TestServeFailureCarriesStderr(t *testing.T) {
	s := fake(t, `echo "serve not permitted" >&2; exit 1`, io.Discard)
	err := s.serve(context.Background(), "host.tailnet.ts.net")
	if err == nil || !strings.Contains(err.Error(), "serve not permitted") {
		t.Errorf("err = %v, want stderr detail", err)
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
