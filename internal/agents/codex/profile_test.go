package codex

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

// The trust fingerprint must match Codex's own scheme byte for byte. The
// reference pair is a hash codex-cli 0.151.0 itself wrote for the herdr
// hook (command `bash '/home/jeremytondo/.codex/herdr-agent-state.sh'
// session`, timeout 10) — reproduced here from the recorded definition.
func TestTrustHashMatchesCodex(t *testing.T) {
	got := trustHash("session_start", "bash '/home/jeremytondo/.codex/herdr-agent-state.sh' session", 10)
	want := "sha256:86605e91c12d54413dec71e32b6258306218bf702d7f250f19fc32bcaaed1d98"
	if got != want {
		t.Errorf("trust hash = %s, want %s", got, want)
	}
	// Codex serializes with serde_json, which writes & < > literally; a
	// serializer that HTML-escapes them silently produces untrusted hooks
	// (caught live against codex-cli 0.151.0 — this command's hash was
	// verified by codex running the hook without a review prompt).
	got = trustHash("session_start", `a && b || exit 0; c >/dev/null 2>&1 <"$X"`, 10)
	want = "sha256:9479e38a0e9627661d3151e61a7e3b1b06803cb0226a4b49f9ae134b741ab9d5"
	if got != want {
		t.Errorf("special-character trust hash = %s, want %s", got, want)
	}
}

func TestProfileContent(t *testing.T) {
	home := t.TempDir()
	content := profileContent(home)

	// The document must parse as TOML and declare all eight events, each
	// with the one gated command, plus a trust hash per event keyed by
	// the profile's own absolute path.
	var raw map[string]any
	if err := toml.Unmarshal([]byte(content), &raw); err != nil {
		t.Fatalf("profile does not parse as TOML: %v", err)
	}
	features, ok := raw["features"].(map[string]any)
	if !ok || features["hooks"] != true {
		t.Errorf("profile does not enable the hooks feature: %v", raw["features"])
	}
	hooks, ok := raw["hooks"].(map[string]any)
	if !ok {
		t.Fatalf("profile has no hooks table: %v", raw)
	}
	state, ok := hooks["state"].(map[string]any)
	if !ok {
		t.Fatal("profile has no hooks.state table")
	}
	for _, event := range hookEvents {
		groups, ok := hooks[event.name].([]any)
		if !ok || len(groups) != 1 {
			t.Errorf("event %s: groups = %v", event.name, hooks[event.name])
			continue
		}
		// The parsed command must round-trip to the exact string the hash
		// was computed over — a TOML escaping slip would make codex hash a
		// different definition and silently distrust the hooks.
		handlers, _ := groups[0].(map[string]any)["hooks"].([]any)
		if len(handlers) != 1 || handlers[0].(map[string]any)["command"] != hookCommand {
			t.Errorf("event %s: parsed command diverges from hookCommand: %v", event.name, handlers)
		}
		entry, ok := state[profilePath(home)+":"+event.label+":0:0"].(map[string]any)
		if !ok {
			t.Errorf("event %s: no trust state entry", event.name)
			continue
		}
		if entry["trusted_hash"] != trustHash(event.label, hookCommand, event.timeout) {
			t.Errorf("event %s: trusted_hash = %v", event.name, entry["trusted_hash"])
		}
	}
	// Codex clamps SessionEnd and Interrupt hook timeouts to three
	// seconds; a larger declared timeout would be normalized away and the
	// written hash would no longer match Codex's own.
	for _, event := range hookEvents {
		if (event.label == "session_end" || event.label == "interrupt") && event.timeout > 3 {
			t.Errorf("event %s: timeout %d exceeds codex's clamp", event.name, event.timeout)
		}
	}
	// The command must be env-gated so a hand-run `codex -p atc` produces
	// no ingest, and must never carry a secret or URL inline.
	if !strings.Contains(content, "exit 0") || !strings.Contains(content, envURL) || !strings.Contains(content, envHeader) {
		t.Errorf("hook command is not env-gated:\n%s", content)
	}
}

func TestEnsureProfileLifecycle(t *testing.T) {
	home := t.TempDir()
	path := profilePath(home)

	// Fresh home: written, private.
	if err := ensureProfile(home); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("profile mode = %o, want 0600", info.Mode().Perm())
	}

	// Current: untouched — the same file, not an equal rewrite (rewrites
	// go through a temp-file rename, which would replace the inode).
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := ensureProfile(home); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Error("a current profile was rewritten")
	}

	// Stale but ATC-written (an older ATC's shape): rewritten.
	stale := profileMarker + "\nsomething older\n"
	if err := os.WriteFile(path, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureProfile(home); err != nil {
		t.Fatal(err)
	}
	current, _ := os.ReadFile(path)
	if string(current) != profileContent(home) {
		t.Error("stale ATC profile was not rewritten")
	}

	// Foreign: refused, never overwritten.
	foreign := "# my own profile\n[[hooks.SessionStart]]\n"
	if err := os.WriteFile(path, []byte(foreign), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := ensureProfile(home); err == nil || !strings.Contains(err.Error(), path) {
		t.Errorf("foreign profile: err = %v, want refusal naming %s", err, path)
	}
	kept, _ := os.ReadFile(path)
	if string(kept) != foreign {
		t.Error("foreign profile was modified")
	}

	// A missing home directory is created on the way.
	nested := filepath.Join(t.TempDir(), "deeper", "codex-home")
	if err := ensureProfile(nested); err != nil {
		t.Fatal(err)
	}
}
