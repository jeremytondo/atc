package artifacts

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/store"
)

// entry is one archive member for the test archive builder.
type entry struct {
	name    string
	body    string
	typ     byte // 0 means regular file
	link    string
	mode    int64
	rawSize int64 // when set, the header lies about the size
}

// archive builds a gzip tar in memory.
func archive(entries ...entry) []byte {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		typ := e.typ
		if typ == 0 {
			typ = tar.TypeReg
		}
		header := &tar.Header{Name: e.name, Typeflag: typ, Linkname: e.link, Mode: 0o644, Size: int64(len(e.body))}
		if typ == tar.TypeDir {
			header.Mode = 0o755
		}
		if e.rawSize != 0 {
			header.Size = e.rawSize
		}
		if err := tw.WriteHeader(header); err != nil {
			panic(err)
		}
		if typ == tar.TypeReg {
			if _, err := tw.Write([]byte(e.body)); err != nil {
				panic(err)
			}
		}
	}
	_ = tw.Close()
	_ = gz.Close()
	return buf.Bytes()
}

func build(index string) []byte {
	return archive(
		entry{name: "./", typ: tar.TypeDir},
		entry{name: "index.html", body: index},
		entry{name: "assets/", typ: tar.TypeDir},
		entry{name: "assets/app.js", body: "console.log(1)"},
	)
}

func source(version string) []byte {
	return archive(entry{name: "src/document/index.tsx", body: "export default " + version})
}

type fixture struct {
	t       *testing.T
	service *Service
	store   *store.Store
	hub     *events.Hub
	root    string
	clock   time.Time
	mu      sync.Mutex
	tailnet string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	db, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "atc.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	f := &fixture{t: t, store: db, hub: events.NewHubAt(16, 1), root: filepath.Join(t.TempDir(), "artifacts"),
		clock: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)}
	f.service = f.open()
	return f
}

// open builds a service over the fixture's store and root, as a restart
// would.
func (f *fixture) open() *Service {
	f.t.Helper()
	service, err := New(context.Background(), Options{
		Repository: f.store.Artifacts(), Hub: f.hub, Root: f.root,
		Bases: func() (string, string) {
			f.mu.Lock()
			defer f.mu.Unlock()
			return "http://127.0.0.1:7332", f.tailnet
		},
		Limits: Limits{MaxFiles: 8, MaxFileBytes: 4096, MaxTotalBytes: 8192},
		Now: func() time.Time {
			f.mu.Lock()
			defer f.mu.Unlock()
			f.clock = f.clock.Add(time.Second)
			return f.clock
		},
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return service
}

func (f *fixture) publish(artifactID string, params api.ArtifactPublishParams, build, source []byte) (api.ArtifactPublication, bool, error) {
	var b, s io.Reader
	if build != nil {
		b = bytes.NewReader(build)
	}
	if source != nil {
		s = bytes.NewReader(source)
	}
	return f.service.Publish(context.Background(), artifactID, params, b, s)
}

func (f *fixture) mustPublish(artifactID string, params api.ArtifactPublishParams, build, source []byte) api.ArtifactPublication {
	f.t.Helper()
	result, replayed, err := f.publish(artifactID, params, build, source)
	if err != nil || replayed {
		f.t.Fatalf("Publish(%q, %+v) = %+v, replayed=%v, %v", artifactID, params, result, replayed, err)
	}
	return result
}

func (f *fixture) insertProject(id string) {
	f.t.Helper()
	dir := f.t.TempDir()
	if ok, err := f.store.Projects().Insert(context.Background(), store.ProjectRecord{
		ID: id, Name: id, Directory: dir, CreatedAt: f.clock, UpdatedAt: f.clock,
	}); err != nil || !ok {
		f.t.Fatalf("insert project = %v, %v", ok, err)
	}
}

func (f *fixture) stagings() []string {
	f.t.Helper()
	entries, err := os.ReadDir(filepath.Join(f.root, stagingDir))
	if err != nil {
		f.t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestPublishLifecycle(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	sub := f.hub.Subscribe(0, false)
	defer sub.Close()

	first := f.mustPublish("", api.ArtifactPublishParams{
		Title: "  Design  ", PublicationID: "pub-1", Platform: "atc v1",
		Source: api.ArtifactSource{Thread: "thrd-aaaaa", Revision: "abc", Links: []string{"https://linear.app/x"}},
	}, build("<h1>one</h1>"), source("one"))
	id := first.Artifact.ID
	if !strings.HasPrefix(id, "artf-") || len(id) != 10 {
		t.Fatalf("artifact id = %q", id)
	}
	wantArtifact := api.Artifact{
		ID: id, Title: "Design", CurrentVersion: 1, CreatedAt: first.Artifact.CreatedAt, UpdatedAt: first.Artifact.CreatedAt,
		URL: "http://127.0.0.1:7332/a/" + id + "/",
	}
	wantVersion := api.ArtifactVersion{
		ArtifactID: id, Number: 1, Title: "Design", Platform: "atc v1", PublishedAt: first.Artifact.CreatedAt,
		Source: api.ArtifactSource{Thread: "thrd-aaaaa", Revision: "abc", Links: []string{"https://linear.app/x"}},
		URL:    "http://127.0.0.1:7332/a/" + id + "/v/1/",
	}
	if diff := cmp.Diff(api.ArtifactPublication{Artifact: wantArtifact, Version: wantVersion}, first); diff != "" {
		t.Errorf("first publication mismatch (-want +got):\n%s", diff)
	}
	if got := readFile(t, filepath.Join(f.root, id, "1", "build", "index.html")); got != "<h1>one</h1>" {
		t.Errorf("stored index = %q", got)
	}
	if got := readFile(t, filepath.Join(f.root, id, "1", "build", "assets", "app.js")); got != "console.log(1)" {
		t.Errorf("stored asset = %q", got)
	}
	sourcePath, err := f.service.SourcePath(ctx, id, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := readFile(t, sourcePath); got != string(source("one")) {
		t.Error("stored source differs from the upload")
	}

	// A repeat of the same publication replays the result.
	replay, replayed, err := f.publish("", api.ArtifactPublishParams{Title: "Design", PublicationID: "pub-1"}, build("<h1>dup</h1>"), source("dup"))
	if err != nil || !replayed {
		t.Fatalf("replay = %+v, %v, %v", replay, replayed, err)
	}
	if diff := cmp.Diff(first, replay); diff != "" {
		t.Errorf("replay mismatch (-want +got):\n%s", diff)
	}

	// A correction on the current base appends version 2; the reader
	// links point at it, the old link and title stay.
	f.mu.Lock()
	f.tailnet = "https://node.ts.net:7332"
	f.mu.Unlock()
	second := f.mustPublish(id, api.ArtifactPublishParams{Title: "Design v2", PublicationID: "pub-2", BaseVersion: 1}, build("<h1>two</h1>"), source("two"))
	if second.Artifact.CurrentVersion != 2 || second.Version.Number != 2 || second.Artifact.Title != "Design v2" {
		t.Errorf("second publication = %+v", second)
	}
	if second.Artifact.TailnetURL != "https://node.ts.net:7332/a/"+id+"/" || second.Version.TailnetURL != "https://node.ts.net:7332/a/"+id+"/v/2/" {
		t.Errorf("tailnet links = %q, %q", second.Artifact.TailnetURL, second.Version.TailnetURL)
	}
	if !second.Artifact.UpdatedAt.After(first.Artifact.UpdatedAt) {
		t.Errorf("updatedAt not advanced: %v -> %v", first.Artifact.UpdatedAt, second.Artifact.UpdatedAt)
	}
	if got := readFile(t, filepath.Join(f.root, id, "1", "build", "index.html")); got != "<h1>one</h1>" {
		t.Errorf("version 1 changed: %q", got)
	}
	if v1, err := f.service.Version(ctx, id, 1); err != nil || v1.Title != "Design" {
		t.Errorf("version 1 = %+v, %v; want its original title", v1, err)
	}

	// Publishing on a stale base is refused, naming the current version.
	var stale *StaleBaseError
	if _, _, err := f.publish(id, api.ArtifactPublishParams{Title: "late", PublicationID: "pub-3", BaseVersion: 1}, build("<h1>late</h1>"), source("late")); !errors.As(err, &stale) || stale.Current != 2 || stale.Base != 1 {
		t.Errorf("stale publish = %v, want StaleBaseError{1, 2}", err)
	}
	if versions, err := f.service.Versions(ctx, id); err != nil || len(versions) != 2 {
		t.Errorf("Versions = %v, %v; want 2", versions, err)
	}
	if names := f.stagings(); len(names) != 0 {
		t.Errorf("stagings left behind: %v", names)
	}

	// A publication id committed elsewhere cannot be replayed onto
	// another artifact.
	other := f.mustPublish("", api.ArtifactPublishParams{Title: "Other", PublicationID: "pub-other"}, build("<p>o</p>"), source("o"))
	if _, _, err := f.publish(other.Artifact.ID, api.ArtifactPublishParams{Title: "x", PublicationID: "pub-2", BaseVersion: 1}, build("<p>x</p>"), source("x")); !errors.Is(err, ErrPublicationTaken) {
		t.Errorf("foreign publication id = %v, want ErrPublicationTaken", err)
	}

	// Restoring version 1 republishes its frozen snapshots as version 3.
	third := f.mustPublish(id, api.ArtifactPublishParams{Title: "Design again", PublicationID: "pub-4", BaseVersion: 2, RestoreFrom: 1}, nil, nil)
	if third.Version.Number != 3 || third.Version.RestoredFrom != 1 || third.Version.Platform != "atc v1" || third.Version.Title != "Design again" {
		t.Errorf("restoration = %+v", third.Version)
	}
	if got := readFile(t, filepath.Join(f.root, id, "3", "build", "index.html")); got != "<h1>one</h1>" {
		t.Errorf("restored index = %q", got)
	}
	if got := readFile(t, filepath.Join(f.root, id, "3", "source.tar.gz")); got != string(source("one")) {
		t.Error("restored source differs from version 1")
	}
	if _, _, err := f.publish(id, api.ArtifactPublishParams{Title: "x", PublicationID: "pub-5", BaseVersion: 3, RestoreFrom: 9}, nil, nil); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("restore unknown version = %v, want ErrVersionNotFound", err)
	}
	if _, _, err := f.publish(id, api.ArtifactPublishParams{Title: "x", PublicationID: "pub-6", BaseVersion: 3, RestoreFrom: 1}, build("<p>x</p>"), source("x")); err == nil {
		t.Error("restore with archives accepted")
	}

	// The reader resolves the current version by default, any version by
	// number, with the whole history.
	doc, err := f.service.Document(ctx, id, 0)
	if err != nil {
		t.Fatal(err)
	}
	if doc.Version.Number != 3 || !doc.Latest() || len(doc.Versions) != 3 || doc.BuildDir != filepath.Join(f.root, id, "3", "build") {
		t.Errorf("Document(latest) = %+v", doc)
	}
	if old, err := f.service.Document(ctx, id, 1); err != nil || old.Latest() || old.Version.Title != "Design" || old.Artifact.Title != "Design again" {
		t.Errorf("Document(1) = %+v, %v", old, err)
	}
	if _, err := f.service.Document(ctx, id, 4); !errors.Is(err, ErrVersionNotFound) {
		t.Errorf("Document(4) = %v, want ErrVersionNotFound", err)
	}
	if _, err := f.service.Document(ctx, "artf-nope1", 0); !errors.Is(err, ErrNotFound) {
		t.Errorf("Document(unknown) = %v, want ErrNotFound", err)
	}

	// Whole-artifact deletion removes history and content; the id is
	// not reusable by publication.
	if err := f.service.Delete(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(f.root, id)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("content after delete: %v", err)
	}
	if _, err := f.service.Get(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("Get after delete = %v", err)
	}
	if err := f.service.Delete(ctx, id); !errors.Is(err, ErrNotFound) {
		t.Errorf("second Delete = %v", err)
	}
	if _, _, err := f.publish(id, api.ArtifactPublishParams{Title: "x", PublicationID: "pub-7", BaseVersion: 3}, build("<p>x</p>"), source("x")); !errors.Is(err, ErrNotFound) {
		t.Errorf("publish to deleted = %v, want ErrNotFound", err)
	}
	if list, err := f.service.List(ctx, ""); err != nil || len(list) != 1 || list[0].ID != other.Artifact.ID {
		t.Errorf("List after delete = %+v, %v", list, err)
	}

	var got []string
	for len(got) < 5 {
		select {
		case change := <-sub.C:
			got = append(got, change.Type+" "+change.ID)
		case <-time.After(time.Second):
			t.Fatalf("events so far: %v", got)
		}
	}
	want := []string{
		api.EventArtifactCreated + " " + id, api.EventArtifactUpdated + " " + id,
		api.EventArtifactCreated + " " + other.Artifact.ID, api.EventArtifactUpdated + " " + id,
		api.EventArtifactDeleted + " " + id,
	}
	// The replayed publication and refused publications emit nothing.
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("events mismatch (-want +got):\n%s", diff)
	}
}

func TestPublishRefusesBadPackages(t *testing.T) {
	f := newFixture(t)
	good := build("<p>ok</p>")
	big := strings.Repeat("x", 5000)
	many := []entry{{name: "index.html", body: "i"}}
	for i := 0; i < 8; i++ {
		many = append(many, entry{name: "f" + string(rune('a'+i)), body: "x"})
	}
	for name, tc := range map[string]struct {
		build, source []byte
		detail        string
	}{
		"traversal":      {archive(entry{name: "../escape.html", body: "x"}, entry{name: "index.html", body: "x"}), source("s"), "safe relative path"},
		"absolute":       {archive(entry{name: "/etc/passwd", body: "x"}), source("s"), "safe relative path"},
		"symlink":        {archive(entry{name: "index.html", body: "x"}, entry{name: "link", typ: tar.TypeSymlink, link: "/etc/passwd"}), source("s"), "symlinks"},
		"hardlink":       {archive(entry{name: "index.html", body: "x"}, entry{name: "link", typ: tar.TypeLink, link: "index.html"}), source("s"), "symlinks"},
		"no index":       {archive(entry{name: "main.html", body: "x"}), source("s"), "index.html"},
		"nested index":   {archive(entry{name: "dist/index.html", body: "x"}), source("s"), "index.html"},
		"too many files": {archive(many...), source("s"), "more than 8 files"},
		"file too large": {archive(entry{name: "index.html", body: big}), source("s"), "exceeds 4096 bytes"},
		"total too large": {archive(entry{name: "index.html", body: strings.Repeat("a", 4000)}, entry{name: "b", body: strings.Repeat("b", 4000)},
			entry{name: "c", body: strings.Repeat("c", 4000)}), source("s"), "total size"},
		"not gzip":     {[]byte("<html>"), source("s"), "not gzip"},
		"empty source": {good, archive(entry{name: "dir/", typ: tar.TypeDir}), "no files"},
		"source escape": {good, archive(entry{name: "../../x", body: "x"}), "safe relative path"},
		"source symlink": {good, archive(entry{name: "x", typ: tar.TypeSymlink, link: "y"}), "symlinks"},
	} {
		var pkg *PackageError
		_, _, err := f.publish("", api.ArtifactPublishParams{Title: "t", PublicationID: "pub-" + name}, tc.build, tc.source)
		if !errors.As(err, &pkg) || !strings.Contains(pkg.Detail, tc.detail) {
			t.Errorf("%s: err = %v, want PackageError containing %q", name, err, tc.detail)
		}
	}
	if list, err := f.service.List(context.Background(), ""); err != nil || len(list) != 0 {
		t.Errorf("artifacts after refusals = %v, %v; want none", list, err)
	}
	if names := f.stagings(); len(names) != 0 {
		t.Errorf("stagings left behind: %v", names)
	}
	entries, _ := os.ReadDir(f.root)
	for _, e := range entries {
		if e.Name() != stagingDir {
			t.Errorf("unexpected content entry %q", e.Name())
		}
	}
	for name, params := range map[string]api.ArtifactPublishParams{
		"blank title":    {Title: "  ", PublicationID: "p"},
		"no publication": {Title: "t"},
		"restore on new": {Title: "t", PublicationID: "p", RestoreFrom: 1},
	} {
		var pkg *PackageError
		if _, _, err := f.publish("", params, good, source("s")); !errors.As(err, &pkg) {
			t.Errorf("%s: err = %v, want PackageError", name, err)
		}
	}
	var pkg *PackageError
	if _, _, err := f.publish("", api.ArtifactPublishParams{Title: "t", PublicationID: "p"}, good, nil); !errors.As(err, &pkg) {
		t.Errorf("missing source: err = %v, want PackageError", err)
	}
}

func TestUpdateMetadata(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	f.insertProject("proj-aaaaa")
	published := f.mustPublish("", api.ArtifactPublishParams{Title: "Design", PublicationID: "pub-1"}, build("<p>1</p>"), source("1"))
	id := published.Artifact.ID

	renamed, err := f.service.Update(ctx, id, api.ArtifactUpdateParams{Title: api.Some(" Renamed ")})
	if err != nil || renamed.Title != "Renamed" || renamed.URL != published.Artifact.URL {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	if version, err := f.service.Version(ctx, id, 1); err != nil || version.Title != "Design" {
		t.Errorf("version title after rename = %q, %v; want the recorded one", version.Title, err)
	}
	assigned, err := f.service.Update(ctx, id, api.ArtifactUpdateParams{ProjectID: api.Some("proj-aaaaa")})
	if err != nil || assigned.ProjectID != "proj-aaaaa" || assigned.Title != "Renamed" {
		t.Fatalf("assign = %+v, %v", assigned, err)
	}
	if list, err := f.service.List(ctx, "proj-aaaaa"); err != nil || len(list) != 1 {
		t.Errorf("List(project) = %v, %v", list, err)
	}
	if _, err := f.service.Update(ctx, id, api.ArtifactUpdateParams{ProjectID: api.Some("proj-nope1")}); !errors.Is(err, ErrProjectUnknown) {
		t.Errorf("assign unknown project = %v, want ErrProjectUnknown", err)
	}
	cleared, err := f.service.Update(ctx, id, api.ArtifactUpdateParams{ProjectID: api.Clear[string]()})
	if err != nil || cleared.ProjectID != "" {
		t.Fatalf("clear = %+v, %v", cleared, err)
	}
	for name, params := range map[string]api.ArtifactUpdateParams{
		"nothing":       {},
		"null title":    {Title: api.Clear[string]()},
		"blank title":   {Title: api.Some(" ")},
		"empty project": {ProjectID: api.Some("")},
	} {
		if _, err := f.service.Update(ctx, id, params); !errors.Is(err, ErrInvalidUpdate) {
			t.Errorf("%s: err = %v, want ErrInvalidUpdate", name, err)
		}
	}
	if _, err := f.service.Update(ctx, "artf-nope1", api.ArtifactUpdateParams{Title: api.Some("x")}); !errors.Is(err, ErrNotFound) {
		t.Errorf("update unknown = %v, want ErrNotFound", err)
	}
	if got := readFile(t, filepath.Join(f.root, id, "1", "build", "index.html")); got != "<p>1</p>" {
		t.Errorf("content changed by metadata edits: %q", got)
	}

	// Project deletion preserves the artifact, unassigned.
	if _, err := f.service.Update(ctx, id, api.ArtifactUpdateParams{ProjectID: api.Some("proj-aaaaa")}); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.store.Projects().Delete(ctx, "proj-aaaaa"); err != nil || !ok {
		t.Fatalf("delete project = %v, %v", ok, err)
	}
	if after, err := f.service.Get(ctx, id); err != nil || after.ProjectID != "" || after.CurrentVersion != 1 {
		t.Errorf("after project delete = %+v, %v", after, err)
	}
}

// Content the store does not know about — interrupted stagings, a
// version placed but never committed, an artifact deleted but not yet
// removed — disappears on the next start; committed content stays.
func TestReconcileOnStart(t *testing.T) {
	f := newFixture(t)
	published := f.mustPublish("", api.ArtifactPublishParams{Title: "Design", PublicationID: "pub-1"}, build("<p>1</p>"), source("1"))
	id := published.Artifact.ID
	plant := func(parts ...string) {
		dir := filepath.Join(append([]string{f.root}, parts...)...)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("stray"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	plant(stagingDir, "stage-abc", "build")
	plant(id, "2", "build")
	plant("artf-gone1", "1", "build")
	plant(id, "junk")

	f.service = f.open()
	for _, stray := range [][]string{{stagingDir, "stage-abc"}, {id, "2"}, {"artf-gone1"}, {id, "junk"}} {
		if _, err := os.Stat(filepath.Join(append([]string{f.root}, stray...)...)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%v survived reconcile: %v", stray, err)
		}
	}
	if got := readFile(t, filepath.Join(f.root, id, "1", "build", "index.html")); got != "<p>1</p>" {
		t.Errorf("committed content after reconcile = %q", got)
	}
	if doc, err := f.service.Document(context.Background(), id, 0); err != nil || doc.Version.Number != 1 {
		t.Errorf("Document after restart = %+v, %v", doc, err)
	}
}

// Two publishers on the same base: exactly one wins, the other learns
// the current version; nothing partial is visible.
func TestConcurrentPublishers(t *testing.T) {
	f := newFixture(t)
	published := f.mustPublish("", api.ArtifactPublishParams{Title: "Design", PublicationID: "pub-1"}, build("<p>1</p>"), source("1"))
	id := published.Artifact.ID
	results := make(chan error, 2)
	for _, name := range []string{"a", "b"} {
		go func() {
			_, _, err := f.publish(id, api.ArtifactPublishParams{Title: name, PublicationID: "pub-" + name, BaseVersion: 1},
				build("<p>"+name+"</p>"), source(name))
			results <- err
		}()
	}
	var outcomes []string
	for range 2 {
		err := <-results
		var stale *StaleBaseError
		switch {
		case err == nil:
			outcomes = append(outcomes, "won")
		case errors.As(err, &stale) && stale.Current == 2:
			outcomes = append(outcomes, "stale")
		default:
			t.Errorf("unexpected outcome: %v", err)
		}
	}
	sort.Strings(outcomes)
	if diff := cmp.Diff([]string{"stale", "won"}, outcomes); diff != "" {
		t.Errorf("outcomes mismatch (-want +got):\n%s", diff)
	}
	versions, err := f.service.Versions(context.Background(), id)
	if err != nil || len(versions) != 2 {
		t.Fatalf("Versions = %v, %v; want 2", versions, err)
	}
	entries, _ := os.ReadDir(filepath.Join(f.root, id))
	if len(entries) != 2 {
		t.Errorf("content dirs = %d, want 2", len(entries))
	}
	if names := f.stagings(); len(names) != 0 {
		t.Errorf("stagings left behind: %v", names)
	}
}
