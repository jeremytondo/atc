package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestRunNoArgsPrintsUsage(t *testing.T) {
	var stdout, stderr strings.Builder
	if err := run(context.Background(), nil, &stdout, &stderr); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	if !strings.Contains(stdout.String(), "Usage:") {
		t.Errorf("stdout = %q, want usage text", stdout.String())
	}
}

func TestRunVersion(t *testing.T) {
	var stdout, stderr strings.Builder
	if err := run(context.Background(), []string{"version"}, &stdout, &stderr); err != nil {
		t.Fatalf("run() = %v, want nil", err)
	}
	if strings.TrimSpace(stdout.String()) == "" {
		t.Error("version printed nothing")
	}
}

func TestRunRejectsBadInvocations(t *testing.T) {
	for _, args := range [][]string{
		{"frobnicate"},
		{"version", "extra"},
		{"server"},
		{"server", "frobnicate"},
		{"server", "run", "extra"},
		{"server", "token", "frobnicate"},
		{"server", "token", "rotate", "extra"},
	} {
		var stdout, stderr strings.Builder
		if err := run(context.Background(), args, &stdout, &stderr); err == nil {
			t.Errorf("run(%q) = nil, want error", args)
		}
	}
}

// isolateXDG points every ATC file location at a temp directory so tests
// never touch the developer's real state.
func isolateXDG(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir+"/config")
	t.Setenv("XDG_DATA_HOME", dir+"/data")
	t.Setenv("XDG_STATE_HOME", dir+"/state")
}

func TestServerRunStopsOnContextCancel(t *testing.T) {
	isolateXDG(t)
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr strings.Builder
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"server", "run", "--port", "0"}, &stdout, &stderr) }()
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("server run = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server run did not stop after context cancellation")
	}
	if !strings.Contains(stderr.String(), "server started") {
		t.Errorf("stderr = %q, want start log", stderr.String())
	}
}

func TestServerTokenPrintsAndRotates(t *testing.T) {
	isolateXDG(t)
	tokenOut := func(args ...string) string {
		var stdout, stderr strings.Builder
		if err := run(context.Background(), args, &stdout, &stderr); err != nil {
			t.Fatalf("run(%q) = %v", args, err)
		}
		return strings.TrimSpace(stdout.String())
	}
	first := tokenOut("server", "token")
	if !strings.HasPrefix(first, "atc_") || len(first) != len("atc_")+43 {
		t.Fatalf("token %q does not match the contract format", first)
	}
	if again := tokenOut("server", "token"); again != first {
		t.Errorf("second print minted a new token")
	}
	if rotated := tokenOut("server", "token", "rotate"); rotated == first {
		t.Errorf("rotate returned the old token")
	}
}
