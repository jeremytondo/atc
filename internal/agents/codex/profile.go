package codex

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The launch profile is ATC's whole hook footprint in the user's Codex
// home: one file, selected per launch with -p, declaring the eight proven
// hook events (ATC-281) and carrying their trust hashes. The base
// config.toml is never touched, and deleting the profile removes the
// declarations and their trust alike. The file is static — per-launch
// identity travels through environment variables the hook command reads,
// so the content never varies by launch, port, or server process.

// profileName is the launch profile's name: -p atc selects
// <CODEX_HOME>/atc.config.toml.
const profileName = "atc"

// profileMarker identifies a profile ATC wrote. A file at the profile
// path without it was written by someone else and is never overwritten.
const profileMarker = "# Managed by ATC (ATC-280)."

// hookEvents are the eight proven hook events, in the evidence fixture's
// order. Codex clamps SessionEnd and Interrupt hook timeouts to three
// seconds; the other events get a bound that comfortably covers the hook
// command's own five-second curl ceiling.
var hookEvents = []struct {
	name    string
	label   string
	timeout int
}{
	{"SessionStart", "session_start", 10},
	{"UserPromptSubmit", "user_prompt_submit", 10},
	{"PreToolUse", "pre_tool_use", 10},
	{"PermissionRequest", "permission_request", 10},
	{"PostToolUse", "post_tool_use", 10},
	{"Stop", "stop", 10},
	{"Interrupt", "interrupt", 3},
	{"SessionEnd", "session_end", 3},
}

// hookCommand is the one command every event runs, through the user's
// shell (codex executes hook commands via $SHELL -lc). It is gated on the
// per-launch environment: without both variables it exits successfully
// with no side effects, so `codex -p atc` run by hand produces no ingest.
// Output and exit status are swallowed so a hook can never degrade the
// session — evidence delivery is best-effort push.
const hookCommand = `[ -n "$` + envURL + `" ] && [ -n "$` + envHeader + `" ] || exit 0; ` +
	`curl -fsS -m 5 -X POST -H 'Content-Type: application/json' -H @"$` + envHeader + `" ` +
	`--data-binary @- "$` + envURL + `" >/dev/null 2>&1 || true`

// envURL and envHeader are the per-launch environment: the ingest URL and
// the header file carrying this launch's secret. They ride the launch
// command string, shell-quoted, and are inherited by hook subprocesses.
const (
	envURL    = "ATC_CODEX_HOOK_URL"
	envHeader = "ATC_CODEX_HOOK_HEADER"
)

// CodexHome resolves the CODEX_HOME every codex TUI will use: codex's own
// variable, never an ATC setting. Always absolute — the trust-state keys
// embed the profile path, and codex resolves its home to an absolute path
// before keying trust.
func CodexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Abs(home)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// profilePath is the profile file inside the Codex home.
func profilePath(codexHome string) string {
	return filepath.Join(codexHome, profileName+".config.toml")
}

// profileContent renders the profile for a Codex home. The trust-state
// keys embed the profile's own absolute path, so content is per-home.
func profileContent(codexHome string) string {
	var b strings.Builder
	b.WriteString(profileMarker + "\n")
	b.WriteString("# ATC rewrites this file before Codex launches; deleting it removes ATC's\n")
	b.WriteString("# hook declarations and their trust state. Hooks only report for sessions\n")
	b.WriteString("# ATC launched — run by hand, they exit without side effects.\n\n")
	b.WriteString("[features]\nhooks = true\n")
	for _, event := range hookEvents {
		fmt.Fprintf(&b, "\n[[hooks.%s]]\n[[hooks.%s.hooks]]\n", event.name, event.name)
		fmt.Fprintf(&b, "type = \"command\"\ncommand = %s\ntimeout = %d\n", tomlString(hookCommand), event.timeout)
	}
	b.WriteString("\n")
	path := profilePath(codexHome)
	for _, event := range hookEvents {
		fmt.Fprintf(&b, "[hooks.state.%s]\ntrusted_hash = %s\n",
			tomlString(path+":"+event.label+":0:0"), tomlString(trustHash(event.label, hookCommand, event.timeout)))
	}
	return b.String()
}

// trustHash reproduces Codex's hook trust fingerprint (verified against a
// hash Codex itself wrote): sha256 of the compact, key-sorted JSON of the
// normalized hook identity — the event's state-key label plus the matcher
// group holding the one normalized handler. Writing it with the profile
// pre-trusts the hooks, so no "Hooks need review" prompt interrupts the
// first launch; rewriting the profile regenerates the hashes with it.
func trustHash(eventLabel, command string, timeout int) string {
	identity := map[string]any{
		"event_name": eventLabel,
		"hooks": []map[string]any{{
			"async":   false,
			"command": command,
			"timeout": timeout,
			"type":    "command",
		}},
	}
	// encoding/json marshals maps with sorted keys and no padding — the
	// canonical form Codex hashes. HTML escaping must be off: serde_json
	// writes & < > literally, and the command contains them.
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(identity); err != nil {
		panic(err)
	}
	sum := sha256.Sum256(bytes.TrimSuffix(buf.Bytes(), []byte("\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// tomlString renders a TOML basic string. The profile's values are ASCII
// without control characters, so escaping quote and backslash suffices.
func tomlString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	return `"` + s + `"`
}

// ensureProfile makes the profile at the Codex home current, rewriting a
// stale ATC-written file (trust hashes regenerate with it). A file ATC
// did not write refuses the launch — never overwritten.
func ensureProfile(codexHome string) error {
	path := profilePath(codexHome)
	expected := profileContent(codexHome)
	current, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		return err
	case string(current) == expected:
		return nil
	case !strings.Contains(string(current), profileMarker):
		return fmt.Errorf("codex profile %s exists but was not written by ATC; move it aside to launch codex through ATC", path)
	}
	if err := os.MkdirAll(codexHome, 0o700); err != nil {
		return err
	}
	// Atomic replace: a concurrent launch reads either the old profile or
	// the new one, never a torn write.
	tmp, err := os.CreateTemp(codexHome, profileName+".config.toml.tmp-*")
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	if _, err := tmp.WriteString(expected); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
