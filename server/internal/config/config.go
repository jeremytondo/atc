// Package config resolves Atelier Code's user-facing settings from a layered set of
// sources: a TOML config file, environment variables, and CLI flags. Precedence
// runs flag > env > file > built-in default, so the most specific source wins.
//
// The config file is optional. Its default location is
// $XDG_CONFIG_HOME/atc/atc.toml (falling back to
// ~/.config/atc/atc.toml), overridable with the --config flag or the
// ATC_CONFIG environment variable. A missing default file is not an error;
// an explicitly requested file that is missing or malformed is.
package config

import (
	"errors"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"

	"github.com/jeremytondo/atelier-code/internal/paths"
	"github.com/jeremytondo/atelier-code/internal/server"
	"github.com/jeremytondo/atelier-code/internal/session"
	"github.com/pelletier/go-toml/v2"
)

const (
	// EnvHTTPAddr overrides the server listen address.
	EnvHTTPAddr = "ATC_HTTP_ADDR"
	// EnvConfigPath overrides the config file location.
	EnvConfigPath = "ATC_CONFIG"
	// EnvZmxBin overrides the zmx binary location.
	EnvZmxBin = "ATC_ZMX_BIN"
	// EnvDBPath overrides the SQLite state database path.
	EnvDBPath = "ATC_DB_PATH"
	// EnvActionsPath overrides the file-backed Action overlay path.
	EnvActionsPath = "ATC_ACTIONS_PATH"
	// EnvAPIToken overrides the bearer token required on the TCP listener.
	EnvAPIToken = "ATC_API_TOKEN"
)

// Config holds Atelier Code's resolved settings. The TOML tags mirror the on-disk
// file layout.
type Config struct {
	Server ServerConfig `toml:"server"`
	Log    LogConfig    `toml:"log"`
	Paths  PathsConfig  `toml:"paths"`
	Store  StoreConfig  `toml:"store"`
	Zmx    ZmxConfig    `toml:"zmx"`
	Auth   AuthConfig   `toml:"auth"`
	// ActionsPath is the JSON file where API-managed Action overlays live.
	ActionsPath string `toml:"-"`
	// Environments is the selectable set of launch wrappers. Configured
	// environments overlay the built-in host-login-shell default.
	Environments session.EnvironmentRegistry `toml:"environments"`
}

type ZmxConfig struct {
	// Bin is the zmx binary location; "zmx" resolves it from PATH.
	Bin string `toml:"bin"`
}

type AuthConfig struct {
	// Token is the bearer token required on the TCP listener. Empty disables TCP
	// authentication (the owner-only Unix socket is always trusted).
	Token string `toml:"token"`
}

type ServerConfig struct {
	HTTPAddr string `toml:"http_addr"`
}

type LogConfig struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
}

type PathsConfig struct {
	// ControlDir overrides where the service keeps its socket, PID, and log
	// files. Empty means resolve from the environment (see internal/paths).
	ControlDir string `toml:"control_dir"`
}

type StoreConfig struct {
	// DBPath is the SQLite state database path. Empty means resolve from the
	// environment (see internal/paths).
	DBPath string `toml:"db_path"`
}

// Defaults returns the baseline configuration used before any source overrides.
func Defaults() Config {
	return Config{
		Server: ServerConfig{HTTPAddr: server.DefaultHTTPAddr},
		Log:    LogConfig{Level: "info", Format: "text"},
		Paths:  PathsConfig{ControlDir: ""},
		Store:  StoreConfig{DBPath: ""},
		Zmx:    ZmxConfig{Bin: "zmx"},
	}
}

// DefaultEnvironments is the built-in environment registry. It is always
// present unless explicitly replaced by a future config mode.
func DefaultEnvironments() session.EnvironmentRegistry {
	return session.EnvironmentRegistry{
		session.DefaultEnvironmentName: {
			Kind:        session.EnvironmentKindHostLoginShell,
			Label:       "Host login shell",
			Description: "Run through the host user's login-interactive shell",
		},
	}
}

// Options carries the CLI- and environment-derived inputs Load needs to resolve
// configuration. Env defaults to os.Getenv when nil.
type Options struct {
	// ConfigPath is the --config flag value; ConfigChanged reports whether the
	// flag was set explicitly.
	ConfigPath    string
	ConfigChanged bool

	// HTTPAddr is the --http-addr flag value; HTTPAddrChanged reports whether
	// the flag was set explicitly.
	HTTPAddr        string
	HTTPAddrChanged bool

	Env func(string) string
}

// Load resolves configuration from file, environment, and flags in increasing
// order of precedence, then validates the result.
func Load(opts Options) (Config, error) {
	env := opts.Env
	if env == nil {
		env = os.Getenv
	}

	cfg := Defaults()

	// 1. File (lowest precedence above defaults).
	path, explicit := resolveConfigPath(opts, env)
	cfg.ActionsPath = defaultActionsPath(path, env)
	if path != "" {
		data, err := os.ReadFile(path)
		switch {
		case err == nil:
			if err := rejectLegacyActionTables(data); err != nil {
				return Config{}, err
			}
			if err := toml.Unmarshal(data, &cfg); err != nil {
				return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
			}
		case errors.Is(err, os.ErrNotExist) && !explicit:
			// A missing default config file is fine; keep defaults.
		default:
			return Config{}, fmt.Errorf("read config file %s: %w", path, err)
		}
	}

	// 2. Environment overrides.
	if v := env(EnvHTTPAddr); v != "" {
		cfg.Server.HTTPAddr = v
	}
	if v := env(EnvZmxBin); v != "" {
		cfg.Zmx.Bin = v
	}
	if v := env(EnvDBPath); v != "" {
		cfg.Store.DBPath = v
	}
	if v := env(EnvActionsPath); v != "" {
		cfg.ActionsPath = v
	}
	if v := env(EnvAPIToken); v != "" {
		cfg.Auth.Token = v
	}

	// 3. Flag overrides (highest precedence).
	if opts.HTTPAddrChanged {
		cfg.Server.HTTPAddr = opts.HTTPAddr
	}

	cfg.Environments = mergeEnvironments(DefaultEnvironments(), cfg.Environments)
	if cfg.Store.DBPath == "" {
		cfg.Store.DBPath = paths.DBPathForEnv(paths.EnvLookup(env))
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// resolveConfigPath picks the config file path and reports whether it was
// explicitly requested (via flag or env), which controls whether a missing file
// is an error.
func resolveConfigPath(opts Options, env func(string) string) (path string, explicit bool) {
	if opts.ConfigChanged && opts.ConfigPath != "" {
		return opts.ConfigPath, true
	}
	if v := env(EnvConfigPath); v != "" {
		return v, true
	}
	return defaultConfigPath(env), false
}

func defaultConfigPath(env func(string) string) string {
	if dir := env("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, "atc", "atc.toml")
	}
	if home := env("HOME"); home != "" {
		return filepath.Join(home, ".config", "atc", "atc.toml")
	}
	return ""
}

func defaultActionsPath(configPath string, env func(string) string) string {
	if configPath == "" {
		if dir := env("TMPDIR"); dir != "" {
			return filepath.Join(dir, "atc", "actions.json")
		}
		return filepath.Join(os.TempDir(), "atc", "actions.json")
	}
	return filepath.Join(filepath.Dir(configPath), "actions.json")
}

func rejectLegacyActionTables(data []byte) error {
	var raw map[string]any
	if err := toml.Unmarshal(data, &raw); err != nil {
		return nil
	}
	if _, ok := raw["agents"]; ok {
		return fmt.Errorf("invalid config: [agents] has been renamed to [actions]")
	}
	if _, ok := raw["actions"]; ok {
		return fmt.Errorf("invalid config: actions are now managed in actions.json or through the API")
	}
	return nil
}

func (c Config) validate() error {
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("invalid log level %q: want debug, info, warn, or error", c.Log.Level)
	}
	switch c.Log.Format {
	case "text", "json":
	default:
		return fmt.Errorf("invalid log format %q: want text or json", c.Log.Format)
	}
	if err := validateTCPAddr(c.Server.HTTPAddr); err != nil {
		return err
	}
	if err := c.Environments.Validate(); err != nil {
		return err
	}
	return nil
}

func mergeEnvironments(base, overlay session.EnvironmentRegistry) session.EnvironmentRegistry {
	merged := make(session.EnvironmentRegistry, len(base)+len(overlay))
	maps.Copy(merged, base)
	maps.Copy(merged, overlay)
	return merged
}

func validateTCPAddr(addr string) error {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid TCP listen address %q: expected host:port: %w", addr, err)
	}
	if port == "" {
		return fmt.Errorf("invalid TCP listen address %q: missing port", addr)
	}
	return nil
}
