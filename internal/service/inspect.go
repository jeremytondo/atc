package service

// Discovery for guided remote setup (ATC-325): what `atc __remote
// inspect` reports on the target. Facts only, gathered without changing
// anything — a launch that is then declined must leave the machine as it
// found it. The local side decides what, if anything, to do.

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/authtoken"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/remote"
	"github.com/jeremytondo/atc/internal/tailscale"
	"github.com/jeremytondo/atc/internal/version"
)

// managedPrefixes are locations a package manager owns. An executable
// under one is updated through that manager, never overwritten.
var managedPrefixes = []string{
	"/opt/homebrew/", "/usr/local/Cellar/", "/home/linuxbrew/", "/opt/local/",
	"/nix/", "/snap/", "/var/lib/flatpak/",
	"/usr/bin/", "/usr/sbin/", "/usr/lib/", "/usr/libexec/", "/usr/share/",
}

// Inspect gathers the inspection for the executable running it.
func Inspect(ctx context.Context, opts Options) (remote.Inspection, error) {
	if err := supported(); err != nil {
		return remote.Inspection{}, err
	}
	executable, err := os.Executable()
	if err != nil {
		return remote.Inspection{}, err
	}
	if resolved, err := filepath.EvalSymlinks(executable); err == nil {
		executable = resolved
	}
	insp := remote.Inspection{
		Executable: remote.Executable{
			Path:     executable,
			Version:  opts.Version,
			Channel:  version.Channel(opts.Version),
			Protocol: api.Protocol,
			Writable: dirWritable(filepath.Dir(executable)),
			Managed:  packageManaged(executable),
		},
		Tailnet: remote.Tailnet{Configured: opts.Config.Tailscale},
	}
	unitFile, err := UnitPath()
	if err != nil {
		return remote.Inspection{}, err
	}
	if content, err := os.ReadFile(unitFile); err == nil {
		if path, err := unitExecutable(runtime.GOOS, string(content)); err == nil {
			insp.Server.Executable = path
		}
		if supervisorRunning(ctx) {
			insp.Server.Supervised = true
			if flags, err := unitLaunchFlags(runtime.GOOS, string(content)); err == nil {
				insp.Tailnet.Launch = flags.Tailscale
			}
		}
	}
	// Read-only: an absent token stays absent and the probe goes
	// tokenless, which still learns liveness, version, and protocol.
	tokenPath, err := paths.AuthTokenFile()
	if err != nil {
		return remote.Inspection{}, err
	}
	token, err := (&authtoken.Store{Path: tokenPath}).Read()
	if err != nil {
		return remote.Inspection{}, err
	}
	probe := probeOnce(ctx, opts, token)
	insp.Server.Responding = probe.responding
	insp.Server.Healthy = probe.healthy
	insp.Server.Version = probe.serverVersion
	insp.Server.Protocol = probe.serverProtocol
	insp.Tailnet.Problem = inspectTailnetProblem(ctx, opts.Config)
	return insp, nil
}

// inspectTailnetProblem is the node query behind Inspect, a seam so tests
// never consult the developer's tailnet.
var inspectTailnetProblem = tailnetProblem

// tailnetProblem reports why Tailscale cannot serve ATC yet — not
// installed, not running, logged out — with the remedy, since the user
// fixes it and reruns; "" when the node is up.
func tailnetProblem(ctx context.Context, cfg config.Config) string {
	executable, err := resolveTailscaleExecutable(cfg.TailscaleExecutable)
	if err != nil {
		return fmt.Sprintf("%v; install Tailscale from https://tailscale.com/download and run `tailscale up`", err)
	}
	nodeCtx, cancel := context.WithTimeout(ctx, tailnetInspectionTimeout)
	defer cancel()
	if _, err := tailscale.DNSName(nodeCtx, executable); err != nil {
		return fmt.Sprintf("%v; run `tailscale up` and check `tailscale status`", err)
	}
	return ""
}

// packageManaged reports whether an executable lives where a package
// manager installs.
func packageManaged(path string) bool {
	for _, prefix := range managedPrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
