package service

// Health probing: GET /v1/health is the source of truth for liveness. The
// server version rides the Atc-Server-Version header on every response,
// 401s included, so liveness and version detection never require a valid
// token even though health itself does.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/authtoken"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/paths"
	"github.com/jeremytondo/atc/internal/tailscale"
)

// resolveTailscaleExecutable is the lifecycle preflight, a seam variable
// (the stdioIsTerminal pattern from package main) so lifecycle tests can
// script resolution without a tailscale install.
var resolveTailscaleExecutable = tailscale.ResolveExecutable

// Health-gate contract (ATC-260, carried from legacy): 15s total, 150ms
// interval, 1s per probe.
const (
	healthGateTimeout  = 15 * time.Second
	healthGateInterval = 150 * time.Millisecond
	probeTimeout       = time.Second
)

type probeOutcome struct {
	responding    bool // any HTTP response at all
	healthy       bool // authenticated 200
	unauthorized  bool
	serverVersion string
}

// probeOnce is a seam variable for the same reason: faking the one probe
// lets lifecycle tests exercise the real health gate hermetically.
var probeOnce = func(ctx context.Context, opts Options, token string) probeOutcome {
	var serverVersion string
	client := api.NewClient("http://"+probeAddr(opts.Config), token, opts.Version,
		&http.Client{Timeout: probeTimeout},
		func(version string) { serverVersion = version })
	_, err := client.Health(ctx)
	if err == nil {
		return probeOutcome{responding: true, healthy: true, serverVersion: serverVersion}
	}
	// Any *Problem means an HTTP response arrived — including a 2xx whose
	// body was not decodable; anything else means nothing answered.
	problem, ok := errors.AsType[*api.Problem](err)
	if !ok {
		return probeOutcome{}
	}
	return probeOutcome{
		responding:    true,
		unauthorized:  problem.Status == http.StatusUnauthorized,
		serverVersion: serverVersion,
	}
}

// probeWebhooks asks the running server for its webhook ingress report.
// A seam variable so lifecycle tests script it.
var probeWebhooks = func(ctx context.Context, opts Options, token string) (api.Webhooks, error) {
	client := api.NewClient("http://"+probeAddr(opts.Config), token, opts.Version,
		&http.Client{Timeout: probeTimeout}, nil)
	return client.Webhooks(ctx)
}

// Probe reports whether a server answers on the configured address and the
// version it claims. Tokenless: the Atc-Server-Version header rides every
// response, 401s included. This is `atc upgrade`'s post-swap check.
func Probe(ctx context.Context, opts Options) (responding bool, serverVersion string) {
	outcome := probeOnce(ctx, opts, "")
	return outcome.responding, outcome.serverVersion
}

// awaitHealthy gates start/restart success on the daemon answering
// /v1/health with the local token.
func awaitHealthy(ctx context.Context, opts Options, token string) error {
	deadline := time.Now().Add(healthGateTimeout)
	for {
		if probeOnce(ctx, opts, token).healthy {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("the server did not become healthy within %s", healthGateTimeout)
		}
		select {
		case <-time.After(healthGateInterval):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// portAnswering is the pre-flight conflict check before starting an
// unsupervised port: anything accepting a TCP connection there means
// something else is already serving.
func portAnswering(cfg config.Config) bool {
	conn, err := net.DialTimeout("tcp", probeAddr(cfg), probeTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

// ensureToken mints the credential when absent so the health gate can
// authenticate on a fresh install — the same token the daemon will enforce.
func ensureToken() (string, error) {
	tokenPath, err := paths.AuthTokenFile()
	if err != nil {
		return "", err
	}
	return (&authtoken.Store{Path: tokenPath}).Ensure()
}

// printLastLogs shows the daemon's most recent output after a health-gate
// failure — diagnose, don't guess. Journal on Linux, the launchd-captured
// file on macOS.
func printLastLogs(ctx context.Context, stderr io.Writer) {
	if logs := lastLogLines(ctx, 15); logs != "" {
		say(stderr, "last server log lines:\n%s\n", logs)
	} else {
		say(stderr, "no server logs were found; try `atc server logs`\n")
	}
}

func lastLogLines(ctx context.Context, count int) string {
	if runtime.GOOS == "linux" {
		out, err := exec.CommandContext(ctx, "journalctl",
			"--user", "-u", UnitName, "-n", strconv.Itoa(count), "--no-pager").Output()
		if err != nil {
			return ""
		}
		return strings.TrimSpace(string(out))
	}
	logFile, err := paths.LogFile()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(logFile)
	if err != nil {
		return ""
	}
	return tailLines(string(data), count)
}

// tailLines keeps the last count lines of text.
func tailLines(text string, count int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	if len(lines) > count {
		lines = lines[len(lines)-count:]
	}
	return strings.Join(lines, "\n")
}
