package server

import (
	"context"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestServeRejectsInvalidListenAddress(t *testing.T) {
	err := Serve(context.Background(), Config{HTTPAddr: "not-a-listen-address"})
	if err == nil {
		t.Fatal("Serve returned nil error, want invalid listen address error")
	}
	if !strings.Contains(err.Error(), "invalid TCP listen address") {
		t.Fatalf("error = %q, want invalid listen address", err)
	}
}

func TestServeRejectsEmptySocketPath(t *testing.T) {
	err := Serve(context.Background(), Config{HTTPAddr: "127.0.0.1:0"})
	if err == nil {
		t.Fatal("Serve returned nil error, want missing socket path error")
	}
	if !strings.Contains(err.Error(), "Unix socket path is required") {
		t.Fatalf("error = %q, want missing socket path", err)
	}
}

func TestServeRejectsEmptyDBPath(t *testing.T) {
	err := Serve(context.Background(), Config{
		HTTPAddr:   "127.0.0.1:0",
		SocketPath: filepath.Join(t.TempDir(), "atc.sock"),
	})
	if err == nil {
		t.Fatal("Serve returned nil error, want missing DB path error")
	}
	if !strings.Contains(err.Error(), "session database path is required") {
		t.Fatalf("error = %q, want missing DB path", err)
	}
}

func TestServeListensOnUnixSocket(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dir := t.TempDir()
	socketPath := filepath.Join(dir, "atc.sock")
	dbPath := filepath.Join(dir, "state", "atc.db")
	errCh := make(chan error, 1)
	go func() {
		errCh <- Serve(ctx, Config{
			HTTPAddr:   "127.0.0.1:0",
			SocketPath: socketPath,
			DBPath:     dbPath,
		})
	}()

	waitForHealth(t, socketPath)
	cancel()

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Serve returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Serve did not stop after context cancellation")
	}
}

func TestUnauthenticatedRemoteBind(t *testing.T) {
	cases := []struct {
		name string
		cfg  Config
		want bool
	}{
		{
			name: "loopback without token does not warn",
			cfg:  Config{HTTPAddr: "127.0.0.1:7331", HTTPAddrExplicit: true},
			want: false,
		},
		{
			name: "remote with token does not warn",
			cfg:  Config{HTTPAddr: "0.0.0.0:7331", HTTPAddrExplicit: true, AuthToken: "secret"},
			want: false,
		},
		{
			name: "remote without token warns",
			cfg:  Config{HTTPAddr: "0.0.0.0:7331", HTTPAddrExplicit: true},
			want: true,
		},
		{
			name: "implicit default bind never warns",
			cfg:  Config{HTTPAddr: DefaultHTTPAddr},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := unauthenticatedRemoteBind(tc.cfg); got != tc.want {
				t.Fatalf("unauthenticatedRemoteBind(%+v) = %v, want %v", tc.cfg, got, tc.want)
			}
		})
	}
}

func waitForHealth(t *testing.T, socketPath string) {
	t.Helper()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("unix", socketPath, 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service health over Unix socket did not become ready at %s", socketPath)
}
