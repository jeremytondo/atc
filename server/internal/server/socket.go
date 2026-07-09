package server

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jeremytondo/atelier-code/internal/paths"
)

// prepareUnixSocket ensures the socket's control directory exists and clears a
// stale socket file so the Unix listener can bind. It refuses to remove a
// non-socket file or a socket that is still being served by a live process.
func prepareUnixSocket(socketPath string) error {
	if err := paths.EnsureControlDir(filepath.Dir(socketPath)); err != nil {
		return err
	}

	info, err := os.Lstat(socketPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Unix socket path %s: %w", socketPath, err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		return fmt.Errorf("refusing to remove existing non-socket file at Unix socket path %s", socketPath)
	}

	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err == nil {
		conn.Close()
		return fmt.Errorf("Atelier Code service already reachable at Unix socket %s", socketPath)
	}
	if !isClearlyStaleUnixSocketError(err) {
		return fmt.Errorf("refusing to remove Unix socket %s because liveness check failed ambiguously: %w", socketPath, err)
	}

	if err := os.Remove(socketPath); err != nil {
		return fmt.Errorf("remove stale Unix socket %s: %w", socketPath, err)
	}
	return nil
}

func isClearlyStaleUnixSocketError(err error) bool {
	return errors.Is(err, syscall.ECONNREFUSED)
}
