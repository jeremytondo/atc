package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func noEnv(string) (string, bool) { return "", false }

func env(pairs map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := pairs[key]
		return value, ok
	}
}

func write(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestDefaultsWhenFileAbsent(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "config.toml"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 7331 || cfg.Bind != "127.0.0.1" {
		t.Errorf("defaults = %+v, want port 7331 bind 127.0.0.1", cfg)
	}
}

func TestFileOverridesDefaults(t *testing.T) {
	cfg, err := Load(write(t, "port = 9000\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != 9000 {
		t.Errorf("port = %d, want 9000 from file", cfg.Port)
	}
	if cfg.Bind != "127.0.0.1" {
		t.Errorf("bind = %q, want untouched default", cfg.Bind)
	}
}

func TestEnvBeatsFile(t *testing.T) {
	cfg, err := Load(write(t, "port = 9000\nbind = \"0.0.0.0\"\n"),
		env(map[string]string{"ATC_PORT": "9001", "ATC_BIND": "::1"}))
	if err != nil {
		t.Fatal(err)
	}
	want := Config{Port: 9001, Bind: "::1", TailscaleExecutable: "tailscale", WebhooksPort: 443, DocumentsPort: 7332}
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("Load mismatch (-want +got):\n%s", diff)
	}
}

func TestUnknownKeyRefusesStartup(t *testing.T) {
	for name, content := range map[string]string{
		"typo":                "prot = 8080\n",
		"legacy camelCase":    "tailscaleExecutable = \"tailscale\"\n",
		"unexpected spelling": "tail_scale = true\n",
	} {
		if _, err := Load(write(t, content), noEnv); err == nil {
			t.Errorf("%s: Load succeeded, want unknown-key error", name)
		}
	}
	_, err := Load(write(t, "prot = 8080\n"), noEnv)
	if err == nil || !strings.Contains(err.Error(), "prot") {
		t.Errorf("Load = %v, want error naming the unknown key", err)
	}
}

func TestTailscaleKeys(t *testing.T) {
	cfg, err := Load(write(t, "tailscale = true\ntailscale_executable = \"/opt/bin/tailscale\"\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Tailscale || cfg.TailscaleExecutable != "/opt/bin/tailscale" {
		t.Errorf("cfg = %+v, want file values applied", cfg)
	}
	cfg, err = Load(write(t, "tailscale = true\n"),
		env(map[string]string{"ATC_TAILSCALE": "false", "ATC_TAILSCALE_EXECUTABLE": "/env/tailscale"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Tailscale || cfg.TailscaleExecutable != "/env/tailscale" {
		t.Errorf("cfg = %+v, want env to beat file", cfg)
	}
	if cfg := Default(); cfg.Tailscale || cfg.TailscaleExecutable != "tailscale" {
		t.Errorf("defaults = %+v, want disabled with PATH-name executable", cfg)
	}
}

// Webhooks default off on the Funnel default port; file and environment
// set both, environment winning.
func TestWebhookKeys(t *testing.T) {
	if cfg := Default(); cfg.Webhooks || cfg.WebhooksPort != 443 {
		t.Errorf("defaults = %+v, want webhooks off on port 443", cfg)
	}
	cfg, err := Load(write(t, "webhooks = true\nwebhooks_port = 8443\n"), noEnv)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Webhooks || cfg.WebhooksPort != 8443 {
		t.Errorf("cfg = %+v, want file values applied", cfg)
	}
	cfg, err = Load(write(t, "webhooks = true\n"),
		env(map[string]string{"ATC_WEBHOOKS": "false", "ATC_WEBHOOKS_PORT": "10000"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Webhooks || cfg.WebhooksPort != 10000 {
		t.Errorf("cfg = %+v, want env to beat file", cfg)
	}
}

func TestDocumentsPortKey(t *testing.T) {
	if cfg := Default(); cfg.DocumentsPort != 7332 {
		t.Errorf("defaults = %+v, want documents on port 7332", cfg)
	}
	cfg, err := Load(write(t, "documents_port = 9000\n"), env(map[string]string{"ATC_DOCUMENTS_PORT": "9001"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DocumentsPort != 9001 {
		t.Errorf("cfg = %+v, want env to beat file", cfg)
	}
	// The API and the document origin are distinct origins by port; an
	// ephemeral 0 on either side is allowed for tests.
	if _, err := Load(write(t, "port = 9000\ndocuments_port = 9000\n"), noEnv); err == nil {
		t.Error("documents_port equal to port passed")
	}
	if _, err := Load(write(t, "port = 0\ndocuments_port = 0\n"), noEnv); err != nil {
		t.Errorf("both ephemeral: %v, want nil", err)
	}
}

// Sharing a Tailscale port is a conflict only when both exposures run.
func TestValidateExposure(t *testing.T) {
	cfg := Config{Port: 443, WebhooksPort: 443}
	if err := cfg.ValidateExposure(true, true); err == nil {
		t.Error("both exposures on one port passed")
	}
	for _, tc := range [][2]bool{{true, false}, {false, true}, {false, false}} {
		if err := cfg.ValidateExposure(tc[0], tc[1]); err != nil {
			t.Errorf("tailnet=%v webhooks=%v: %v, want nil", tc[0], tc[1], err)
		}
	}
	if err := (Config{Port: 7331, WebhooksPort: 443, DocumentsPort: 7332}).ValidateExposure(true, true); err != nil {
		t.Errorf("distinct ports: %v, want nil", err)
	}
	if err := (Config{Port: 7331, WebhooksPort: 443, DocumentsPort: 443}).ValidateExposure(true, true); err == nil {
		t.Error("webhooks and documents on one Tailscale port passed")
	}
	if err := (Config{Port: 7331, WebhooksPort: 443, DocumentsPort: 443}).ValidateExposure(true, false); err != nil {
		t.Errorf("webhooks off: %v, want nil", err)
	}
}

func TestMalformedValues(t *testing.T) {
	for name, tc := range map[string]struct {
		content string
		env     map[string]string
	}{
		"non-numeric env port":          {"", map[string]string{"ATC_PORT": "http"}},
		"port out of range":             {"port = 70000\n", nil},
		"wrong toml type":               {"port = \"7331\"\n", nil},
		"empty bind":                    {"bind = \"\"\n", nil},
		"non-boolean env":               {"", map[string]string{"ATC_TAILSCALE": "yep"}},
		"empty executable":              {"tailscale_executable = \"\"\n", nil},
		"non-funnel port":               {"webhooks_port = 9443\n", nil},
		"non-numeric env webhooks port": {"", map[string]string{"ATC_WEBHOOKS_PORT": "https"}},
		"non-boolean env webhooks":      {"", map[string]string{"ATC_WEBHOOKS": "yep"}},
		"documents port out of range":   {"documents_port = 70000\n", nil},
		"non-numeric env documents":     {"", map[string]string{"ATC_DOCUMENTS_PORT": "docs"}},
	} {
		if _, err := Load(write(t, tc.content), env(tc.env)); err == nil {
			t.Errorf("%s: Load succeeded, want error", name)
		}
	}
}

// EnableTailscale edits one key and leaves everything else — comments,
// other keys, ordering — exactly as it was; an absent file is created.
func TestEnableTailscale(t *testing.T) {
	for name, tc := range map[string]struct{ before, after string }{
		"absent file":      {before: "", after: "tailscale = true\n"},
		"empty file":       {before: "", after: "tailscale = true\n"},
		"other keys":       {before: "# my server\nport = 9000\n", after: "# my server\nport = 9000\ntailscale = true\n"},
		"no final newline": {before: "port = 9000", after: "port = 9000\ntailscale = true\n"},
		"false already":    {before: "port = 9000\ntailscale=false # off\nwebhooks = true\n", after: "port = 9000\ntailscale = true\nwebhooks = true\n"},
		"true already":     {before: "tailscale = true\n", after: "tailscale = true\n"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.toml")
			if name != "absent file" {
				if err := os.WriteFile(path, []byte(tc.before), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := EnableTailscale(path); err != nil {
				t.Fatalf("EnableTailscale = %v", err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if diff := cmp.Diff(tc.after, string(got)); diff != "" {
				t.Errorf("file (-want +got):\n%s", diff)
			}
			cfg, err := Load(path, noEnv)
			if err != nil || !cfg.Tailscale {
				t.Errorf("Load after enable = %+v, %v", cfg, err)
			}
		})
	}
}

// The file's mode survives, and a symlinked config is edited through the
// link: the target changes, the link stays.
func TestEnableTailscalePreservesModeAndSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "dotfiles-config.toml")
	if err := os.WriteFile(real, []byte("port = 9000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	if err := EnableTailscale(link); err != nil {
		t.Fatalf("EnableTailscale = %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v, %v", info, err)
	}
	info, err := os.Stat(real)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Errorf("target mode = %v, %v; want 0644 kept", info.Mode(), err)
	}
	if got, _ := os.ReadFile(real); string(got) != "port = 9000\ntailscale = true\n" {
		t.Errorf("target = %q", got)
	}
}

// A dangling link is followed, never replaced: the file it names is
// created and the link keeps pointing at it.
func TestEnableTailscaleFollowsDanglingSymlink(t *testing.T) {
	dir := t.TempDir()
	link := filepath.Join(dir, "config.toml")
	if err := os.Symlink("dotfiles/config.toml", link); err != nil {
		t.Fatal(err)
	}
	if err := EnableTailscale(link); err != nil {
		t.Fatalf("EnableTailscale = %v", err)
	}
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link replaced: %v, %v", info, err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "dotfiles", "config.toml")); err != nil || string(got) != "tailscale = true\n" {
		t.Errorf("target = %q, %v", got, err)
	}
}

// A file the server would refuse is left alone rather than edited into a
// different file the server would still refuse.
func TestEnableTailscaleRefusesUnloadableFile(t *testing.T) {
	path := write(t, "porte = 9000\n")
	if err := EnableTailscale(path); err == nil || !strings.Contains(err.Error(), "does not understand") {
		t.Errorf("EnableTailscale on an invalid file = %v, want the load refusal", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "porte = 9000\n" {
		t.Errorf("invalid file was modified: %q", got)
	}
}
