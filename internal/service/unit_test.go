package service

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestLaunchAgentPlist(t *testing.T) {
	got := launchAgentPlist(
		[]string{`/Users/a b/bin/atc<&>"`, "server", "run"},
		"/Users/ab/.local/state/atc/atc.log",
		[][2]string{
			{"PATH", `/usr/local/bin:/odd"<path>&`},
			{"XDG_STATE_HOME", "/Users/ab/state"},
		},
	)
	want := `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>atc.server</string>
  <key>ProgramArguments</key>
  <array>
    <string>/Users/a b/bin/atc&lt;&amp;&gt;&quot;</string>
    <string>server</string>
    <string>run</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>/usr/local/bin:/odd&quot;&lt;path&gt;&amp;</string>
    <key>XDG_STATE_HOME</key>
    <string>/Users/ab/state</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>/Users/ab/.local/state/atc/atc.log</string>
  <key>StandardErrorPath</key>
  <string>/Users/ab/.local/state/atc/atc.log</string>
</dict>
</plist>
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("launchAgentPlist mismatch (-want +got):\n%s", diff)
	}
}

func TestSystemdUnit(t *testing.T) {
	got := systemdUnit(
		[]string{`/home/a b/bin/100% "atc"\x`, "server", "run"},
		[][2]string{
			{"PATH", `/usr/bin:/50% "quoted"\path`},
			{"XDG_CONFIG_HOME", "/home/ab/cfg"},
		},
	)
	want := `[Unit]
Description=ATC server

[Service]
Type=simple
ExecStart="/home/a b/bin/100%% \"atc\"\\x" "server" "run"
Environment="PATH=/usr/bin:/50%% \"quoted\"\\path"
Environment="XDG_CONFIG_HOME=/home/ab/cfg"
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("systemdUnit mismatch (-want +got):\n%s", diff)
	}
}

// The daemon must resolve the same config, token, and state files as the
// CLI that installed it, so shell XDG overrides ride along with PATH; unset
// ones are omitted.
func TestUnitEnvStampsXDGOverrides(t *testing.T) {
	t.Setenv("PATH", "/usr/bin")
	t.Setenv("XDG_CONFIG_HOME", "/custom/config")
	t.Setenv("XDG_DATA_HOME", "")
	t.Setenv("XDG_STATE_HOME", "/custom/state")
	want := [][2]string{
		{"PATH", "/usr/bin"},
		{"XDG_CONFIG_HOME", "/custom/config"},
		{"XDG_STATE_HOME", "/custom/state"},
	}
	if diff := cmp.Diff(want, unitEnv()); diff != "" {
		t.Errorf("unitEnv mismatch (-want +got):\n%s", diff)
	}
}

func TestUnitPath(t *testing.T) {
	for name, tc := range map[string]struct {
		goos, xdg, home string
		want            string
	}{
		"darwin": {
			goos: "darwin", xdg: "/ignored", home: "/Users/ab",
			want: "/Users/ab/Library/LaunchAgents/atc.server.plist",
		},
		"linux with XDG_CONFIG_HOME": {
			goos: "linux", xdg: "/custom/config", home: "/home/ab",
			want: "/custom/config/systemd/user/atc.server.service",
		},
		"linux default": {
			goos: "linux", xdg: "", home: "/home/ab",
			want: "/home/ab/.config/systemd/user/atc.server.service",
		},
	} {
		if got := unitPath(tc.goos, tc.xdg, tc.home); got != tc.want {
			t.Errorf("%s: unitPath = %q, want %q", name, got, tc.want)
		}
	}
}

func boolPtr(v bool) *bool { return &v }

func TestUnitArgs(t *testing.T) {
	for name, tc := range map[string]struct {
		flags LaunchFlags
		want  []string
	}{
		"no flags":             {LaunchFlags{}, []string{"/opt/atc", "server", "run"}},
		"tailscale on":         {LaunchFlags{Tailscale: boolPtr(true)}, []string{"/opt/atc", "server", "run", "--tailscale"}},
		"tailscale off":        {LaunchFlags{Tailscale: boolPtr(false)}, []string{"/opt/atc", "server", "run", "--tailscale=false"}},
		"webhooks on":          {LaunchFlags{Webhooks: boolPtr(true)}, []string{"/opt/atc", "server", "run", "--webhooks"}},
		"both, mixed":          {LaunchFlags{Tailscale: boolPtr(false), Webhooks: boolPtr(true)}, []string{"/opt/atc", "server", "run", "--tailscale=false", "--webhooks"}},
		"both on, fixed order": {LaunchFlags{Tailscale: boolPtr(true), Webhooks: boolPtr(true)}, []string{"/opt/atc", "server", "run", "--tailscale", "--webhooks"}},
	} {
		if diff := cmp.Diff(tc.want, unitArgs("/opt/atc", tc.flags)); diff != "" {
			t.Errorf("%s: unitArgs mismatch (-want +got):\n%s", name, diff)
		}
	}
}

// The unit is the launch flags' only store, so inspection must recover
// exactly what rendering wrote — for both platforms, every flag
// combination, and args needing escaping.
func TestUnitLaunchFlagsRoundTrip(t *testing.T) {
	env := [][2]string{{"PATH", `/usr/bin:/50% "quoted"\path`}}
	for goos, tc := range map[string]struct {
		render func(args []string) string
		exe    string
	}{
		"darwin": {
			render: func(args []string) string { return launchAgentPlist(args, "/Users/ab/atc.log", env) },
			exe:    `/Users/a b/bin/atc<&>"`,
		},
		"linux": {
			render: func(args []string) string { return systemdUnit(args, env) },
			exe:    `/home/a b/bin/100% "atc"\x`,
		},
	} {
		for _, tailscale := range []*bool{nil, boolPtr(false), boolPtr(true)} {
			for _, webhooks := range []*bool{nil, boolPtr(false), boolPtr(true)} {
				flags := LaunchFlags{Tailscale: tailscale, Webhooks: webhooks}
				content := tc.render(unitArgs(tc.exe, flags))
				got, err := unitLaunchFlags(goos, content)
				if err != nil {
					t.Errorf("%s %+v: unitLaunchFlags = %v, want nil", goos, flags, err)
					continue
				}
				if diff := cmp.Diff(flags, got); diff != "" {
					t.Errorf("%s: unitLaunchFlags mismatch (-want +got):\n%s", goos, diff)
				}
			}
		}
	}
}

// Explicit boolean spellings and either flag order are accepted, since
// they are what a person would type into a hand-repaired unit and mean
// exactly one thing.
func TestFlagsFromArgsAcceptsExplicitBooleans(t *testing.T) {
	got, err := flagsFromArgs([]string{"/opt/atc", "server", "run", "--webhooks=true", "--tailscale=0"})
	if err != nil {
		t.Fatal(err)
	}
	want := LaunchFlags{Tailscale: boolPtr(false), Webhooks: boolPtr(true)}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("flagsFromArgs mismatch (-want +got):\n%s", diff)
	}
	for name, args := range map[string][]string{
		"duplicate flag":   {"/opt/atc", "server", "run", "--tailscale", "--tailscale=false"},
		"unknown flag":     {"/opt/atc", "server", "run", "--port=1"},
		"non-boolean":      {"/opt/atc", "server", "run", "--webhooks=maybe"},
		"positional":       {"/opt/atc", "server", "run", "tailscale"},
		"wrong subcommand": {"/opt/atc", "serve"},
	} {
		if _, err := flagsFromArgs(args); err == nil {
			t.Errorf("%s: flagsFromArgs(%q) = nil error, want unrecognized", name, args)
		}
	}
}

// Anything but the exact shapes renderUnit has ever produced is an error:
// lifecycle and status must fail loudly instead of guessing.
func TestUnitLaunchFlagsRejectsUnrecognizedContent(t *testing.T) {
	env := [][2]string{{"PATH", "/usr/bin"}}
	for name, tc := range map[string]struct {
		goos    string
		content string
	}{
		"darwin not xml":             {"darwin", "not a plist"},
		"darwin systemd content":     {"darwin", systemdUnit(unitArgs("/opt/atc", LaunchFlags{}), env)},
		"darwin extra argument":      {"darwin", launchAgentPlist([]string{"/opt/atc", "server", "run", "--port"}, "/l", env)},
		"darwin wrong subcommand":    {"darwin", launchAgentPlist([]string{"/opt/atc", "serve"}, "/l", env)},
		"darwin non-string argument": {"darwin", `<plist><dict><key>ProgramArguments</key><array><integer>1</integer></array></dict></plist>`},
		"darwin no ProgramArguments": {"darwin", `<plist><dict><key>Label</key><string>atc.server</string></dict></plist>`},
		"darwin stale ProgramArguments key": {"darwin", `<plist><dict><key>ProgramArguments</key><string>bogus</string><array>` +
			`<string>/opt/atc</string><string>server</string><string>run</string><string>--tailscale</string></array></dict></plist>`},
		"darwin arguments in nested dict": {"darwin", `<plist><dict><key>Nested</key><dict><key>ProgramArguments</key><array>` +
			`<string>/opt/atc</string><string>server</string><string>run</string><string>--tailscale</string></array></dict></dict></plist>`},
		"linux garbage":           {"linux", "garbage"},
		"linux plist content":     {"linux", launchAgentPlist(unitArgs("/opt/atc", LaunchFlags{}), "/l", env)},
		"linux missing ExecStart": {"linux", "[Service]\nType=simple\n"},
		"linux ExecStart outside Service": {"linux",
			"[Unit]\nExecStart=\"/opt/atc\" \"server\" \"run\" \"--tailscale\"\n[Service]\nType=simple\n"},
		"linux unquoted ExecStart":  {"linux", "[Service]\nExecStart=/opt/atc server run\n"},
		"linux extra argument":      {"linux", systemdUnit([]string{"/opt/atc", "server", "run", "--port", "1"}, env)},
		"linux flag before run":     {"linux", systemdUnit([]string{"/opt/atc", "--tailscale", "server", "run"}, env)},
		"linux bad escape":          {"linux", "[Service]\nExecStart=\"/opt/atc\\q\" \"server\" \"run\"\n"},
		"linux unterminated quote":  {"linux", "[Service]\nExecStart=\"/opt/atc\" \"server\" \"run\n"},
		"linux duplicate ExecStart": {"linux", "[Service]\nExecStart=\"/opt/atc\" \"server\" \"run\"\nExecStart=\"/opt/atc\" \"server\" \"run\"\n"},
	} {
		if _, err := unitLaunchFlags(tc.goos, tc.content); err == nil {
			t.Errorf("%s: unitLaunchFlags = nil error, want unrecognized-content error", name)
		}
	}
}
