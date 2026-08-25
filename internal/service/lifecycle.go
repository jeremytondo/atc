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

	supervised := supervisorRunning(ctx)
	if !supervised && portAnswering(opts.Config) {
		// An ATC responder that is not the supervised unit is a stray
		// foreground `atc server run`.
		return fmt.Errorf("something is already serving on port %d (a foreground `atc server run`?); stop it first", opts.Config.Port)
	}
	if supervised && !bounce && probeOnce(ctx, opts, token).healthy {
		if err := rewriteUnit(ctx, unitFile); err != nil {
			return err
		}
		say(opts.Stdout, "%s is already running and healthy: %s\n", UnitName, loopbackURL(opts.Config.Port))
		return nil
	}

	_, statErr := os.Stat(unitFile)
	firstRun := errors.Is(statErr, fs.ErrNotExist)
	if err := writeUnit(unitFile); err != nil {
		return err
	}
	if firstRun {
		// Consent-after-the-fact (ATC-246 grill decision): no prompt, no
		// TTY branch — identical interactive or scripted. Honest because
		// uninstall is one cheap, total undo.
		say(opts.Stderr, "%s", firstRunNotice(runtime.GOOS, unitFile))
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
		// the healthy-and-untouched case already returned above.)
		if launchdLoadedDomain(ctx) != "" {
			launchdUnload(ctx)
		}
		// Loading starts it (RunAtLoad).
		if err := launchdBootstrap(ctx, unitFile); err != nil {
			return err
		}
	}

	if err := awaitHealthy(ctx, opts, token); err != nil {
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

// rewriteUnit refreshes the unit without touching the process, keeping
// systemd's loaded view in sync on Linux.
func rewriteUnit(ctx context.Context, unitFile string) error {
	if err := writeUnit(unitFile); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		return runSupervisor(ctx, "systemctl", "--user", "daemon-reload")
	}
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
		launchdBootout(ctx)
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
	if runtime.GOOS == "darwin" {
		launchdBootout(ctx)
	} else if requireSystemctl() == nil {
		// Non-zero here means "not loaded/enabled", an expected state.
		exitCode(ctx, "systemctl", "--user", "disable", "--now", UnitName)
	}
	_, statErr := os.Stat(unitFile)
	existed := statErr == nil
	if err := os.Remove(unitFile); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if runtime.GOOS == "linux" && requireSystemctl() == nil {
		exitCode(ctx, "systemctl", "--user", "daemon-reload")
	}
	say(opts.Stdout, "%s", uninstallReport(existed, remainingFiles()))
	return nil
}

func loopbackURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
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
