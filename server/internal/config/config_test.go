package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jeremytondo/atelier-code/internal/paths"
	"github.com/jeremytondo/atelier-code/internal/server"
	"github.com/jeremytondo/atelier-code/internal/session"
)

// envFunc builds a lookup over a fixed map for deterministic tests.
func envFunc(m map[string]string) func(string) string {
	return func(key string) string { return m[key] }
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(Options{Env: envFunc(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.HTTPAddr != server.DefaultHTTPAddr {
		t.Errorf("http addr = %q, want %q", cfg.Server.HTTPAddr, server.DefaultHTTPAddr)
	}
	if cfg.Log.Level != "info" || cfg.Log.Format != "text" {
		t.Errorf("log = %+v, want info/text", cfg.Log)
	}
	if cfg.Paths.ControlDir != "" {
		t.Errorf("control dir = %q, want empty", cfg.Paths.ControlDir)
	}
	if want := paths.DBPathForEnv(envFunc(nil)); cfg.Store.DBPath != want {
		t.Errorf("db path = %q, want %q", cfg.Store.DBPath, want)
	}
	if want := filepath.Join(os.TempDir(), "atc", "actions.json"); cfg.ActionsPath != want {
		t.Errorf("actions path = %q, want %q", cfg.ActionsPath, want)
	}
}

func TestLoadFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	contents := "" +
		"[server]\nhttp_addr = \"127.0.0.1:9000\"\n" +
		"[log]\nlevel = \"debug\"\nformat = \"json\"\n" +
		"[paths]\ncontrol_dir = \"/var/run/atc\"\n" +
		"[store]\ndb_path = \"/var/lib/atc/atc.db\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.HTTPAddr != "127.0.0.1:9000" {
		t.Errorf("http addr = %q", cfg.Server.HTTPAddr)
	}
	if cfg.Log.Level != "debug" || cfg.Log.Format != "json" {
		t.Errorf("log = %+v", cfg.Log)
	}
	if cfg.Paths.ControlDir != "/var/run/atc" {
		t.Errorf("control dir = %q", cfg.Paths.ControlDir)
	}
	if cfg.Store.DBPath != "/var/lib/atc/atc.db" {
		t.Errorf("db path = %q", cfg.Store.DBPath)
	}
}

func TestLoadPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	if err := os.WriteFile(path, []byte("[server]\nhttp_addr = \"127.0.0.1:1111\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "file only",
			opts: Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)},
			want: "127.0.0.1:1111",
		},
		{
			name: "env over file",
			opts: Options{
				ConfigPath:    path,
				ConfigChanged: true,
				Env:           envFunc(map[string]string{EnvHTTPAddr: "127.0.0.1:2222"}),
			},
			want: "127.0.0.1:2222",
		},
		{
			name: "flag over env and file",
			opts: Options{
				ConfigPath:      path,
				ConfigChanged:   true,
				HTTPAddr:        "127.0.0.1:3333",
				HTTPAddrChanged: true,
				Env:             envFunc(map[string]string{EnvHTTPAddr: "127.0.0.1:2222"}),
			},
			want: "127.0.0.1:3333",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.opts)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Server.HTTPAddr != tt.want {
				t.Fatalf("http addr = %q, want %q", cfg.Server.HTTPAddr, tt.want)
			}
		})
	}
}

func TestLoadDBPathPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	if err := os.WriteFile(path, []byte("[store]\ndb_path = \"/from/file/atc.db\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	defaultEnv := envFunc(map[string]string{"XDG_STATE_HOME": "/state"})
	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "default",
			opts: Options{Env: defaultEnv},
			want: filepath.Join("/state", "atc", "atc.db"),
		},
		{
			name: "file over default",
			opts: Options{ConfigPath: path, ConfigChanged: true, Env: defaultEnv},
			want: "/from/file/atc.db",
		},
		{
			name: "env over file",
			opts: Options{
				ConfigPath:    path,
				ConfigChanged: true,
				Env:           envFunc(map[string]string{"XDG_STATE_HOME": "/state", EnvDBPath: "/from/env/atc.db"}),
			},
			want: "/from/env/atc.db",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.opts)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Store.DBPath != tt.want {
				t.Fatalf("db path = %q, want %q", cfg.Store.DBPath, tt.want)
			}
		})
	}
}

func TestLoadZmxBinPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	if err := os.WriteFile(path, []byte("[zmx]\nbin = \"/from/file/zmx\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "default",
			opts: Options{Env: envFunc(map[string]string{"HOME": t.TempDir()})},
			want: "zmx",
		},
		{
			name: "file over default",
			opts: Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)},
			want: "/from/file/zmx",
		},
		{
			name: "env over file",
			opts: Options{
				ConfigPath:    path,
				ConfigChanged: true,
				Env:           envFunc(map[string]string{EnvZmxBin: "/from/env/zmx"}),
			},
			want: "/from/env/zmx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.opts)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Zmx.Bin != tt.want {
				t.Fatalf("zmx bin = %q, want %q", cfg.Zmx.Bin, tt.want)
			}
		})
	}
}

func TestLoadMissingDefaultFileOK(t *testing.T) {
	// HOME points at an empty dir, so the default config path does not exist.
	home := t.TempDir()
	cfg, err := Load(Options{Env: envFunc(map[string]string{"HOME": home})})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Server.HTTPAddr != server.DefaultHTTPAddr {
		t.Errorf("http addr = %q, want default", cfg.Server.HTTPAddr)
	}
}

func TestLoadMissingExplicitFileErrors(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "nope.toml")

	if _, err := Load(Options{ConfigPath: missing, ConfigChanged: true, Env: envFunc(nil)}); err == nil {
		t.Fatal("expected error for missing --config file")
	}
	if _, err := Load(Options{Env: envFunc(map[string]string{EnvConfigPath: missing})}); err == nil {
		t.Fatal("expected error for missing ATC_CONFIG file")
	}
}

func TestLoadParseError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	if err := os.WriteFile(path, []byte("this is = not valid = toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)}); err == nil {
		t.Fatal("expected parse error")
	}
}

func TestLoadValidation(t *testing.T) {
	tests := []struct {
		name     string
		contents string
	}{
		{name: "bad level", contents: "[log]\nlevel = \"verbose\"\n"},
		{name: "bad format", contents: "[log]\nformat = \"xml\"\n"},
		{name: "bad addr", contents: "[server]\nhttp_addr = \"no-port\"\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "atc.toml")
			if err := os.WriteFile(path, []byte(tt.contents), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)}); err == nil {
				t.Fatalf("expected validation error for %s", tt.name)
			}
		})
	}
}

func TestResolveConfigPathDefault(t *testing.T) {
	xdg := "/home/u/.config"
	got := defaultConfigPath(envFunc(map[string]string{"XDG_CONFIG_HOME": xdg}))
	if want := filepath.Join(xdg, "atc", "atc.toml"); got != want {
		t.Errorf("xdg path = %q, want %q", got, want)
	}

	got = defaultConfigPath(envFunc(map[string]string{"HOME": "/home/u"}))
	if want := filepath.Join("/home/u", ".config", "atc", "atc.toml"); got != want {
		t.Errorf("home path = %q, want %q", got, want)
	}
}

func TestLoadDefaultActionsPathAndEnvironments(t *testing.T) {
	xdg := filepath.Join(t.TempDir(), "config")
	cfg, err := Load(Options{Env: envFunc(map[string]string{"XDG_CONFIG_HOME": xdg})})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(xdg, "atc", "actions.json"); cfg.ActionsPath != want {
		t.Fatalf("actions path = %q, want %q", cfg.ActionsPath, want)
	}
	env, ok := cfg.Environments["host-login-shell"]
	if !ok || env.Kind != session.EnvironmentKindHostLoginShell {
		t.Fatalf("default environments = %+v, want host-login-shell", cfg.Environments)
	}
}

func TestLoadActionsPathPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "atc.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("[log]\nlevel = \"info\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(filepath.Dir(path), "actions.json"); cfg.ActionsPath != want {
		t.Fatalf("actions path = %q, want %q", cfg.ActionsPath, want)
	}

	override := filepath.Join(t.TempDir(), "managed-actions.json")
	cfg, err = Load(Options{
		ConfigPath:    path,
		ConfigChanged: true,
		Env:           envFunc(map[string]string{EnvActionsPath: override}),
	})
	if err != nil {
		t.Fatalf("Load with env override: %v", err)
	}
	if cfg.ActionsPath != override {
		t.Fatalf("actions path = %q, want env override %q", cfg.ActionsPath, override)
	}
}

func TestLoadActionsPathFallsBackToTMPDIR(t *testing.T) {
	tmp := t.TempDir()
	cfg, err := Load(Options{Env: envFunc(map[string]string{"TMPDIR": tmp})})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if want := filepath.Join(tmp, "atc", "actions.json"); cfg.ActionsPath != want {
		t.Fatalf("actions path = %q, want %q", cfg.ActionsPath, want)
	}
}

func TestLoadRejectsActionsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	if err := os.WriteFile(path, []byte("[actions.claude]\nbin = \"claude\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)})
	if err == nil || !strings.Contains(err.Error(), "actions are now managed in actions.json") {
		t.Fatalf("Load err = %v, want actions table error", err)
	}
}

func TestLoadRejectsLegacyAgentsTable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	if err := os.WriteFile(path, []byte("[agents.codex]\nbin = \"codex\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := Load(Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)})
	if err == nil || !strings.Contains(err.Error(), "[agents] has been renamed to [actions]") {
		t.Fatalf("Load err = %v, want legacy agents table error", err)
	}
}

func TestLoadEnvironmentsFromFileOverlaysDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	contents := "" +
		"[environments.custom]\n" +
		"kind = \"host-login-shell\"\n" +
		"label = \"Custom shell\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if _, ok := cfg.Environments["host-login-shell"]; !ok {
		t.Fatalf("environments = %+v, want built-in host-login-shell retained", cfg.Environments)
	}
	if got := cfg.Environments["custom"]; got.Kind != session.EnvironmentKindHostLoginShell || got.Label != "Custom shell" {
		t.Fatalf("custom environment = %+v", got)
	}
}

func TestLoadAuthTokenPrecedence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "atc.toml")
	if err := os.WriteFile(path, []byte("[auth]\ntoken = \"from-file\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		opts Options
		want string
	}{
		{
			name: "default empty",
			opts: Options{Env: envFunc(map[string]string{"HOME": t.TempDir()})},
			want: "",
		},
		{
			name: "file over default",
			opts: Options{ConfigPath: path, ConfigChanged: true, Env: envFunc(nil)},
			want: "from-file",
		},
		{
			name: "env over file",
			opts: Options{
				ConfigPath:    path,
				ConfigChanged: true,
				Env:           envFunc(map[string]string{EnvAPIToken: "from-env"}),
			},
			want: "from-env",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := Load(tt.opts)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if cfg.Auth.Token != tt.want {
				t.Fatalf("auth token = %q, want %q", cfg.Auth.Token, tt.want)
			}
		})
	}
}
