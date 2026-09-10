package remote

// Guided setup (ATC-325). Compatibility is protocol equality and nothing
// else: a compatible installation and a ready compatible server are left
// alone whatever their releases; an incompatible executable is updated in
// place from the latest published build of its own channel, only where
// the user can write and no package manager owns it; an incompatible or
// unhealthy running server is restarted, not reinstalled. Nothing here
// downgrades, switches channels, compiles, or copies a local build. The
// decision (decide) is a pure function of the discovered facts; Run
// sequences discovery, one confirmation, the approved changes, the
// bootstrap, and the live readiness proof.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/version"
)

// Identity is the local build as setup compares and describes it; its
// channel is read off the version.
type Identity struct {
	Version  string
	Protocol int
}

// Releases is the published-build source (internal/upgrade's GitHub in
// production): the latest tag of a channel for a platform, and its
// verified executable bytes.
type Releases interface {
	Latest(ctx context.Context, channel, goos, goarch string) (tag string, err error)
	Fetch(ctx context.Context, tag, goos, goarch string) ([]byte, error)
}

// Setup is one guided launch against one target.
type Setup struct {
	SSH      *SSH
	Local    Identity
	Releases Releases
	// Stdin answers the confirmation; Stdout carries the plan and
	// progress; Stderr shows the remote's own output live.
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
	// Verify checks the bootstrapped endpoint from this machine over the
	// tailnet — the live readiness proof. nil uses the API client.
	Verify func(ctx context.Context, b Bootstrap) (api.Health, error)
}

// Connection is a proven-ready remote server: the bootstrap material and
// the remote executable every later command (attach included) runs.
type Connection struct {
	Bootstrap  Bootstrap
	Executable string
}

// ErrDeclined is the outcome of answering no: nothing was changed.
var ErrDeclined = errors.New("setup declined")

// Plan is what the target needs before the picker can open. A plan
// without changes proceeds straight to the bootstrap: starting a stopped
// compatible server needs no approval, as it never did.
type Plan struct {
	// Executable is the atc every remote command uses once the plan is
	// applied: the compatible one found, or the one Install lands there.
	Executable string
	// Install replaces or creates Executable from a published build.
	Install *Install
	// EnableTailscale sets `tailscale = true` in the target's config.toml.
	EnableTailscale bool
	// Restart bounces the running server, with --tailscale when the
	// running launch disabled exposure by flag.
	Restart       bool
	TailscaleFlag bool
	// Notes are facts worth showing that change nothing.
	Notes []string
	// Server is what answered the probe, for the plan's description.
	Server Server
}

// Install describes one executable installation into Plan.Executable.
type Install struct {
	Channel string
	// Current is the executable being replaced; nil for a fresh
	// installation.
	Current *Executable
	// Tag is the resolved latest release, filled in before the plan is
	// shown; "" until then.
	Tag string
}

// Changes reports whether the plan needs approval.
func (p Plan) Changes() bool { return p.Install != nil || p.EnableTailscale || p.Restart }

// facts is what decide judges: the discovery, the inspection of the
// discovered executable (nil when there is none), and the inspection of
// the executable the server's unit runs when that is a different file
// (nil otherwise).
type facts struct {
	discovery Discovery
	found     *Inspection
	service   *Inspection
}

// decide turns facts into a plan or a refusal with the remedy. Pure.
func decide(target string, local Identity, f facts) (Plan, error) {
	var plan Plan
	// The executable to use: the server's own first (its location is
	// respected), then the discovered one — the first compatible wins.
	var known []*Inspection
	if f.service != nil {
		known = append(known, f.service)
	}
	if f.found != nil {
		known = append(known, f.found)
	}
	if f.discovery.Path == "" && f.found != nil {
		plan.Notes = append(plan.Notes, fmt.Sprintf("atc on %s is at %s, which is not on its non-interactive PATH; it is used directly", target, f.found.Executable.Path))
	}
	if f.service != nil && f.found != nil {
		plan.Notes = append(plan.Notes, fmt.Sprintf("the server on %s runs %s; the atc found first is %s", target, f.service.Executable.Path, f.found.Executable.Path))
	}
	var chosen *Inspection
	for _, insp := range known {
		if insp.Executable.Protocol == local.Protocol {
			chosen = insp
			break
		}
	}
	switch {
	case chosen != nil:
		plan.Executable = chosen.Executable.Path
	case len(known) == 0:
		channel := version.Channel(local.Version)
		if channel == "" {
			return Plan{}, fmt.Errorf("atc is not installed on %s, and this atc (%s) is an unpublished build with nothing to download; install it there first:\n  curl -fsSL https://raw.githubusercontent.com/jeremytondo/atc/main/install.sh | sh", target, local.Version)
		}
		if _, _, err := f.discovery.Platform(); err != nil {
			return Plan{}, fmt.Errorf("atc is not installed on %s, and %w", target, err)
		}
		plan.Executable = path.Join(f.discovery.InstallDir(), "atc")
		plan.Install = &Install{Channel: channel}
	default:
		// The existing installation is updated where it is: the server's
		// own executable when there is one, else the one found.
		current := known[0].Executable
		where := fmt.Sprintf("the atc at %s on %s (%s)", current.Path, target, describe(current))
		switch {
		case current.Channel == "":
			return Plan{}, fmt.Errorf("%s is an unpublished build with nothing to download, and it is not on this atc's protocol %d; replace it there by hand (for example `atc upgrade` on %s), then rerun", where, local.Protocol, target)
		case current.Managed:
			return Plan{}, fmt.Errorf("%s is managed by a package manager and not on this atc's protocol %d; update it with that package manager on %s, then rerun", where, local.Protocol, target)
		case !current.Writable:
			return Plan{}, fmt.Errorf("%s is not writable by the ssh user and not on this atc's protocol %d; update it there with the permissions it needs, then rerun", where, local.Protocol)
		}
		if _, _, err := f.discovery.Platform(); err != nil {
			return Plan{}, fmt.Errorf("%s is not on this atc's protocol %d, and %w", where, local.Protocol, err)
		}
		plan.Executable = current.Path
		plan.Install = &Install{Channel: current.Channel, Current: &current}
	}

	// Server and tailnet facts come from any inspection: all probe the
	// same machine. A fresh install has none, so its plan enables the
	// setting unconditionally (idempotent) and the prerequisite is
	// checked once the installed executable can report it.
	if len(known) == 0 {
		plan.EnableTailscale = true
		return plan, nil
	}
	// The compatible inspection's verdicts count when there is one: an
	// incompatible inspector cannot judge health.
	seen := known[0]
	if chosen != nil {
		seen = chosen
	}
	if seen.Tailnet.Problem != "" {
		return Plan{}, fmt.Errorf("tailscale on %s is not ready: %s; then rerun", target, seen.Tailnet.Problem)
	}
	server := seen.Server
	if server.Responding && !server.ATC() {
		return Plan{}, fmt.Errorf("something that is not ATC answers on ATC's port on %s; stop it or change ATC's port there, then rerun", target)
	}
	plan.Server = server
	if !seen.Tailnet.Configured {
		plan.EnableTailscale = true
	}
	switch {
	case server.Responding && server.Protocol != local.Protocol:
		plan.Restart = true
	case server.Responding && !seen.Tailnet.Exposed():
		plan.Restart = true
	case server.Supervised && !server.Responding:
		// Active under the supervisor but not answering: a start would
		// be a no-op, only a restart reaches it.
		plan.Restart = true
	case server.Responding && !server.Healthy && chosen != nil:
		// Healthy is judged by the inspecting executable, so it means
		// something only when that executable is compatible.
		plan.Restart = true
	}
	if plan.Restart && seen.Tailnet.Launch != nil && !*seen.Tailnet.Launch {
		plan.TailscaleFlag = true
	}
	return plan, nil
}

// describe renders an executable's identity for people.
func describe(e Executable) string {
	channel := e.Channel
	if channel == "" {
		channel = "unpublished"
	}
	return fmt.Sprintf("%s, %s, protocol %d", e.Version, channel, e.Protocol)
}

// render is the plan as shown before the question.
func render(target string, local Identity, d Discovery, plan Plan) string {
	var b strings.Builder
	platform := d.OS + "/" + d.Arch
	if goos, goarch, err := d.Platform(); err == nil {
		platform = goos + "/" + goarch
	}
	fmt.Fprintf(&b, "%s (%s) needs setup before the picker can open:\n", target, platform)
	if in := plan.Install; in != nil {
		release := "the current " + in.Channel + " build"
		if in.Tag != "" && in.Channel != version.ChannelDev {
			release = in.Tag + " (" + in.Channel + ")"
		}
		if in.Current == nil {
			fmt.Fprintf(&b, "  atc: not installed; install %s to %s\n", release, plan.Executable)
		} else {
			fmt.Fprintf(&b, "  atc: %s at %s; update it to %s, this atc's protocol %d\n", describe(*in.Current), plan.Executable, release, local.Protocol)
		}
	}
	if plan.EnableTailscale {
		line := "  tailscale: enable `tailscale = true` in ATC's config.toml"
		if plan.Install != nil && plan.Install.Current == nil {
			line += " (unless already enabled)"
		}
		b.WriteString(line + "\n")
	}
	server := plan.Server
	switch {
	case plan.Restart && !server.Responding:
		fmt.Fprintf(&b, "  server: registered but not answering; restart it — terminals keep running, active agent turns are interrupted\n")
	case plan.Restart:
		reason := "so it exposes the API on the tailnet"
		switch {
		case server.Protocol != local.Protocol:
			reason = fmt.Sprintf("so it runs %s", plan.Executable)
		case !server.Healthy:
			reason = "as it does not answer its health check"
		}
		fmt.Fprintf(&b, "  server: running %s (%s); restart it %s — terminals keep running, active agent turns are interrupted\n", server.Version, protocolText(server.Protocol), reason)
	case !server.Responding:
		fmt.Fprintf(&b, "  server: start it with %s\n", plan.Executable)
	}
	for _, note := range plan.Notes {
		fmt.Fprintf(&b, "note: %s\n", note)
	}
	if plan.Install != nil && plan.Install.Current == nil {
		fmt.Fprintf(&b, "Tailscale itself must already be installed and connected on %s.\n", target)
	}
	return b.String()
}

// Run performs the whole launch-time flow and returns a proven-ready
// connection. Every run rediscovers: nothing from an earlier success is
// trusted.
func (s *Setup) Run(ctx context.Context) (Connection, error) {
	target := s.SSH.target
	discovery, err := s.SSH.Discover(ctx, s.Stderr)
	if err != nil {
		return Connection{}, err
	}
	f := facts{discovery: discovery}
	serviceExe := discovery.Unit
	if executable := discovery.Executable(); executable != "" {
		insp, err := s.SSH.Inspect(ctx, executable, s.Stderr)
		if err != nil {
			return Connection{}, predatesRemedy(target, executable, err)
		}
		f.found = &insp
		if insp.Server.Executable != "" {
			serviceExe = insp.Server.Executable
		}
		if serviceExe == insp.Executable.Path {
			serviceExe = ""
		}
	}
	if serviceExe != "" {
		service, err := s.SSH.Inspect(ctx, serviceExe, s.Stderr)
		switch {
		case errors.Is(err, ErrPredatesSetup):
			return Connection{}, predatesRemedy(target, serviceExe, err)
		case err != nil:
			say(s.Stderr, "note: the server on %s is registered with %s, which could not be inspected (%v); it is left alone\n", target, serviceExe, err)
		default:
			f.service = &service
		}
	}
	plan, err := decide(target, s.Local, f)
	if err != nil {
		return Connection{}, err
	}
	goos, goarch, _ := discovery.Platform()
	if in := plan.Install; in != nil {
		tag, err := s.Releases.Latest(ctx, in.Channel, goos, goarch)
		if err != nil {
			return Connection{}, err
		}
		if in.Current != nil && tag == in.Current.Version {
			return Connection{}, fmt.Errorf("%s already runs the latest %s release (%s), which is not on this atc's protocol %d; publish a compatible %s release, or update this machine to match, then rerun", target, in.Channel, tag, s.Local.Protocol, in.Channel)
		}
		if in.Current != nil && in.Channel == version.ChannelStable && olderRelease(tag, in.Current.Version) {
			return Connection{}, fmt.Errorf("the latest stable release (%s) is older than the %s on %s; nothing is downgraded — publish a newer compatible release, or update it there by hand, then rerun", tag, in.Current.Version, target)
		}
		in.Tag = tag
	}
	if plan.Changes() {
		say(s.Stdout, "%s", render(target, s.Local, discovery, plan))
		if !s.confirm(fmt.Sprintf("Apply these changes to %s? [y/N] ", target)) {
			return Connection{}, fmt.Errorf("%w; nothing was changed on %s", ErrDeclined, target)
		}
	}
	installed := ""
	if plan.Install != nil {
		insp, err := s.install(ctx, target, goos, goarch, plan.Executable, *plan.Install)
		if err != nil {
			return Connection{}, err
		}
		installed = insp.Executable.Version
		say(s.Stdout, "installed atc %s at %s on %s\n", installed, plan.Executable, target)
		if insp.Tailnet.Problem != "" {
			return Connection{}, fmt.Errorf("tailscale on %s is not ready: %s; then rerun (atc stays installed)", target, insp.Tailnet.Problem)
		}
		if plan.Install.Current == nil && insp.Tailnet.Configured {
			plan.EnableTailscale = false
		}
	}
	if plan.EnableTailscale {
		if err := s.SSH.Configure(ctx, plan.Executable, s.Stderr); err != nil {
			return Connection{}, afterInstall(target, installed, plan.Executable, err)
		}
		say(s.Stdout, "enabled `tailscale = true` in ATC's config.toml on %s\n", target)
	}
	bootstrap, err := s.SSH.Bootstrap(ctx, plan.Executable, plan.Restart, plan.TailscaleFlag, s.Stdin, s.Stderr)
	if err != nil {
		return Connection{}, afterInstall(target, installed, plan.Executable, err)
	}
	if bootstrap.Protocol != s.Local.Protocol {
		return Connection{}, fmt.Errorf("the server on %s answers on protocol %d after setup, not this atc's protocol %d; check `%s server status` there", target, bootstrap.Protocol, s.Local.Protocol, plan.Executable)
	}
	verify := s.Verify
	if verify == nil {
		verify = VerifyHealth
	}
	if _, err := verify(ctx, bootstrap); err != nil {
		return Connection{}, readinessFailure(target, bootstrap, err)
	}
	return Connection{Bootstrap: bootstrap, Executable: plan.Executable}, nil
}

// install fetches the plan's release, stages it beside the target,
// proves the staged executable runs on the machine, is the release it
// was resolved as, and speaks this atc's protocol, and only then
// promotes it. The staged file is removed whatever happens — after a
// promotion that is a no-op, and cancellation still cleans up.
func (s *Setup) install(ctx context.Context, target, goos, goarch, executable string, in Install) (Inspection, error) {
	say(s.Stdout, "downloading %s for %s/%s...\n", in.Tag, goos, goarch)
	bytes, err := s.Releases.Fetch(ctx, in.Tag, goos, goarch)
	if err != nil {
		return Inspection{}, err
	}
	staged, err := s.SSH.Stage(ctx, path.Dir(executable), bytes, s.Stderr)
	if err != nil {
		return Inspection{}, err
	}
	defer func() { _ = s.SSH.Remove(context.WithoutCancel(ctx), staged, io.Discard) }()
	insp, err := s.SSH.Inspect(ctx, staged, s.Stderr)
	if err != nil {
		return Inspection{}, fmt.Errorf("the downloaded %s build does not run on %s: %w", in.Tag, target, err)
	}
	candidate := insp.Executable
	switch {
	case candidate.Channel != in.Channel, in.Channel == version.ChannelStable && candidate.Version != in.Tag:
		return Inspection{}, fmt.Errorf("the downloaded %s build identifies as %s, not the %s release resolved; nothing was installed", in.Tag, describe(candidate), in.Channel)
	case candidate.Protocol > s.Local.Protocol:
		return Inspection{}, fmt.Errorf("the latest %s release (%s) speaks protocol %d, newer than this atc's %d; update this machine first (`atc upgrade%s`), then rerun — nothing was installed on %s", in.Channel, candidate.Version, candidate.Protocol, s.Local.Protocol, devFlag(in.Channel), target)
	case candidate.Protocol < s.Local.Protocol:
		return Inspection{}, fmt.Errorf("the latest %s release (%s) speaks protocol %d, older than this atc's %d; there is no compatible published %s release to install on %s — publish one, or install a matching build there by hand", in.Channel, candidate.Version, candidate.Protocol, s.Local.Protocol, in.Channel, target)
	}
	if err := s.SSH.Promote(ctx, staged, executable, s.Stderr); err != nil {
		return Inspection{}, err
	}
	return insp, nil
}

// olderRelease reports whether stable release a is older than stable
// release b (both vX.Y.Z); unparsable versions are never called older.
func olderRelease(a, b string) bool {
	parse := func(v string) ([3]int, bool) {
		var n [3]int
		parts := strings.Split(strings.TrimPrefix(v, "v"), ".")
		if len(parts) != 3 {
			return n, false
		}
		for i, part := range parts {
			value, err := strconv.Atoi(part)
			if err != nil {
				return n, false
			}
			n[i] = value
		}
		return n, true
	}
	x, okA := parse(a)
	y, okB := parse(b)
	if !okA || !okB {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return x[i] < y[i]
		}
	}
	return false
}

func devFlag(channel string) string {
	if channel == version.ChannelDev {
		return " --dev"
	}
	return ""
}

// confirm asks a default-no question on the user's terminal. The answer
// is read one byte at a time, never ahead of its newline: what follows
// on stdin belongs to the bootstrap's ssh session.
func (s *Setup) confirm(prompt string) bool {
	say(s.Stdout, "%s", prompt)
	var line []byte
	for {
		var b [1]byte
		n, err := s.Stdin.Read(b[:])
		if n == 1 {
			if b[0] == '\n' {
				break
			}
			line = append(line, b[0])
		}
		if err != nil {
			if len(line) == 0 {
				return false
			}
			break
		}
	}
	switch strings.ToLower(strings.TrimSpace(string(line))) {
	case "y", "yes":
		return true
	}
	return false
}

// predatesRemedy turns an inspection failure into guidance: an atc
// that predates guided setup is replaced by hand, since nothing infers
// its protocol or channel; other failures are reported as they are.
func predatesRemedy(target, executable string, err error) error {
	if errors.Is(err, ErrPredatesSetup) {
		return fmt.Errorf("the atc at %s on %s predates guided setup and cannot be inspected; update it there by hand (`%s upgrade`, or `%s upgrade --dev` for a dev installation) and restart its server, then rerun", executable, target, executable, executable)
	}
	return err
}

// afterInstall distinguishes an installation that succeeded from the
// startup or configuration that failed after it: the executable stays
// (startup may already have migrated the database), and the recovery is
// named on the target.
func afterInstall(target, installed, executable string, err error) error {
	if installed == "" {
		return err
	}
	return fmt.Errorf("atc %s is installed at %s on %s, but the server could not be made ready: %w\nrecover on %s with `%s server restart`, or `%s server logs` for the cause, then rerun", installed, executable, target, err, target, executable, executable)
}

// VerifyHealth is the live readiness proof: the bootstrapped endpoint,
// token, and protocol, from this machine, over the tailnet. Setup.Verify
// defaults to it; the CLI holds it in a seam variable for its tests.
func VerifyHealth(ctx context.Context, b Bootstrap) (api.Health, error) {
	return api.NewClient(b.URL, b.Token, version.String(), nil).Health(ctx)
}

// readinessFailure names why the bootstrapped endpoint is not usable
// from here, and what to do.
func readinessFailure(target string, b Bootstrap, err error) error {
	var problem *api.Problem
	if !errors.As(err, &problem) {
		return fmt.Errorf("the server on %s is up at %s but this machine cannot reach it: %w\nthis machine must be on the same tailnet (check `tailscale status` here), then rerun", target, b.URL, err)
	}
	switch {
	case problem.Code == api.CodeProtocolMismatch:
		return fmt.Errorf("the server at %s answers on %s, not this atc's protocol %d; rerun the remote command", b.URL, protocolText(problem.ServerProtocol), api.Protocol)
	case problem.Status == http.StatusUnauthorized:
		return fmt.Errorf("the server at %s rejected the token its bootstrap issued; rerun the remote command", b.URL)
	}
	return fmt.Errorf("the server at %s is not ready: %w", b.URL, err)
}

func protocolText(protocol int) string {
	if protocol == 0 {
		return "no protocol"
	}
	return fmt.Sprintf("protocol %d", protocol)
}

// say writes a user-facing message; a failed write to the user's own
// terminal has no better remedy than the message it would have carried.
func say(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, format, args...)
}
