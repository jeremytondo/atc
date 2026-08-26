// Package upgrade implements atc's self-upgrade from GitHub releases
// (ATC-261). The channel is stateless — a machine has no channel identity:
// plain `atc upgrade` moves it to the latest production release (even when
// that is semver-backwards from a dev build, and it says so), while
// `--dev` unconditionally reinstalls the current rolling dev build.
//
// Discovery uses no GitHub API and no tokens: the releases/latest redirect
// names the production tag, and the fixed `dev` prerelease has a stable
// download URL. The swap is checksum-verified and atomic: the new binary is
// staged beside the resolved os.Executable() (same filesystem), proved
// runnable, then renamed over the target. sudo is never invoked.
//
// A running server is never restarted silently. Interactive runs are asked
// (default yes); headless runs without --restart leave the server alone and
// say exactly what to do next — the skew stays loud in `atc server status`
// and on every subsequent command.
//
// Boundaries (deliberate): asset naming, checksum verification, archive
// extraction, message rendering, and the restart policy are pure and
// tested; HTTP fetches and process invocations stay thin and untested.
package upgrade

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/jeremytondo/atc/internal/service"
)

const (
	// repo is the GitHub repository releases are served from.
	repo = "jeremytondo/atc"
	// devTag is the fixed tag of the rolling dev prerelease; its assets are
	// clobbered by every dev cut.
	devTag = "dev"
	// checksumsAsset is GoReleaser's checksum file, one per release.
	checksumsAsset = "checksums.txt"
	// binaryName is the executable member inside each release archive.
	binaryName = "atc"
)

// RestartMode is how upgrade treats a server left running the old version
// after the swap: ask (the default; only a human at a TTY is actually
// asked), or the --restart / --no-restart pre-answers for scripts.
type RestartMode int

const (
	RestartAsk RestartMode = iota
	RestartAlways
	RestartNever
)

// Options carries one upgrade invocation's settled inputs.
type Options struct {
	// Dev selects the rolling dev build and disables the staleness
	// short-circuit: --dev always reinstalls ("make this machine match the
	// shelf").
	Dev     bool
	Restart RestartMode
	// Interactive gates the restart prompt; headless runs never prompt.
	Interactive bool
	// Version is the running client's build identity.
	Version string
	// Service carries the settled config for post-swap server probing and
	// restart. ConfigErr, when non-nil, is why it could not be settled: the
	// swap still proceeds and only the server check is skipped, so a broken
	// config.toml can never block getting a new binary.
	Service   service.Options
	ConfigErr error
	Stdin     io.Reader
	Stdout    io.Writer
	Stderr    io.Writer
}

// Run downloads the selected build, verifies and installs it over this
// binary, and then deals with a server still running the old version.
func Run(ctx context.Context, opts Options) error {
	asset, err := assetName(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}

	tag := devTag
	if opts.Dev {
		say(opts.Stdout, "downloading the current dev build...\n")
	} else {
		if tag, err = latestProductionTag(ctx); err != nil {
			return err
		}
		if tag == opts.Version {
			say(opts.Stdout, "%s\n", upToDateMessage(opts.Version))
			return nil
		}
		say(opts.Stdout, "downloading %s...\n", tag)
	}

	archive, err := fetch(ctx, assetURL(tag, asset))
	if err != nil {
		return err
	}
	sums, err := fetch(ctx, assetURL(tag, checksumsAsset))
	if err != nil {
		return err
	}
	if err := verifyChecksum(archive, sums, asset); err != nil {
		return err
	}
	binary, err := extractBinary(archive)
	if err != nil {
		return err
	}

	target, err := executablePath()
	if err != nil {
		return err
	}
	staged, err := stage(target, binary)
	if err != nil {
		return err
	}
	// A no-op after the promoting rename; cleanup for every earlier exit.
	defer func() { _ = os.Remove(staged) }()
	newVersion, err := binaryVersion(ctx, staged)
	if err != nil {
		return err
	}
	if err := promote(staged, target); err != nil {
		return err
	}
	say(opts.Stdout, "%s\n", replacedMessage(opts.Version, newVersion, target))

	return checkServer(ctx, opts, newVersion)
}

// checkServer handles the post-swap server via the existing handshake: not
// running means done; a server on any other version than the one just
// installed is restarted or deliberately left, per the policy matrix. It
// never returns a failure for an unrestarted server — that state is loud on
// every later command.
func checkServer(ctx context.Context, opts Options, newVersion string) error {
	if opts.ConfigErr != nil {
		say(opts.Stderr, "cannot check the running server (%v); run `atc server restart` if one is running\n", opts.ConfigErr)
		return nil
	}
	responding, serverVersion := service.Probe(ctx, opts.Service)
	if !responding {
		return nil
	}
	if serverVersion == newVersion {
		say(opts.Stdout, "server is already running %s\n", newVersion)
		return nil
	}
	action := decideRestart(opts.Restart, opts.Interactive)
	if action == actionAsk && promptYes(opts.Stdin, opts.Stdout, restartPrompt(serverVersion)) {
		action = actionRestart
	}
	if action == actionRestart {
		return restartServer(ctx, opts, newVersion)
	}
	say(opts.Stdout, "%s\n", staleServerLine(serverVersion))
	return nil
}

// restartServer bounces the supervised server with the same path `atc
// server restart` uses; the unit re-renders from this executable, which now
// holds the new binary.
func restartServer(ctx context.Context, opts Options, newVersion string) error {
	svc := opts.Service
	svc.Version = newVersion
	return service.Restart(ctx, svc)
}

type restartAction int

const (
	actionSkip restartAction = iota
	actionAsk
	actionRestart
)

// decideRestart is the ATC-261 policy matrix: explicit flags win; a human
// at a TTY is asked (default yes); headless runs never restart — community
// consensus reserves auto-restart for provably-cheap interruption, and
// ATC's mid-turn agent threads are not that.
func decideRestart(mode RestartMode, interactive bool) restartAction {
	switch {
	case mode == RestartAlways:
		return actionRestart
	case mode == RestartNever:
		return actionSkip
	case interactive:
		return actionAsk
	default:
		return actionSkip
	}
}

// promptYes asks a default-yes question on the user's terminal.
func promptYes(in io.Reader, out io.Writer, prompt string) bool {
	say(out, "%s", prompt)
	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes":
		return true
	default:
		return false
	}
}

// binaryVersion runs the staged binary's `version` command — proof the
// download executes on this machine before it replaces anything, and the
// only way to learn a dev build's stamped version.
func binaryVersion(ctx context.Context, path string) (string, error) {
	out, err := exec.CommandContext(ctx, path, "version").Output()
	if err != nil {
		return "", fmt.Errorf("downloaded binary failed to run: %w", err)
	}
	version := strings.TrimSpace(string(out))
	if version == "" {
		return "", errors.New("downloaded binary printed no version")
	}
	return version, nil
}

// say writes a user-facing message; a failed write to the user's own
// terminal has no better remedy than the message it would have carried.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
