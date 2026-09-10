// Package authoring is the artifact authoring environment (ATC-318): the
// shared React/Vite platform carried inside the binary, a private Node
// runtime and the platform's fixed dependencies provisioned lazily under
// the authoring directory, and the working copies agents edit, preview,
// build, and publish from. Only the current platform is installed;
// working copies pick it up whenever they are opened or built, and a
// published version never depends on it again. Nothing here runs on the
// serving side — publication is an upload through the API.
package authoring

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The platform source shipped with the binary: exactly the files a working
// copy is made of. Listed explicitly so a developer's node_modules or dist
// beside them can never be embedded.
//
//go:embed platform/package.json platform/package-lock.json platform/index.html platform/vite.config.ts platform/tsconfig.json platform/src platform/examples
var embedded embed.FS

// platform is the embedded platform rooted at its own directory.
var platform = func() fs.FS {
	sub, err := fs.Sub(embedded, "platform")
	if err != nil {
		panic(err)
	}
	return sub
}()

const (
	// NodeVersion is the pinned private runtime. Node's own SHASUMS256.txt
	// verifies the download; the version changes only with ATC.
	NodeVersion = "24.21.0"
	nodeDist    = "https://nodejs.org/dist/v" + NodeVersion + "/"

	// documentDir is the part of a working copy the author owns; everything
	// else is the platform and is resynced from the binary. Only this
	// directory and the platform go into a build and its source archive.
	documentDir = "src/document"
	// examplesDir holds the worked examples a working copy can start from.
	examplesDir = "examples"
	// scratchDir is a working copy's local state: its record, lock, build
	// scratch, and pending publication. Never part of a build.
	scratchDir = ".atc"
)

// Options wires the service.
type Options struct {
	// Root is the authoring directory (paths.AuthoringDir).
	Root string
	// Version is the ATC build identity, recorded on publications as part
	// of the platform identity.
	Version string
	// Fetch downloads a URL; nil means plain HTTP. The runtime install's
	// network seam.
	Fetch func(ctx context.Context, url string) (io.ReadCloser, error)
	// Output receives the runtime's and the tools' own output (install
	// progress, type errors, build logs); nil discards it.
	Output io.Writer
	// Logger receives what is worth a note but not the author's attention
	// (an unreadable working copy record); nil discards it.
	Logger *slog.Logger
	// Now is the clock; nil means time.Now.
	Now func() time.Time
}

// Service is the authoring environment.
type Service struct {
	root    string
	version string
	fetch   func(ctx context.Context, url string) (io.ReadCloser, error)
	output  io.Writer
	logger  *slog.Logger
	now     func() time.Time
	// identity digests the platform files a build is made of; deps
	// digests the dependency manifest and lockfile. The first names the
	// platform on publications and decides when working copies resync,
	// the second when the installed dependencies must be replaced.
	identity string
	deps     string
}

// New builds the service.
func New(opts Options) (*Service, error) {
	if opts.Fetch == nil {
		opts.Fetch = fetchHTTP
	}
	if opts.Output == nil {
		opts.Output = io.Discard
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	identity, err := digest(isPlatformFile)
	if err != nil {
		return nil, err
	}
	deps, err := digest(func(p string) bool { return p == "package.json" || p == "package-lock.json" })
	if err != nil {
		return nil, err
	}
	return &Service{
		root: opts.Root, version: opts.Version, fetch: opts.Fetch, output: opts.Output, logger: opts.Logger,
		now: opts.Now, identity: identity, deps: deps,
	}, nil
}

// PlatformID identifies the shipped platform as publications record it:
// the ATC version and the platform digest.
func (s *Service) PlatformID() string {
	return "atc " + s.version + " platform " + s.identity
}

// isPlatformFile reports whether an embedded path is part of the platform
// a build is made of — everything but the starter document and the
// examples, which only seed working copies.
func isPlatformFile(p string) bool {
	return p != documentDir && !strings.HasPrefix(p, documentDir+"/") && p != examplesDir && !strings.HasPrefix(p, examplesDir+"/")
}

// Example is a worked example a working copy can start from.
type Example struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// Examples lists the shipped examples, by name.
func Examples() []Example {
	entries, err := fs.ReadDir(platform, examplesDir)
	if err != nil {
		return nil
	}
	var examples []Example
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		example := Example{Name: entry.Name(), Title: entry.Name()}
		if data, err := fs.ReadFile(platform, path.Join(examplesDir, entry.Name(), "example.json")); err == nil {
			_ = json.Unmarshal(data, &example)
			example.Name = entry.Name()
		}
		examples = append(examples, example)
	}
	return examples
}

// ExampleNames lists the shipped examples' names, for help text.
func ExampleNames() []string {
	var names []string
	for _, example := range Examples() {
		names = append(names, example.Name)
	}
	return names
}

// digest hashes the embedded files include selects, by path and content.
func digest(include func(string) bool) (string, error) {
	var paths []string
	if err := fs.WalkDir(platform, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && include(p) {
			paths = append(paths, p)
		}
		return nil
	}); err != nil {
		return "", err
	}
	sort.Strings(paths)
	hash := sha256.New()
	for _, p := range paths {
		content, err := fs.ReadFile(platform, p)
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(hash, "%s\x00%d\x00", p, len(content))
		hash.Write(content)
	}
	return hex.EncodeToString(hash.Sum(nil))[:12], nil
}

// downloadClient bounds connecting and the wait for headers, not the
// whole transfer: the runtime archive is large and its pace is the
// network's.
var downloadClient = &http.Client{
	Transport: &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   30 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	},
}

func fetchHTTP(ctx context.Context, url string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := downloadClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return resp.Body, nil
}

// writePlatformFiles writes the platform's files into dir. The platform
// source directory is assembled beside its place and swapped in whole,
// so a copy never holds a half-written or stale mix; the fixed root
// files are written through temporary files, never through a symlink an
// author may have left in their place.
func writePlatformFiles(dir string) error {
	staging := filepath.Join(dir, "src", ".platform-next")
	if err := os.RemoveAll(staging); err != nil {
		return err
	}
	err := fs.WalkDir(platform, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !isPlatformFile(p) {
			return err
		}
		target := filepath.Join(dir, filepath.FromSlash(p))
		if strings.HasPrefix(p, "src/platform/") {
			target = filepath.Join(staging, filepath.FromSlash(strings.TrimPrefix(p, "src/platform/")))
		}
		content, err := fs.ReadFile(platform, p)
		if err != nil {
			return err
		}
		return writeFile(target, content)
	})
	if err != nil {
		return err
	}
	current := filepath.Join(dir, "src", "platform")
	previous := filepath.Join(dir, "src", ".platform-previous")
	if err := os.RemoveAll(previous); err != nil {
		return err
	}
	if err := os.Rename(current, previous); err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.Rename(staging, current); err != nil {
		return err
	}
	return os.RemoveAll(previous)
}

// writeFile replaces the file at target atomically, whatever is there
// now (a symlink included: the link itself goes, not what it points to).
func writeFile(target string, content []byte) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(target), "."+filepath.Base(target)+".*")
	if err != nil {
		return err
	}
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return err
	}
	return os.Rename(tmp.Name(), target)
}

// copyTree copies the regular files under src in fsys into dir.
func copyTree(fsys fs.FS, src, dir string) error {
	return fs.WalkDir(fsys, src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(filepath.FromSlash(src), filepath.FromSlash(p))
		if err != nil {
			return err
		}
		target := filepath.Join(dir, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		content, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		return os.WriteFile(target, content, 0o600)
	})
}
