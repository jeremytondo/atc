package documents

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/artifacts"
	"github.com/jeremytondo/atc/internal/tailscale"
)

// fakeResolver serves one artifact with two versions from temp dirs.
type fakeResolver struct {
	t     *testing.T
	root  string
	title string
}

const (
	indexOne = `<!doctype html><html><head><meta charset="utf-8"><script type="module" crossorigin src="./assets/index-1.js"></script><link rel="stylesheet" href="./assets/index-1.css"></head><body><div id="root"></div></body></html>`
	indexTwo = `<html><HEAD><script src="./assets/index-2.js"></script></HEAD><body>two</body></html>`
)

func newResolver(t *testing.T) *fakeResolver {
	t.Helper()
	root := t.TempDir()
	write := func(parts ...string) {
		p := filepath.Join(append([]string{root}, parts[:len(parts)-1]...)...)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(parts[len(parts)-1]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write("1", "index.html", indexOne)
	write("1", "assets", "index-1.js", "console.log('one')")
	write("1", "assets", "index-1.css", "body{}")
	write("1", "assets", "fonts", "a.woff2", "font")
	write("2", "index.html", indexTwo)
	write("2", "assets", "index-2.js", "console.log('two')")
	return &fakeResolver{t: t, root: root, title: "Renamed"}
}

func (f *fakeResolver) Document(_ context.Context, id string, number int) (artifacts.Document, error) {
	if id != "artf-aaaaa" {
		return artifacts.Document{}, artifacts.ErrNotFound
	}
	if number == 0 {
		number = 2
	}
	if number > 2 {
		return artifacts.Document{}, artifacts.ErrVersionNotFound
	}
	at := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	versions := []api.ArtifactVersion{
		{ArtifactID: id, Number: 1, Title: "One", PublishedAt: at, Platform: "atc v1", Source: api.ArtifactSource{Thread: "thrd-aaaaa"}},
		{ArtifactID: id, Number: 2, Title: "Two", PublishedAt: at.Add(time.Hour), RestoredFrom: 1},
	}
	return artifacts.Document{
		Artifact: api.Artifact{ID: id, Title: f.title, CurrentVersion: 2},
		Version:  versions[number-1],
		Versions: versions,
		BuildDir: filepath.Join(f.root, map[int]string{1: "1", 2: "2"}[number]),
	}, nil
}

func get(t *testing.T, h http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func metadata(t *testing.T, page string) Metadata {
	t.Helper()
	start := strings.Index(page, `<script id="atc-document" type="application/json">`)
	if start < 0 {
		t.Fatalf("no metadata script in page:\n%s", page)
	}
	rest := page[start+len(`<script id="atc-document" type="application/json">`):]
	end := strings.Index(rest, "</script>")
	var m Metadata
	if err := json.Unmarshal([]byte(rest[:end]), &m); err != nil {
		t.Fatalf("decoding metadata %q: %v", rest[:end], err)
	}
	return m
}

func TestReaderServesVersionsWithPinnedAssets(t *testing.T) {
	h := Handler(newResolver(t), nil)

	// The main link resolves to the current version, its assets pinned to
	// that version's permanent path, and is never cached.
	rec := get(t, h, "/a/artf-aaaaa/")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("latest: %d %v", rec.Code, rec.Header())
	}
	page := rec.Body.String()
	if !strings.Contains(page, `src="/a/artf-aaaaa/v/2/assets/index-2.js"`) {
		t.Errorf("latest assets not pinned:\n%s", page)
	}
	if strings.Index(page, `<script id="atc-document"`) > strings.Index(page, "</HEAD>") {
		t.Errorf("metadata not inside head:\n%s", page)
	}
	want := Metadata{
		ArtifactID: "artf-aaaaa", Title: "Renamed", CurrentVersion: 2, LatestPath: "/a/artf-aaaaa/", Latest: true,
		Version: Version{Number: 2, Title: "Two", PublishedAt: time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC), RestoredFrom: 1, Path: "/a/artf-aaaaa/v/2/"},
		Versions: []Version{
			{Number: 1, Title: "One", PublishedAt: time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC), Platform: "atc v1", Source: api.ArtifactSource{Thread: "thrd-aaaaa"}, Path: "/a/artf-aaaaa/v/1/"},
			{Number: 2, Title: "Two", PublishedAt: time.Date(2026, 9, 9, 13, 0, 0, 0, time.UTC), RestoredFrom: 1, Path: "/a/artf-aaaaa/v/2/"},
		},
	}
	if diff := cmp.Diff(want, metadata(t, page)); diff != "" {
		t.Errorf("latest metadata mismatch (-want +got):\n%s", diff)
	}

	// A fixed link shows its own version, marked as not the latest, with
	// its recorded title and the artifact's current one.
	page = get(t, h, "/a/artf-aaaaa/v/1/").Body.String()
	old := metadata(t, page)
	if old.Latest || old.Version.Number != 1 || old.Version.Title != "One" || old.Title != "Renamed" {
		t.Errorf("version 1 metadata = %+v", old)
	}
	if !strings.Contains(page, `src="/a/artf-aaaaa/v/1/assets/index-1.js"`) || !strings.Contains(page, `href="/a/artf-aaaaa/v/1/assets/index-1.css"`) {
		t.Errorf("version 1 assets not pinned:\n%s", page)
	}
	if page2 := get(t, h, "/a/artf-aaaaa/v/1/index.html").Body.String(); page2 != page {
		t.Error("index.html under the version path differs from the page")
	}

	// Assets come from the version's build, immutable, nested paths
	// included.
	asset := get(t, h, "/a/artf-aaaaa/v/1/assets/index-1.js")
	if asset.Code != http.StatusOK || asset.Body.String() != "console.log('one')" || !strings.Contains(asset.Header().Get("Content-Type"), "javascript") {
		t.Errorf("asset: %d %q %v", asset.Code, asset.Body, asset.Header())
	}
	if cc := asset.Header().Get("Cache-Control"); cc != "public, max-age=31536000, immutable" {
		t.Errorf("asset Cache-Control = %q", cc)
	}
	if font := get(t, h, "/a/artf-aaaaa/v/1/assets/fonts/a.woff2"); font.Code != http.StatusOK || font.Body.String() != "font" {
		t.Errorf("nested asset: %d %q", font.Code, font.Body)
	}

	// The mux redirects a traversing path to its clean form, which then
	// misses; either way nothing outside the build is served.
	if rec := get(t, h, "/a/artf-aaaaa/v/1/../../../etc/passwd"); rec.Code == http.StatusOK {
		t.Errorf("traversal served: %q", rec.Body)
	}
	for _, path := range []string{"/a/artf-aaaaa/v/2/assets/index-1.js", "/a/artf-aaaaa/v/1/assets/",
		"/a/artf-aaaaa/v/3/", "/a/artf-aaaaa/v/0/", "/a/artf-aaaaa/v/x/", "/a/artf-nope1/", "/a/artf-aaaaa/assets/index-2.js", "/nothing"} {
		if rec := get(t, h, path); rec.Code != http.StatusNotFound {
			t.Errorf("%s: %d, want 404", path, rec.Code)
		}
	}
	for from, to := range map[string]string{"/a/artf-aaaaa": "/a/artf-aaaaa/", "/a/artf-aaaaa/v/1": "/a/artf-aaaaa/v/1/"} {
		if rec := get(t, h, from); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != to {
			t.Errorf("%s: %d -> %q, want redirect to %s", from, rec.Code, rec.Header().Get("Location"), to)
		}
	}
	if rec := get(t, h, "/"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "ATC document origin") {
		t.Errorf("root: %d %q", rec.Code, rec.Body)
	}
}

// Every response carries the browser policy: only published assets,
// no live network, no embedding — errors included.
func TestReaderResponsePolicy(t *testing.T) {
	h := Handler(newResolver(t), nil)
	for _, path := range []string{"/a/artf-aaaaa/", "/a/artf-aaaaa/v/1/assets/index-1.js", "/a/artf-nope1/", "/"} {
		header := get(t, h, path).Header()
		csp := header.Get("Content-Security-Policy")
		for _, directive := range []string{"default-src 'none'", "script-src 'self'", "connect-src 'none'", "worker-src 'none'", "frame-src 'none'", "form-action 'none'", "frame-ancestors 'none'", "base-uri 'none'"} {
			if !strings.Contains(csp, directive) {
				t.Errorf("%s: CSP %q lacks %s", path, csp, directive)
			}
		}
		if header.Get("X-Content-Type-Options") != "nosniff" || header.Get("Referrer-Policy") != "no-referrer" || header.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s: headers %v", path, header)
		}
	}
}

// Render escapes the metadata so a title cannot close the script
// element, and injects at the start of the document when there is no
// head.
func TestRenderEscapesAndFallsBack(t *testing.T) {
	doc := artifacts.Document{
		Artifact: api.Artifact{ID: "artf-aaaaa", Title: `</script><script>alert(1)</script>`, CurrentVersion: 1},
		Version:  api.ArtifactVersion{ArtifactID: "artf-aaaaa", Number: 1, Title: "x"},
		Versions: []api.ArtifactVersion{{ArtifactID: "artf-aaaaa", Number: 1, Title: "x"}},
	}
	page, err := Render([]byte(`<div>no head</div>`), doc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page, "</script><script>alert") || !strings.HasPrefix(page, `<script id="atc-document"`) {
		t.Errorf("page:\n%s", page)
	}
	if m := metadata(t, page); m.Title != `</script><script>alert(1)</script>` {
		t.Errorf("title round trip = %q", m.Title)
	}
}

// The listener's failure is a reported state, retried, never a crash;
// once bound the origin is ready at the actual port.
func TestServiceReportsBindFailureAndRecovers(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	s := New(Options{
		Resolver: newResolver(t), Bind: "127.0.0.1", Port: 0,
		listen: func(network, address string) (net.Listener, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			if attempts == 1 {
				return nil, errors.New("address already in use")
			}
			return net.Listen(network, address)
		},
	})
	if status := s.Status(context.Background()); status.State != api.DocumentsStarting || status.URL != "http://127.0.0.1:0" || status.Tailnet.State != api.TailnetDisabled {
		t.Fatalf("initial status = %+v", status)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()

	var status api.Documents
	sawUnavailable := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status = s.Status(ctx)
		if status.State == api.DocumentsUnavailable {
			sawUnavailable = true
			if !strings.Contains(status.Reason, "address already in use") {
				t.Errorf("unavailable reason = %q", status.Reason)
			}
		}
		if status.State == api.DocumentsReady {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if status.State != api.DocumentsReady || !sawUnavailable {
		t.Fatalf("status = %+v, sawUnavailable=%v", status, sawUnavailable)
	}
	local, tailnet := s.Bases()
	if local != status.URL || tailnet != "" || strings.HasSuffix(local, ":0") {
		t.Errorf("Bases = %q, %q; status URL %q", local, tailnet, status.URL)
	}
	resp, err := http.Get(status.URL + "/a/artf-aaaaa/v/1/assets/index-1.js")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(body) != "console.log('one')" {
		t.Errorf("served asset: %d %q", resp.StatusCode, body)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if status := s.Status(context.Background()); status.State != api.DocumentsStarting || status.Reason != "stopped" {
		t.Errorf("status after stop = %+v", status)
	}
	if _, err := http.Get(status.URL + "/"); err == nil {
		t.Error("listener still answering after stop")
	}
}

// The observer maps exposure reports onto the tailnet status, and the
// tailnet base joins Bases only while serving.
func TestTailnetObservation(t *testing.T) {
	s := New(Options{Resolver: newResolver(t), Bind: "127.0.0.1", Port: 7332, TailscaleExecutable: "/usr/bin/tailscale"})
	if status := s.Status(context.Background()); status.Tailnet.State != api.TailnetStarting {
		t.Fatalf("initial tailnet = %+v", status.Tailnet)
	}
	s.observe(tailscale.Report{URL: "https://node.ts.net:7332", Problem: "tailscale is logged out", Action: "run tailscale up"})
	status := s.Status(context.Background())
	want := api.DocumentsTailnet{State: api.TailnetStarting, URL: "https://node.ts.net:7332", Reason: "tailscale is logged out", Action: "run tailscale up"}
	if diff := cmp.Diff(want, status.Tailnet); diff != "" {
		t.Errorf("tailnet mismatch (-want +got):\n%s", diff)
	}
	if _, tailnet := s.Bases(); tailnet != "" {
		t.Errorf("tailnet base while converging = %q", tailnet)
	}
	s.observe(tailscale.Report{Serving: true, URL: "https://node.ts.net:7332"})
	if local, tailnet := s.Bases(); local != "http://127.0.0.1:7332" || tailnet != "https://node.ts.net:7332" {
		t.Errorf("Bases = %q, %q", local, tailnet)
	}
}

func TestLocalURL(t *testing.T) {
	for bind, want := range map[string]string{"127.0.0.1": "http://127.0.0.1:7332", "0.0.0.0": "http://127.0.0.1:7332", "::": "http://127.0.0.1:7332",
		"localhost": "http://127.0.0.1:7332", "192.168.1.5": "http://192.168.1.5:7332", "::1": "http://[::1]:7332"} {
		s := New(Options{Resolver: newResolver(t), Bind: bind, Port: 7332})
		if got := s.Status(context.Background()).URL; got != want {
			t.Errorf("bind %s: URL = %q, want %q", bind, got, want)
		}
	}
}
