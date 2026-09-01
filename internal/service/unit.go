package service

// Unit rendering, inspection, and placement: pure functions plus the one
// writer that stamps them onto disk. The rendered unit execs `atc server
// run` via the absolute binary path from os.Executable(); the installing
// shell's PATH is the only environment stamped in — supervisors start
// services with a minimal environment, and without PATH the daemon could
// not resolve the tools it shells out to. The unit also persists exactly
// one imperative setting: the --tailscale service override (ATC-283),
// rendered into and read back from the exec arguments, with the unit as
// its only durable store. Declarative configuration belongs in config.toml.

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/jeremytondo/atc/internal/paths"
)

// unitArgs is the exec line the supervisor runs. os.Executable(), no dev
// special case: registering a `go run` binary is unsupported and undetected.
// The tailscale service override rides here and nowhere else.
func unitArgs(executable string, tailscale bool) []string {
	args := []string{executable, "server", "run"}
	if tailscale {
		args = append(args, "--tailscale")
	}
	return args
}

// unitTailscale reads the --tailscale override back out of installed unit
// content — the read half of the contract that the unit is the override's
// only durable store. An error means the exec arguments cannot be
// confidently read (hand-edited or foreign content); callers fail loudly
// on it rather than guessing.
func unitTailscale(goos, content string) (bool, error) {
	var args []string
	var err error
	if goos == "darwin" {
		args, err = plistProgramArguments(content)
	} else {
		args, err = systemdExecStart(content)
	}
	if err != nil {
		return false, err
	}
	return overrideFromArgs(args)
}

// overrideFromArgs recognizes exactly the argument shapes renderUnit has
// ever produced: `<binary> server run` (a valid pre-feature unit, no
// override) and `<binary> server run --tailscale`.
func overrideFromArgs(args []string) (bool, error) {
	if len(args) >= 3 && args[1] == "server" && args[2] == "run" {
		switch {
		case len(args) == 3:
			return false, nil
		case len(args) == 4 && args[3] == "--tailscale":
			return true, nil
		}
	}
	return false, fmt.Errorf("installed unit has unrecognized exec arguments %q", args)
}

// plistProgramArguments extracts the ProgramArguments strings from a
// launchd plist, strictly: the key must sit in the top-level job
// dictionary (the only place launchd reads it), one array, string
// children only.
func plistProgramArguments(content string) ([]string, error) {
	decoder := xml.NewDecoder(strings.NewReader(content))
	var args []string
	found := false
	lastKey := ""
	inArguments := false
	depth := 0 // dict/array nesting; the job dictionary is depth 1
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("installed unit is not a readable plist: %w", err)
		}
		switch element := token.(type) {
		case xml.StartElement:
			if inArguments && element.Name.Local != "string" {
				return nil, fmt.Errorf("installed unit has a non-string program argument <%s>", element.Name.Local)
			}
			key := lastKey
			if depth == 1 && element.Name.Local != "key" {
				// A plist key applies only to the immediately following value.
				// Never let malformed intervening content attribute a later
				// keyless array to ProgramArguments.
				lastKey = ""
			}
			switch element.Name.Local {
			case "key":
				var key string
				if err := decoder.DecodeElement(&key, &element); err != nil {
					return nil, fmt.Errorf("installed unit is not a readable plist: %w", err)
				}
				if depth == 1 {
					lastKey = key
				}
			case "array", "dict":
				if element.Name.Local == "array" && depth == 1 && key == "ProgramArguments" {
					if found {
						return nil, errors.New("installed unit has multiple ProgramArguments arrays")
					}
					inArguments, found = true, true
				}
				depth++
			case "string":
				if inArguments {
					var value string
					if err := decoder.DecodeElement(&value, &element); err != nil {
						return nil, fmt.Errorf("installed unit is not a readable plist: %w", err)
					}
					args = append(args, value)
				}
			}
		case xml.EndElement:
			if element.Name.Local == "array" || element.Name.Local == "dict" {
				inArguments = false
				depth--
			}
		}
	}
	if !found {
		return nil, errors.New("installed unit has no ProgramArguments")
	}
	return args, nil
}

// systemdExecStart extracts and unquotes the ExecStart command line,
// accepting only the fully-double-quoted form systemdUnit renders, and
// only inside [Service] — the one section systemd executes it from.
func systemdExecStart(content string) ([]string, error) {
	value := ""
	found := false
	section := ""
	for line := range strings.Lines(content) {
		line = strings.TrimRight(line, "\n")
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if after, ok := strings.CutPrefix(line, "ExecStart="); ok {
			if section != "[Service]" {
				return nil, errors.New("installed unit has ExecStart outside [Service]")
			}
			if found {
				return nil, errors.New("installed unit has multiple ExecStart lines")
			}
			value, found = after, true
		}
	}
	if !found {
		return nil, errors.New("installed unit has no ExecStart line")
	}
	return unquoteUnitArgs(value)
}

// unquoteUnitArgs inverts systemdUnit's quoting exactly: each argument
// double-quoted with `\\`, `\"`, and `%%` escapes, joined by single
// spaces. Anything else is unrecognized.
func unquoteUnitArgs(line string) ([]string, error) {
	unrecognized := fmt.Errorf("installed unit has an unrecognized ExecStart value %q", line)
	var args []string
	for i := 0; i < len(line); {
		if line[i] != '"' {
			return nil, unrecognized
		}
		i++
		var arg strings.Builder
		closed := false
		for i < len(line) && !closed {
			switch line[i] {
			case '"':
				closed = true
				i++
			case '\\':
				if i+1 >= len(line) || (line[i+1] != '\\' && line[i+1] != '"') {
					return nil, unrecognized
				}
				arg.WriteByte(line[i+1])
				i += 2
			case '%':
				if i+1 >= len(line) || line[i+1] != '%' {
					return nil, unrecognized
				}
				arg.WriteByte('%')
				i += 2
			default:
				arg.WriteByte(line[i])
				i++
			}
		}
		if !closed {
			return nil, unrecognized
		}
		args = append(args, arg.String())
		if i < len(line) {
			if line[i] != ' ' || i+1 == len(line) {
				return nil, unrecognized
			}
			i++
		}
	}
	if len(args) == 0 {
		return nil, unrecognized
	}
	return args, nil
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
// binary, environment, and tailscale override. Start compares it against
// the installed file to decide whether the running daemon must be bounced
// onto it.
func renderUnit(tailscale bool) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		logFile, err := paths.LogFile()
		if err != nil {
			return "", err
		}
		return launchAgentPlist(unitArgs(executable, tailscale), logFile, unitEnv()), nil
	}
	return systemdUnit(unitArgs(executable, tailscale), unitEnv()), nil
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
