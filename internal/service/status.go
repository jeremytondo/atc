package service

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/tailscale"
)

// Status reports liveness (the health probe is the source of truth), unit
// state as supplementary, client and server versions with skew flagged, and
// ready-to-paste API URLs. Exit codes: 0 healthy, 1 installed but not
// responding, 2 not installed.
func Status(ctx context.Context, opts Options) error {
	if err := supported(); err != nil {
		return err
	}
	unitFile, err := UnitPath()
	if err != nil {
		return err
	}
	token, err := ensureToken()
	if err != nil {
		return err
	}
	probe := probeOnce(ctx, opts, token)

	info := statusInfo{
		unitFile:      unitFile,
		responding:    probe.responding,
		healthy:       probe.healthy,
		unauthorized:  probe.unauthorized,
		clientVersion: opts.Version,
		serverVersion: probe.serverVersion,
		port:          opts.Config.Port,
		bind:          opts.Config.Bind,
		tailscale:     opts.Config.Tailscale,
	}
	if _, statErr := os.Stat(unitFile); statErr == nil {
		info.installed = true
		info.supervisor = supervisorState(ctx)
	}
	if hostname, hostErr := os.Hostname(); hostErr == nil {
		info.hostname = hostname
	}
	if opts.Config.Tailscale {
		info.tailnetDNS, info.tailnetProblem = tailnetDNS(ctx, opts.Config)
	}

	report, code := renderStatus(info)
	say(opts.Stdout, "%s", report)
	if code != 0 {
		return &ExitError{Code: code}
	}
	return nil
}

// supervisorState is the supplementary unit-state string shown next to the
// unit path (thin supervisor queries, untested by design).
func supervisorState(ctx context.Context) string {
	if runtime.GOOS == "darwin" {
		if domain := launchdLoadedDomain(ctx); domain != "" {
			return "loaded in " + domain
		}
		return "not loaded"
	}
	if requireSystemctl() != nil {
		return "systemctl unavailable"
	}
	// is-active prints the state and exits non-zero for anything but
	// "active" — the printed word is the interesting part either way.
	out, _ := exec.CommandContext(ctx, "systemctl", "--user", "is-active", UnitName).Output()
	if state := strings.TrimSpace(string(out)); state != "" {
		return state
	}
	return "unknown"
}

func tailnetDNS(ctx context.Context, cfg config.Config) (dns, problem string) {
	executable, err := tailscale.ResolveExecutable(cfg.TailscaleExecutable)
	if err != nil {
		return "", err.Error()
	}
	dns, err = tailscale.DNSName(ctx, executable)
	if err != nil {
		return "", err.Error()
	}
	return dns, ""
}

type statusInfo struct {
	installed      bool
	unitFile       string
	supervisor     string // supplementary unit state; "" when not installed
	responding     bool
	healthy        bool
	unauthorized   bool
	clientVersion  string
	serverVersion  string // "" when no response carried one
	port           int
	bind           string
	hostname       string
	tailscale      bool
	tailnetDNS     string
	tailnetProblem string
}

// renderStatus formats the report and picks the exit code. Pure and fully
// tested; the headline states liveness first, everything else supports it.
func renderStatus(s statusInfo) (string, int) {
	var b strings.Builder
	code := 0
	switch {
	case s.healthy && s.installed:
		fmt.Fprintf(&b, "%s: running and healthy\n", UnitName)
	case s.healthy:
		fmt.Fprintf(&b, "%s: healthy, but not installed — likely a foreground `atc server run`\n", UnitName)
	case !s.installed:
		code = 2
		fmt.Fprintf(&b, "%s: not installed; `atc server start` registers and starts it\n", UnitName)
	case s.unauthorized:
		code = 1
		fmt.Fprintf(&b, "%s: responding but rejected the local token; `atc server restart`, then `atc server token rotate` if it persists\n", UnitName)
	case s.responding:
		code = 1
		fmt.Fprintf(&b, "%s: responding but unhealthy; try `atc server logs`\n", UnitName)
	default:
		code = 1
		fmt.Fprintf(&b, "%s: installed but not responding; try `atc server logs` or `atc server restart`\n", UnitName)
	}
	if s.installed {
		fmt.Fprintf(&b, "  unit: %s (%s)\n", s.unitFile, s.supervisor)
	}
	fmt.Fprintf(&b, "  client: %s\n", s.clientVersion)
	switch {
	case s.serverVersion == "" && !s.responding:
		b.WriteString("  server: unknown (not responding)\n")
	case s.serverVersion == "":
		b.WriteString("  server: unknown\n")
	case s.serverVersion != s.clientVersion:
		fmt.Fprintf(&b, "  server: %s — differs from client %s; `atc server restart` updates it\n", s.serverVersion, s.clientVersion)
	default:
		fmt.Fprintf(&b, "  server: %s\n", s.serverVersion)
	}
	for _, url := range apiURLs(s) {
		fmt.Fprintf(&b, "  %s\n", url)
	}
	b.WriteString("  token: `atc server token` prints the bearer token remote clients use\n")
	return b.String(), code
}

// apiURLs builds the ready-to-paste URLs the configuration serves: loopback
// always, a LAN form when bound wider, the tailnet URL when exposure is on.
func apiURLs(s statusInfo) []string {
	urls := []string{fmt.Sprintf("api: http://127.0.0.1:%d", s.port)}
	ip := net.ParseIP(s.bind)
	loopback := s.bind == "localhost" || (ip != nil && ip.IsLoopback())
	if !loopback {
		host := s.bind
		if ip != nil && ip.IsUnspecified() {
			host = s.hostname
		}
		if host != "" {
			urls = append(urls, "api (lan): http://"+net.JoinHostPort(host, strconv.Itoa(s.port)))
		}
	}
	if s.tailscale {
		if s.tailnetDNS != "" {
			urls = append(urls, fmt.Sprintf("api (tailnet): https://%s:%d", s.tailnetDNS, s.port))
		} else {
			urls = append(urls, fmt.Sprintf("api (tailnet): unavailable (%s)", s.tailnetProblem))
		}
	}
	return urls
}
