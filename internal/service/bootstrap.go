package service

// The remote bootstrap (ATC-316, extended by ATC-325): what `atc __remote
// bootstrap` runs on the machine an `atc --remote <target>` launch names.
// It reuses the lifecycle wholesale — the health probe, Start and
// Restart, the token store, the tailnet inspection — and adds only the
// bounded wait for exposure and the one JSON object the caller decodes.
// It never modifies configuration; the guided setup's configuration
// change is the separate `configure` step.

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"time"

	"github.com/jeremytondo/atc/internal/api"
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

// Bootstrap starts the supervised server if it is not answering — or
// restarts it when restart is set, the guided setup's approved remedy for
// a running server on another protocol or without tailnet exposure —
// waits for its tailnet exposure to report serving, and returns the
// material a remote picker needs, with the running server's version and
// protocol as it answered after any start. opts.Flags apply to the start
// or restart exactly as `atc server start` and `restart` apply them. A
// running server on another protocol is never restarted without restart
// (that decision belongs to the person who approved it), and an approved
// restart is skipped when the server proves ready in the meantime.
// Lifecycle output goes to opts.Stderr so stdout can carry the JSON
// object alone.
func Bootstrap(ctx context.Context, opts Options, restart bool) (remote.Bootstrap, error) {
	if err := supported(); err != nil {
		return remote.Bootstrap{}, err
	}
	if err := requireTailnet(ctx, opts, restart); err != nil {
		return remote.Bootstrap{}, err
	}
	token, err := ensureToken()
	if err != nil {
		return remote.Bootstrap{}, err
	}
	probe := probeOnce(ctx, opts, token)
	if restart && probe.healthy {
		// The approval was given against facts that may have moved: a
		// server that is now healthy, on this protocol, and serving on the
		// tailnet is left alone — never bounced for its release.
		if endpoint, problem, err := inspectTailnetWithTimeout(ctx, opts.Config, ""); err != nil {
			return remote.Bootstrap{}, err
		} else if endpoint != "" && problem == "" {
			say(opts.Stderr, "the server is already healthy, on this protocol, and serving on the tailnet; not restarted\n")
			restart = false
		}
	}
	lifecycleOpts := opts
	lifecycleOpts.Stdout = opts.Stderr
	switch {
	case restart:
		if err := Restart(ctx, lifecycleOpts); err != nil {
			return remote.Bootstrap{}, fmt.Errorf("cannot restart the server: %w", err)
		}
	case probe.incompatible:
		return remote.Bootstrap{}, fmt.Errorf("the running server (%s, %s) is not on this executable's protocol %d; rerun the remote command to restart it", versionOrUnknown(probe.serverVersion), protocolText(probe.serverProtocol), api.Protocol)
	case !probe.healthy:
		if err := Start(ctx, lifecycleOpts); err != nil {
			return remote.Bootstrap{}, fmt.Errorf("cannot start the server: %w", err)
		}
	}
	if restart || !probe.healthy {
		if probe = probeOnce(ctx, opts, token); !probe.healthy {
			return remote.Bootstrap{}, errors.New("the server started but does not answer its health check")
		}
	}
	serverVersion := probe.serverVersion
	if serverVersion == "" {
		serverVersion = opts.Version
	}
	serverProtocol := probe.serverProtocol
	if serverProtocol == 0 {
		// Healthy means it answered on this build's protocol.
		serverProtocol = api.Protocol
	}

	// One deadline covers every inspection, so a hung tailscale CLI
	// cannot hold the bootstrap past the bound.
	waitCtx, cancel := context.WithTimeout(ctx, tailnetServingTimeout)
	defer cancel()
	for {
		endpoint, problem := inspectTailnetEndpoint(waitCtx, opts.Config, "")
		if err := ctx.Err(); err != nil {
			return remote.Bootstrap{}, err
		}
		if problem == "" && endpoint != "" && waitCtx.Err() == nil {
			return remote.Bootstrap{URL: endpoint, Token: token, Version: serverVersion, Protocol: serverProtocol}, nil
		}
		select {
		case <-time.After(tailnetPollInterval):
		case <-waitCtx.Done():
			if problem == "" {
				problem = "inspection did not finish"
			}
			return remote.Bootstrap{}, fmt.Errorf("tailnet exposure did not reach serving within %s: %s", tailnetServingTimeout, problem)
		}
	}
}

func versionOrUnknown(v string) string {
	if v == "" {
		return "an unknown version"
	}
	return v
}

// requireTailnet refuses a bootstrap whose server will never be reachable
// over the tailnet, naming the exact remedy: the configuration key when
// config.toml lacks it, the restart flag when the launch that will be
// running disabled it. The launch judged is the one the bootstrap leaves
// running: the current one, or — with restart — the one Restart renders
// from opts.Flags over the current launch's flags, exactly as
// startOrRestart inherits them. Nothing here writes configuration.
func requireTailnet(ctx context.Context, opts Options, restart bool) error {
	var running LaunchFlags
	if supervisorRunning(ctx) {
		if flags, err := installedLaunchFlags(); err == nil {
			running = flags
		}
		if restart {
			running = opts.Flags.inherit(running)
		}
	} else {
		running = opts.Flags
	}
	if effective(running.Tailscale, opts.Config.Tailscale) {
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
