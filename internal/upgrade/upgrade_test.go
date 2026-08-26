package upgrade

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssetName(t *testing.T) {
	for _, tt := range []struct {
		goos, goarch string
		want         string
	}{
		{"darwin", "arm64", "atc_darwin_arm64.tar.gz"},
		{"linux", "amd64", "atc_linux_amd64.tar.gz"},
		{"linux", "arm64", "atc_linux_arm64.tar.gz"},
	} {
		got, err := assetName(tt.goos, tt.goarch)
		if err != nil || got != tt.want {
			t.Errorf("assetName(%s, %s) = %q, %v, want %q", tt.goos, tt.goarch, got, err, tt.want)
		}
	}
}

// Unsupported platforms get a named diagnostic — never a bare 404.
func TestAssetNameUnsupported(t *testing.T) {
	for _, tt := range []struct{ goos, goarch string }{
		{"darwin", "amd64"},
		{"windows", "amd64"},
	} {
		_, err := assetName(tt.goos, tt.goarch)
		if err == nil || !strings.Contains(err.Error(), tt.goos+"/"+tt.goarch) {
			t.Errorf("assetName(%s, %s) = %v, want an error naming the platform", tt.goos, tt.goarch, err)
		}
	}
}

func TestTagFromLocation(t *testing.T) {
	tag, err := tagFromLocation("https://github.com/jeremytondo/atc/releases/tag/v0.1.0")
	if err != nil || tag != "v0.1.0" {
		t.Errorf("tagFromLocation() = %q, %v, want v0.1.0", tag, err)
	}
}

func TestTagFromLocationRejectsNonReleaseTargets(t *testing.T) {
	// The releases page itself is where GitHub lands when no release exists.
	for _, location := range []string{"", "https://github.com/jeremytondo/atc/releases"} {
		if tag, err := tagFromLocation(location); err == nil {
			t.Errorf("tagFromLocation(%q) = %q, want an error", location, tag)
		}
	}
}

func checksumsFor(name string, data []byte) []byte {
	return fmt.Appendf(nil, "%x  %s\n%x  other.tar.gz\n", sha256.Sum256(data), name, sha256.Sum256([]byte("other")))
}

func TestVerifyChecksum(t *testing.T) {
	archive := []byte("release bytes")
	sums := checksumsFor("atc_linux_amd64.tar.gz", archive)
	if err := verifyChecksum(archive, sums, "atc_linux_amd64.tar.gz"); err != nil {
		t.Errorf("verifyChecksum() = %v, want nil", err)
	}
	if err := verifyChecksum([]byte("tampered"), sums, "atc_linux_amd64.tar.gz"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("verifyChecksum(tampered) = %v, want a mismatch error", err)
	}
	if err := verifyChecksum(archive, sums, "atc_darwin_arm64.tar.gz"); err == nil || !strings.Contains(err.Error(), "no entry") {
		t.Errorf("verifyChecksum(missing asset) = %v, want a no-entry error", err)
	}
}

// releaseArchive builds a GoReleaser-shaped tar.gz: the binary plus a
// README, as the archives config produces.
func releaseArchive(t *testing.T, members map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, data := range members {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractBinary(t *testing.T) {
	archive := releaseArchive(t, map[string][]byte{
		"README.md": []byte("docs"),
		"atc":       []byte("the binary"),
	})
	binary, err := extractBinary(archive)
	if err != nil || string(binary) != "the binary" {
		t.Errorf("extractBinary() = %q, %v, want the binary member", binary, err)
	}
}

func TestExtractBinaryMissingMember(t *testing.T) {
	archive := releaseArchive(t, map[string][]byte{"README.md": []byte("docs")})
	if _, err := extractBinary(archive); err == nil || !strings.Contains(err.Error(), `"atc"`) {
		t.Errorf("extractBinary() = %v, want a missing-member error", err)
	}
	if _, err := extractBinary([]byte("not a gzip")); err == nil {
		t.Error("extractBinary(garbage) = nil, want an error")
	}
}

func TestStageAndPromoteReplacesTarget(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "atc")
	if err := os.WriteFile(target, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	staged, err := stage(target, []byte("new"))
	if err != nil {
		t.Fatalf("stage() = %v", err)
	}
	if filepath.Dir(staged) != dir {
		t.Errorf("staged in %s, want beside the target in %s", filepath.Dir(staged), dir)
	}
	info, err := os.Stat(staged)
	if err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("staged mode = %v, %v, want 0755", info.Mode(), err)
	}
	if err := promote(staged, target); err != nil {
		t.Fatalf("promote() = %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "new" {
		t.Errorf("target after promote = %q, %v, want the new binary", got, err)
	}
}

func TestStageUnwritableDirectoryNamesTheRemedy(t *testing.T) {
	if os.Getuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })
	_, err := stage(filepath.Join(dir, "atc"), []byte("new"))
	if err == nil || !strings.Contains(err.Error(), "never invokes sudo") {
		t.Errorf("stage() = %v, want the unwritable-directory diagnostic", err)
	}
}

func TestDecideRestart(t *testing.T) {
	for _, tt := range []struct {
		mode        RestartMode
		interactive bool
		want        restartAction
	}{
		{RestartAlways, false, actionRestart},
		{RestartAlways, true, actionRestart},
		{RestartNever, true, actionSkip},
		{RestartNever, false, actionSkip},
		{RestartAsk, true, actionAsk},
		// Headless with no flag never restarts: interruption is not
		// provably cheap.
		{RestartAsk, false, actionSkip},
	} {
		if got := decideRestart(tt.mode, tt.interactive); got != tt.want {
			t.Errorf("decideRestart(%v, %v) = %v, want %v", tt.mode, tt.interactive, got, tt.want)
		}
	}
}

func TestPromptYesDefaultsYes(t *testing.T) {
	for input, want := range map[string]bool{
		"\n":    true,
		"y\n":   true,
		"YES\n": true,
		"n\n":   false,
		"no\n":  false,
		"":      false, // closed stdin cannot consent
	} {
		var out strings.Builder
		if got := promptYes(strings.NewReader(input), &out, "restart? "); got != want {
			t.Errorf("promptYes(%q) = %v, want %v", input, got, want)
		}
		if out.String() != "restart? " {
			t.Errorf("prompt output = %q", out.String())
		}
	}
}

func TestMessages(t *testing.T) {
	if got := upToDateMessage("v0.1.1"); got != "atc v0.1.1 is already up to date" {
		t.Errorf("upToDateMessage() = %q", got)
	}
	// The walk-back from dev to production states both versions plainly.
	if got := replacedMessage("v0.1.2-dev.3cf5189", "v0.1.1", "/home/u/.local/bin/atc"); got != "replaced v0.1.2-dev.3cf5189 with v0.1.1 at /home/u/.local/bin/atc" {
		t.Errorf("replacedMessage() = %q", got)
	}
	if got := replacedMessage("v0.1.1", "v0.1.1", "/b/atc"); !strings.HasPrefix(got, "reinstalled v0.1.1") {
		t.Errorf("replacedMessage(same) = %q, want a reinstall report", got)
	}
	if got := staleServerLine("v0.1.0"); !strings.Contains(got, "v0.1.0") || !strings.Contains(got, "atc server restart") || !strings.Contains(got, "--restart") {
		t.Errorf("staleServerLine() = %q, want the version and both remedies", got)
	}
	if got := staleServerLine(""); !strings.Contains(got, "an unknown version") {
		t.Errorf("staleServerLine(\"\") = %q", got)
	}
	prompt := restartPrompt("v0.1.0")
	if !strings.Contains(prompt, "terminals persist") || !strings.Contains(prompt, "[Y/n]") {
		t.Errorf("restartPrompt() = %q, want the risk summary and a default-yes prompt", prompt)
	}
}
