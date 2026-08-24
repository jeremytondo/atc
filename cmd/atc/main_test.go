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
	} {
		var stdout, stderr strings.Builder
		if err := run(context.Background(), args, &stdout, &stderr); err == nil {
			t.Errorf("run(%q) = nil, want error", args)
		}
	}
}

func TestServerRunStopsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var stdout, stderr strings.Builder
	done := make(chan error, 1)
	go func() { done <- run(ctx, []string{"server", "run"}, &stdout, &stderr) }()
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
