package service

// Unit rendering, inspection, and placement: pure functions plus the one
// writer that stamps them onto disk. The rendered unit execs `atc server
// run` via the absolute binary path from os.Executable(); the installing
// shell's PATH is the only environment stamped in — supervisors start
// services with a minimal environment, and without PATH the daemon could
// not resolve the tools it shells out to. The unit also records the
// launch's exposure flags (--tailscale, --webhooks; ATC-283, ATC-306) in
// its exec arguments so supervised recovery relaunches the same way; they
// are read back only for the launch that is running. Declarative
// configuration belongs in config.toml.

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/jeremytondo/atc/internal/paths"
)

// LaunchFlags are the exposure overrides one launch was started with
// (ATC-283, ATC-306). nil means the flag was not supplied and
// configuration decides; a non-nil value is an explicit override either
// way — false is an ordinary override, never a return to configuration.
type LaunchFlags struct {
	Tailscale *bool
	Webhooks  *bool
}

// inherit fills the flags the caller left unset from a running launch's
// recorded flags: a restart keeps what the current process was started
// with unless a replacement is supplied.
func (f LaunchFlags) inherit(running LaunchFlags) LaunchFlags {
	if f.Tailscale == nil {
		f.Tailscale = running.Tailscale
	}
	if f.Webhooks == nil {
		f.Webhooks = running.Webhooks
	}
	return f
}

// effective settles one exposure: the launch flag when supplied, the
// configured value otherwise.
func effective(flag *bool, configured bool) bool {
	if flag != nil {
		return *flag
	}
	return configured
}

// unitArgs is the exec line the supervisor runs. os.Executable(), no dev
// special case: registering a `go run` binary is unsupported and undetected.
// The launch flags ride here and nowhere else: the unit records how the
// running launch was started so supervised recovery relaunches it the
// same way, and lifecycle reads them back only while that launch runs.
func unitArgs(executable string, flags LaunchFlags) []string {
	args := []string{executable, "server", "run"}
	args = appendFlag(args, "tailscale", flags.Tailscale)
	args = appendFlag(args, "webhooks", flags.Webhooks)
	return args
}

// appendFlag renders one launch flag: bare for true (the shape units have
// carried since ATC-283), an explicit =false for false, nothing when the
// flag was not supplied.
func appendFlag(args []string, name string, value *bool) []string {
	switch {
	case value == nil:
		return args
	case *value:
		return append(args, "--"+name)
	default:
		return append(args, "--"+name+"=false")
	}
}

// unitLaunchFlags reads the launch flags back out of installed unit
// content. An error means the exec arguments cannot be confidently read
// (hand-edited or foreign content); callers fail loudly on it rather than
// guessing.
func unitLaunchFlags(goos, content string) (LaunchFlags, error) {
	var args []string
	var err error
	if goos == "darwin" {
		args, err = plistProgramArguments(content)
	} else {
		args, err = systemdExecStart(content)
	}
	if err != nil {
		return LaunchFlags{}, err
	}
	return flagsFromArgs(args)
}

// flagsFromArgs recognizes exactly the argument shapes renderUnit has ever
// produced: `<binary> server run` followed by each exposure flag at most
// once, bare or with an explicit boolean.
func flagsFromArgs(args []string) (LaunchFlags, error) {
	unrecognized := fmt.Errorf("installed unit has unrecognized exec arguments %q", args)
	if len(args) < 3 || args[1] != "server" || args[2] != "run" {
		return LaunchFlags{}, unrecognized
	}
	var flags LaunchFlags
	for _, arg := range args[3:] {
		flag, ok := strings.CutPrefix(arg, "--")
		if !ok {
			return LaunchFlags{}, unrecognized
		}
		name, text, explicit := strings.Cut(flag, "=")
		value := true
		if explicit {
			var err error
			if value, err = strconv.ParseBool(text); err != nil {
				return LaunchFlags{}, unrecognized
			}
		}
		var slot **bool
		switch name {
		case "tailscale":
			slot = &flags.Tailscale
		case "webhooks":
			slot = &flags.Webhooks
		default:
			return LaunchFlags{}, unrecognized
		}
		if *slot != nil {
			return LaunchFlags{}, unrecognized
		}
		*slot = &value
	}
	return flags, nil
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
// binary, environment, and launch flags. Start compares it against the
// installed file to decide whether the running daemon must be bounced
// onto it.
func renderUnit(flags LaunchFlags) (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	if runtime.GOOS == "darwin" {
		logFile, err := paths.LogFile()
		if err != nil {
			return "", err
		}
		return launchAgentPlist(unitArgs(executable, flags), logFile, unitEnv()), nil
	}
	return systemdUnit(unitArgs(executable, flags), unitEnv()), nil
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
	// Written beside and renamed into place, so the installed unit is
	// always a whole one.
	temp := unitFile + ".tmp"
	if err := os.WriteFile(temp, []byte(content), 0o644); err != nil {
		return err
	}
	return os.Rename(temp, unitFile)
}
