package service

// Unit rendering and placement: pure functions plus the one writer that
// stamps them onto disk. The rendered unit execs `atc server run` via the
// absolute binary path from os.Executable(); the installing shell's PATH is
// the only environment stamped in — supervisors start services with a
// minimal environment, and without PATH the daemon could not resolve the
// tools it shells out to. Everything else belongs in config.toml.

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jeremytondo/atc/internal/paths"
)

// unitArgs is the exec line the supervisor runs. os.Executable(), no dev
// special case: registering a `go run` binary is unsupported and undetected.
func unitArgs(executable string) []string {
	return []string{executable, "server", "run"}
}

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

// launchAgentPlist renders the macOS unit. KeepAlive relaunches the server
// whenever it exits; launchd redirects both streams to logFile because it
// has no journal.
func launchAgentPlist(args []string, logFile, pathEnv string) string {
	var lines strings.Builder
	for _, arg := range args {
		fmt.Fprintf(&lines, "    <string>%s</string>\n", xmlEscaper.Replace(arg))
	}
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
%s  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>PATH</key>
    <string>%s</string>
  </dict>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, UnitName, lines.String(), xmlEscaper.Replace(pathEnv),
		xmlEscaper.Replace(logFile), xmlEscaper.Replace(logFile))
}

// unitValueEscaper escapes a double-quoted systemd unit value: unit files
// C-unescape inside double quotes and expand % specifiers everywhere, so
// both need escaping along with the quotes themselves.
var unitValueEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")

// systemdUnit renders the Linux unit: Restart=always with backoff, output
// left to the journal's default capture (no log file of our own).
func systemdUnit(args []string, pathEnv string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = `"` + unitValueEscaper.Replace(arg) + `"`
	}
	return fmt.Sprintf(`[Unit]
Description=ATC server

[Service]
Type=simple
ExecStart=%s
Environment="PATH=%s"
Restart=always
RestartSec=5

[Install]
WantedBy=default.target
`, strings.Join(quoted, " "), unitValueEscaper.Replace(pathEnv))
}

// UnitPath is where this platform's unit file lives: the LaunchAgents
// folder on macOS, systemd's own user-unit config home on Linux (systemd's
// convention, not an atc path — hence the direct XDG_CONFIG_HOME read).
func UnitPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return unitPath(runtime.GOOS, os.Getenv("XDG_CONFIG_HOME"), home), nil
}

func unitPath(goos, xdgConfigHome, home string) string {
	if goos == "darwin" {
		return filepath.Join(home, "Library", "LaunchAgents", UnitName+".plist")
	}
	configHome := xdgConfigHome
	if configHome == "" {
		configHome = filepath.Join(home, ".config")
	}
	return filepath.Join(configHome, "systemd", "user", UnitName+".service")
}

// writeUnit renders and installs the unit for this platform. Every start
// runs it, so the unit always reflects the current binary and PATH.
func writeUnit(unitFile string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/usr/local/bin:/usr/bin:/bin"
	}
	var content string
	if runtime.GOOS == "darwin" {
		logFile, err := paths.LogFile()
		if err != nil {
			return err
		}
		// launchd creates the log file itself, but not its directory.
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			return err
		}
		content = launchAgentPlist(unitArgs(executable), logFile, pathEnv)
	} else {
		content = systemdUnit(unitArgs(executable), pathEnv)
	}
	if err := os.MkdirAll(filepath.Dir(unitFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(unitFile, []byte(content), 0o644)
}
