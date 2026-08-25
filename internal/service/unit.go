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

// unitEnv is the environment stamped into the unit as ordered name/value
// pairs. Supervisors start services with a minimal environment, so the unit
// carries the installing shell's PATH (without it the daemon could not
// resolve the tools it shells out to) plus any XDG overrides that move the
// paths package: the daemon must resolve the same config, token, and state
// files as the CLI that installed and health-gated it.
func unitEnv() [][2]string {
	pathEnv := os.Getenv("PATH")
	if pathEnv == "" {
		pathEnv = "/usr/local/bin:/usr/bin:/bin"
	}
	env := [][2]string{{"PATH", pathEnv}}
	for _, name := range []string{"XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME"} {
		if value := os.Getenv(name); value != "" {
			env = append(env, [2]string{name, value})
		}
	}
	return env
}

// launchAgentPlist renders the macOS unit. KeepAlive relaunches the server
// whenever it exits; launchd redirects both streams to logFile because it
// has no journal.
func launchAgentPlist(args []string, logFile string, env [][2]string) string {
	var lines strings.Builder
	for _, arg := range args {
		fmt.Fprintf(&lines, "    <string>%s</string>\n", xmlEscaper.Replace(arg))
	}
	var envLines strings.Builder
	for _, pair := range env {
		fmt.Fprintf(&envLines, "    <key>%s</key>\n    <string>%s</string>\n",
			xmlEscaper.Replace(pair[0]), xmlEscaper.Replace(pair[1]))
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
%s  </dict>
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
`, UnitName, lines.String(), envLines.String(),
		xmlEscaper.Replace(logFile), xmlEscaper.Replace(logFile))
}

// unitValueEscaper escapes a double-quoted systemd unit value: unit files
// C-unescape inside double quotes and expand % specifiers everywhere, so
// both need escaping along with the quotes themselves.
var unitValueEscaper = strings.NewReplacer(`\`, `\\`, `"`, `\"`, "%", "%%")

// systemdUnit renders the Linux unit: Restart=always with backoff, output
// left to the journal's default capture (no log file of our own).
func systemdUnit(args []string, env [][2]string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = `"` + unitValueEscaper.Replace(arg) + `"`
	}
	var envLines strings.Builder
	for _, pair := range env {
		fmt.Fprintf(&envLines, "Environment=\"%s=%s\"\n", pair[0], unitValueEscaper.Replace(pair[1]))
	}
	return fmt.Sprintf(`[Unit]
Description=ATC server

[Service]
Type=simple
ExecStart=%s
%sRestart=always
RestartSec=5

[Install]
WantedBy=default.target
`, strings.Join(quoted, " "), envLines.String())
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

// renderUnit produces the unit content for this platform from the current
// binary and environment. Start compares it against the installed file to
// decide whether the running daemon must be bounced onto it.
func renderUnit() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		logFile, err := paths.LogFile()
		if err != nil {
			return "", err
		}
		return launchAgentPlist(unitArgs(executable), logFile, unitEnv()), nil
	}
	return systemdUnit(unitArgs(executable), unitEnv()), nil
}

// writeUnit installs rendered unit content, creating the directories the
// unit depends on (launchd creates its log file, but not the directory).
func writeUnit(unitFile, content string) error {
	if runtime.GOOS == "darwin" {
		logFile, err := paths.LogFile()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(logFile), 0o700); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(unitFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(unitFile, []byte(content), 0o644)
}
