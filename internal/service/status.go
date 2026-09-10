package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/config"
	"github.com/jeremytondo/atc/internal/tailscale"
)

// Status reports liveness (the health probe is the source of truth), unit
// state as supplementary, client and server versions and protocols with
// incompatibility flagged, and ready-to-paste API URLs. Exit codes: 0 healthy, 1 installed but not
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
		unitFile:       unitFile,
		responding:     probe.responding,
		healthy:        probe.healthy,
		unauthorized:   probe.unauthorized,
		incompatible:   probe.incompatible,
		clientVersion:  opts.Version,
		serverVersion:  probe.serverVersion,
		serverProtocol: probe.serverProtocol,
		port:           opts.Config.Port,
		bind:           opts.Config.Bind,
	}
	// The installed unit records the running launch's flags, so status
	// reads it with the same inspection lifecycle uses — displayed intent
	// cannot diverge from what the supervisor executed. Only a running
	// launch's flags count (a stopped launch's are history, exactly as
	// start treats them); an unreadable or unrecognized unit is reported
	// as unknown, never guessed.
	switch unit, unitErr := os.ReadFile(unitFile); {
	case unitErr == nil:
		info.installed = true
		info.supervisor = supervisorState(ctx)
		if flags, parseErr := unitLaunchFlags(runtime.GOOS, string(unit)); parseErr != nil {
			info.flagsProblem = parseErr.Error()
		} else if supervisorRunning(ctx) {
			info.flags = flags
		}
	case !errors.Is(unitErr, fs.ErrNotExist):
		info.installed = true
		info.supervisor = supervisorState(ctx)
		info.flagsProblem = unitErr.Error()
	}
	info.tailnet = effective(info.flags.Tailscale, opts.Config.Tailscale)
	if hostname, hostErr := os.Hostname(); hostErr == nil {
		info.hostname = hostname
	}
	if info.tailnet {
		info.tailnetURL, info.tailnetProblem, err = inspectTailnetWithTimeout(ctx, opts.Config, "")
		if err != nil {
			return err
		}
	}
	if info.healthy {
		// The running server alone knows its webhook state — whether its
		// receiver is isolated, its Funnel established, what its inbox
		// holds — so it is asked whenever it answers, whatever config and
		// unit predict about it.
		info.webhookStatus, info.webhookErr = probeWebhooks(ctx, opts, token)
		info.documentsStatus, info.documentsErr = probeDocuments(ctx, opts, token)
		if err := ctx.Err(); err != nil {
			return err
		}
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

// inspectTailnetEndpoint is shared by status and lifecycle success output. It
// is a seam so lifecycle tests never consult the developer's real tailnet.
var inspectTailnetEndpoint = tailnetEndpoint

var tailnetInspectionTimeout = 2 * time.Second

// inspectTailnetWithTimeout gives the two CLI queries one shared short budget.
// Expiry is endpoint diagnostics; cancellation of the caller aborts the command.
func inspectTailnetWithTimeout(ctx context.Context, cfg config.Config, executable string) (endpoint, problem string, err error) {
	inspectCtx, cancel := context.WithTimeout(ctx, tailnetInspectionTimeout)
	defer cancel()
	endpoint, problem = inspectTailnetEndpoint(inspectCtx, cfg, executable)
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	if errors.Is(inspectCtx.Err(), context.DeadlineExceeded) {
		problem = "tailnet endpoint inspection timed out"
	}
	return endpoint, problem, nil
}

func tailnetEndpoint(ctx context.Context, cfg config.Config, executable string) (endpoint, problem string) {
	if executable == "" {
		var err error
		if executable, err = resolveTailscaleExecutable(cfg.TailscaleExecutable); err != nil {
			return "", err.Error()
		}
	}
	dns, err := tailscale.DNSName(ctx, executable)
	if err != nil {
		return "", err.Error()
	}
	expected := tailscale.HTTPSURL(dns, cfg.Port)
	endpoint, err = tailscale.ServeURL(ctx, executable, dns, cfg.Port)
	if err != nil {
		return expected, err.Error()
	}
	return endpoint, ""
}

type statusInfo struct {
	installed    bool
	unitFile     string
	supervisor   string // supplementary unit state; "" when not installed
	responding   bool
	healthy      bool
	unauthorized bool
	// incompatible is a responding server on another protocol; the
	// client's api.Protocol is the one it is compared with.
	incompatible   bool
	clientVersion  string
	serverVersion  string // "" when no response carried one
	serverProtocol int    // 0 when no response carried one
	port           int
	bind           string
	hostname       string
	// flags are the running launch's exposure flags, read from the unit;
	// zero when no launch is running or none were supplied.
	flags LaunchFlags
	// flagsProblem is why the installed unit's launch flags are unknown
	// (unreadable or unrecognized content); "" when readable.
	flagsProblem string
	// tailnet is the effective tailnet exposure: launch flag when supplied,
	// configuration otherwise.
	tailnet        bool
	tailnetURL     string
	tailnetProblem string
	// webhookStatus is the server's own report when it is healthy;
	// webhookErr is why it could not be fetched.
	webhookStatus api.Webhooks
	webhookErr    error
	// documentsStatus is the server's document origin report when it is
	// healthy; documentsErr is why it could not be fetched.
	documentsStatus api.DocumentOrigin
	documentsErr    error
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
	case s.incompatible && !s.installed:
		// Something answers on another protocol with no unit registered:
		// a foreground `atc server run` of another build owns the port,
		// so starting the supervised server would not help.
		code = 1
		fmt.Fprintf(&b, "%s: not installed, but a server on %s answers on the port (a foreground `atc server run` of another build?) while this client speaks protocol %d; stop it, then `atc server start`\n", UnitName, protocolText(s.serverProtocol), api.Protocol)
	case !s.installed:
		code = 2
		fmt.Fprintf(&b, "%s: not installed; `atc server start` registers and starts it\n", UnitName)
	case s.incompatible:
		// A compatible server on another release is simply healthy; only
		// the protocol decides, and the installed build is the remedy on
		// this machine since restart re-renders the unit from it.
		code = 1
		fmt.Fprintf(&b, "%s: running on %s while this client speaks protocol %d; `atc server restart` runs the installed build (a foreground `atc server run` holding the port must be stopped first)\n", UnitName, protocolText(s.serverProtocol), api.Protocol)
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
	fmt.Fprintf(&b, "  client: %s (protocol %d)\n", s.clientVersion, api.Protocol)
	switch {
	case s.serverVersion == "" && !s.responding:
		b.WriteString("  server: unknown (not responding)\n")
	case s.serverVersion == "":
		b.WriteString("  server: unknown\n")
	case s.serverVersion != s.clientVersion && !s.incompatible:
		// Release identity is shown, never acted on: a different release
		// on the same protocol needs nothing.
		fmt.Fprintf(&b, "  server: %s (%s; a different release from the client, and compatible)\n", s.serverVersion, protocolText(s.serverProtocol))
	default:
		fmt.Fprintf(&b, "  server: %s (%s)\n", s.serverVersion, protocolText(s.serverProtocol))
	}
	for _, url := range apiURLs(s) {
		fmt.Fprintf(&b, "  %s\n", url)
	}
	if s.healthy {
		for _, line := range renderDocuments(s.documentsStatus, s.documentsErr) {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		for _, line := range renderWebhooks(s.webhookStatus, s.webhookErr) {
			fmt.Fprintf(&b, "  %s\n", line)
		}
	}
	for _, line := range renderLaunchFlags(s.flags) {
		fmt.Fprintf(&b, "  %s\n", line)
	}
	if s.flagsProblem != "" {
		fmt.Fprintf(&b, "  launch flags: unknown (%s); `atc server stop`, then `atc server start` with the flags you want\n", s.flagsProblem)
	}
	b.WriteString("  token: `atc server token` prints the bearer token remote clients use\n")
	return b.String(), code
}

// protocolText names a server's protocol for people.
func protocolText(protocol int) string {
	if protocol == 0 {
		return "no protocol"
	}
	return fmt.Sprintf("protocol %d", protocol)
}

// renderLaunchFlags attributes each effective override to the running
// launch's flag, with the way back: restart replaces a flag, stop then
// start returns to config.toml.
func renderLaunchFlags(flags LaunchFlags) []string {
	var lines []string
	render := func(name string, value *bool) {
		if value == nil {
			return
		}
		state, replacement := "enabled", "--"+name+"=false"
		if !*value {
			state, replacement = "disabled", "--"+name
		}
		lines = append(lines, fmt.Sprintf("%s: %s by this launch's flag; `atc server restart %s` replaces it, stop then start returns to config.toml", name, state, replacement))
	}
	render("tailscale", flags.Tailscale)
	render("webhooks", flags.Webhooks)
	return lines
}

// renderWebhooks formats the server's webhook report for the CLI: the
// readiness line, the awaited action, the registered routes, and the
// failure summaries. Disabled intake with nothing left to process prints
// nothing — the common case is silent. err is the failure to fetch the
// report at all.
func renderWebhooks(status api.Webhooks, err error) []string {
	if err != nil {
		return []string{fmt.Sprintf("webhooks: unknown (%s)", err)}
	}
	var lines []string
	switch status.State {
	case "":
		return nil
	case api.WebhooksReady:
		line := fmt.Sprintf("webhooks: %s (%d pending)", status.URL, status.Pending)
		if status.IntakeBlocked {
			line += "; intake blocked"
		}
		lines = append(lines, line)
	case api.WebhooksStarting:
		line := "webhooks: starting"
		if status.URL != "" {
			line += " at " + status.URL
		}
		if status.Reason != "" {
			line += " (" + status.Reason + ")"
		}
		lines = append(lines, line)
	case api.WebhooksUnavailable:
		lines = append(lines, "webhooks: unavailable ("+status.Reason+")")
	case api.WebhooksDisabled:
		if status.Pending == 0 && status.ProcessingFailures == 0 {
			return nil
		}
		lines = append(lines, fmt.Sprintf("webhooks: disabled (%d pending from earlier intake)", status.Pending))
	default:
		lines = append(lines, "webhooks: "+string(status.State))
	}
	if status.Action != "" {
		lines = append(lines, "webhooks action: "+strings.Join(strings.Fields(status.Action), " "))
	}
	for _, route := range status.Routes {
		target := route.Path
		if status.URL != "" {
			target = strings.TrimSuffix(status.URL, "/") + route.Path
		}
		lines = append(lines, fmt.Sprintf("webhook route (%s): %s", route.IntegrationID, target))
	}
	if status.Rejected > 0 {
		lines = append(lines, fmt.Sprintf("webhooks rejected: %d since start (last: %s)", status.Rejected, status.LastRejection))
	}
	if status.ProcessingFailures > 0 {
		lines = append(lines, fmt.Sprintf("webhook processing failures: %d since start (last: %s)", status.ProcessingFailures, status.LastProcessingFailure))
	}
	return lines
}

// renderDocuments formats the server's document origin report: the local
// base URL (or why it is not serving) and the tailnet exposure when the
// launch has one. err is the failure to fetch the report at all.
func renderDocuments(status api.DocumentOrigin, err error) []string {
	if err != nil {
		return []string{fmt.Sprintf("documents: unknown (%s)", err)}
	}
	var lines []string
	switch status.State {
	case "":
		return nil
	case api.OriginReady:
		lines = append(lines, "documents: "+status.URL)
	case api.OriginStarting:
		lines = append(lines, "documents: starting ("+status.Reason+")")
	case api.OriginUnavailable:
		lines = append(lines, "documents: unavailable ("+status.Reason+"); publishing still works, and the listener is retried")
	default:
		lines = append(lines, "documents: "+string(status.State))
	}
	switch tailnet := status.Tailnet; tailnet.State {
	case api.TailnetReady:
		lines = append(lines, "documents (tailnet): "+tailnet.URL)
	case api.TailnetStarting:
		lines = append(lines, "documents (tailnet): "+renderPending(tailnet.URL, tailnet.Reason))
		if tailnet.Action != "" {
			lines = append(lines, "documents action: "+strings.Join(strings.Fields(tailnet.Action), " "))
		}
	}
	return lines
}

func renderPending(endpoint, problem string) string {
	if endpoint != "" {
		return fmt.Sprintf("pending at %s (%s)", endpoint, problem)
	}
	return fmt.Sprintf("unavailable (%s)", problem)
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
	if s.tailnet {
		urls = append(urls, renderTailnetURL(s.tailnetURL, s.tailnetProblem))
	}
	return urls
}

func renderTailnetURL(endpoint, problem string) string {
	switch {
	case endpoint != "" && problem == "":
		return "api (tailnet): " + endpoint
	case endpoint != "":
		return fmt.Sprintf("api (tailnet): pending at %s (%s)", endpoint, problem)
	default:
		return fmt.Sprintf("api (tailnet): unavailable (%s)", problem)
	}
}
