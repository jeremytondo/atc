package remote

// Discovery (ATC-325): the first thing an `atc --remote` launch learns
// about the target, from a POSIX sh script that needs no atc there —
// the platform, the atc on the non-interactive PATH, the first one in the
// places installations land, and the executable the server's unit runs,
// which need not be any of those. Output is plain key=value lines.

import (
	"fmt"
	"path"
	"strings"
)

// discoverScript is fed to `sh` on stdin. Every shell parses the
// invocation identically because it is one word; sh parses the script.
const discoverScript = `os=$(uname -s 2>/dev/null || echo unknown)
arch=$(uname -m 2>/dev/null || echo unknown)
printf 'os=%s\narch=%s\nhome=%s\n' "$os" "$arch" "$HOME"
found=$(command -v atc 2>/dev/null) && [ -n "$found" ] && printf 'path=%s\n' "$found"
for candidate in "$HOME/.local/bin/atc" /opt/homebrew/bin/atc /usr/local/bin/atc /home/linuxbrew/.linuxbrew/bin/atc; do
  [ -x "$candidate" ] && printf 'candidate=%s\n' "$candidate" && break
done
unit="${XDG_CONFIG_HOME:-$HOME/.config}/systemd/user/atc.server.service"
[ -r "$unit" ] && exe=$(sed -n 's/^ExecStart="\([^"]*\)".*/\1/p' "$unit" | head -n 1) && [ -n "$exe" ] && printf 'unit=%s\n' "$exe"
plist="$HOME/Library/LaunchAgents/atc.server.plist"
[ -r "$plist" ] && exe=$(awk '/<key>ProgramArguments<\/key>/{f=1} f && /<string>/{sub(/.*<string>/,""); sub(/<\/string>.*/,""); print; exit}' "$plist") && [ -n "$exe" ] && printf 'unit=%s\n' "$exe"
exit 0
`

// Discovery is what the script found.
type Discovery struct {
	// OS and Arch are uname's names (Linux, x86_64).
	OS, Arch string
	// Home is the remote user's home directory, where a fresh
	// installation lands.
	Home string
	// Path is the atc on the non-interactive PATH, "" when none.
	Path string
	// Candidate is the first executable in the usual installation places
	// (~/.local/bin, the Homebrew prefixes, /usr/local/bin), "" when none.
	Candidate string
	// Unit is the executable the installed server unit runs, "" when no
	// unit is installed or it cannot be read.
	Unit string
}

// Executable is the atc discovery would use: the PATH one, else the
// candidate; "" when the target has none.
func (d Discovery) Executable() string {
	if d.Path != "" {
		return d.Path
	}
	return d.Candidate
}

// Platform maps uname's names to the release targets. An unsupported
// platform is an error naming it, for the moment an installation is
// needed — a compatible atc already there is used whatever the platform.
func (d Discovery) Platform() (goos, goarch string, err error) {
	switch d.OS + "/" + d.Arch {
	case "Darwin/arm64":
		return "darwin", "arm64", nil
	case "Linux/x86_64":
		return "linux", "amd64", nil
	case "Linux/aarch64", "Linux/arm64":
		return "linux", "arm64", nil
	}
	return "", "", fmt.Errorf("atc has no release build for %s/%s (supported: darwin/arm64, linux/amd64, linux/arm64)", d.OS, d.Arch)
}

// InstallDir is where a fresh installation lands: ~/.local/bin, the
// user's own, needing no administrator.
func (d Discovery) InstallDir() string { return path.Join(d.Home, ".local", "bin") }

// parseDiscovery reads the script's lines. Only absolute executables are
// believed; the platform and home lines are required.
func parseDiscovery(target string, out []byte) (Discovery, error) {
	var d Discovery
	for line := range strings.Lines(string(out)) {
		key, value, ok := strings.Cut(strings.TrimRight(line, "\n"), "=")
		if !ok {
			continue
		}
		switch key {
		case "os":
			d.OS = value
		case "arch":
			d.Arch = value
		case "home":
			d.Home = value
		case "path":
			if path.IsAbs(value) {
				d.Path = value
			}
		case "candidate":
			if path.IsAbs(value) && d.Candidate == "" {
				d.Candidate = value
			}
		case "unit":
			if path.IsAbs(value) && d.Unit == "" {
				d.Unit = value
			}
		}
	}
	if d.OS == "" || d.Arch == "" || !path.IsAbs(d.Home) {
		return Discovery{}, fmt.Errorf("discovery on %s returned unexpected output: %q", target, lastLine(string(out)))
	}
	return d, nil
}
