package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jeremytondo/atelier-code/internal/paths"
	"github.com/jeremytondo/atelier-code/internal/server"
	"github.com/spf13/cobra"
)

func TestRootCommandIncludesLifecycleCommands(t *testing.T) {
	cmd := rootCommand()
	want := map[string]bool{
		"serve":        false,
		"start":        false,
		"stop":         false,
		"status":       false,
		"health":       false,
		"version":      false,
		"actions":      false,
		"environments": false,
		"sessions":     false,
	}

	for _, child := range cmd.Commands() {
		if _, ok := want[child.Name()]; ok {
			want[child.Name()] = true
		}
		if child.Name() == "session" {
			t.Fatal("root command includes old singular session group")
		}
	}

	for name, found := range want {
		if !found {
			t.Fatalf("root command is missing %q", name)
		}
	}
}

func TestVersionCommandPrintsBuildInfo(t *testing.T) {
	cmd := versionCommand()
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}

	got := out.String()
	for _, want := range []string{"atc dev", "commit unknown", "date unknown"} {
		if !strings.Contains(got, want) {
			t.Fatalf("output = %q, want %q", got, want)
		}
	}
}

func TestStatusCommandReportsNotRunningWithoutPIDFile(t *testing.T) {
	lookup := testRuntimeLookup(t)
	cmd := statusCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "not running\n" {
		t.Fatalf("output = %q, want not running", got)
	}
}

func TestStatusCommandReportsStalePIDFile(t *testing.T) {
	envDir := t.TempDir()
	lookup := xdgLookup(envDir)
	pidPath := paths.PathsForEnv(paths.EnvLookup(lookup), os.Getuid()).PIDPath
	if err := os.MkdirAll(filepath.Dir(pidPath), 0o700); err != nil {
		t.Fatalf("create control directory: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte("2147483647\n"), 0o600); err != nil {
		t.Fatalf("write PID file: %v", err)
	}

	cmd := statusCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "not running stale pid 2147483647\n" {
		t.Fatalf("output = %q, want stale status", got)
	}
}

func TestLifecycleCommandHelpDocumentsControlPaths(t *testing.T) {
	for _, cmd := range []*cobra.Command{
		startCommand(func(string) string { return "" }),
		stopCommand(func(string) string { return "" }),
		statusCommand(func(string) string { return "" }),
	} {
		help := cmd.Long
		if !strings.Contains(help, "atc.pid") || !strings.Contains(help, "atc.sock") {
			t.Fatalf("%s help = %q, want PID and socket paths", cmd.Name(), help)
		}
	}
}

func TestHealthCommandCallsServiceThroughUnixSocket(t *testing.T) {
	lookup := testRuntimeLookup(t)
	socketPath := paths.PathsForEnv(paths.EnvLookup(lookup), os.Getuid()).SocketPath
	envDir := t.TempDir()
	dbPath := filepath.Join(envDir, "state", "atc.db")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 1)
	go func() {
		errCh <- server.Serve(ctx, server.Config{
			HTTPAddr:    "127.0.0.1:0",
			SocketPath:  socketPath,
			DBPath:      dbPath,
			ActionsPath: filepath.Join(envDir, "config", "actions.json"),
		})
	}()
	waitForSocketHealth(t, socketPath)

	cmd := healthCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if got := out.String(); got != "ok\n" {
		t.Fatalf("output = %q, want ok", got)
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("server returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop after cancellation")
	}
}

func TestHealthCommandFailsClearlyWhenSocketUnavailable(t *testing.T) {
	lookup := testRuntimeLookup(t)
	socketPath := paths.PathsForEnv(paths.EnvLookup(lookup), os.Getuid()).SocketPath

	cmd := healthCommand(lookup)
	var out bytes.Buffer
	cmd.SetOut(&out)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("Execute returned nil error, want unavailable socket error")
	}
	if !strings.Contains(err.Error(), "health check failed") || !strings.Contains(err.Error(), socketPath) {
		t.Fatalf("error = %q, want health failure with socket path %s", err, socketPath)
	}
}

func xdgLookup(dir string) envLookup {
	return func(key string) string {
		if key == "XDG_RUNTIME_DIR" {
			return dir
		}
		return ""
	}
}

func testRuntimeLookup(t *testing.T) envLookup {
	t.Helper()

	return xdgLookup(testRuntimeDir(t))
}

func testSocketPath(t *testing.T, name string) string {
	t.Helper()

	return filepath.Join(testRuntimeDir(t), name)
}

func testRuntimeDir(t *testing.T) string {
	t.Helper()

	dir, err := os.MkdirTemp(os.TempDir(), "cp.")
	if err != nil {
		t.Fatalf("create short runtime directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func waitForSocketHealth(t *testing.T, socketPath string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		body, err := newAPIClient(socketPath).get(context.Background(), "health")
		var health struct {
			Status string `json:"status"`
		}
		if err == nil && json.Unmarshal(body, &health) == nil && health.Status == "ok" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service health over Unix socket did not become ready at %s", socketPath)
}
