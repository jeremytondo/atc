package service

// The remote bootstrap (ATC-316): what `atc __bootstrap` runs on the
// machine an `atc --remote <target>` launch names. It reuses the lifecycle
// wholesale — the health probe, Start, the token store, the tailnet
// inspection — and adds only the bounded wait for exposure and the one
// JSON object the caller decodes. It never modifies configuration.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/remote"
)

// tailnetServingTimeout bounds the wait for `tailscale serve` to report
// the API route live; the exposure supervisor retries with backoff behind
// it, so a slow tailnet still converges later even when this launch gives
// up.
var (
	tailnetServingTimeout = 30 * time.Second
	tailnetPollInterval   = time.Second
)

// Bootstrap starts the supervised server if it is not answering, waits
// for its tailnet exposure to report serving, and returns the material a
// remote picker needs. Lifecycle output goes to opts.Stderr so stdout can
// carry the JSON object alone.
func Bootstrap(ctx context.Context, opts Options) (remote.Bootstrap, error) {
	if err := supported(); err != nil {
		return remote.Bootstrap{}, err
	}
	if err := requireTailnet(ctx, opts); err != nil {
		return remote.Bootstrap{}, err
	}
	token, err := ensureToken()
	if err != nil {
		return remote.Bootstrap{}, err
	}
	probe := probeOnce(ctx, opts, token)
	if !probe.healthy {
		startOpts := opts
		startOpts.Stdout = opts.Stderr
		if err := Start(ctx, startOpts); err != nil {
			return remote.Bootstrap{}, fmt.Errorf("cannot start the server: %w", err)
		}
		if probe = probeOnce(ctx, opts, token); !probe.healthy {
			return remote.Bootstrap{}, errors.New("the server started but does not answer its health check")
		}
	}
	serverVersion := probe.serverVersion
	if serverVersion == "" {
		serverVersion = opts.Version
	}

	deadline := time.Now().Add(tailnetServingTimeout)
	for {
		endpoint, problem := inspectTailnetEndpoint(ctx, opts.Config, "")
		if err := ctx.Err(); err != nil {
			return remote.Bootstrap{}, err
		}
		if problem == "" && endpoint != "" {
			return remote.Bootstrap{URL: endpoint, Token: token, Version: serverVersion}, nil
		}
		if time.Now().After(deadline) {
			return remote.Bootstrap{}, fmt.Errorf("tailnet exposure did not reach serving within %s: %s", tailnetServingTimeout, problem)
		}
		select {
		case <-time.After(tailnetPollInterval):
		case <-ctx.Done():
			return remote.Bootstrap{}, ctx.Err()
		}
	}
}

// requireTailnet refuses a bootstrap whose server will never be reachable
// over the tailnet, naming the exact remedy: the configuration key when
// config.toml lacks it, the restart flag when the running launch disabled
// it. Nothing here writes configuration.
func requireTailnet(ctx context.Context, opts Options) error {
	tailnet := opts.Config.Tailscale
	if supervisorRunning(ctx) {
		if flags, err := installedLaunchFlags(); err == nil {
			tailnet = effective(flags.Tailscale, tailnet)
		}
	}
	if tailnet {
		return nil
	}
	if !opts.Config.Tailscale {
		configPath, err := paths.ConfigFile()
		if err != nil {
			configPath = "~/.config/atc/config.toml"
		}
		return fmt.Errorf("tailnet exposure is not enabled on this machine: add `tailscale = true` to %s and run `atc server restart`", configPath)
	}
	return errors.New("tailnet exposure is disabled for the running launch: run `atc server restart --tailscale`")
}

// installedLaunchFlags reads the running launch's exposure flags from the
// installed unit, the same inspection status and lifecycle use.
func installedLaunchFlags() (LaunchFlags, error) {
	unitFile, err := UnitPath()
	if err != nil {
		return LaunchFlags{}, err
	}
	content, err := os.ReadFile(unitFile)
	if err != nil {
		return LaunchFlags{}, err
	}
	return unitLaunchFlags(runtime.GOOS, string(content))
}
