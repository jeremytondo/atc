// Package fs implements Atelier Code's read-only remote filesystem browsing behind
// GET /api/fs/list.
package fs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Entry kinds. Anything that is neither a listable directory nor an ordinary
// file (broken symlinks, sockets, FIFOs, devices) is KindUnknown: visible in
// listings, not enterable.
const (
	KindDirectory = "directory"
	KindFile      = "file"
	KindUnknown   = "unknown"
)

const (
	// defaultListTimeout bounds one List call end to end.
	defaultListTimeout = 10 * time.Second
	// maxListEntries caps a single listing to protect response size and client
	// memory; it counts entries after hidden filtering.
	maxListEntries = 10_000
	// ctxCheckInterval is how often the per-entry stat pass polls for
	// cancellation.
	ctxCheckInterval = 1_000
)

var (
	ErrInvalidPath      = errors.New("invalid path")
	ErrNotFound         = errors.New("not found")
	ErrNotDirectory     = errors.New("not a directory")
	ErrPermissionDenied = errors.New("permission denied")
)

// Entry is one child of a listed directory. Path is the lexical listing path
// plus the entry name; path strings are entry identity.
type Entry struct {
	Name      string
	Path      string
	Kind      string
	IsSymlink bool
	// Size is set only for KindFile (of the symlink target when applicable).
	Size *int64
	// ModifiedAt is set for KindFile and KindDirectory, never KindUnknown.
	ModifiedAt *time.Time
}

// Listing is the result of listing one directory. Path is the cleaned lexical
// path that was listed, never symlink-resolved.
type Listing struct {
	Path      string
	Entries   []Entry
	Truncated bool
}

// Service answers directory-listing queries over the host filesystem.
type Service struct {
	home    string
	homeErr error
	logger  *slog.Logger
	// timeout and maxEntries are fields (not consts) so tests can inject tiny
	// values; they are not exposed in config.
	timeout    time.Duration
	maxEntries int
}

// NewService builds a Service. Empty list paths default to the user's home
// directory; explicit absolute paths may browse anywhere the process can read.
func NewService(logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	s := &Service{logger: logger, timeout: defaultListTimeout, maxEntries: maxListEntries}
	home, err := os.UserHomeDir()
	if err != nil {
		s.homeErr = fmt.Errorf("resolve home directory: %w", err)
		return s
	}
	s.home = filepath.Clean(home)
	return s
}

// List returns the immediate children of one directory. It is directory-only
// and never recursive; symlink cycles cannot hang the service because each
// request lists exactly one directory.
func (s *Service) List(ctx context.Context, path string, showHidden bool) (Listing, error) {
	cleaned, err := s.normalizePath(path)
	if err != nil {
		return Listing{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return Listing{}, fmt.Errorf("list %s: %w", cleaned, err)
	}

	dirEntries, err := readDir(ctx, cleaned)
	if err != nil {
		s.logger.Debug("fs list failed", "path", cleaned, "err", err)
		return Listing{}, err
	}

	entries := make([]Entry, 0, len(dirEntries))
	for i, dirEntry := range dirEntries {
		if i%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return Listing{}, fmt.Errorf("list %s: %w", cleaned, err)
			}
		}
		if !showHidden && strings.HasPrefix(dirEntry.Name(), ".") {
			continue
		}
		entries = append(entries, classify(cleaned, dirEntry))
	}
	slices.SortFunc(entries, compareEntries)

	truncated := false
	if len(entries) > s.maxEntries {
		entries = entries[:s.maxEntries]
		truncated = true
	}
	return Listing{Path: cleaned, Entries: entries, Truncated: truncated}, nil
}

// normalizePath validates and lexically cleans a request path. Empty path means
// the user's home directory.
func (s *Service) normalizePath(path string) (string, error) {
	if path == "" {
		if s.homeErr != nil {
			return "", s.homeErr
		}
		return s.home, nil
	}
	if strings.IndexByte(path, 0) >= 0 {
		return "", fmt.Errorf("%w: path contains a NUL byte", ErrInvalidPath)
	}
	if !filepath.IsAbs(path) {
		return "", fmt.Errorf("%w: path must be absolute", ErrInvalidPath)
	}
	return filepath.Clean(path), nil
}

// readDir stats and reads the directory in a goroutine raced against ctx so a
// hung filesystem (e.g. dead NFS) cannot stall the request past the deadline.
// On timeout the goroutine leaks until the blocked syscall returns; that is
// accepted because a blocked stat/readdir cannot be canceled. Stat-before-open
// also keeps List from ever opening a non-directory (opening a FIFO would
// block).
func readDir(ctx context.Context, path string) ([]os.DirEntry, error) {
	type result struct {
		entries []os.DirEntry
		err     error
	}
	resultCh := make(chan result, 1)
	go func() {
		info, err := os.Stat(path)
		if err != nil {
			resultCh <- result{err: mapOSError(path, err)}
			return
		}
		if !info.IsDir() {
			resultCh <- result{err: fmt.Errorf("%w: %s", ErrNotDirectory, path)}
			return
		}
		entries, err := os.ReadDir(path)
		if err != nil {
			resultCh <- result{err: mapOSError(path, err)}
			return
		}
		resultCh <- result{entries: entries}
	}()
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("list %s: %w", path, ctx.Err())
	case res := <-resultCh:
		return res.entries, res.err
	}
}

func mapOSError(path string, err error) error {
	switch {
	case errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("%w: %s", ErrNotFound, path)
	case errors.Is(err, os.ErrPermission):
		return fmt.Errorf("%w: %s", ErrPermissionDenied, path)
	case errors.Is(err, syscall.ENOTDIR):
		return fmt.Errorf("%w: %s", ErrNotDirectory, path)
	default:
		return err
	}
}

// classify maps one directory entry to its wire kind. Symlinks are classified
// by their target; a failed follow (broken link, unstatable target) or a
// vanished entry stays visible as KindUnknown with no size or mtime.
func classify(dir string, dirEntry os.DirEntry) Entry {
	entry := Entry{
		Name: dirEntry.Name(),
		Path: filepath.Join(dir, dirEntry.Name()),
		Kind: KindUnknown,
	}
	info, err := dirEntry.Info()
	if err != nil {
		return entry
	}
	entry.IsSymlink = info.Mode()&os.ModeSymlink != 0
	if entry.IsSymlink {
		info, err = os.Stat(entry.Path)
		if err != nil {
			return entry
		}
	}
	switch {
	case info.IsDir():
		entry.Kind = KindDirectory
	case info.Mode().IsRegular():
		entry.Kind = KindFile
	default:
		return entry
	}
	modifiedAt := info.ModTime().UTC()
	entry.ModifiedAt = &modifiedAt
	if entry.Kind == KindFile {
		size := info.Size()
		entry.Size = &size
	}
	return entry
}

// compareEntries orders directories first, then dot-prefixed names within
// each group, then case-insensitively by name with a byte-wise tie-break so
// names differing only by case sort deterministically.
func compareEntries(a, b Entry) int {
	aDir, bDir := a.Kind == KindDirectory, b.Kind == KindDirectory
	if aDir != bDir {
		if aDir {
			return -1
		}
		return 1
	}
	aDot, bDot := strings.HasPrefix(a.Name, "."), strings.HasPrefix(b.Name, ".")
	if aDot != bDot {
		if aDot {
			return -1
		}
		return 1
	}
	if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
		return c
	}
	return strings.Compare(a.Name, b.Name)
}
