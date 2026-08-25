package service

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/config"
)

func TestFirstRunNotice(t *testing.T) {
	linux := firstRunNotice("linux", "/home/ab/.config/systemd/user/atc.server.service")
	wantLinux := "registered atc.server (/home/ab/.config/systemd/user/atc.server.service)\n" +
		"the server now starts automatically at every login and restarts if it exits\n" +
		"undo at any time with `atc server uninstall`\n" +
		"headless machines: run `loginctl enable-linger` once so the server outlives logins\n"
	if diff := cmp.Diff(wantLinux, linux); diff != "" {
		t.Errorf("linux notice mismatch (-want +got):\n%s", diff)
	}

	darwin := firstRunNotice("darwin", "/Users/ab/Library/LaunchAgents/atc.server.plist")
	wantDarwin := "registered atc.server (/Users/ab/Library/LaunchAgents/atc.server.plist)\n" +
		"the server now starts automatically at every login and restarts if it exits\n" +
		"undo at any time with `atc server uninstall`\n"
	if diff := cmp.Diff(wantDarwin, darwin); diff != "" {
		t.Errorf("darwin notice mismatch (-want +got):\n%s", diff)
	}
}

func TestUninstallReport(t *testing.T) {
	got := uninstallReport(true, []remainingFile{
		{"config", "/home/ab/.config/atc/config.toml"},
		{"token", "/home/ab/.local/share/atc/auth-token"},
	})
	want := "uninstalled atc.server\n" +
		"left in place (uninstall never deletes data):\n" +
		"  config: /home/ab/.config/atc/config.toml\n" +
		"  token: /home/ab/.local/share/atc/auth-token\n"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("uninstallReport mismatch (-want +got):\n%s", diff)
	}

	got = uninstallReport(false, nil)
	want = "atc.server was not installed; nothing removed\n"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("empty uninstallReport mismatch (-want +got):\n%s", diff)
	}
}

func TestProbeAddr(t *testing.T) {
	for name, tc := range map[string]struct {
		bind string
		want string
	}{
		"loopback":      {"127.0.0.1", "127.0.0.1:7331"},
		"wildcard v4":   {"0.0.0.0", "127.0.0.1:7331"},
		"wildcard v6":   {"::", "127.0.0.1:7331"},
		"specific bind": {"192.168.1.20", "192.168.1.20:7331"},
	} {
		got := probeAddr(config.Config{Port: 7331, Bind: tc.bind})
		if got != tc.want {
			t.Errorf("%s: probeAddr = %q, want %q", name, got, tc.want)
		}
	}
}

func TestTailLines(t *testing.T) {
	for name, tc := range map[string]struct {
		text  string
		count int
		want  string
	}{
		"shorter than count":  {"a\nb\n", 5, "a\nb"},
		"trimmed to count":    {"a\nb\nc\nd\n", 2, "c\nd"},
		"no trailing newline": {"a\nb", 1, "b"},
	} {
		if got := tailLines(tc.text, tc.count); got != tc.want {
			t.Errorf("%s: tailLines = %q, want %q", name, got, tc.want)
		}
	}
}
