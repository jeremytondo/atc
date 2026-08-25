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

func TestUnitArgs(t *testing.T) {
	want := []string{"/opt/atc", "server", "run"}
	if diff := cmp.Diff(want, unitArgs("/opt/atc")); diff != "" {
		t.Errorf("unitArgs mismatch (-want +got):\n%s", diff)
	}
}
