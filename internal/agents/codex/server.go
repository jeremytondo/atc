package codex

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// startServer launches the shared app-server the way Codex Desktop does
// when none is running: `codex app-server --listen unix://` (no path —
// codex derives the well-known socket from its CODEX_HOME), output
// appended to the control directory's log file, detached in its own
// session so it outlives ATC. ATC never stops, restarts, adopts, or
// health-manages it: the server is the user's, shared with every other
// Codex client, and Codex's own startup lock settles a race with one of
// them. The server starts in the user's home — a neutral directory for a
// client that sends no cwd, rather than whatever ATC happened to be
// started from.
func startServer(codexHome string) error {
	executable, err := exec.LookPath("codex")
	if err != nil {
		return fmt.Errorf("codex executable not found on PATH: %w", err)
	}
	logPath := serverLogPath(codexHome)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return err
	}
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { _ = logFile.Close() }()
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		return err
	}
	defer func() { _ = devnull.Close() }()

	cmd := exec.Command(executable, "app-server", "--listen", "unix://")
	cmd.Stdin = devnull
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Env = append(os.Environ(), "CODEX_HOME="+codexHome)
	if home, err := os.UserHomeDir(); err == nil {
		cmd.Dir = home
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("starting codex app-server: %w", err)
	}
	// Reaped when it exits (a losing starter exits at once); never
	// waited on otherwise.
	go func() { _ = cmd.Wait() }()
	return nil
}
