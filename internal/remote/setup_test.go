package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

const localProtocol = 7

func boolPtr(b bool) *bool { return &b }

var (
	local = Identity{Version: "v0.3.1", Protocol: localProtocol}
	linux = Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Path: "/home/u/.local/bin/atc"}
)

// compatible is the target's atc when nothing needs doing: same protocol,
// another release, healthy exposed server.
func compatible() Inspection {
	return Inspection{
		Executable: Executable{Path: "/home/u/.local/bin/atc", Version: "v0.2.9", Channel: "stable", Protocol: localProtocol, Writable: true},
		Server:     Server{Executable: "/home/u/.local/bin/atc", Supervised: true, Responding: true, Healthy: true, Version: "v0.2.9", Protocol: localProtocol},
		Tailnet:    Tailnet{Configured: true},
	}
}

func ptr(i Inspection) *Inspection { return &i }

func with(edit func(*Inspection)) Inspection {
	i := compatible()
	edit(&i)
	return i
}

func stopped(i *Inspection) { i.Server = Server{Executable: "/home/u/.local/bin/atc"} }

// The decision matrix of the spec (ATC-325): protocol equality is the
// whole compatibility policy, the existing installation and the
// server's own executable are respected, and every refusal names its
// remedy.
func TestDecide(t *testing.T) {
	older := Executable{Path: "/home/u/.local/bin/atc", Version: "v0.2.0", Channel: "stable", Protocol: localProtocol - 1, Writable: true}
	for name, tc := range map[string]struct {
		local   Identity
		facts   facts
		want    Plan
		wantErr string
	}{
		"compatible on another release connects unchanged": {
			local: local,
			facts: facts{discovery: linux, found: ptr(compatible())},
			want:  Plan{Executable: "/home/u/.local/bin/atc", Server: compatible().Server},
		},
		"dev remote and stable local on one protocol connect": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) {
				i.Executable.Version, i.Executable.Channel = "v0.3.2-dev.abc1234", "dev"
				i.Server.Version = "v0.3.2-dev.abc1234"
			}))},
			want: Plan{Executable: "/home/u/.local/bin/atc", Server: with(func(i *Inspection) { i.Server.Version = "v0.3.2-dev.abc1234" }).Server},
		},
		"unpublished local connects to a compatible server": {
			local: Identity{Version: "devel-abc", Protocol: localProtocol},
			facts: facts{discovery: linux, found: ptr(compatible())},
			want:  Plan{Executable: "/home/u/.local/bin/atc", Server: compatible().Server},
		},
		"stopped compatible server starts on connect without approval": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(stopped))},
			want:  Plan{Executable: "/home/u/.local/bin/atc", Server: Server{Executable: "/home/u/.local/bin/atc"}},
		},
		"missing atc installs the local channel fresh": {
			local: local,
			facts: facts{discovery: Discovery{OS: "Darwin", Arch: "arm64", Home: "/Users/u"}},
			want:  Plan{Executable: "/Users/u/.local/bin/atc", Install: &Install{Channel: "stable"}, EnableTailscale: true},
		},
		"missing atc follows a dev local build": {
			local: Identity{Version: "v0.3.2-dev.abc1234", Protocol: localProtocol},
			facts: facts{discovery: Discovery{OS: "Linux", Arch: "aarch64", Home: "/home/u"}},
			want:  Plan{Executable: "/home/u/.local/bin/atc", Install: &Install{Channel: "dev"}, EnableTailscale: true},
		},
		"missing atc and an unpublished local build": {
			local:   Identity{Version: "devel-abc", Protocol: localProtocol},
			facts:   facts{discovery: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u"}},
			wantErr: "unpublished build with nothing to download",
		},
		"missing atc on an unsupported platform": {
			local:   local,
			facts:   facts{discovery: Discovery{OS: "Darwin", Arch: "x86_64", Home: "/Users/u"}},
			wantErr: "no release build for Darwin/x86_64",
		},
		"incompatible executable updates in place, running server restarts": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) {
				i.Executable = older
				i.Server.Version, i.Server.Protocol, i.Server.Healthy = "v0.2.0", localProtocol-1, false
			}))},
			want: Plan{Executable: "/home/u/.local/bin/atc", Install: &Install{Channel: "stable", Current: &older}, Restart: true,
				Server: Server{Executable: "/home/u/.local/bin/atc", Supervised: true, Responding: true, Version: "v0.2.0", Protocol: localProtocol - 1}},
		},
		"incompatible dev installation stays on dev": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) {
				i.Executable.Version, i.Executable.Channel, i.Executable.Protocol = "v0.2.1-dev.abc1234", "dev", localProtocol-1
				stopped(i)
			}))},
			want: Plan{Executable: "/home/u/.local/bin/atc",
				Install: &Install{Channel: "dev", Current: &Executable{Path: "/home/u/.local/bin/atc", Version: "v0.2.1-dev.abc1234", Channel: "dev", Protocol: localProtocol - 1, Writable: true}},
				Server:  Server{Executable: "/home/u/.local/bin/atc"}},
		},
		"compatible executable with an incompatible running server restarts only": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) {
				i.Server.Version, i.Server.Protocol, i.Server.Healthy = "v0.2.0", 0, false
			}))},
			want: Plan{Executable: "/home/u/.local/bin/atc", Restart: true,
				Server: Server{Executable: "/home/u/.local/bin/atc", Supervised: true, Responding: true, Version: "v0.2.0"}},
		},
		"supervised server that does not answer restarts": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) { i.Server = Server{Executable: "/home/u/.local/bin/atc", Supervised: true} }))},
			want:  Plan{Executable: "/home/u/.local/bin/atc", Restart: true, Server: Server{Executable: "/home/u/.local/bin/atc", Supervised: true}},
		},
		"compatible but unhealthy server restarts": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) { i.Server.Healthy = false }))},
			want:  Plan{Executable: "/home/u/.local/bin/atc", Restart: true, Server: with(func(i *Inspection) { i.Server.Healthy = false }).Server},
		},
		"unhealthy as judged by an incompatible inspector is not a verdict": {
			local: local,
			facts: facts{
				discovery: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Path: "/opt/old/atc"},
				found: ptr(with(func(i *Inspection) {
					i.Executable = Executable{Path: "/opt/old/atc", Version: "v0.2.0", Channel: "stable", Protocol: localProtocol - 1, Writable: true}
					i.Server.Healthy = false
				})),
				service: ptr(with(func(i *Inspection) { i.Server.Healthy = false })),
			},
			// The server speaks the local protocol; both inspectors report
			// it unhealthy, but the compatible one (the service's own) is
			// the one whose verdict counts.
			want: Plan{Executable: "/home/u/.local/bin/atc", Restart: true,
				Server: with(func(i *Inspection) { i.Server.Healthy = false }).Server,
				Notes:  []string{"the server on ws runs /home/u/.local/bin/atc; the atc found first is /opt/old/atc"}},
		},
		"the compatible atc found judges health when the server's own is incompatible": {
			local: local,
			facts: facts{
				discovery: linux,
				found: ptr(with(func(i *Inspection) {
					i.Server = Server{Executable: "/opt/atc/atc", Supervised: true, Responding: true, Version: "v0.3.0", Protocol: localProtocol}
				})),
				service: ptr(with(func(i *Inspection) {
					i.Executable = Executable{Path: "/opt/atc/atc", Version: "v0.2.0", Channel: "stable", Protocol: localProtocol - 1, Writable: true}
					i.Server = Server{Executable: "/opt/atc/atc", Supervised: true, Responding: true, Version: "v0.3.0", Protocol: localProtocol}
				})),
			},
			// The server speaks the local protocol but answers unhealthy
			// to the compatible inspector: it restarts onto that atc.
			want: Plan{Executable: "/home/u/.local/bin/atc", Restart: true,
				Server: Server{Executable: "/opt/atc/atc", Supervised: true, Responding: true, Version: "v0.3.0", Protocol: localProtocol},
				Notes:  []string{"the server on ws runs /opt/atc/atc; the atc found first is /home/u/.local/bin/atc"}},
		},
		"something that is not ATC on the port is refused": {
			local:   local,
			facts:   facts{discovery: linux, found: ptr(with(func(i *Inspection) { i.Server = Server{Executable: "/home/u/.local/bin/atc", Responding: true} }))},
			wantErr: "not ATC answers on ATC's port",
		},
		"restart replaces a launch flag that disabled exposure": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) { i.Tailnet.Launch = boolPtr(false) }))},
			want:  Plan{Executable: "/home/u/.local/bin/atc", Restart: true, TailscaleFlag: true, Server: compatible().Server},
		},
		"tailnet setting off with a running server enables and restarts": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) { i.Tailnet.Configured = false }))},
			want:  Plan{Executable: "/home/u/.local/bin/atc", EnableTailscale: true, Restart: true, Server: compatible().Server},
		},
		"tailnet setting off with a stopped server enables and starts": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) { i.Tailnet.Configured = false; stopped(i) }))},
			want:  Plan{Executable: "/home/u/.local/bin/atc", EnableTailscale: true, Server: Server{Executable: "/home/u/.local/bin/atc"}},
		},
		"tailscale not ready is a prerequisite refusal": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) {
				i.Tailnet = Tailnet{Configured: true, Problem: "tailscale is logged out; run `tailscale up`"}
			}))},
			wantErr: "tailscale on ws is not ready: tailscale is logged out; run `tailscale up`",
		},
		"installation off the PATH is used directly": {
			local: local,
			facts: facts{discovery: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Candidate: "/home/u/.local/bin/atc"}, found: ptr(compatible())},
			want: Plan{Executable: "/home/u/.local/bin/atc", Server: compatible().Server,
				Notes: []string{"atc on ws is at /home/u/.local/bin/atc, which is not on its non-interactive PATH; it is used directly"}},
		},
		"the server's own compatible executable wins over an incompatible PATH one": {
			local: local,
			facts: facts{
				discovery: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Path: "/opt/homebrew/bin/atc"},
				found: ptr(with(func(i *Inspection) {
					i.Executable = Executable{Path: "/opt/homebrew/Cellar/atc/0.2.0/bin/atc", Version: "v0.2.0", Channel: "stable", Protocol: localProtocol - 1, Managed: true}
				})),
				service: ptr(compatible()),
			},
			want: Plan{Executable: "/home/u/.local/bin/atc", Server: compatible().Server,
				Notes: []string{"the server on ws runs /home/u/.local/bin/atc; the atc found first is /opt/homebrew/Cellar/atc/0.2.0/bin/atc"}},
		},
		"only the server's own executable exists and is compatible": {
			local: local,
			facts: facts{discovery: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Unit: "/srv/atc/atc"}, service: ptr(with(func(i *Inspection) {
				i.Executable.Path, i.Server.Executable = "/srv/atc/atc", "/srv/atc/atc"
			}))},
			want: Plan{Executable: "/srv/atc/atc", Server: with(func(i *Inspection) { i.Server.Executable = "/srv/atc/atc" }).Server},
		},
		"a compatible atc found is used and the server restarts onto it": {
			local: local,
			facts: facts{
				discovery: linux,
				found: ptr(with(func(i *Inspection) {
					i.Executable = Executable{Path: "/home/u/.local/bin/atc", Version: "v0.3.1", Channel: "stable", Protocol: localProtocol, Writable: true}
					i.Server = Server{Executable: "/opt/atc/atc", Supervised: true, Responding: true, Version: "v0.2.0", Protocol: localProtocol - 1}
				})),
				service: ptr(with(func(i *Inspection) {
					i.Executable = Executable{Path: "/opt/atc/atc", Version: "v0.2.0", Channel: "stable", Protocol: localProtocol - 1, Writable: true}
					i.Server = Server{Executable: "/opt/atc/atc", Supervised: true, Responding: true, Version: "v0.2.0", Protocol: localProtocol - 1}
				})),
			},
			want: Plan{Executable: "/home/u/.local/bin/atc", Restart: true,
				Server: Server{Executable: "/opt/atc/atc", Supervised: true, Responding: true, Version: "v0.2.0", Protocol: localProtocol - 1},
				Notes:  []string{"the server on ws runs /opt/atc/atc; the atc found first is /home/u/.local/bin/atc"}},
		},
		"package-managed installation is never overwritten": {
			local: local,
			facts: facts{discovery: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Path: "/home/linuxbrew/.linuxbrew/bin/atc"}, found: ptr(with(func(i *Inspection) {
				i.Executable = Executable{Path: "/home/linuxbrew/.linuxbrew/Cellar/atc/0.2.0/bin/atc", Version: "v0.2.0", Channel: "stable", Protocol: localProtocol - 1, Writable: true, Managed: true}
			}))},
			wantErr: "managed by a package manager",
		},
		"unwritable installation is never bypassed": {
			local: local,
			facts: facts{discovery: Discovery{OS: "Linux", Arch: "x86_64", Home: "/home/u", Path: "/usr/local/bin/atc"}, found: ptr(with(func(i *Inspection) {
				i.Executable = Executable{Path: "/usr/local/bin/atc", Version: "v0.2.0", Channel: "stable", Protocol: localProtocol - 1}
			}))},
			wantErr: "not writable by the ssh user",
		},
		"unpublished remote build cannot be replaced": {
			local: local,
			facts: facts{discovery: linux, found: ptr(with(func(i *Inspection) {
				i.Executable = Executable{Path: "/home/u/.local/bin/atc", Version: "devel-abc", Protocol: localProtocol - 1, Writable: true}
			}))},
			wantErr: "unpublished build with nothing to download",
		},
		"update on an unsupported platform": {
			local: local,
			facts: facts{discovery: Discovery{OS: "FreeBSD", Arch: "amd64", Home: "/home/u", Path: "/home/u/.local/bin/atc"}, found: ptr(with(func(i *Inspection) {
				i.Executable.Protocol = localProtocol - 1
			}))},
			wantErr: "no release build for FreeBSD/amd64",
		},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := decide("ws", tc.local, tc.facts)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Plan (-want +got):\n%s", diff)
			}
		})
	}
}

func TestRenderPlan(t *testing.T) {
	fresh := Plan{Executable: "/home/u/.local/bin/atc", Install: &Install{Channel: "stable", Tag: "v0.3.1"}, EnableTailscale: true}
	want := "ws (linux/amd64) needs setup before the picker can open:\n" +
		"  atc: not installed; install v0.3.1 (stable) to /home/u/.local/bin/atc\n" +
		"  tailscale: enable `tailscale = true` in ATC's config.toml (unless already enabled)\n" +
		"  server: start it with /home/u/.local/bin/atc\n" +
		"Tailscale itself must already be installed and connected on ws.\n"
	if diff := cmp.Diff(want, render("ws", local, linux, fresh)); diff != "" {
		t.Errorf("fresh (-want +got):\n%s", diff)
	}
	update := Plan{Executable: "/home/u/.local/bin/atc",
		Install: &Install{Channel: "dev", Tag: "dev", Current: &Executable{Path: "/home/u/.local/bin/atc", Version: "v0.2.1-dev.abc1234", Channel: "dev", Protocol: 6}},
		Restart: true, Server: Server{Responding: true, Version: "v0.2.1-dev.abc1234", Protocol: 6},
		Notes: []string{"atc on ws is at /home/u/.local/bin/atc, which is not on its non-interactive PATH; it is used directly"}}
	want = "ws (linux/amd64) needs setup before the picker can open:\n" +
		"  atc: v0.2.1-dev.abc1234, dev, protocol 6 at /home/u/.local/bin/atc; update it to the current dev build, this atc's protocol 7\n" +
		"  server: running v0.2.1-dev.abc1234 (protocol 6); restart it so it runs /home/u/.local/bin/atc — terminals keep running, active agent turns are interrupted\n" +
		"note: atc on ws is at /home/u/.local/bin/atc, which is not on its non-interactive PATH; it is used directly\n"
	if diff := cmp.Diff(want, render("ws", local, linux, update)); diff != "" {
		t.Errorf("update (-want +got):\n%s", diff)
	}
	exposure := Plan{Executable: "/home/u/.local/bin/atc", EnableTailscale: true, Restart: true, TailscaleFlag: true, Server: Server{Responding: true, Healthy: true, Version: "v0.3.0", Protocol: 7}}
	want = "ws (linux/amd64) needs setup before the picker can open:\n" +
		"  tailscale: enable `tailscale = true` in ATC's config.toml\n" +
		"  server: running v0.3.0 (protocol 7); restart it so it exposes the API on the tailnet — terminals keep running, active agent turns are interrupted\n"
	if diff := cmp.Diff(want, render("ws", local, linux, exposure)); diff != "" {
		t.Errorf("exposure (-want +got):\n%s", diff)
	}
	unhealthy := Plan{Executable: "/home/u/.local/bin/atc", Restart: true, Server: Server{Responding: true, Version: "v0.3.0", Protocol: 7}}
	if got := render("ws", local, linux, unhealthy); !strings.Contains(got, "restart it as it does not answer its health check") {
		t.Errorf("unhealthy:\n%s", got)
	}
	hung := Plan{Executable: "/home/u/.local/bin/atc", Restart: true, Server: Server{Supervised: true}}
	if got := render("ws", local, linux, hung); !strings.Contains(got, "registered but not answering; restart it") {
		t.Errorf("hung:\n%s", got)
	}
}

func TestOlderRelease(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v0.2.9", "v0.3.0", true}, {"v0.3.0", "v0.2.9", false}, {"v0.3.0", "v0.3.0", false},
		{"v1.0.0", "v0.9.9", false}, {"v0.10.0", "v0.9.0", false}, {"dev", "v0.3.0", false}, {"v0.3.0", "devel-abc", false},
	} {
		if got := olderRelease(tc.a, tc.b); got != tc.want {
			t.Errorf("olderRelease(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

// fakeHost plays the target: it answers each remote command from its
// scripted state and records what was run and what was fed.
type fakeHost struct {
	discovery    string
	inspections  map[string]Inspection
	staged       string
	bootstrap    Bootstrap
	bootstrapErr string

	commands       [][]string
	stagedBytes    string
	promoted       []string
	removed        []string
	configured     bool
	bootstrapFlags []string
}

func (h *fakeHost) run(cmd *exec.Cmd) error {
	if len(cmd.Args) > 3 && cmd.Args[3] == "-O" {
		return nil
	}
	sep := 0
	for i, arg := range cmd.Args {
		if arg == "--" {
			sep = i
		}
	}
	words := cmd.Args[sep+2:]
	h.commands = append(h.commands, words)
	fail := func(stderr string, code int) error {
		_, _ = io.WriteString(cmd.Stderr, stderr+"\n")
		return scriptedExit(code)
	}
	switch {
	case len(words) == 1 && words[0] == "sh":
		if in, _ := io.ReadAll(cmd.Stdin); string(in) != discoverScript {
			return fail("not the discovery script", 2)
		}
		_, _ = io.WriteString(cmd.Stdout, h.discovery)
	case words[0] == "sh" && words[1] == "-c":
		in, _ := io.ReadAll(cmd.Stdin)
		h.stagedBytes = string(in)
		_, _ = io.WriteString(cmd.Stdout, h.staged+"\n")
	case words[0] == "mv":
		h.promoted = words[2:]
	case words[0] == "rm":
		h.removed = append(h.removed, words[2])
	case len(words) >= 3 && words[1] == "__remote" && words[2] == "inspect":
		insp, ok := h.inspections[words[0]]
		if !ok {
			return fail("sh: "+words[0]+": No such file or directory", 127)
		}
		if insp.Executable.Path == "" {
			return fail(`atc: unknown command "__remote" for "atc"`, 1)
		}
		_ = json.NewEncoder(cmd.Stdout).Encode(insp)
	case len(words) >= 3 && words[1] == "__remote" && words[2] == "configure":
		h.configured = true
	case len(words) >= 3 && words[1] == "__remote" && words[2] == "bootstrap":
		h.bootstrapFlags = words[3:]
		if h.bootstrapErr != "" {
			return fail(h.bootstrapErr, 1)
		}
		_ = json.NewEncoder(cmd.Stdout).Encode(h.bootstrap)
	default:
		return fail("unexpected command "+strings.Join(words, " "), 2)
	}
	return nil
}

// index is the position of the first recorded command starting with
// the given words, or -1.
func (h *fakeHost) index(words ...string) int {
	for i, command := range h.commands {
		if len(command) >= len(words) && strings.Join(command[:len(words)], " ") == strings.Join(words, " ") {
			return i
		}
	}
	return -1
}

// fakeReleases serves one candidate per channel.
type fakeReleases struct {
	tags    map[string]string
	bytes   string
	fetched []string
}

func (r *fakeReleases) Latest(_ context.Context, channel, goos, goarch string) (string, error) {
	if goos != "linux" || goarch != "amd64" {
		return "", fmt.Errorf("no build for %s/%s", goos, goarch)
	}
	tag, ok := r.tags[channel]
	if !ok {
		return "", errors.New("no channel " + channel)
	}
	return tag, nil
}

func (r *fakeReleases) Fetch(_ context.Context, tag, goos, goarch string) ([]byte, error) {
	r.fetched = append(r.fetched, tag+" "+goos+"/"+goarch)
	return []byte(r.bytes), nil
}

func newSetup(t *testing.T, host *fakeHost, releases *fakeReleases, answer string) (*Setup, *strings.Builder) {
	t.Helper()
	var out strings.Builder
	ssh := &SSH{executable: "/usr/bin/ssh", target: "ws", controlDir: t.TempDir(), run: host.run}
	s := &Setup{
		SSH: ssh, Local: local, Releases: releases,
		Stdin: strings.NewReader(answer), Stdout: &out, Stderr: io.Discard,
		Verify: func(context.Context, Bootstrap) (api.Health, error) { return api.Health{Status: "ok"}, nil },
	}
	return s, &out
}

const stagedPath = "/home/u/.local/bin/.atc-setup-abc"

var ready = Bootstrap{URL: "https://ws.tailnet.ts.net:7331", Token: "atc_secret", Version: "v0.3.1", Protocol: localProtocol}

func TestRunFreshInstallEndsInAReadyConnection(t *testing.T) {
	candidate := Inspection{Executable: Executable{Path: stagedPath, Version: "v0.3.1", Channel: "stable", Protocol: localProtocol, Writable: true}}
	host := &fakeHost{
		discovery:   "os=Linux\narch=x86_64\nhome=/home/u\n",
		inspections: map[string]Inspection{stagedPath: candidate},
		staged:      stagedPath,
		bootstrap:   ready,
	}
	releases := &fakeReleases{tags: map[string]string{"stable": "v0.3.1"}, bytes: "ELF"}
	s, out := newSetup(t, host, releases, "y\n")

	got, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run = %v\n%s", err, out)
	}
	want := Connection{Bootstrap: ready, Executable: "/home/u/.local/bin/atc"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Connection (-want +got):\n%s", diff)
	}
	if !strings.Contains(out.String(), "install v0.3.1 (stable) to /home/u/.local/bin/atc") || !strings.Contains(out.String(), "[y/N]") {
		t.Errorf("plan not shown before the question:\n%s", out)
	}
	if host.stagedBytes != "ELF" || releases.fetched[0] != "v0.3.1 linux/amd64" {
		t.Errorf("fetched %v, staged %q", releases.fetched, host.stagedBytes)
	}
	if diff := cmp.Diff([]string{stagedPath, "/home/u/.local/bin/atc"}, host.promoted); diff != "" {
		t.Errorf("promote (-want +got):\n%s", diff)
	}
	if !host.configured || len(host.bootstrapFlags) != 0 {
		t.Errorf("configured %v, bootstrap flags %v; want the setting enabled and a plain start", host.configured, host.bootstrapFlags)
	}
	// The candidate is inspected before it is promoted, and nothing but
	// the staged file is ever removed (a no-op after promotion).
	if inspected, promoted := host.index(stagedPath, "__remote", "inspect"), host.index("mv"); inspected < 0 || promoted < inspected {
		t.Errorf("inspect at %d, promote at %d", inspected, promoted)
	}
	if diff := cmp.Diff([]string{stagedPath}, host.removed); diff != "" {
		t.Errorf("removed (-want +got):\n%s", diff)
	}
}

// The answer is consumed up to its newline and nothing more: what follows
// on stdin still belongs to whoever reads it next.
func TestConfirmReadsOneLine(t *testing.T) {
	stdin := strings.NewReader("y\nfor the bootstrap")
	s := &Setup{Stdin: stdin, Stdout: io.Discard}
	if !s.confirm("? ") {
		t.Error("y not accepted")
	}
	rest, _ := io.ReadAll(stdin)
	if string(rest) != "for the bootstrap" {
		t.Errorf("confirm read ahead; remaining stdin = %q", rest)
	}
	for input, want := range map[string]bool{"YES\n": true, "yes": true, "n\n": false, "\n": false, "": false, "yeah\n": false} {
		if got := (&Setup{Stdin: strings.NewReader(input), Stdout: io.Discard}).confirm("? "); got != want {
			t.Errorf("confirm(%q) = %v, want %v", input, got, want)
		}
	}
}

func TestRunDeclinedChangesNothing(t *testing.T) {
	host := &fakeHost{discovery: "os=Linux\narch=x86_64\nhome=/home/u\n"}
	releases := &fakeReleases{tags: map[string]string{"stable": "v0.3.1"}, bytes: "ELF"}
	for _, answer := range []string{"n\n", "\n", ""} {
		host.commands = nil
		s, _ := newSetup(t, host, releases, answer)
		_, err := s.Run(context.Background())
		if !errors.Is(err, ErrDeclined) || !strings.Contains(err.Error(), "nothing was changed on ws") {
			t.Errorf("answer %q: err = %v, want ErrDeclined", answer, err)
		}
		if len(releases.fetched) != 0 || len(host.commands) != 1 {
			t.Errorf("answer %q: fetched %v, commands %v", answer, releases.fetched, host.commands)
		}
	}
}

func TestRunReadyTargetNeedsNoQuestion(t *testing.T) {
	host := &fakeHost{
		discovery:   "os=Linux\narch=x86_64\nhome=/home/u\npath=/home/u/.local/bin/atc\n",
		inspections: map[string]Inspection{"/home/u/.local/bin/atc": compatible()},
		bootstrap:   ready,
	}
	s, out := newSetup(t, host, &fakeReleases{}, "")
	got, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got.Executable != "/home/u/.local/bin/atc" || strings.Contains(out.String(), "[y/N]") || host.configured || len(host.bootstrapFlags) != 0 {
		t.Errorf("ready target: %+v, out %q, configured %v, flags %v", got, out.String(), host.configured, host.bootstrapFlags)
	}
}

func TestRunRestartsAnIncompatibleServerWithoutReinstalling(t *testing.T) {
	host := &fakeHost{
		discovery: "os=Linux\narch=x86_64\nhome=/home/u\npath=/home/u/.local/bin/atc\n",
		inspections: map[string]Inspection{"/home/u/.local/bin/atc": with(func(i *Inspection) {
			i.Server.Version, i.Server.Protocol, i.Server.Healthy = "v0.2.0", localProtocol-1, false
			i.Tailnet.Launch = boolPtr(false)
		})},
		bootstrap: ready,
	}
	releases := &fakeReleases{}
	s, out := newSetup(t, host, releases, "yes\n")
	if _, err := s.Run(context.Background()); err != nil {
		t.Fatalf("Run = %v\n%s", err, out)
	}
	if len(releases.fetched) != 0 || host.promoted != nil {
		t.Errorf("restart-only plan fetched %v / promoted %v", releases.fetched, host.promoted)
	}
	if diff := cmp.Diff([]string{"--restart", "--tailscale"}, host.bootstrapFlags); diff != "" {
		t.Errorf("bootstrap flags (-want +got):\n%s", diff)
	}
}

func TestRunRefusesAnIncompatibleCandidate(t *testing.T) {
	current := with(func(i *Inspection) {
		i.Executable.Version, i.Executable.Protocol = "v0.2.0", localProtocol-1
		stopped(i)
	})
	for name, tc := range map[string]struct {
		candidate Executable
		tag       string
		wantErr   string
	}{
		"newer than this client": {candidate: Executable{Path: stagedPath, Version: "v0.9.0", Channel: "stable", Protocol: localProtocol + 1}, tag: "v0.9.0", wantErr: "update this machine first (`atc upgrade`)"},
		"older than this client": {candidate: Executable{Path: stagedPath, Version: "v0.2.5", Channel: "stable", Protocol: localProtocol - 1}, tag: "v0.2.5", wantErr: "no compatible published stable release"},
		"wrong channel":          {candidate: Executable{Path: stagedPath, Version: "v0.3.2-dev.abc", Channel: "dev", Protocol: localProtocol}, tag: "v0.3.1", wantErr: "not the stable release resolved"},
		"not the resolved tag":   {candidate: Executable{Path: stagedPath, Version: "v0.3.0", Channel: "stable", Protocol: localProtocol}, tag: "v0.3.1", wantErr: "identifies as v0.3.0"},
		"already the latest":     {tag: "v0.2.0", wantErr: "already runs the latest stable release (v0.2.0)"},
		"would downgrade":        {tag: "v0.1.9", wantErr: "nothing is downgraded"},
		"does not run":           {tag: "v0.3.1", wantErr: "does not run on ws"},
	} {
		t.Run(name, func(t *testing.T) {
			host := &fakeHost{
				discovery:   "os=Linux\narch=x86_64\nhome=/home/u\npath=/home/u/.local/bin/atc\n",
				inspections: map[string]Inspection{"/home/u/.local/bin/atc": current},
				staged:      stagedPath,
				bootstrap:   ready,
			}
			if tc.candidate.Path != "" {
				host.inspections[tc.candidate.Path] = Inspection{Executable: tc.candidate}
			}
			releases := &fakeReleases{tags: map[string]string{"stable": tc.tag}, bytes: "ELF"}
			s, out := newSetup(t, host, releases, "y\n")
			_, err := s.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("err = %v, want containing %q\n%s", err, tc.wantErr, out)
			}
			if host.promoted != nil {
				t.Errorf("an incompatible candidate was promoted: %v", host.promoted)
			}
			if len(host.stagedBytes) > 0 && !slicesEqual(host.removed, []string{stagedPath}) {
				t.Errorf("staged candidate not removed: %v", host.removed)
			}
			if host.bootstrapFlags != nil {
				t.Error("bootstrap ran after a refused installation")
			}
		})
	}
}

func slicesEqual(a, b []string) bool { return cmp.Diff(a, b) == "" }

func TestRunReportsStartupFailureAfterInstallWithoutRollback(t *testing.T) {
	candidate := Inspection{Executable: Executable{Path: stagedPath, Version: "v0.3.1", Channel: "stable", Protocol: localProtocol}, Tailnet: Tailnet{Configured: true}}
	host := &fakeHost{
		discovery:    "os=Linux\narch=x86_64\nhome=/home/u\n",
		inspections:  map[string]Inspection{stagedPath: candidate},
		staged:       stagedPath,
		bootstrapErr: "cannot start the server: the server did not become healthy within 15s",
	}
	s, out := newSetup(t, host, &fakeReleases{tags: map[string]string{"stable": "v0.3.1"}, bytes: "ELF"}, "y\n")
	_, err := s.Run(context.Background())
	for _, want := range []string{"atc v0.3.1 is installed at /home/u/.local/bin/atc on ws", "did not become healthy", "/home/u/.local/bin/atc server restart", "server logs"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want containing %q", err, want)
		}
	}
	if host.promoted == nil || host.configured || slicesEqual(host.removed, []string{"/home/u/.local/bin/atc"}) {
		t.Errorf("promoted %v configured %v removed %v; want the installation kept and the already-enabled setting untouched", host.promoted, host.configured, host.removed)
	}
	if !strings.Contains(out.String(), "installed atc v0.3.1") {
		t.Errorf("installation not reported:\n%s", out)
	}
}

func TestRunChecksTailscaleAfterAFreshInstall(t *testing.T) {
	candidate := Inspection{Executable: Executable{Path: stagedPath, Version: "v0.3.1", Channel: "stable", Protocol: localProtocol}, Tailnet: Tailnet{Problem: "tailscale executable not found; install Tailscale from https://tailscale.com/download"}}
	host := &fakeHost{
		discovery:   "os=Linux\narch=x86_64\nhome=/home/u\n",
		inspections: map[string]Inspection{stagedPath: candidate},
		staged:      stagedPath,
		bootstrap:   ready,
	}
	s, _ := newSetup(t, host, &fakeReleases{tags: map[string]string{"stable": "v0.3.1"}, bytes: "ELF"}, "y\n")
	_, err := s.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "tailscale.com/download") || !strings.Contains(err.Error(), "atc stays installed") {
		t.Errorf("err = %v", err)
	}
	if host.promoted == nil || host.configured || host.bootstrapFlags != nil {
		t.Errorf("promoted %v configured %v bootstrap %v", host.promoted, host.configured, host.bootstrapFlags)
	}
}

func TestRunRequiresLiveReadiness(t *testing.T) {
	host := &fakeHost{
		discovery:   "os=Linux\narch=x86_64\nhome=/home/u\npath=/home/u/.local/bin/atc\n",
		inspections: map[string]Inspection{"/home/u/.local/bin/atc": compatible()},
		bootstrap:   ready,
	}
	for name, tc := range map[string]struct {
		verifyErr error
		want      string
	}{
		"unreachable": {verifyErr: errors.New("dial tcp: no route to host"), want: "same tailnet"},
		"token":       {verifyErr: &api.Problem{Status: 401, Code: api.CodeUnauthorized}, want: "rejected the token"},
		"protocol":    {verifyErr: &api.Problem{Status: 426, Code: api.CodeProtocolMismatch, ServerProtocol: 2}, want: "answers on protocol 2"},
	} {
		s, _ := newSetup(t, host, &fakeReleases{}, "")
		s.Verify = func(context.Context, Bootstrap) (api.Health, error) { return api.Health{}, tc.verifyErr }
		if _, err := s.Run(context.Background()); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want containing %q", name, err, tc.want)
		}
	}
	// A bootstrap that reports another protocol never reaches verification.
	host.bootstrap.Protocol = localProtocol + 1
	s, _ := newSetup(t, host, &fakeReleases{}, "")
	if _, err := s.Run(context.Background()); err == nil || !strings.Contains(err.Error(), "answers on protocol 8 after setup") {
		t.Errorf("bootstrap on another protocol = %v", err)
	}
}

func TestRunNamesAnAtcThatPredatesSetup(t *testing.T) {
	host := &fakeHost{
		discovery:   "os=Linux\narch=x86_64\nhome=/home/u\npath=/usr/local/bin/atc\n",
		inspections: map[string]Inspection{"/usr/local/bin/atc": {}},
	}
	s, _ := newSetup(t, host, &fakeReleases{}, "")
	_, err := s.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "predates guided setup") || !strings.Contains(err.Error(), "/usr/local/bin/atc upgrade") {
		t.Errorf("err = %v", err)
	}
}

func TestRunInspectsTheServersOwnExecutable(t *testing.T) {
	pathOne := with(func(i *Inspection) {
		i.Executable = Executable{Path: "/opt/homebrew/Cellar/atc/0.2.0/bin/atc", Version: "v0.2.0", Channel: "stable", Protocol: localProtocol - 1, Managed: true}
	})
	host := &fakeHost{
		discovery:   "os=Darwin\narch=arm64\nhome=/Users/u\npath=/opt/homebrew/bin/atc\n",
		inspections: map[string]Inspection{"/opt/homebrew/bin/atc": pathOne, "/home/u/.local/bin/atc": compatible()},
		bootstrap:   ready,
	}
	s, out := newSetup(t, host, &fakeReleases{}, "")
	got, err := s.Run(context.Background())
	if err != nil {
		t.Fatalf("Run = %v\n%s", err, out)
	}
	if got.Executable != "/home/u/.local/bin/atc" || strings.Contains(out.String(), "[y/N]") {
		t.Errorf("connection %+v; out %q", got, out.String())
	}

	// No atc on the PATH or in the usual places, but the unit names one:
	// it is inspected and used.
	host = &fakeHost{
		discovery:   "os=Linux\narch=x86_64\nhome=/home/u\nunit=/srv/atc/atc\n",
		inspections: map[string]Inspection{"/srv/atc/atc": with(func(i *Inspection) { i.Executable.Path, i.Server.Executable = "/srv/atc/atc", "/srv/atc/atc" })},
		bootstrap:   ready,
	}
	s, out = newSetup(t, host, &fakeReleases{}, "")
	if got, err := s.Run(context.Background()); err != nil || got.Executable != "/srv/atc/atc" || strings.Contains(out.String(), "[y/N]") {
		t.Errorf("unit-only target: %+v, %v\n%s", got, err, out)
	}

	// The unit names an executable that is gone: it is left alone, noted,
	// and the compatible atc found is used.
	host = &fakeHost{
		discovery: "os=Darwin\narch=arm64\nhome=/Users/u\npath=/opt/homebrew/bin/atc\n",
		inspections: map[string]Inspection{"/opt/homebrew/bin/atc": with(func(i *Inspection) {
			i.Executable = Executable{Path: "/opt/homebrew/Cellar/atc/0.3.1/bin/atc", Version: "v0.3.1", Channel: "stable", Protocol: localProtocol, Managed: true}
		})},
		bootstrap: ready,
	}
	var stderr strings.Builder
	s, _ = newSetup(t, host, &fakeReleases{}, "")
	s.Stderr = &stderr
	if got, err := s.Run(context.Background()); err != nil || got.Executable != "/opt/homebrew/Cellar/atc/0.3.1/bin/atc" {
		t.Errorf("Run = %+v, %v", got, err)
	}
	if !strings.Contains(stderr.String(), "could not be inspected") {
		t.Errorf("missing unit executable not noted: %q", stderr.String())
	}
}
