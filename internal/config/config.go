// Package config loads server settings under the contract decided in
// ATC-259: precedence flags > ATC_<KEY> env > config.toml > defaults;
// snake_case keys; unknown keys in the file refuse startup so a typo'd key
// can never be silently ignored. Flags apply at the command seam in
// cmd/atc; everything below the flag level resolves here. The TOML format
// never leaks past this package.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strconv"

	"github.com/pelletier/go-toml/v2"
)

// Config is the settled server configuration. v1 is deliberately minimal.
type Config struct {
	// Port the server listens on and clients connect to. 0 means an
	// OS-assigned ephemeral port (useful in tests, not for real use: the
	// stable default is what remote clients paste into their setup).
	Port int `toml:"port"`
	// Bind address. Default loopback only; the practical non-loopback
	// value is 0.0.0.0, but binding a single non-loopback address drops
	// the loopback listener and breaks local clients. Documented, not
	// policed — any address is accepted (legacy-observed semantics).
	Bind string `toml:"bind"`
	// Tailscale supervises a loopback-fronting `tailscale serve` exposure
	// for the server's lifetime.
	Tailscale bool `toml:"tailscale"`
	// TailscaleExecutable names the tailscale CLI; a custom value is exact
	// user intent and disables the macOS app-bundle fallback.
	TailscaleExecutable string `toml:"tailscale_executable"`
}

// Default is the configuration with no file, environment, or flags present.
// Port 7331 is the stable contract port (ATC-245).
func Default() Config {
	return Config{Port: 7331, Bind: "127.0.0.1", TailscaleExecutable: "tailscale"}
}

// Load resolves the file and environment levels: defaults, overlaid with
// path's contents when the file exists, overlaid with ATC_PORT / ATC_BIND.
// lookupEnv is injected so tests control the environment (os.LookupEnv in
// production).
func Load(path string, lookupEnv func(string) (string, bool)) (Config, error) {
	cfg := Default()

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		// No file is a normal install state.
	case err != nil:
		return Config{}, err
	default:
		dec := toml.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&cfg); err != nil {
			var strict *toml.StrictMissingError
			if errors.As(err, &strict) {
				return Config{}, fmt.Errorf(
					"%s contains keys the server does not understand (fix or remove them):\n%s",
					path, strict.String())
			}
			return Config{}, fmt.Errorf("parsing %s: %w", path, err)
		}
	}

	if value, ok := lookupEnv("ATC_PORT"); ok {
		port, err := strconv.Atoi(value)
		if err != nil {
			return Config{}, fmt.Errorf("ATC_PORT=%q is not a number", value)
		}
		cfg.Port = port
	}
	if value, ok := lookupEnv("ATC_BIND"); ok {
		cfg.Bind = value
	}
	if value, ok := lookupEnv("ATC_TAILSCALE"); ok {
		enabled, err := strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("ATC_TAILSCALE=%q is not a boolean", value)
		}
		cfg.Tailscale = enabled
	}
	if value, ok := lookupEnv("ATC_TAILSCALE_EXECUTABLE"); ok {
		cfg.TailscaleExecutable = value
	}

	if cfg.Port < 0 || cfg.Port > 65535 {
		return Config{}, fmt.Errorf("port %d is outside 0-65535", cfg.Port)
	}
	if cfg.Bind == "" {
		return Config{}, errors.New("bind must not be empty")
	}
	if cfg.TailscaleExecutable == "" {
		return Config{}, errors.New("tailscale_executable must not be empty")
	}
	return cfg, nil
}
