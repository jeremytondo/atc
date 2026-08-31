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

	"github.com/jeremytondo/atc/internal/config"
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
		return fmt.Errorf("cannot read the installed unit %s: %w; replace it with `atc server restart --tailscale` or `atc server restart --tailscale=false`", unitFile, readErr)
	}
	// The unit is the tailscale override's only durable store (ATC-283):
	// an explicit flag wins, an omitted flag preserves what the installed
	// unit carries, and a unit that cannot be confidently read fails loudly
	// rather than being guessed at.
	override, err := resolveUnitTailscale(opts.Tailscale, runtime.GOOS, string(existing), readErr == nil)
	if err != nil {
		return err
	}
	tailnet := tailscaleEnabled(override, opts.Config.Tailscale)
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
	if supervised && !bounce && unchanged && probeOnce(ctx, opts, token).healthy {
		// Idempotent: healthy and running the current unit — leave the
		// process untouched. enable still runs so an active-but-disabled
		// unit returns at boot.
		if runtime.GOOS == "linux" {
			lingerChanged, err := enableLingering(ctx)
			if err != nil {
				return err
			}
			if err := runSupervisor(ctx, "systemctl", "--user", "enable", UnitName); err != nil {
				return rollbackLingering(ctx, lingerChanged, err)
			}
		}
		report, err := lifecycleSuccess(ctx, UnitName+" is already running and healthy", opts.Config, tailnet, "")
		if err != nil {
			return err
		}
		say(opts.Stdout, "%s", report)
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
	var tailscaleExecutable string
	if tailnet {
		if tailscaleExecutable, err = resolveTailscaleExecutable(opts.Config.TailscaleExecutable); err != nil {
			return err
		}
	}
	lingerChanged := false
	if runtime.GOOS == "linux" {
		// A user unit enabled without lingering only returns after the next
		// login. ATC is a remote server, so start/restart establish the full
		// boot-persistence contract before installing or replacing the unit.
		if lingerChanged, err = enableLingering(ctx); err != nil {
			return err
		}
	}

	if err := writeUnit(unitFile, content); err != nil {
		return rollbackLingering(ctx, lingerChanged, err)
	}
	if runtime.GOOS == "linux" {
		if err := runSupervisor(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
			return rollbackLingering(ctx, lingerChanged, err)
		}
		if err := runSupervisor(ctx, "systemctl", "--user", "enable", UnitName); err != nil {
			return rollbackLingering(ctx, lingerChanged, err)
		}
		verb := "start"
		if bounce {
			verb = "restart"
		}
		if err := runSupervisor(ctx, "systemctl", "--user", verb, UnitName); err != nil {
			return rollbackLingering(ctx, lingerChanged, err)
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

	if err := awaitHealthy(ctx, opts, token); err != nil {
		printLastLogs(ctx, opts.Stderr)
		return err
	}
	verb := "started"
	if bounce {
		verb = "restarted"
	}
	report, err := lifecycleSuccess(ctx, verb+" "+UnitName, opts.Config, tailnet, tailscaleExecutable)
	if err != nil {
		return err
	}
	say(opts.Stdout, "%s", report)
	return nil
}

// Stop stops the supervised process without uninstalling it. The enabled unit
// returns at next boot on Linux and next login on macOS; uninstall is the way
// out permanently.
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
	returns := "next login"
	if runtime.GOOS == "linux" {
		returns = "next boot"
	}
	say(opts.Stdout, "stopped %s (still installed; returns at %s)\n", UnitName, returns)
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
	lingerEnabled := false
	if existed && runtime.GOOS == "linux" {
		lingerEnabled, _ = userLingering(ctx, os.Getuid())
	}
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
	if lingerEnabled {
		say(opts.Stdout, "%s", lingerUninstallNotice(os.Getuid()))
	}
	return nil
}

func loopbackURL(port int) string {
	return fmt.Sprintf("http://127.0.0.1:%d", port)
}

func tailscaleEnabled(override, configured bool) bool {
	return override || configured
}

// lifecycleSuccess reports every endpoint the just-checked service intends
// to expose. Tailnet inspection distinguishes a live Serve route from an
// expected URL that is still converging; tailnet trouble never turns a
// healthy loopback server into a failed start.
func lifecycleSuccess(ctx context.Context, headline string, cfg config.Config, tailnet bool, executable string) (string, error) {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n  api: %s\n", headline, loopbackURL(cfg.Port))
	if !tailnet {
		return b.String(), nil
	}
	url, problem, err := inspectTailnetWithTimeout(ctx, cfg, executable)
	if err != nil {
		return "", err
	}
	fmt.Fprintf(&b, "  %s\n", renderTailnetURL(url, problem))
	return b.String(), nil
}

func rollbackLingering(ctx context.Context, changed bool, cause error) error {
	if !changed {
		return cause
	}
	if err := disableLingering(context.WithoutCancel(ctx)); err != nil {
		return errors.Join(cause, err)
	}
	return cause
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
		return false, fmt.Errorf("%w; replace the unit with `atc server restart --tailscale` or `atc server restart --tailscale=false`", err)
	}
	return override, nil
}

// firstRunNotice replaces the ATC-246 consent prompt (2026-08-25 grill):
// printed to stderr when no unit existed before this start.
func firstRunNotice(goos, unitFile string) string {
	notice := fmt.Sprintf("registered %s (%s)\n", UnitName, unitFile)
	if goos == "linux" {
		notice += "the server now starts automatically when this machine boots and restarts if it exits\n"
	} else {
		notice += "the server now starts automatically at every login and restarts if it exits\n"
	}
	notice += "undo at any time with `atc server uninstall`\n"
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

func lingerUninstallNotice(uid int) string {
	return fmt.Sprintf("ATC did not disable systemd lingering because other user services may use it\n"+
		"disable it, if no longer needed, with `sudo loginctl disable-linger %d`\n", uid)
}
