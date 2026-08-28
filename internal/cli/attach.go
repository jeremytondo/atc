package cli

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/zmx"
)

// StdioIsTerminal reports whether this process's stdin and stdout are both
// real TTYs. Callers pass the result (or their own knowledge) into
// AttachPreflight, so the check stays an explicit capability rather than
// package state.
func StdioIsTerminal() bool {
	stdin, err := os.Stdin.Stat()
	if err != nil || stdin.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	stdout, err := os.Stdout.Stat()
	return err == nil && stdout.Mode()&os.ModeCharDevice != 0
}

// AttachPreflight refuses attach attempts that can never succeed: a
// non-interactive stdio pair (stdioIsTerminal, supplied by the caller), or
// a non-loopback server (the session socket lives on the server's machine).
func AttachPreflight(baseURL string, stdioIsTerminal bool) error {
	if !stdioIsTerminal {
		return fmt.Errorf("attaching requires an interactive terminal (stdin and stdout must be TTYs)")
	}
	if !isLoopback(baseURL) {
		return fmt.Errorf("atc terminal attach is local-only (the session socket lives on the server's machine); this client targets %s", baseURL)
	}
	return nil
}

// AttachSession hands this process's TTY over to the terminal's running
// session. On success it never returns: the process is replaced by zmx
// until detach.
func AttachSession(terminal api.Terminal) error {
	if terminal.Status != api.TerminalRunning {
		return fmt.Errorf("terminal %s is %s, not running", terminal.ID, terminal.Status)
	}
	socketDir, err := paths.TerminalSocketDir()
	if err != nil {
		return err
	}
	// A loopback URL does not prove the server shares this
	// process's namespace (an SSH-forwarded remote server, a
	// different XDG_STATE_HOME). A session the API calls running
	// must have its socket here; anything else would hand the TTY
	// to zmx's auto-create.
	if _, err := os.Stat(filepath.Join(socketDir, terminal.ID)); err != nil {
		return fmt.Errorf("terminal %s has no session socket under %s — the server appears to be remote (an SSH-forwarded port?) or running with a different state directory", terminal.ID, socketDir)
	}
	executable, argv, env, err := zmx.AttachCommand(socketDir, terminal.ID)
	if err != nil {
		return err
	}
	// Exec replaces this process: the user's real TTY belongs to
	// zmx until detach.
	return syscall.Exec(executable, argv, env)
}

func isLoopback(baseURL string) bool {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
