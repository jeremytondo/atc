package cli

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"syscall"

	"github.com/jeremytondo/atc/internal/api"
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

// SessionAttacher supplies the driver-specific mechanics of attach: a
// cheap availability check and the exec handover for one session. The
// composition root wires the concrete driver (zmx today) — this package
// knows only the capability, never a driver, mirroring how the server
// side reaches its driver solely through terminals.Driver.
type SessionAttacher interface {
	// Preflight reports whether attach can possibly succeed on this
	// machine (the driver's attach tooling is present).
	Preflight() error
	// AttachCommand validates that the session is reachable in this
	// process's namespace and returns the exec handover for it.
	AttachCommand(id string) (executable string, argv, env []string, err error)
}

// AttachSession hands this process's TTY over to the terminal's running
// session. On success it never returns: the process is replaced by the
// driver's attach client until detach.
func AttachSession(terminal api.Terminal, attacher SessionAttacher) error {
	if terminal.Status != api.TerminalRunning {
		return fmt.Errorf("terminal %s is %s, not running", terminal.ID, terminal.Status)
	}
	executable, argv, env, err := attacher.AttachCommand(terminal.ID)
	if err != nil {
		return err
	}
	// Exec replaces this process: the user's real TTY belongs to the
	// session until detach.
	return syscall.Exec(executable, argv, env)
}

// IsLocalServer reports whether the client targets a server on this
// machine (a loopback URL): only then is this process's working
// directory meaningful to the server, and only then can attach work.
func IsLocalServer(baseURL string) bool { return isLoopback(baseURL) }

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
