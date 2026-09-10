package zmx

// Installer and runtime-selection tests (ATC-324). No network and no real
// zmx: a controlled "release" is a tiny script tarred as the zmx member,
// served from an httptest server, its sums measured into a release table.
// These cover the behaviors the spec enumerates — verification, unsafe
// archives, offline reuse, concurrency, cancellation, activation gating —
// without the real assets, which the real-zmx seam exercises separately.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const testVersion = "9.9.9"

// fakeZmx is a script that answers `version` like real zmx and otherwise
// sleeps, so it can stand in as a session daemon's executable too.
func fakeZmx(version string) []byte {
	return []byte("#!/bin/sh\nif [ \"$1\" = version ]; then printf 'zmx\\t\\t%s\\n' " + version + "; exit 0; fi\nsleep 1\n")
}

// tarball packs one regular member; extra entries let a test inject an
// unsafe path or a second executable.
type entry struct {
	name string
	mode int64
	body []byte
	typ  byte
	link string
}

func tarball(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		hdr := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: typ, Linkname: e.link}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(e.body); err != nil {
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

func sha256hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// releaseServer serves the archive at the version-and-asset path the
// installer builds, counting requests so a test can prove reuse hits no
// network. hang blocks the body until the test's context is cancelled.
type releaseServer struct {
	*httptest.Server
	asset    string
	archive  []byte
	requests int
	hang     bool
	mu       sync.Mutex
}

func newReleaseServer(t *testing.T, asset string, archive []byte) *releaseServer {
	t.Helper()
	rs := &releaseServer{asset: asset, archive: archive}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.requests++
		hang := rs.hang
		rs.mu.Unlock()
		if !strings.HasSuffix(r.URL.Path, "/"+asset) {
			http.NotFound(w, r)
			return
		}
		if hang {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write(rs.archive)
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *releaseServer) count() int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.requests
}

// controlledRuntime builds a Runtime whose release table names the served
// asset with measured sums and whose downloads hit rs.
func controlledRuntime(t *testing.T, rs *releaseServer, exeSum string) *Runtime {
	t.Helper()
	table := map[string]Release{testVersion: {Assets: map[string]Asset{
		target(): {Name: rs.asset, ArchiveSHA256: sha256hex(rs.archive), ExecutableSHA256: exeSum},
	}}}
	return NewRuntime(RuntimeOptions{
		RuntimeDir:    t.TempDir(),
		SelectionFile: filepath.Join(t.TempDir(), "runtime.json"),
		Releases:      table,
		Desired:       testVersion,
		DownloadBase:  rs.URL + "/",
	})
}

func TestInstallVerifiesAndReuses(t *testing.T) {
	exe := fakeZmx(testVersion)
	archive := tarball(t, entry{name: "zmx-dir/zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "zmx-"+testVersion+".tar.gz", archive)
	rt := controlledRuntime(t, rs, sha256hex(exe))

	executable, err := rt.Install(context.Background(), testVersion)
	if err != nil {
		t.Fatalf("Install: %v", err)
	}
	if got := sha256File(t, executable); got != sha256hex(exe) {
		t.Fatalf("installed executable sha = %s", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(executable), noticesName)); err != nil {
		t.Errorf("notices not installed beside the executable: %v", err)
	}
	// A second install reuses the verified bytes without any download.
	if _, err := rt.Install(context.Background(), testVersion); err != nil {
		t.Fatalf("second Install: %v", err)
	}
	if rs.count() != 1 {
		t.Errorf("install hit the network %d times, want 1 (offline reuse)", rs.count())
	}
}

func TestInstallRejectsArchiveChecksumMismatch(t *testing.T) {
	exe := fakeZmx(testVersion)
	archive := tarball(t, entry{name: "zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "asset.tar.gz", archive)
	rt := controlledRuntime(t, rs, sha256hex(exe))
	// Corrupt the recorded archive sum: the download no longer matches.
	rt.releases[testVersion].Assets[target()] = Asset{Name: "asset.tar.gz", ArchiveSHA256: strings.Repeat("0", 64), ExecutableSHA256: sha256hex(exe)}
	_, err := rt.Install(context.Background(), testVersion)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("Install = %v, want a checksum mismatch", err)
	}
}

func TestInstallRejectsExecutableChecksumMismatch(t *testing.T) {
	exe := fakeZmx(testVersion)
	archive := tarball(t, entry{name: "zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "asset.tar.gz", archive)
	rt := controlledRuntime(t, rs, strings.Repeat("a", 64))
	_, err := rt.Install(context.Background(), testVersion)
	if err == nil || !strings.Contains(err.Error(), "verified identity") {
		t.Fatalf("Install = %v, want an executable identity mismatch", err)
	}
}

func TestInstallRejectsUnsafeArchive(t *testing.T) {
	exe := fakeZmx(testVersion)
	for name, e := range map[string]entry{
		"escaping path": {name: "../evil", mode: 0o755, body: exe},
		"symlink":       {name: "zmx", typ: tar.TypeSymlink, link: "/etc/passwd"},
	} {
		archive := tarball(t, e)
		rs := newReleaseServer(t, "asset.tar.gz", archive)
		rt := controlledRuntime(t, rs, sha256hex(exe))
		if _, err := rt.Install(context.Background(), testVersion); err == nil || !strings.Contains(err.Error(), "unsafe archive entry") {
			t.Errorf("%s: Install = %v, want an unsafe-entry refusal", name, err)
		}
	}
}

func TestInstallRejectsTwoExecutables(t *testing.T) {
	exe := fakeZmx(testVersion)
	archive := tarball(t, entry{name: "a/zmx", mode: 0o755, body: exe}, entry{name: "b/zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "asset.tar.gz", archive)
	rt := controlledRuntime(t, rs, sha256hex(exe))
	if _, err := rt.Install(context.Background(), testVersion); err == nil || !strings.Contains(err.Error(), "more than one") {
		t.Fatalf("Install = %v, want a two-executable refusal", err)
	}
}

func TestInstallRejectsWrongReportedVersion(t *testing.T) {
	exe := fakeZmx("1.2.3") // runs, but reports the wrong version
	archive := tarball(t, entry{name: "zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "asset.tar.gz", archive)
	rt := controlledRuntime(t, rs, sha256hex(exe))
	if _, err := rt.Install(context.Background(), testVersion); err == nil || !strings.Contains(err.Error(), "reports version") {
		t.Fatalf("Install = %v, want a version-mismatch refusal", err)
	}
}

func TestInstallRejectsUnsupportedVersion(t *testing.T) {
	rs := newReleaseServer(t, "asset.tar.gz", nil)
	rt := controlledRuntime(t, rs, "")
	if _, err := rt.Install(context.Background(), "0.0.0"); err == nil || !strings.Contains(err.Error(), "not a version this ATC build supports") {
		t.Fatalf("Install = %v, want an unsupported-version refusal", err)
	}
}

func TestInstallCancellation(t *testing.T) {
	exe := fakeZmx(testVersion)
	archive := tarball(t, entry{name: "zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "asset.tar.gz", archive)
	rs.hang = true
	rt := controlledRuntime(t, rs, sha256hex(exe))
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	if _, err := rt.Install(ctx, testVersion); err == nil {
		t.Fatal("Install returned nil despite cancellation")
	}
	// Nothing was published: the version directory does not exist.
	if _, err := os.Stat(filepath.Join(rt.installDir, testVersion)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a cancelled install left a version directory: %v", err)
	}
}

func TestConcurrentInstallsSerialize(t *testing.T) {
	exe := fakeZmx(testVersion)
	archive := tarball(t, entry{name: "zmx", mode: 0o755, body: exe})
	rs := newReleaseServer(t, "asset.tar.gz", archive)
	rt := controlledRuntime(t, rs, sha256hex(exe))
	var wg sync.WaitGroup
	errs := make([]error, 5)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); _, errs[i] = rt.Install(context.Background(), testVersion) }()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("concurrent install %d: %v", i, err)
		}
	}
}

func sha256File(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sha256hex(data)
}

var _ = fmt.Sprintf
