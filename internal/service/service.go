// Package service manages the supervised ATC server (ATC-260, the ATC-246
// lifecycle family on the ATC-259 chassis): a user-scope launchd
// LaunchAgent on macOS, a systemd user unit on Linux. Supervisor-only —
// there is no pidfile family. The unit execs `atc server run`, keeping the
// daemon structurally unable to call back into the supervisor.
//
// Registration is folded into start: every start re-renders the unit from
// os.Executable() and the installing shell's PATH, so the unit can never go
// stale — upgrades are `atc server restart`. Hand-editing the generated
// unit is unsupported for the same reason.
//
// Logging is supervisor-owned: the server writes logfmt lines to stderr
// only. systemd's journal captures them on Linux; on macOS the LaunchAgent
// redirects both streams to the state-dir log file (launchd has no
// journal).
//
// Boundaries (deliberate): unit rendering and message formatting are pure
// and fully tested; launchctl/systemctl/journalctl invocations stay thin
// and untested.
package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/config"
)

// UnitName is the launchd label / systemd unit name, and the unit file
// basename (legacy convention, tag legacy-product-2026-08).
const UnitName = "atc.server"

// Options carries what every lifecycle command needs. Config must hold the
// file and default layers only — the supervised daemon runs with a minimal
// environment where ATC_* variables do not exist, so probing a port taken
// from the invoking shell's environment would target a server that will
// never serve it. Stop, Logs, and Uninstall never consume Config; their
// callers leave it zero so a broken config.toml cannot block diagnostics,
// stopping, or the total undo.
type Options struct {
	Config config.Config
	// Version is the client build identity, for skew reporting.
	Version string
	Stdout  io.Writer
	Stderr  io.Writer
}

// ExitError requests a specific process exit code after the command already
// reported its state on stdout; main exits with Code without printing more.
type ExitError struct{ Code int }

func (e *ExitError) Error() string { return fmt.Sprintf("exit code %d", e.Code) }

// say writes a user-facing message; a failed write to the user's own
// terminal has no better remedy than the message it would have carried.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}

func supported() error {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return fmt.Errorf("unsupported platform %s (launchd on macOS, systemd user units on Linux)", runtime.GOOS)
	}
	return nil
}

// requireSystemctl is the Linux pre-flight: without a systemd user manager
// there is no supervisor family at all, only the foreground primitive.
func requireSystemctl() error {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return fmt.Errorf("systemctl not found: this machine cannot supervise the server; run `atc server run` in the foreground instead")
	}
	return nil
}

// runSupervisor executes one supervisor command; a failure surfaces the
// tool's own stderr as one diagnostic (thin and untested by design).
func runSupervisor(ctx context.Context, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(stderr.String()); detail != "" {
			return fmt.Errorf("%s %s failed: %s", name, strings.Join(args, " "), detail)
		}
		return fmt.Errorf("%s %s failed: %w", name, strings.Join(args, " "), err)
	}
	return nil
}

// exitCode runs a command whose non-zero exit is an expected state, not a
// failure; -1 means the command could not run at all.
func exitCode(ctx context.Context, name string, args ...string) int {
	cmd := exec.CommandContext(ctx, name, args...)
	if err := cmd.Run(); err != nil {
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return exit.ExitCode()
		}
		return -1
	}
	return 0
}

// launchdDomains lists the domains an agent may live in: gui first (normal
// console login), user as the ssh/headless fallback — the gui domain does
// not exist without an Aqua session.
func launchdDomains() []string {
	uid := os.Getuid()
	return []string{fmt.Sprintf("gui/%d", uid), fmt.Sprintf("user/%d", uid)}
}

// launchdLoadedDomain is the domain the agent is currently loaded in, or "".
func launchdLoadedDomain(ctx context.Context) string {
	for _, domain := range launchdDomains() {
		if exitCode(ctx, "launchctl", "print", domain+"/"+UnitName) == 0 {
			return domain
		}
	}
	return ""
}

// launchdBootstrap loads the unit into the first domain that accepts it,
// surfacing only the last domain's failure.
func launchdBootstrap(ctx context.Context, unitFile string) error {
	domains := launchdDomains()
	for _, domain := range domains[:len(domains)-1] {
		if exitCode(ctx, "launchctl", "bootstrap", domain, unitFile) == 0 {
			return nil
		}
	}
	return runSupervisor(ctx, "launchctl", "bootstrap", domains[len(domains)-1], unitFile)
}

// launchdUnload boots the agent out of every domain it is loaded in and
// confirms launchd forgot the label — bootstrapping again while the old
// instance is still winding down fails, and reporting "stopped" while the
// job survives would be a lie. Success is judged by observation (the label
// gone from every domain) rather than bootout's exit status alone; a label
// still loaded at the deadline is a failure carrying the supervisor's own
// diagnostic. Domains proven absent are skipped, not "ignored on error".
func launchdUnload(ctx context.Context) error {
	var bootoutErr error
	for _, domain := range launchdDomains() {
		if exitCode(ctx, "launchctl", "print", domain+"/"+UnitName) != 0 {
			continue // not loaded there
		}
		if err := runSupervisor(ctx, "launchctl", "bootout", domain+"/"+UnitName); err != nil {
			bootoutErr = err
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for launchdLoadedDomain(ctx) != "" {
		if time.Now().After(deadline) {
			if bootoutErr != nil {
				return fmt.Errorf("%s is still loaded: %w", UnitName, bootoutErr)
			}
			return fmt.Errorf("%s is still loaded after bootout", UnitName)
		}
		select {
		case <-time.After(100 * time.Millisecond):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

// supervisorRunning reports whether the supervised daemon is under active
// management: unit loaded on macOS (KeepAlive keeps a loaded agent's
// process alive), unit active on Linux.
func supervisorRunning(ctx context.Context) bool {
	if runtime.GOOS == "darwin" {
		return launchdLoadedDomain(ctx) != ""
	}
	return exitCode(ctx, "systemctl", "--user", "is-active", "--quiet", UnitName) == 0
}

// probeAddr is where a local client reaches the server: the bind address,
// with wildcard binds reached via loopback.
func probeAddr(cfg config.Config) string {
	return net.JoinHostPort(probeHost(cfg.Bind), strconv.Itoa(cfg.Port))
}

func probeHost(bind string) string {
	if ip := net.ParseIP(bind); ip != nil && ip.IsUnspecified() {
		return "127.0.0.1"
	}
	return bind
}
