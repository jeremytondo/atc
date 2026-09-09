package authoring

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

// fakeNode is the private runtime's node: a shell script that plays npm
// ci, tsc, and vite according to the script it is asked to run, and
// records installs beside the runtime so tests can count them.
const fakeNode = `#!/bin/sh
here=$(cd "$(dirname "$0")/../../.." && pwd)
script="$1"; shift
case "$script" in
  */npm-cli.js)
    [ "$1" = ci ] || { echo "unexpected npm args: $*" >&2; exit 2; }
    mkdir -p node_modules/typescript/bin node_modules/vite/bin
    echo fake > node_modules/typescript/bin/tsc
    echo fake > node_modules/vite/bin/vite.js
    echo "ci $(pwd)" >> "$here/installs.log"
    ;;
  */typescript/bin/tsc)
    if grep -rq TYPE_ERROR src/document; then echo "src/document/index.tsx(1,1): error TS2322: incompatible"; exit 1; fi
    ;;
  */vite/bin/vite.js)
    if [ "$1" = build ]; then
      mkdir -p dist/assets
      cp src/document/index.tsx dist/assets/document.txt
      cp src/platform/index.ts dist/assets/platform.txt
      printf '<html><head></head><body>%s</body></html>' "$VITE_ATC_TITLE" > dist/index.html
    else
      echo "  Local:   http://127.0.0.1:5173/"
      sleep 30
    fi
    ;;
  *) echo "unexpected script: $script" >&2; exit 2 ;;
esac
`

// runtimeArchive builds a Node-shaped tar.gz: one top-level directory,
// bin/node, npm under lib, and the relative symlink the real archive has.
func runtimeArchive(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	add := func(header *tar.Header, body string) {
		header.Size = int64(len(body))
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	add(&tar.Header{Name: "node-v0-fake/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "node-v0-fake/bin/", Typeflag: tar.TypeDir, Mode: 0o755}, "")
	add(&tar.Header{Name: "node-v0-fake/bin/node", Typeflag: tar.TypeReg, Mode: 0o755}, fakeNode)
	add(&tar.Header{Name: "node-v0-fake/lib/node_modules/npm/bin/npm-cli.js", Typeflag: tar.TypeReg, Mode: 0o755}, "// npm\n")
	add(&tar.Header{Name: "node-v0-fake/bin/npm", Typeflag: tar.TypeSymlink, Linkname: "../lib/node_modules/npm/bin/npm-cli.js", Mode: 0o777}, "")
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

type fixture struct {
	t         *testing.T
	service   *Service
	root      string
	output    bytes.Buffer
	mu        sync.Mutex
	downloads int
	fetchErr  error
	clock     time.Time
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	f := &fixture{t: t, root: t.TempDir(), clock: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
	archive := runtimeArchive(t)
	sum := sha256.Sum256(archive)
	asset, err := runtimeAsset()
	if err != nil {
		t.Fatal(err)
	}
	sums := hex.EncodeToString(sum[:]) + "  " + asset + "\n" + "deadbeef  other.tar.gz\n"
	service, err := New(Options{
		Root: f.root, Version: "v1.2.3", Output: &f.output,
		Fetch: func(_ context.Context, url string) (io.ReadCloser, error) {
			f.mu.Lock()
			defer f.mu.Unlock()
			if f.fetchErr != nil {
				return nil, f.fetchErr
			}
			switch {
			case strings.HasSuffix(url, "/SHASUMS256.txt"):
				return io.NopCloser(strings.NewReader(sums)), nil
			case strings.HasSuffix(url, "/"+asset):
				f.downloads++
				return io.NopCloser(bytes.NewReader(archive)), nil
			}
			return nil, fmt.Errorf("unexpected download %s", url)
		},
		Now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.clock = f.clock.Add(time.Second)
			return f.clock
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	f.service = service
	return f
}

func (f *fixture) installs() int {
	data, _ := os.ReadFile(filepath.Join(f.root, platformDir, "installs.log"))
	return strings.Count(string(data), "\n")
}

func (f *fixture) downloadCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.downloads
}

// archiveNames lists a gzip tar's regular members.
func archiveNames(t *testing.T, path string) []string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	reader := tar.NewReader(gz)
	var names []string
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag == tar.TypeReg {
			names = append(names, header.Name)
		}
	}
	sort.Strings(names)
	return names
}

// documentOf reads assets/document.txt (the fake build's copy of the
// document) out of a build archive.
func documentOf(archive io.Reader) string {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return "unreadable: " + err.Error()
	}
	reader := tar.NewReader(gz)
	for {
		header, err := reader.Next()
		if err != nil {
			return "no document in build"
		}
		if header.Name == "assets/document.txt" {
			content, _ := io.ReadAll(reader)
			return string(content)
		}
	}
}

type fakePublisher struct {
	calls   []api.ArtifactPublishParams
	targets []string
	builds  []string
	err     error
	next    int
}

func (p *fakePublisher) publish(target string, params api.ArtifactPublishParams, build io.Reader) (api.ArtifactPublication, error) {
	p.calls = append(p.calls, params)
	p.targets = append(p.targets, target)
	p.builds = append(p.builds, documentOf(build))
	if p.err != nil {
		return api.ArtifactPublication{}, p.err
	}
	p.next++
	id := target
	if id == "" {
		id = "artf-newxx"
	}
	return api.ArtifactPublication{
		Artifact: api.Artifact{ID: id, Title: params.Title, CurrentVersion: p.next, URL: "http://127.0.0.1:7332/a/" + id + "/"},
		Version:  api.ArtifactVersion{ArtifactID: id, Number: p.next, Title: params.Title},
	}, nil
}

func (p *fakePublisher) PublishArtifact(_ context.Context, params api.ArtifactPublishParams, build, _ io.Reader) (api.ArtifactPublication, error) {
	return p.publish("", params, build)
}

func (p *fakePublisher) PublishArtifactVersion(_ context.Context, id string, params api.ArtifactPublishParams, build, _ io.Reader) (api.ArtifactPublication, error) {
	return p.publish(id, params, build)
}

func TestCreateBuildPublish(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if diff := cmp.Diff([]string{"architecture", "comparison", "demo"}, ExampleNames()); diff != "" {
		t.Errorf("ExampleNames mismatch (-want +got):\n%s", diff)
	}

	copy, err := f.service.Create(ctx, CreateOptions{Example: "demo"})
	if err != nil {
		t.Fatalf("Create: %v\n%s", err, f.output.String())
	}
	if !strings.HasPrefix(copy.ID, "copy-") || copy.Dir != filepath.Join(f.root, copiesDir, copy.ID) || copy.Title != "Retry backoff, explored" || copy.Document != filepath.Join(copy.Dir, "src", "document") {
		t.Errorf("copy = %+v", copy)
	}
	for _, name := range []string{"package.json", "package-lock.json", "vite.config.ts", "tsconfig.json", "index.html", "src/main.tsx", "src/platform/index.ts", "src/document/index.tsx", ".atc/copy.json"} {
		if _, err := os.Stat(filepath.Join(copy.Dir, filepath.FromSlash(name))); err != nil {
			t.Errorf("copy lacks %s: %v", name, err)
		}
	}
	if link, err := os.Readlink(filepath.Join(copy.Dir, "node_modules")); err != nil || link != filepath.Join(f.root, platformDir, depsDir, currentLink, "node_modules") {
		t.Errorf("node_modules link = %q, %v", link, err)
	}
	if doc, _ := os.ReadFile(filepath.Join(copy.Document, "index.tsx")); !strings.Contains(string(doc), "jittered exponential backoff") {
		t.Error("document not seeded from the demo example")
	}
	if _, err := os.Stat(filepath.Join(copy.Dir, examplesDir)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("examples copied into the working copy")
	}
	if f.downloadCount() != 1 || f.installs() != 1 {
		t.Errorf("downloads=%d installs=%d after first use, want 1 and 1", f.downloadCount(), f.installs())
	}
	// The runtime's symlink survived extraction.
	if link, err := os.Readlink(filepath.Join(f.root, platformDir, runtimeDir, NodeVersion, "bin", "npm")); err != nil || link != filepath.FromSlash("../lib/node_modules/npm/bin/npm-cli.js") {
		t.Errorf("npm link = %q, %v", link, err)
	}

	// Only the platform and src/document reach a build and its source;
	// whatever else lands in the copy's directory does not.
	for _, stray := range []string{".env", ".git/config", "public/leak.txt", "notes.md", "src/other.ts"} {
		p := filepath.Join(copy.Dir, filepath.FromSlash(stray))
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("SECRET"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	built, err := f.service.Build(ctx, copy.ID)
	if err != nil {
		t.Fatalf("Build: %v\n%s", err, f.output.String())
	}
	if page, _ := os.ReadFile(filepath.Join(built.Dir, "index.html")); !strings.Contains(string(page), "Retry backoff, explored") {
		t.Errorf("built page = %q, want the title", page)
	}
	if names := strings.Join(archiveNames(t, built.BuildArchive), " "); !strings.Contains(names, "index.html") || !strings.Contains(names, "assets/document.txt") {
		t.Errorf("build archive = %v", names)
	}
	source := strings.Join(archiveNames(t, built.SourceArchive), "\n")
	for _, want := range []string{"src/document/index.tsx", "src/platform/index.ts", "package.json", "package-lock.json", "vite.config.ts", "index.html"} {
		if !strings.Contains(source, want) {
			t.Errorf("source archive lacks %s:\n%s", want, source)
		}
	}
	for _, leak := range []string{"node_modules", "dist/", scratchDir, ".env", ".git", "public", "notes.md", "src/other.ts", "SECRET"} {
		if strings.Contains(source, leak) {
			t.Errorf("source archive carries %s:\n%s", leak, source)
		}
	}

	publisher := &fakePublisher{}
	result, err := f.service.Publish(ctx, publisher, copy.ID, PublishOptions{Provenance: api.ArtifactProvenance{Revision: "abc"}})
	if err != nil {
		t.Fatalf("Publish: %v\n%s", err, f.output.String())
	}
	if result.Artifact.ID != "artf-newxx" || result.Resumed || publisher.targets[0] != "" {
		t.Errorf("first publish = %+v, target %q", result, publisher.targets[0])
	}
	first := publisher.calls[0]
	if first.Title != "Retry backoff, explored" || first.BaseVersion != 0 || first.Platform != "atc v1.2.3 platform "+f.service.identity || first.Provenance.Revision != "abc" || !strings.HasPrefix(first.PublicationID, "pub-") {
		t.Errorf("first params = %+v", first)
	}
	after, err := f.service.Get(copy.ID)
	if err != nil || after.Artifact != "artf-newxx" || after.BaseVersion != 1 || after.Pending != "" {
		t.Errorf("copy after publish = %+v, %v", after, err)
	}

	// A revision publishes a version on the recorded base, with a fresh
	// retry identity; a --base override replaces the recorded base.
	if _, err := f.service.Publish(ctx, publisher, copy.Document, PublishOptions{Title: "Backoff, revised"}); err != nil {
		t.Fatal(err)
	}
	second := publisher.calls[1]
	if publisher.targets[1] != "artf-newxx" || second.BaseVersion != 1 || second.Title != "Backoff, revised" || second.PublicationID == first.PublicationID {
		t.Errorf("second publish = %+v target %q", second, publisher.targets[1])
	}
	if _, err := f.service.Publish(ctx, publisher, copy.ID, PublishOptions{Base: 7}); err != nil {
		t.Fatal(err)
	}
	if publisher.calls[2].BaseVersion != 7 {
		t.Errorf("base override = %d", publisher.calls[2].BaseVersion)
	}
	if f.downloadCount() != 1 || f.installs() != 1 {
		t.Errorf("downloads=%d installs=%d after reuse, want 1 and 1", f.downloadCount(), f.installs())
	}
	if final, _ := f.service.Get(copy.ID); final.BaseVersion != 3 || final.Title != "Backoff, revised" {
		t.Errorf("copy after revisions = %+v", final)
	}
}

// An unconfirmed publication is resent exactly as it was — even after
// the document changed — before later edits publish; a definite refusal
// drops it.
func TestPublishResendsPendingPackage(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	copy, err := f.service.Create(ctx, CreateOptions{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	publisher := &fakePublisher{err: errors.New("connection reset")}
	if _, err := f.service.Publish(ctx, publisher, copy.ID, PublishOptions{}); err == nil {
		t.Fatal("transport failure reported success")
	}
	pending, _ := f.service.Get(copy.ID)
	if pending.Pending == "" {
		t.Fatal("retry identity not kept after an uncertain outcome")
	}
	if err := os.WriteFile(filepath.Join(copy.Document, "index.tsx"), []byte("EDITED"), 0o600); err != nil {
		t.Fatal(err)
	}
	publisher.err = nil
	result, err := f.service.Publish(ctx, publisher, copy.ID, PublishOptions{Title: "ignored while pending"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Resumed || publisher.calls[1].PublicationID != pending.Pending || publisher.builds[1] != publisher.builds[0] || publisher.calls[1].Title != "t" {
		t.Errorf("resend = %+v, params %+v; want the frozen package", result, publisher.calls[1])
	}
	if strings.Contains(publisher.builds[1], "EDITED") {
		t.Error("the resend carried the later edit")
	}
	// Now the edit publishes as the next version.
	next, err := f.service.Publish(ctx, publisher, copy.ID, PublishOptions{})
	if err != nil || next.Resumed || next.Version.Number != 2 || !strings.Contains(publisher.builds[2], "EDITED") {
		t.Errorf("next publish = %+v, %v; build %q", next, err, publisher.builds[2])
	}
	publisher.err = &api.Problem{Status: http.StatusConflict, Code: api.CodeArtifactBaseStale}
	if _, err := f.service.Publish(ctx, publisher, copy.ID, PublishOptions{}); err == nil {
		t.Fatal("conflict reported success")
	}
	if after, _ := f.service.Get(copy.ID); after.Pending != "" || after.BaseVersion != 2 {
		t.Errorf("copy after conflict = %+v; want the package dropped and the base kept", after)
	}
}

// A platform shipped by a newer ATC brings old working copies to it —
// stale platform files included — and a dependency change reinstalls
// the dependencies (not the unchanged runtime).
func TestPlatformRefreshFollowsUpgrade(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	copy, err := f.service.Create(ctx, CreateOptions{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(copy.Dir, "src", "platform", "gone.ts")
	if err := os.WriteFile(stale, []byte("export {}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A new ATC ships a different dependency set: its digest names a
	// directory that does not exist yet.
	f.service.deps = "0123456789ab"
	if _, err := f.service.Open(ctx, copy.ID); err != nil {
		t.Fatalf("Open: %v\n%s", err, f.output.String())
	}
	if f.installs() != 2 || f.downloadCount() != 1 {
		t.Errorf("installs=%d downloads=%d, want a dependency refresh without a runtime download", f.installs(), f.downloadCount())
	}
	if _, err := os.Stat(stale); !errors.Is(err, fs.ErrNotExist) {
		t.Error("stale platform file survived the sync")
	}
	if link, _ := os.Readlink(filepath.Join(f.root, platformDir, depsDir, currentLink)); link != "0123456789ab" {
		t.Errorf("deps current = %q", link)
	}
	if _, err := os.Stat(filepath.Join(f.root, platformDir, depsDir, f.service.deps)); err != nil {
		t.Error("new dependencies missing")
	}
	if _, err := f.service.Open(ctx, copy.ID); err != nil || f.installs() != 2 {
		t.Errorf("second Open reinstalled: installs=%d, %v", f.installs(), err)
	}
	// A damaged installation is repaired, not trusted.
	if err := os.Remove(filepath.Join(f.root, platformDir, depsDir, f.service.deps, "node_modules", "vite", "bin", "vite.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Open(ctx, copy.ID); err != nil || f.installs() != 3 {
		t.Errorf("Open after damage: installs=%d, %v; want a reinstall", f.installs(), err)
	}
	if err := os.Remove(filepath.Join(f.root, platformDir, runtimeDir, NodeVersion, "bin", "node")); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Open(ctx, copy.ID); err != nil || f.downloadCount() != 2 {
		t.Errorf("Open after runtime damage: downloads=%d, %v; want a fresh runtime", f.downloadCount(), err)
	}
}

// A failed runtime download changes nothing and says what to do; the
// next attempt starts clean. A corrupted download is refused.
func TestInstallFailureLeavesNothingBehind(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.mu.Lock()
	f.fetchErr = errors.New("dial tcp: network is unreachable")
	f.mu.Unlock()
	_, err := f.service.Create(ctx, CreateOptions{Title: "t"})
	if err == nil || !strings.Contains(err.Error(), "Node runtime") || !strings.Contains(err.Error(), "network is unreachable") {
		t.Fatalf("Create during outage = %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, platformDir, runtimeDir, currentLink)); !errors.Is(err, fs.ErrNotExist) {
		t.Error("runtime link created after a failed install")
	}
	if copies, _ := f.service.List(); len(copies) != 0 {
		t.Errorf("working copies after failure = %v", copies)
	}
	f.mu.Lock()
	f.fetchErr = nil
	f.mu.Unlock()
	if _, err := f.service.Create(ctx, CreateOptions{Title: "t"}); err != nil {
		t.Fatalf("Create after outage: %v", err)
	}
	f2 := newFixture(t)
	original := f2.service.fetch
	f2.service.fetch = func(ctx context.Context, url string) (io.ReadCloser, error) {
		if strings.HasSuffix(url, ".tar.gz") {
			return io.NopCloser(strings.NewReader("not the runtime")), nil
		}
		return original(ctx, url)
	}
	if _, err := f2.service.Create(ctx, CreateOptions{Title: "t"}); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("corrupted download = %v", err)
	}
}

// A document the current platform cannot compile fails the check with
// the tool's report; nothing is published.
func TestCheckReportsIncompatibleSource(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	copy, err := f.service.Create(ctx, CreateOptions{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(copy.Document, "index.tsx"), []byte("TYPE_ERROR"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Check(ctx, copy.ID); !errors.Is(err, ErrCheckFailed) || !strings.Contains(f.output.String(), "error TS2322") {
		t.Errorf("Check = %v; output:\n%s", err, f.output.String())
	}
	publisher := &fakePublisher{}
	if _, err := f.service.Publish(ctx, publisher, copy.ID, PublishOptions{}); !errors.Is(err, ErrCheckFailed) || len(publisher.calls) != 0 {
		t.Errorf("Publish = %v, calls %d; want the check to stop it", err, len(publisher.calls))
	}
}

// A copy retrieved from a published version takes only the document out
// of the source archive and records what it revises.
func TestCreateFromPublishedSource(t *testing.T) {
	f := newFixture(t)
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range map[string]string{
		"./src/document/index.tsx": "export default 1", "src/document/data/x.json": "{}", "src/platform/index.ts": "OLD",
		"package.json": "{}", "../escape": "no", "src/document/../../evil": "no",
	} {
		_ = tw.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
		_, _ = tw.Write([]byte(body))
	}
	_ = tw.Close()
	_ = gz.Close()
	copy, err := f.service.Create(context.Background(), CreateOptions{Title: "Revised", Source: &buf, Artifact: "artf-aaaaa", BaseVersion: 3})
	if err != nil {
		t.Fatal(err)
	}
	if copy.Artifact != "artf-aaaaa" || copy.BaseVersion != 3 {
		t.Errorf("copy = %+v", copy)
	}
	if doc, _ := os.ReadFile(filepath.Join(copy.Document, "index.tsx")); string(doc) != "export default 1" {
		t.Errorf("document = %q", doc)
	}
	if _, err := os.Stat(filepath.Join(copy.Document, "data", "x.json")); err != nil {
		t.Error("nested document file missing")
	}
	if platform, _ := os.ReadFile(filepath.Join(copy.Dir, "src", "platform", "index.ts")); string(platform) == "OLD" {
		t.Error("old platform source replaced the current platform")
	}
	for _, stray := range []string{"evil", "../escape"} {
		if _, err := os.Stat(filepath.Join(copy.Dir, stray)); err == nil {
			t.Errorf("%s extracted", stray)
		}
	}
	if _, err := f.service.Create(context.Background(), CreateOptions{Source: strings.NewReader("junk")}); err == nil {
		t.Error("junk source accepted")
	}
}

// Copies resolve only inside the copies directory: an id, or a path
// under a copy; never an arbitrary directory, a symlink into one, or a
// directory whose record names another id.
func TestLocateStaysInsideCopies(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	first, err := f.service.Create(ctx, CreateOptions{Title: "first"})
	if err != nil {
		t.Fatal(err)
	}
	second, err := f.service.Create(ctx, CreateOptions{Title: "second", Example: "comparison"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Create(ctx, CreateOptions{Example: "nope"}); err == nil || !strings.Contains(err.Error(), `no example named "nope"`) {
		t.Errorf("unknown example = %v", err)
	}
	copies, err := f.service.List()
	if err != nil || len(copies) != 2 || copies[0].ID != first.ID || copies[1].Example != "comparison" {
		t.Errorf("List = %+v, %v", copies, err)
	}
	if got, err := f.service.Get(second.Document); err != nil || got.ID != second.ID {
		t.Errorf("Get(path inside) = %+v, %v", got, err)
	}

	// A repository holding an extracted source archive, with a record
	// planted in it, is not a working copy.
	repo := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo, scratchDir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, scratchDir, recordFile), []byte(`{"id":"`+first.ID+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "precious"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Discard(repo); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("Discard(repository) = %v, want ErrCopyNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(repo, "precious")); err != nil {
		t.Fatal("the repository was deleted")
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(repo, link); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Get(link); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("Get(symlink to repository) = %v", err)
	}
	if err := os.WriteFile(filepath.Join(second.Dir, scratchDir, recordFile), []byte(`{"id":"copy-other"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := f.service.Get(second.ID); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("Get(mismatched record) = %v", err)
	}
	for _, ref := range []string{"copy-nope1", t.TempDir(), filepath.Join(f.root, copiesDir), "../" + first.ID} {
		if _, err := f.service.Get(ref); !errors.Is(err, ErrCopyNotFound) {
			t.Errorf("Get(%q) = %v", ref, err)
		}
	}
	if err := f.service.Discard(first.ID); err != nil {
		t.Fatal(err)
	}
	if err := f.service.Discard(first.ID); !errors.Is(err, ErrCopyNotFound) {
		t.Errorf("second Discard = %v", err)
	}
}

// Two processes on one copy take turns: the second first publish finds
// the first one's result and publishes a version, never a second
// artifact.
func TestConcurrentPublishesSerialize(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	copy, err := f.service.Create(ctx, CreateOptions{Title: "t"})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	publisher := &lockedPublisher{mu: &mu}
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for range 2 {
		wg.Go(func() {
			_, err := f.service.Publish(ctx, publisher, copy.ID, PublishOptions{})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Errorf("Publish: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if diff := cmp.Diff([]string{"", "artf-newxx"}, publisher.targets); diff != "" {
		t.Errorf("targets mismatch (-want +got):\n%s", diff)
	}
	if after, _ := f.service.Get(copy.ID); after.BaseVersion != 2 {
		t.Errorf("copy after concurrent publishes = %+v", after)
	}
}

// lockedPublisher is fakePublisher safe for concurrent use.
type lockedPublisher struct {
	fakePublisher
	mu *sync.Mutex
}

func (p *lockedPublisher) PublishArtifact(ctx context.Context, params api.ArtifactPublishParams, build, source io.Reader) (api.ArtifactPublication, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fakePublisher.PublishArtifact(ctx, params, build, source)
}

func (p *lockedPublisher) PublishArtifactVersion(ctx context.Context, id string, params api.ArtifactPublishParams, build, source io.Reader) (api.ArtifactPublication, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.fakePublisher.PublishArtifactVersion(ctx, id, params, build, source)
}
