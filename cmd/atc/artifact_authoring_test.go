package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/authoring"
)

// fakeRuntime is a Node-shaped runtime whose node script plays npm ci,
// tsc, and vite build well enough to drive the CLI end to end.
const fakeRuntime = `#!/bin/sh
script="$1"; shift
case "$script" in
  */npm-cli.js) mkdir -p node_modules/typescript/bin node_modules/vite/bin; echo fake > node_modules/typescript/bin/tsc; echo fake > node_modules/vite/bin/vite.js ;;
  */typescript/bin/tsc) if grep -rq TYPE_ERROR src/document; then echo "src/document/index.tsx: error TS2322"; exit 1; fi ;;
  */vite/bin/vite.js) mkdir -p dist/assets; printf '<html><head></head><body>%s</body></html>' "$VITE_ATC_TITLE" > dist/index.html; cp src/document/index.tsx dist/assets/doc.txt ;;
  *) echo "unexpected script: $script" >&2; exit 2 ;;
esac
`

// installFakeRuntime points the CLI's authoring service at a fetch that
// serves the fake runtime as Node's release archive.
func installFakeRuntime(t *testing.T) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, member := range []struct {
		name, body string
		mode       int64
	}{
		{"node/bin/node", fakeRuntime, 0o755},
		{"node/lib/node_modules/npm/bin/npm-cli.js", "// npm\n", 0o755},
	} {
		_ = tw.WriteHeader(&tar.Header{Name: member.name, Typeflag: tar.TypeReg, Mode: member.mode, Size: int64(len(member.body))})
		_, _ = tw.Write([]byte(member.body))
	}
	_ = tw.Close()
	_ = gz.Close()
	archive := buf.Bytes()
	sum := sha256.Sum256(archive)
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	asset := "node-v24.21.0-" + runtime.GOOS + "-" + arch + ".tar.gz"
	original := newAuthoringService
	t.Cleanup(func() { newAuthoringService = original })
	newAuthoringService = func(opts authoring.Options) (*authoring.Service, error) {
		opts.Fetch = func(_ context.Context, url string) (io.ReadCloser, error) {
			if strings.HasSuffix(url, "/SHASUMS256.txt") {
				return io.NopCloser(strings.NewReader(hex.EncodeToString(sum[:]) + "  " + asset + "\n")), nil
			}
			return io.NopCloser(bytes.NewReader(archive)), nil
		}
		return authoring.New(opts)
	}
}

var copyIDFormat = regexp.MustCompile(`copy-[a-z2-9]{5}`)

func TestArtifactAuthoringCLI(t *testing.T) {
	startTestServer(t)
	isolateXDG(t)
	t.Setenv("XDG_DATA_HOME", filepath.Join(t.TempDir(), "data"))
	installFakeRuntime(t)

	stdout, _, err := runCLI(t, "artifact", "copies")
	if err != nil || !strings.Contains(stdout, "no working copies") {
		t.Fatalf("copies = %q, %v", stdout, err)
	}
	stdout, stderr, err := runCLI(t, "artifact", "new", "--example", "demo", "--json")
	if err != nil {
		t.Fatalf("new: %v\n%s", err, stderr)
	}
	var created struct{ ID, Title, Directory, Document string }
	if err := json.Unmarshal([]byte(stdout), &created); err != nil {
		t.Fatalf("new --json output %q: %v", stdout, err)
	}
	if !copyIDFormat.MatchString(created.ID) || created.Title != "Retry backoff, explored" || !strings.Contains(stderr, "installing the private Node") {
		t.Fatalf("new output:\n%s\n%s", stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(created.Document, "index.tsx")); err != nil {
		t.Fatalf("document directory %q: %v", created.Document, err)
	}

	if stdout, _, err := runCLI(t, "artifact", "check", created.ID); err != nil || !strings.Contains(stdout, "ok") {
		t.Fatalf("check = %q, %v", stdout, err)
	}
	stdout, _, err = runCLI(t, "artifact", "publish", created.Directory, "--link", "https://linear.app/x", "--thread", "thrd-aaaaa", "--revision", "abc", "--json")
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	var published struct {
		Artifact struct{ ID, URL string }
		Version  struct {
			Number     int
			Provenance struct{ ThreadID, Revision string }
		}
	}
	if err := json.Unmarshal([]byte(stdout), &published); err != nil {
		t.Fatalf("publish --json output %q: %v", stdout, err)
	}
	if !strings.HasPrefix(published.Artifact.ID, "artf-") || published.Version.Number != 1 || published.Version.Provenance.ThreadID != "thrd-aaaaa" || published.Version.Provenance.Revision != "abc" {
		t.Fatalf("publication = %+v", published)
	}
	stdout, _, err = runCLI(t, "artifact", "copies")
	if err != nil || !strings.Contains(stdout, created.ID) || !strings.Contains(stdout, published.Artifact.ID) {
		t.Fatalf("copies after publish:\n%s\n%v", stdout, err)
	}
	if stdout, _, err := runCLI(t, "artifact", "publish", created.ID, "--title", "Backoff, revised"); err != nil || !strings.Contains(stdout, "version 2") {
		t.Fatalf("second publish = %q, %v", stdout, err)
	}

	// A copy retrieved from version 1 revises on base 1 and conflicts
	// with the advice to move the base forward; --base does.
	stdout, _, err = runCLI(t, "artifact", "new", "--from", published.Artifact.ID+"@1")
	if err != nil {
		t.Fatalf("new --from: %v", err)
	}
	revising := copyIDFormat.FindString(stdout)
	if revising == "" || !strings.Contains(stdout, published.Artifact.ID+" (based on version 1)") {
		t.Fatalf("new --from output:\n%s", stdout)
	}
	if _, _, err := runCLI(t, "artifact", "publish", revising); err == nil || !strings.Contains(err.Error(), "409") || !strings.Contains(err.Error(), "--base") {
		t.Fatalf("stale publish = %v, want a 409 with advice", err)
	}
	if stdout, _, err := runCLI(t, "artifact", "publish", revising, "--base", "2"); err != nil || !strings.Contains(stdout, "version 3") {
		t.Fatalf("publish --base = %q, %v", stdout, err)
	}
	if stdout, _, err := runCLI(t, "artifact", "discard", revising); err != nil || !strings.Contains(stdout, "discarded "+revising) {
		t.Fatalf("discard = %q, %v", stdout, err)
	}
}
