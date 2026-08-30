// Package paths resolves where ATC keeps its files: one XDG rule on every
// platform, macOS included, honoring the XDG_* environment overrides. This
// is the legacy convention adopted by ATC-259; nothing else in the codebase
// may derive these locations independently.
//
//	config  $XDG_CONFIG_HOME/atc/config.toml  (~/.config/atc/config.toml)
//	data    $XDG_DATA_HOME/atc/auth-token     (~/.local/share/atc/auth-token)
//	data    $XDG_DATA_HOME/atc/atc.db         (~/.local/share/atc/atc.db)
//	state   $XDG_STATE_HOME/atc/atc.log       (~/.local/state/atc/atc.log)
//	state   $XDG_STATE_HOME/atc/terminals     (~/.local/state/atc/terminals)
//	state   $XDG_STATE_HOME/atc/exits         (~/.local/state/atc/exits)
//	state   $XDG_STATE_HOME/atc/hooks         (~/.local/state/atc/hooks)
//	state   $XDG_STATE_HOME/atc/hooks/codex   (~/.local/state/atc/hooks/codex)
//
// It also owns CanonicalDir, the one rule for canonicalizing user-supplied
// directories (ATC-256) — project identity and CLI project resolution must
// agree on it exactly.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

// CanonicalDir resolves path to the canonical form ATC stores and compares
// (ATC-256): absolute, cleaned, symlinks resolved. It fails when the path
// does not exist or is not a directory (symlink resolution already
// requires existence; this is one check, not two).
func CanonicalDir(path string) (string, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", path)
	}
	return resolved, nil
}

// ConfigFile is the TOML configuration file the server reads.
func ConfigFile() (string, error) {
	return resolve("XDG_CONFIG_HOME", []string{".config"}, "config.toml")
}

// AuthTokenFile is the bearer-token credential file.
func AuthTokenFile() (string, error) {
	return resolve("XDG_DATA_HOME", []string{".local", "share"}, "auth-token")
}

// LogFile is where the macOS LaunchAgent captures the supervised server's
// output (launchd has no journal, so the unit redirects both streams here).
// The server itself logs only to stderr (ATC-260); on Linux the systemd
// journal owns capture and this file is unused.
func LogFile() (string, error) {
	return resolve("XDG_STATE_HOME", []string{".local", "state"}, "atc.log")
}

// DatabaseFile is the SQLite database holding ATC-owned durable facts
// (ATC-262). It lives beside auth-token in the data dir — legacy's exact
// location.
func DatabaseFile() (string, error) {
	return resolve("XDG_DATA_HOME", []string{".local", "share"}, "atc.db")
}

// TerminalSocketDir is ATC's private zmx socket directory (ATC-251).
// Sessions created here are invisible to hand-run zmx, which is the whole
// isolation story — ATC never touches sessions outside this directory.
func TerminalSocketDir() (string, error) {
	return resolve("XDG_STATE_HOME", []string{".local", "state"}, "terminals")
}

// ExitMarkerDir holds the wrapper's exit-evidence marker files, one
// <terminal-id>.json per session (ATC-251).
func ExitMarkerDir() (string, error) {
	return resolve("XDG_STATE_HOME", []string{".local", "state"}, "exits")
}

// HookDir holds the per-launch agent hook files (ATC-255): for each
// Claude launch, <terminal-id>.json hook settings and <terminal-id>.header
// carrying the hook secret, both mode 0600.
func HookDir() (string, error) {
	return resolve("XDG_STATE_HOME", []string{".local", "state"}, "hooks")
}

// CodexHookDir holds Codex's per-launch hook files (ATC-280): one
// <terminal-id>.header per launch carrying the hook secret, mode 0600.
// Its own directory, so each agent's boot cleanup owns everything in its
// dir without knowing about the other's files.
func CodexHookDir() (string, error) {
	return resolve("XDG_STATE_HOME", []string{".local", "state"}, filepath.Join("hooks", "codex"))
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
