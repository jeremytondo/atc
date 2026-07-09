package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestPrepareUnixSocketRefusesNonSocketFile(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "atc.sock")
	if err := os.WriteFile(socketPath, []byte("not a socket"), 0o600); err != nil {
		t.Fatalf("write test file: %v", err)
	}

	err := prepareUnixSocket(socketPath)
	if err == nil {
		t.Fatal("prepareUnixSocket returned nil error, want non-socket refusal")
	}
	if !strings.Contains(err.Error(), "refusing to remove existing non-socket file") {
		t.Fatalf("error = %q, want non-socket refusal", err)
	}
	if _, statErr := os.Stat(socketPath); statErr != nil {
		t.Fatalf("regular file was removed: %v", statErr)
	}
}

func TestPrepareUnixSocketRemovesStaleSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "atc.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatalf("close listener: %v", err)
	}

	if err := prepareUnixSocket(socketPath); err != nil {
		t.Fatalf("prepareUnixSocket: %v", err)
	}
	if _, err := os.Lstat(socketPath); !os.IsNotExist(err) {
		t.Fatalf("socket path still exists, stat err = %v", err)
	}
}

func TestPrepareUnixSocketRefusesLiveSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "atc.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()

	err = prepareUnixSocket(socketPath)
	if err == nil {
		t.Fatal("prepareUnixSocket returned nil error, want live socket refusal")
	}
	if !strings.Contains(err.Error(), "already reachable") {
		t.Fatalf("error = %q, want already reachable", err)
	}
	if _, statErr := os.Stat(socketPath); statErr != nil {
		t.Fatalf("live socket was removed: %v", statErr)
	}
}

func TestIsClearlyStaleUnixSocketError(t *testing.T) {
	if !isClearlyStaleUnixSocketError(fmt.Errorf("dial unix: %w", syscall.ECONNREFUSED)) {
		t.Fatal("ECONNREFUSED was not treated as clearly stale")
	}

	ambiguousErrors := []error{
		syscall.EACCES,
		syscall.EPERM,
		os.ErrPermission,
		errors.New("temporary liveness probe failure"),
	}
	for _, err := range ambiguousErrors {
		if isClearlyStaleUnixSocketError(err) {
			t.Fatalf("%v was treated as clearly stale", err)
		}
	}
}
