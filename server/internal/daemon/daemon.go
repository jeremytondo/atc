// Package daemon starts, stops, and inspects the local atc service process.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jeremytondo/atc/internal/paths"
)

const LogName = "atc.log"

type Config struct {
	HTTPAddr         string
	HTTPAddrExplicit bool
	// ConfigPath, when set, is forwarded to the detached serve process as
	// --config so the child resolves the same config file the parent did.
	// Environment-based config (ATC_CONFIG) already propagates via the
	// inherited environment, so only the explicit flag needs forwarding.
	ConfigPath string
	Paths      paths.Paths
}

type Status struct {
	Running         bool
	PID             int
	Stale           bool
	SocketReachable bool
	SocketPID       int
}

type StartResult struct {
	PID        int
	HTTPAddr   string
	SocketPath string
	PIDPath    string
	LogPath    string
}

func Start(ctx context.Context, cfg Config) (StartResult, error) {
	if err := ensureConfig(cfg); err != nil {
		return StartResult{}, err
	}
	if err := paths.EnsureControlDir(cfg.Paths.Dir); err != nil {
		return StartResult{}, err
	}

	status, err := InspectService(cfg.Paths)
	if err != nil {
		return StartResult{}, err
	}
	if status.Running {
		return StartResult{}, fmt.Errorf("atc service already appears to be running with PID %d", status.PID)
	}
	if status.Stale {
		if err := os.Remove(cfg.Paths.PIDPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			return StartResult{}, fmt.Errorf("remove stale PID file %s: %w", cfg.Paths.PIDPath, err)
		}
	}
	if status.SocketReachable {
		if status.SocketPID > 0 {
			return StartResult{}, fmt.Errorf("atc service already appears to be reachable at Unix socket %s by PID %d; run \"atc stop\" to stop it", cfg.Paths.SocketPath, status.SocketPID)
		}
		return StartResult{}, fmt.Errorf("atc service already appears to be reachable at Unix socket %s; run \"atc stop\" to stop it", cfg.Paths.SocketPath)
	}

	exe, err := os.Executable()
	if err != nil {
		return StartResult{}, fmt.Errorf("locate current executable: %w", err)
	}

	args := []string{"serve"}
	if cfg.HTTPAddrExplicit {
		args = append(args, "--http-addr", cfg.HTTPAddr)
	}
	if cfg.ConfigPath != "" {
		args = append(args, "--config", cfg.ConfigPath)
	}

	logPath := filepath.Join(cfg.Paths.Dir, LogName)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return StartResult{}, fmt.Errorf("open daemon log %s: %w", logPath, err)
	}
	defer logFile.Close()

	cmd := exec.CommandContext(context.Background(), exe, args...)
	cmd.Env = os.Environ()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	detachDaemonCommand(cmd)

	if err := cmd.Start(); err != nil {
		return StartResult{}, fmt.Errorf("start atc service: %w", err)
	}

	pid := cmd.Process.Pid
	if err := writePIDFile(cfg.Paths.PIDPath, pid); err != nil {
		_ = terminateDaemonPID(pid)
		return StartResult{}, err
	}

	waitCh := make(chan error, 1)
	go func() {
		waitCh <- cmd.Wait()
	}()

	if err := waitForStartup(ctx, cfg.Paths.SocketPath, waitCh); err != nil {
		_ = os.Remove(cfg.Paths.PIDPath)
		return StartResult{}, err
	}

	return StartResult{
		PID:        pid,
		HTTPAddr:   cfg.HTTPAddr,
		SocketPath: cfg.Paths.SocketPath,
		PIDPath:    cfg.Paths.PIDPath,
		LogPath:    logPath,
	}, nil
}

func Stop(ctx context.Context, servicePaths paths.Paths) (int, error) {
	status, err := InspectService(servicePaths)
	if err != nil {
		return 0, err
	}

	switch {
	case status.Running:
		return stopPID(ctx, servicePaths.PIDPath, status.PID)
	case status.SocketPID > 0:
		return stopPID(ctx, servicePaths.PIDPath, status.SocketPID)
	case status.SocketReachable:
		return 0, fmt.Errorf("atc service is reachable at Unix socket %s, but its PID could not be determined", servicePaths.SocketPath)
	case status.Stale:
		_ = os.Remove(servicePaths.PIDPath)
	}

	return 0, fmt.Errorf("atc service is not running")
}

func stopPID(ctx context.Context, pidPath string, pid int) (int, error) {
	if err := terminateDaemonPID(pid); err != nil {
		return 0, fmt.Errorf("stop atc service with PID %d: %w", pid, err)
	}
	if err := waitForExit(ctx, pid); err != nil {
		return pid, err
	}
	if err := os.Remove(pidPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return pid, fmt.Errorf("remove PID file %s: %w", pidPath, err)
	}
	return pid, nil
}

func InspectService(servicePaths paths.Paths) (Status, error) {
	status, err := Inspect(servicePaths.PIDPath)
	if err != nil {
		return Status{}, err
	}
	if socketReachable(servicePaths.SocketPath) {
		status.SocketReachable = true
		status.SocketPID = socketOwnerPID(servicePaths.SocketPath)
	}
	return status, nil
}

func Inspect(pidPath string) (Status, error) {
	pid, err := readPIDFile(pidPath)
	if errors.Is(err, os.ErrNotExist) {
		return Status{}, nil
	}
	if err != nil {
		return Status{}, err
	}

	running, err := daemonPIDRunning(pid)
	if err != nil {
		return Status{}, err
	}
	return Status{
		Running: running,
		PID:     pid,
		Stale:   !running,
	}, nil
}

func ensureConfig(cfg Config) error {
	if cfg.Paths.Dir == "" || cfg.Paths.SocketPath == "" || cfg.Paths.PIDPath == "" {
		return fmt.Errorf("service control paths are required")
	}
	if cfg.HTTPAddr == "" {
		return fmt.Errorf("HTTP listen address is required")
	}
	return nil
}

func readPIDFile(path string) (int, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(content)))
	if err != nil || pid <= 0 {
		return 0, fmt.Errorf("invalid PID file %s", path)
	}
	return pid, nil
}

func writePIDFile(path string, pid int) error {
	if err := paths.EnsureControlDir(filepath.Dir(path)); err != nil {
		return err
	}
	tmpPath := fmt.Sprintf("%s.tmp", path)
	if err := os.WriteFile(tmpPath, []byte(fmt.Sprintf("%d\n", pid)), 0o600); err != nil {
		return fmt.Errorf("write PID file %s: %w", path, err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("write PID file %s: %w", path, err)
	}
	return nil
}

func socketReachable(socketPath string) bool {
	conn, err := net.DialTimeout("unix", socketPath, 200*time.Millisecond)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func waitForStartup(ctx context.Context, socketPath string, waitCh <-chan error) error {
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for atc service startup: %w", ctx.Err())
		case <-timeout.C:
			return fmt.Errorf("atc service did not become reachable at Unix socket %s", socketPath)
		case err := <-waitCh:
			if err != nil {
				return fmt.Errorf("atc service exited during startup: %w", err)
			}
			return fmt.Errorf("atc service exited during startup")
		case <-ticker.C:
			if socketReachable(socketPath) {
				return nil
			}
		}
	}
}

func waitForExit(ctx context.Context, pid int) error {
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()

	for {
		running, err := daemonPIDRunning(pid)
		if err != nil {
			return err
		}
		if !running {
			return nil
		}

		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for atc service with PID %d to stop: %w", pid, ctx.Err())
		case <-timeout.C:
			return fmt.Errorf("atc service with PID %d did not stop after SIGTERM", pid)
		case <-ticker.C:
		}
	}
}

func detachDaemonCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
}

func daemonPIDRunning(pid int) (bool, error) {
	process, err := os.FindProcess(pid)
	if err != nil {
		return false, err
	}
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true, nil
	}
	if errors.Is(err, os.ErrProcessDone) || errors.Is(err, syscall.ESRCH) {
		return false, nil
	}
	if errors.Is(err, syscall.EPERM) {
		return true, nil
	}
	return false, err
}

func terminateDaemonPID(pid int) error {
	process, err := os.FindProcess(pid)
	if err != nil {
		return err
	}
	return process.Signal(syscall.SIGTERM)
}
