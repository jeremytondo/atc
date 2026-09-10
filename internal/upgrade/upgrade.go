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
// A running server is never restarted silently. A server on the old
// release that still speaks the new build's protocol keeps running and
// is only noted (the protocol, api.Protocol, decides compatibility — not
// the release); --restart bounces it anyway. A server the new build
// cannot talk to is asked about on a TTY (default yes); headless runs
// without --restart leave it alone and say exactly what to do next.
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
	"strconv"
	"strings"

	"github.com/jeremytondo/atc/internal/service"
	"github.com/jeremytondo/atc/internal/version"
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

// GitHub is the release source: tokenless discovery of the latest
// published build per channel and verified download of its executable.
// Run uses it for this machine; guided remote setup (ATC-325) uses it for
// the target's platform through the remote.Releases seam.
type GitHub struct{}

// Latest names the latest published build in channel (version.ChannelStable
// or version.ChannelDev) for the platform, as its release tag. The stable
// head comes from the releases/latest redirect; the dev head is the fixed
// rolling tag. There is no search of older releases: latest is the only
// candidate. An unsupported platform fails here, before any download.
func (GitHub) Latest(ctx context.Context, channel, goos, goarch string) (string, error) {
	if _, err := assetName(goos, goarch); err != nil {
		return "", err
	}
	switch channel {
	case version.ChannelDev:
		return devTag, nil
	case version.ChannelStable:
		return latestProductionTag(ctx)
	}
	return "", fmt.Errorf("no release channel %q (stable or dev)", channel)
}

// Fetch downloads the tag's archive and checksums for the platform,
// verifies the archive against checksums.txt, and returns the
// executable's bytes.
func (GitHub) Fetch(ctx context.Context, tag, goos, goarch string) ([]byte, error) {
	asset, err := assetName(goos, goarch)
	if err != nil {
		return nil, err
	}
	archive, err := fetch(ctx, assetURL(tag, asset))
	if err != nil {
		return nil, err
	}
	sums, err := fetch(ctx, assetURL(tag, checksumsAsset))
	if err != nil {
		return nil, err
	}
	if err := verifyChecksum(archive, sums, asset); err != nil {
		return nil, err
	}
	return extractBinary(archive)
}

// Run downloads the selected build, verifies and installs it over this
// binary, and then deals with a server still running the old version.
func Run(ctx context.Context, opts Options) error {
	channel := version.ChannelStable
	if opts.Dev {
		channel = version.ChannelDev
	}
	var releases GitHub
	tag, err := releases.Latest(ctx, channel, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return err
	}
	if opts.Dev {
		say(opts.Stdout, "downloading the current dev build...\n")
	} else {
		if tag == opts.Version {
			say(opts.Stdout, "%s\n", upToDateMessage(opts.Version))
			return nil
		}
		say(opts.Stdout, "downloading %s...\n", tag)
	}
	binary, err := releases.Fetch(ctx, tag, runtime.GOOS, runtime.GOARCH)
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
	newProtocol := binaryProtocol(ctx, staged)
	if err := promote(staged, target); err != nil {
		return err
	}
	say(opts.Stdout, "%s\n", replacedMessage(opts.Version, newVersion, target))

	return checkServer(ctx, opts, newVersion, newProtocol)
}

// checkServer handles the post-swap server via the existing probe: not
// running means done; a compatible server on the old release is left
// running with a note; a server the new build cannot talk to is
// restarted or deliberately left, per the policy matrix. It never
// returns a failure for an unrestarted server — that state is loud on
// every later command.
func checkServer(ctx context.Context, opts Options, newVersion string, newProtocol int) error {
	if opts.ConfigErr != nil {
		say(opts.Stderr, "cannot check the running server (%v); run `atc server restart` if one is running\n", opts.ConfigErr)
		return nil
	}
	responding, serverVersion, serverProtocol := service.Probe(ctx, opts.Service)
	if !responding {
		return nil
	}
	if serverVersion == newVersion {
		say(opts.Stdout, "server is already running %s\n", newVersion)
		return nil
	}
	// Protocol equality is the whole compatibility policy (ATC-325), and
	// the protocol that matters is the installed build's, not this old
	// process's: a server on another release that still speaks it keeps
	// working and is only offered a restart on request. An installed
	// build whose protocol could not be read is not assumed compatible.
	if newProtocol != 0 && serverProtocol == newProtocol && opts.Restart != RestartAlways {
		say(opts.Stdout, "%s\n", compatibleServerLine(serverVersion))
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

// binaryProtocol asks the staged binary which ATC protocol it speaks
// (`version --protocol`); 0 when it cannot say, as a build from before
// the protocol contract cannot.
func binaryProtocol(ctx context.Context, path string) int {
	out, err := exec.CommandContext(ctx, path, "version", "--protocol").Output()
	if err != nil {
		return 0
	}
	protocol, err := strconv.Atoi(strings.TrimSpace(string(out)))
	if err != nil || protocol <= 0 {
		return 0
	}
	return protocol
}

// say writes a user-facing message; a failed write to the user's own
// terminal has no better remedy than the message it would have carried.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
