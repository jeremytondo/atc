// Package paths resolves where ATC keeps its files: one XDG rule on every
// platform, macOS included, honoring the XDG_* environment overrides. This
// is the legacy convention adopted by ATC-259; nothing else in the codebase
// may derive these locations independently.
//
//	config  $XDG_CONFIG_HOME/atc/config.toml  (~/.config/atc/config.toml)
//	data    $XDG_DATA_HOME/atc/auth-token     (~/.local/share/atc/auth-token)
//	state   $XDG_STATE_HOME/atc/atc.log       (~/.local/state/atc/atc.log)
package paths

import (
	"os"
	"path/filepath"
)

// ConfigFile is the TOML configuration file the server reads.
func ConfigFile() (string, error) {
	return resolve("XDG_CONFIG_HOME", []string{".config"}, "config.toml")
}

// AuthTokenFile is the bearer-token credential file.
func AuthTokenFile() (string, error) {
	return resolve("XDG_DATA_HOME", []string{".local", "share"}, "auth-token")
}

// LogFile is the server's JSON-lines log.
func LogFile() (string, error) {
	return resolve("XDG_STATE_HOME", []string{".local", "state"}, "atc.log")
}

func resolve(envVar string, fallback []string, file string) (string, error) {
	if dir := os.Getenv(envVar); dir != "" {
		return filepath.Join(dir, "atc", file), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	parts := append([]string{home}, fallback...)
	return filepath.Join(filepath.Join(parts...), "atc", file), nil
}
