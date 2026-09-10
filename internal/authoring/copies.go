package authoring

// Working copies: <root>/working-copies/<id>, each a complete Vite project
// — the shipped platform files, the author's src/document, and a
// node_modules link into the shared installation — plus .atc/ holding the
// copy's record, its lock, build scratch, and any pending publication.
// They persist until discarded, publication included; several may target
// one artifact. Opening or building a copy resyncs the platform files, so
// old copies always author against the current platform and any
// incompatibility surfaces as a check error. A copy is only ever
// resolved inside the copies directory, so no other directory can be
// mistaken for one and discarded.

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/jeremytondo/atc/internal/ids"
)

const (
	copiesDir  = "working-copies"
	idPrefix   = "copy-"
	recordFile = "copy.json"
	copyLock   = "lock"
	// A retrieved document is bounded like an upload: the archive comes
	// over the network.
	maxDocumentFile  = 64 << 20
	maxDocumentFiles = 4096
)

// ErrCopyNotFound: no working copy has that id or contains that path.
var ErrCopyNotFound = errors.New("working copy not found")

// Copy is a working copy's record.
type Copy struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Artifact and BaseVersion name the publication target: the artifact
	// the copy revises and the version it is based on. Empty until the
	// copy is published or when it was not started from a publication.
	Artifact    string `json:"artifact,omitempty"`
	BaseVersion int    `json:"baseVersion,omitempty"`
	// Example names the worked example the copy started from.
	Example   string    `json:"example,omitempty"`
	CreatedAt time.Time `json:"createdAt"`
	// Dir is where the copy lives; Document the directory the author
	// edits. Not stored.
	Dir      string `json:"-"`
	Document string `json:"-"`
	// Pending is the retry identity of a publication attempted but not
	// confirmed; the next publish resends it. Not stored in the record
	// (the pending package is).
	Pending string `json:"-"`
}

// CreateOptions shape a new working copy.
type CreateOptions struct {
	// Title defaults to the example's title, or "Untitled document".
	Title string
	// Example seeds src/document from a worked example; "" seeds the
	// starter document.
	Example string
	// Source seeds src/document from a published version's source archive
	// (the API's source download); Artifact and BaseVersion then record
	// what it revises.
	Source      io.Reader
	Artifact    string
	BaseVersion int
}

// Create makes a working copy: platform files, the document, and the
// dependency link.
func (s *Service) Create(ctx context.Context, opts CreateOptions) (Copy, error) {
	if opts.Example != "" {
		known := false
		for _, example := range Examples() {
			if example.Name == opts.Example {
				known = true
				if opts.Title == "" {
					opts.Title = example.Title
				}
			}
		}
		if !known {
			return Copy{}, fmt.Errorf("no example named %q (examples: %s)", opts.Example, strings.Join(ExampleNames(), ", "))
		}
	}
	if opts.Title == "" {
		opts.Title = "Untitled document"
	}
	inst, unlock, err := s.ensurePlatform(ctx)
	if err != nil {
		return Copy{}, err
	}
	defer unlock()
	if err := os.MkdirAll(filepath.Join(s.root, copiesDir), 0o700); err != nil {
		return Copy{}, err
	}
	var dir, id string
	for {
		id = ids.New(idPrefix)
		dir = filepath.Join(s.root, copiesDir, id)
		if err := os.Mkdir(dir, 0o700); err == nil {
			break
		} else if !errors.Is(err, fs.ErrExist) {
			return Copy{}, err
		}
	}
	copy := Copy{
		ID: id, Title: opts.Title, Artifact: opts.Artifact, BaseVersion: opts.BaseVersion, Example: opts.Example,
		CreatedAt: s.now(), Dir: dir, Document: filepath.Join(dir, filepath.FromSlash(documentDir)),
	}
	err = errors.Join(os.MkdirAll(filepath.Join(dir, scratchDir), 0o700), s.seed(copy.Document, opts), syncCopy(dir, inst), writeRecord(copy))
	if err != nil {
		_ = os.RemoveAll(dir)
		return Copy{}, err
	}
	return copy, nil
}

// seed writes the document the copy starts from.
func (s *Service) seed(document string, opts CreateOptions) error {
	switch {
	case opts.Source != nil:
		return extractDocument(opts.Source, document)
	case opts.Example != "":
		return copyTree(platform, path.Join(examplesDir, opts.Example), document)
	default:
		return copyTree(platform, documentDir, document)
	}
}

// extractDocument takes src/document out of a source archive: regular
// files only, at safe paths, nothing else from the snapshot — the rest of
// it is the platform of its time, which the copy replaces with the
// current one.
func extractDocument(archive io.Reader, dir string) error {
	gz, err := gzip.NewReader(archive)
	if err != nil {
		return fmt.Errorf("source archive is not gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	reader := tar.NewReader(gz)
	files := 0
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("reading source archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			continue
		}
		name := path.Clean(strings.TrimPrefix(header.Name, "./"))
		rel, ok := strings.CutPrefix(name, documentDir+"/")
		if !ok || !fs.ValidPath(rel) {
			continue
		}
		if header.Size > maxDocumentFile {
			return fmt.Errorf("%s in the source archive is implausibly large", header.Name)
		}
		if files >= maxDocumentFiles {
			return fmt.Errorf("the source archive has more than %d document files", maxDocumentFiles)
		}
		target := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return err
		}
		if _, err := io.Copy(out, reader); err != nil {
			_ = out.Close()
			return err
		}
		if err := out.Close(); err != nil {
			return err
		}
		files++
	}
	if files == 0 {
		return errors.New("source archive has no " + documentDir + " files")
	}
	return nil
}

// syncCopy brings a copy's platform files and dependency link to the current
// installation. The link is replaced atomically.
func syncCopy(dir string, inst installation) error {
	if err := writePlatformFiles(dir); err != nil {
		return err
	}
	link := filepath.Join(dir, "node_modules")
	if existing, err := os.Readlink(link); err == nil && existing == inst.modules {
		return nil
	}
	// Whatever an author left in its place (an npm install, say) goes:
	// only the link belongs here.
	if info, err := os.Lstat(link); err == nil && info.Mode()&os.ModeSymlink == 0 {
		if err := os.RemoveAll(link); err != nil {
			return err
		}
	}
	tmp := filepath.Join(dir, ".node_modules-"+ids.NewLong(""))
	if err := os.Symlink(inst.modules, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Open finds a working copy by id or by a path inside it and brings it to
// the current platform.
func (s *Service) Open(ctx context.Context, ref string) (Copy, error) {
	copy, _, release, err := s.prepare(ctx, ref)
	if err != nil {
		return Copy{}, err
	}
	release()
	return copy, nil
}

// prepare is what every operation on a copy starts with: the copy
// resolved and locked against other ATC processes, the installation
// current and held, and the copy synced to it. release undoes the locks.
func (s *Service) prepare(ctx context.Context, ref string) (Copy, installation, func(), error) {
	dir, err := s.locate(ref)
	if err != nil {
		return Copy{}, installation{}, nil, err
	}
	lock, err := os.OpenFile(filepath.Join(dir, scratchDir, copyLock), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return Copy{}, installation{}, nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return Copy{}, installation{}, nil, err
	}
	// Read under the lock: another process may have published or retitled
	// the copy since the caller looked.
	copy, err := readRecord(dir)
	if err != nil {
		_ = lock.Close()
		return Copy{}, installation{}, nil, err
	}
	inst, unlock, err := s.ensurePlatform(ctx)
	if err != nil {
		_ = lock.Close()
		return Copy{}, installation{}, nil, err
	}
	release := func() {
		unlock()
		_ = lock.Close()
	}
	if err := syncCopy(dir, inst); err != nil {
		release()
		return Copy{}, installation{}, nil, err
	}
	return copy, inst, release, nil
}

// Get reads a working copy without touching it.
func (s *Service) Get(ref string) (Copy, error) {
	dir, err := s.locate(ref)
	if err != nil {
		return Copy{}, err
	}
	return readRecord(dir)
}

// locate resolves an id, or a path at or under a working copy, to the
// copy's directory — always an immediate child of the copies directory
// whose record names it, never anywhere else.
func (s *Service) locate(ref string) (string, error) {
	copies := filepath.Join(s.root, copiesDir)
	var id string
	if strings.HasPrefix(ref, idPrefix) && !strings.ContainsAny(ref, `/\`) {
		id = ref
	} else {
		abs, err := filepath.Abs(ref)
		if err != nil {
			return "", err
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrCopyNotFound, ref)
		}
		root, err := filepath.EvalSymlinks(copies)
		if err != nil {
			return "", fmt.Errorf("%w: %s", ErrCopyNotFound, ref)
		}
		rel, err := filepath.Rel(root, resolved)
		if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
			return "", fmt.Errorf("%w: %s is not inside a working copy", ErrCopyNotFound, ref)
		}
		id = strings.SplitN(filepath.ToSlash(rel), "/", 2)[0]
	}
	dir := filepath.Join(copies, id)
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%w: %s", ErrCopyNotFound, ref)
	}
	copy, err := readRecord(dir)
	if err != nil {
		return "", err
	}
	if copy.ID != id {
		return "", fmt.Errorf("%w: %s records a different id", ErrCopyNotFound, dir)
	}
	return dir, nil
}

// List returns every working copy, oldest first.
func (s *Service) List() ([]Copy, error) {
	entries, err := os.ReadDir(filepath.Join(s.root, copiesDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var copies []Copy
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		copy, err := readRecord(filepath.Join(s.root, copiesDir, entry.Name()))
		if err != nil {
			s.logger.Warn("unreadable working copy", "dir", entry.Name(), "error", err)
			continue
		}
		copies = append(copies, copy)
	}
	sort.Slice(copies, func(i, j int) bool {
		if !copies[i].CreatedAt.Equal(copies[j].CreatedAt) {
			return copies[i].CreatedAt.Before(copies[j].CreatedAt)
		}
		return copies[i].ID < copies[j].ID
	})
	return copies, nil
}

// Discard removes a working copy. Published versions are untouched.
func (s *Service) Discard(ref string) error {
	dir, err := s.locate(ref)
	if err != nil {
		return err
	}
	return os.RemoveAll(dir)
}

func readRecord(dir string) (Copy, error) {
	data, err := os.ReadFile(filepath.Join(dir, scratchDir, recordFile))
	if errors.Is(err, fs.ErrNotExist) {
		return Copy{}, ErrCopyNotFound
	}
	if err != nil {
		return Copy{}, err
	}
	var copy Copy
	if err := json.Unmarshal(data, &copy); err != nil {
		return Copy{}, fmt.Errorf("%s: %w", filepath.Join(dir, scratchDir, recordFile), err)
	}
	if copy.ID == "" {
		return Copy{}, fmt.Errorf("%s: no id", filepath.Join(dir, scratchDir, recordFile))
	}
	copy.Dir = dir
	copy.Document = filepath.Join(dir, filepath.FromSlash(documentDir))
	if params, err := readPending(dir); err == nil {
		copy.Pending = params.Params.PublicationID
	}
	return copy, nil
}

// writeRecord replaces the record atomically.
func writeRecord(copy Copy) error {
	encoded, err := json.MarshalIndent(copy, "", "  ")
	if err != nil {
		return err
	}
	return writeFile(filepath.Join(copy.Dir, scratchDir, recordFile), append(encoded, '\n'))
}
