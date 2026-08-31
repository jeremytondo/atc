package service

// The lifecycle commands: start (registration folded in), restart, stop,
// uninstall. Start and restart share one path — verify supervisor, one
// pre-flight port probe, (re)render the unit, start, health-gate — and
// differ only in whether they bounce a running process.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"runtime"
	"strings"

	"github.com/jeremytondo/atc/internal/paths"
)

// Start registers and starts the supervised server; there is no separate
// install command. Idempotent: a healthy running server is left untouched,
// though the unit is still re-rendered so it can never go stale.
func Start(ctx context.Context, opts Options) error {
	return startOrRestart(ctx, opts, false)
}

// Restart is the same path as Start — re-render, health gate, same failure
// diagnostics — but always bounces the process. The remedy for upgrades,
// config edits, and version skew.
func Restart(ctx context.Context, opts Options) error {
	return startOrRestart(ctx, opts, true)
}

func startOrRestart(ctx context.Context, opts Options, bounce bool) error {
	if err := supported(); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if err := requireSystemctl(); err != nil {
			return err
		}
	}
	unitFile, err := UnitPath()
	if err != nil {
		return err
	}
	token, err := ensureToken()
	if err != nil {
		return err
	}
	existing, readErr := os.ReadFile(unitFile)
	firstRun := errors.Is(readErr, fs.ErrNotExist)
	if readErr != nil && !firstRun && opts.Tailscale == nil {
		return fmt.Errorf("cannot read the installed unit %s: %w; rerun with --tailscale or --tailscale=false to replace it", unitFile, readErr)
	}
	// The unit is the tailscale override's only durable store (ATC-283):
	// an explicit flag wins, an omitted flag preserves what the installed
	// unit carries, and a unit that cannot be confidently read fails loudly
	// rather than being guessed at.
	override, err := resolveUnitTailscale(opts.Tailscale, runtime.GOOS, string(existing), readErr == nil)
	if err != nil {
		return err
	}
	content, err := renderUnit(override)
	if err != nil {
		return err
	}
	unchanged := readErr == nil && string(existing) == content

	supervised := supervisorRunning(ctx)
	if !supervised && portAnswering(opts.Config) {
		// An ATC responder that is not the supervised unit is a stray
		// foreground `atc server run`.
		return fmt.Errorf("something is already serving on port %d (a foreground `atc server run`?); stop it first", opts.Config.Port)
	}
	if supervised && !bounce && unchanged && probeHealthy(ctx, opts, token) {
		// Idempotent: healthy and running the current unit — leave the
		// process untouched. enable still runs so an active-but-disabled
		// unit returns at next login.
		if runtime.GOOS == "linux" {
			if err := runSupervisor(ctx, "systemctl", "--user", "enable", UnitName); err != nil {
				return err
			}
		}
		say(opts.Stdout, "%s is already running and healthy: %s\n", UnitName, loopbackURL(opts.Config.Port))
		return nil
	}
	// A supervised daemon whose unit changed must be bounced onto the new
	// one: daemon-reload alone leaves the running process (and launchd's
	// loaded job) on the stale configuration.
	if supervised && !unchanged {
		bounce = true
	}
	// Preflight before any mutation: a unit that will boot with Tailscale
	// enabled — by the override or by config.toml — needs the executable
	// resolvable, or the daemon would crash-loop; fail here, before the
	// unit changes or a healthy process is interrupted. Runtime tailnet
	// state (logged out, tailscaled down) is deliberately not checked: the
	// exposure supervisor self-heals those after the loopback server is up.
	if override || opts.Config.Tailscale {
		if _, err := resolveTailscaleExecutable(opts.Config.TailscaleExecutable); err != nil {
			return err
		}
	}

	if err := writeUnit(unitFile, content); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if err := runSupervisor(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return err
		}
		if err := runSupervisor(ctx, "systemctl", "--user", "enable", UnitName); err != nil {
			return err
		}
		verb := "start"
		if bounce {
			verb = "restart"
		}
		if err := runSupervisor(ctx, "systemctl", "--user", verb, UnitName); err != nil {
			return err
		}
	} else {
		// Bootstrap is the only launchd operation that (re)reads the plist,
		// so every path that (re)starts the process unloads first and
		// bootstraps the freshly rendered unit. (`kickstart -k` would bounce
		// the process but leave launchd running the stale configuration —
		// the healthy-and-current case already returned above.)
		if launchdLoadedDomain(ctx) != "" {
			if err := launchdUnload(ctx); err != nil {
				return err
			}
		}
		// Loading starts it (RunAtLoad).
		if err := launchdBootstrap(ctx, unitFile); err != nil {
			return err
		}
	}
	if firstRun {
		// Consent-after-the-fact (ATC-246 grill decision): no prompt, no
		// TTY branch — identical interactive or scripted. Honest because
		// uninstall is one cheap, total undo. Printed only after the
		// supervisor accepted the unit, so the notice reports a fact.
		say(opts.Stderr, "%s", firstRunNotice(runtime.GOOS, unitFile))
	}

	if err := healthGate(ctx, opts, token); err != nil {
		printLastLogs(ctx, opts.Stderr)
		return err
	}
	verb := "started"
	if bounce {
		verb = "restarted"
	}
	say(opts.Stdout, "%s %s: %s\n", verb, UnitName, loopbackURL(opts.Config.Port))
	return nil
}

// Stop stops the supervised process; "stop" means "until next login" on
// both platforms — the unit stays installed and enabled. Uninstall is the
// way out.
func Stop(ctx context.Context, opts Options) error {
	if err := supported(); err != nil {
		return err
	}
	unitFile, err := UnitPath()
	if err != nil {
		return err
	}
	if _, err := os.Stat(unitFile); errors.Is(err, fs.ErrNotExist) {
		say(opts.Stdout, "%s is not installed; nothing to stop\n", UnitName)
		return nil
	}
	if runtime.GOOS == "darwin" {
		if launchdLoadedDomain(ctx) == "" {
			say(opts.Stdout, "%s is not running\n", UnitName)
			return nil
		}
		if err := launchdUnload(ctx); err != nil {
			return err
		}
	} else {
		if err := requireSystemctl(); err != nil {
			return err
		}
		// stop with no disable; already a no-op success on an inactive unit.
		if err := runSupervisor(ctx, "systemctl", "--user", "stop", UnitName); err != nil {
			return err
		}
	}
	say(opts.Stdout, "stopped %s (still installed; returns at next login)\n", UnitName)
	return nil
}

// Uninstall stops the server and removes the unit — the one total undo for
// start's registration. No purge: the leftovers are predictable XDG paths,
// reported so nothing is hidden.
func Uninstall(ctx context.Context, opts Options) error {
	if err := supported(); err != nil {
		return err
	}
	unitFile, err := UnitPath()
	if err != nil {
		return err
	}
	_, statErr := os.Stat(unitFile)
	existed := statErr == nil
	if runtime.GOOS == "darwin" {
		if err := launchdUnload(ctx); err != nil {
			return err
		}
	} else if existed && requireSystemctl() == nil {
		// Surfaced, not ignored: proceeding past a failed disable/stop
		// would remove the unit under a still-running daemon and defeat
		// uninstall as the total undo. (On an installed unit, disable and
		// stop are both no-op successes when already disabled/inactive.)
		if err := runSupervisor(ctx, "systemctl", "--user", "disable", "--now", UnitName); err != nil {
			return err
		}
	}
	if err := os.Remove(unitFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if existed && runtime.GOOS == "linux" && requireSystemctl() == nil {
		exitCode(ctx, "systemctl", "--user", "daemon-reload")
	}
	say(opts.Stdout, "%s", uninstallReport(existed, remainingFiles()))
	return nil
}

func loopbackURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

// resolveUnitTailscale settles the unit's --tailscale override for one
// start/restart. Tri-state: an explicit flag is the requested state
// outright (it never consults the installed unit), an omitted flag
// preserves what a readable installed unit already carries, and no unit
// means no override — config.toml governs. An installed unit whose exec
// arguments cannot be confidently read is an error, never a guess.
func resolveUnitTailscale(flag *bool, goos, existing string, installed bool) (bool, error) {
	if flag != nil {
		return *flag, nil
	}
	if !installed {
		return false, nil
	}
	override, err := unitTailscale(goos, existing)
	if err != nil {
		return false, fmt.Errorf("%w; rerun with --tailscale or --tailscale=false to replace the unit", err)
	}
	return override, nil
}

// firstRunNotice replaces the ATC-246 consent prompt (2026-08-25 grill):
// printed to stderr when no unit existed before this start.
func firstRunNotice(goos, unitFile string) string {
	notice := fmt.Sprintf("registered %s (%s)\n", UnitName, unitFile) +
		"the server now starts automatically at every login and restarts if it exits\n" +
		"undo at any time with `atc server uninstall`\n"
	if goos == "linux" {
		notice += "headless machines: run `loginctl enable-linger` once so the server outlives logins\n"
	}
	return notice
}

type remainingFile struct{ label, path string }

// remainingFiles lists what uninstall leaves behind, filtered to files that
// actually exist: config, token, and on macOS the captured log file.
func remainingFiles() []remainingFile {
	var files []remainingFile
	add := func(label string, path string, err error) {
		if err != nil {
			return
		}
		if _, statErr := os.Stat(path); statErr == nil {
			files = append(files, remainingFile{label, path})
		}
	}
	configPath, err := paths.ConfigFile()
	add("config", configPath, err)
	tokenPath, err := paths.AuthTokenFile()
	add("token", tokenPath, err)
	if runtime.GOOS == "darwin" {
		logPath, err := paths.LogFile()
		add("logs", logPath, err)
	}
	return files
}

func uninstallReport(existed bool, remaining []remainingFile) string {
	var b strings.Builder
	if existed {
		fmt.Fprintf(&b, "uninstalled %s\n", UnitName)
	} else {
		fmt.Fprintf(&b, "%s was not installed; nothing removed\n", UnitName)
	}
	if len(remaining) == 0 {
		return b.String()
	}
	b.WriteString("left in place (uninstall never deletes data):\n")
	for _, f := range remaining {
		fmt.Fprintf(&b, "  %s: %s\n", f.label, f.path)
	}
	return b.String()
}
