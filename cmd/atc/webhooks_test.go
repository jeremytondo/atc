//go:build linux

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
)

// buildATC builds the real binary for subprocess tests: forced
// termination has to kill an actual server process, and the receiver is
// the binary's own hidden command.
func buildATC(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "atc")
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		t.Skipf("cannot build atc: %v\n%s", err, out)
	}
	return binary
}

func alive(pid int) bool { return syscall.Kill(pid, 0) == nil }

func waitUntil(t *testing.T, what string, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A foreground `atc server run --webhooks` brings up the restricted
// receiver and Funnel, reports them on the API, and — killed outright,
// with no chance to clean up — leaves neither process behind.
func TestServerRunWebhooksForcedTerminationLeavesNoOrphans(t *testing.T) {
	binary := buildATC(t)
	isolateXDG(t)
	dir := t.TempDir()
	tailscale := filepath.Join(dir, "tailscale")
	script := `#!/bin/sh
if [ "$1" = status ]; then printf '%s' '{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."}}'; exit 0; fi
echo "$$" > "` + dir + `/funnel.pid"
echo 'Available on the internet:'; echo; echo 'https://host.tailnet.ts.net/'
exec sleep 300
`
	if err := os.WriteFile(tailscale, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// The port goes in config.toml so `server status` probes the same
	// server; only --webhooks is a flag, which config knows nothing about.
	port := closedPort(t)
	configDir := filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "atc")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(fmt.Sprintf("port = %d\n", port)), 0o600); err != nil {
		t.Fatal(err)
	}
	server := exec.Command(binary, "server", "run", "--webhooks")
	server.Env = append(os.Environ(), "ATC_TAILSCALE_EXECUTABLE="+tailscale)
	var stderr syncBuffer
	server.Stderr = &stderr
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	})

	waitUntil(t, "server start", 20*time.Second, func() bool { return strings.Contains(stderr.String(), "server started") })
	tokenOut, err := exec.Command(binary, "server", "token").Output()
	if err != nil {
		t.Fatal(err)
	}
	client := api.NewClient(fmt.Sprintf("http://127.0.0.1:%d", port), strings.TrimSpace(string(tokenOut)), "test", nil, nil)
	var status api.Webhooks
	waitUntil(t, "webhooks ready or unavailable", 30*time.Second, func() bool {
		status, err = client.Webhooks(context.Background())
		return err == nil && (status.State == api.WebhooksReady || status.State == api.WebhooksUnavailable)
	})
	if status.State == api.WebhooksUnavailable {
		if strings.Contains(strings.ToLower(status.Reason), "landlock") {
			t.Skipf("this kernel cannot run the receiver: %s", status.Reason)
		}
		t.Fatalf("webhooks unavailable: %s\n%s", status.Reason, stderr.String())
	}
	if status.URL != "https://host.tailnet.ts.net" || len(status.Routes) != 0 || status.Pending != 0 {
		t.Errorf("status = %+v, want ready at the public URL with an empty registry", status)
	}
	match := regexp.MustCompile(`msg="webhook receiver started" pid=(\d+)`).FindStringSubmatch(stderr.String())
	if match == nil {
		t.Fatalf("no receiver pid in logs:\n%s", stderr.String())
	}
	receiverPID, _ := strconv.Atoi(match[1])
	funnelData, err := os.ReadFile(filepath.Join(dir, "funnel.pid"))
	if err != nil {
		t.Fatal(err)
	}
	funnelPID, err := strconv.Atoi(strings.TrimSpace(string(funnelData)))
	if err != nil {
		t.Fatalf("funnel pid file %q: %v", funnelData, err)
	}
	if !alive(receiverPID) || !alive(funnelPID) {
		t.Fatalf("receiver alive=%v funnel alive=%v before termination", alive(receiverPID), alive(funnelPID))
	}

	// `server status` reports what the running server reports, whatever
	// config predicts — this launch was enabled by a flag config knows
	// nothing about.
	var stdout strings.Builder
	statusCmd := exec.Command(binary, "server", "status")
	statusCmd.Stdout = &stdout
	_ = statusCmd.Run() // healthy but not installed exits 0; the report is what matters
	if !strings.Contains(stdout.String(), "webhooks: https://host.tailnet.ts.net (0 pending)") {
		t.Errorf("status output lacks the server's webhook report:\n%s", stdout.String())
	}

	// Forced termination: SIGKILL runs no handlers in the server.
	if err := server.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = server.Wait()
	waitUntil(t, "receiver death", 5*time.Second, func() bool { return !alive(receiverPID) })
	waitUntil(t, "funnel death", 5*time.Second, func() bool { return !alive(funnelPID) })
}

// With intake off, the API still reports the inbox and the empty registry
// as disabled, over bearer auth, and the public endpoint does not exist.
func TestServerRunWebhooksDisabledStatus(t *testing.T) {
	isolateXDG(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stdout strings.Builder
	var stderr syncBuffer
	done := make(chan error, 1)
	port := closedPort(t)
	go func() {
		done <- run(ctx, []string{"server", "run", "--port", strconv.Itoa(port)}, strings.NewReader(""), &stdout, &stderr)
	}()
	waitUntil(t, "server start", 20*time.Second, func() bool { return strings.Contains(stderr.String(), "server started") })
	var tokenOut strings.Builder
	if err := run(context.Background(), []string{"server", "token"}, strings.NewReader(""), &tokenOut, io.Discard); err != nil {
		t.Fatal(err)
	}
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/v1/webhooks", port))
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("tokenless GET /v1/webhooks = %d, want 401", resp.StatusCode)
	}
	client := api.NewClient(fmt.Sprintf("http://127.0.0.1:%d", port), strings.TrimSpace(tokenOut.String()), "test", nil, nil)
	status, err := client.Webhooks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.State != api.WebhooksDisabled || status.IntakeBlocked || len(status.Routes) != 0 {
		raw, _ := json.Marshal(status)
		t.Errorf("status = %s, want disabled with no routes", raw)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}
