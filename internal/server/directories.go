package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/paths"
)

// GET /v1/directories (ATC-316): the read-only directory browser behind
// the picker's Create Space flow. One directory's immediate
// subdirectories, canonicalized with the same rule spaces store, hidden
// names excluded, capped and time-bounded. There is no file listing, no
// recursion, and no mutation — this is the whole filesystem surface.

const (
	// directoryEntryCap bounds one listing; the response says when it
	// was hit rather than silently dropping entries.
	directoryEntryCap = 2000
	// directoryListTimeout bounds one listing on slow filesystems (a
	// network mount, a directory of a million files). Expiry is an error,
	// never a partial listing.
	directoryListTimeout = 5 * time.Second
	// directoryReadBatch is how many entries are read between deadline
	// checks.
	directoryReadBatch = 256
)

var (
	errDirectoryRelative = errors.New("path must be absolute")
	errDirectoryInvalid  = errors.New("invalid directory")
	errDirectoryTimeout  = errors.New("directory listing timed out")
)

type directoryListOutput struct {
	Body api.DirectoryList
}

func registerDirectories(humaAPI huma.API, homeDir string) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "list-directories",
		Method:      http.MethodGet,
		Path:        "/v1/directories",
		Summary:     "List a directory's subdirectories",
		Description: "The immediate subdirectories of one absolute directory on the server's machine (the server user's home when path is omitted), for browsing to a space directory. Directories only, non-recursive, names beginning with a dot excluded, symlinks included only when they resolve to a directory, sorted case-insensitively by name; the listing stops at 2000 entries and says so. A relative, missing, unreadable, or non-directory path is refused; a listing that exceeds the time bound is an error, never a partial answer.",
		Errors:      []int{http.StatusUnprocessableEntity, http.StatusGatewayTimeout},
	}, func(ctx context.Context, input *struct {
		Path string `query:"path" doc:"Absolute directory to list; the server user's home directory when omitted."`
	}) (*directoryListOutput, error) {
		list, err := listDirectories(ctx, input.Path, homeDir)
		if err != nil {
			return nil, mapDirectoryError(err)
		}
		return &directoryListOutput{Body: list}, nil
	})
}

// listDirectories reads dir's immediate subdirectories under the entry cap
// and the time bound. Entries are read in batches with the deadline
// checked between them, so a huge directory cannot hold the request past
// the bound; the sort runs on what the cap admitted.
func listDirectories(ctx context.Context, path, homeDir string) (api.DirectoryList, error) {
	if path == "" {
		path = homeDir
	}
	if !filepath.IsAbs(path) {
		return api.DirectoryList{}, fmt.Errorf("%w: %q", errDirectoryRelative, path)
	}
	dir, err := paths.CanonicalDir(path)
	if err != nil {
		return api.DirectoryList{}, fmt.Errorf("%w: %w", errDirectoryInvalid, err)
	}
	ctx, cancel := context.WithTimeout(ctx, directoryListTimeout)
	defer cancel()

	f, err := os.Open(dir)
	if err != nil {
		return api.DirectoryList{}, fmt.Errorf("%w: %w", errDirectoryInvalid, err)
	}
	defer func() { _ = f.Close() }()

	list := api.DirectoryList{Path: dir, Entries: []api.DirectoryEntry{}}
	if parent := filepath.Dir(dir); parent != dir {
		list.Parent = &parent
	}
read:
	for {
		if err := ctx.Err(); err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return api.DirectoryList{}, errDirectoryTimeout
			}
			return api.DirectoryList{}, err
		}
		batch, readErr := f.ReadDir(directoryReadBatch)
		for _, entry := range batch {
			name := entry.Name()
			if strings.HasPrefix(name, ".") {
				continue
			}
			full := filepath.Join(dir, name)
			isDir := entry.IsDir()
			if entry.Type()&os.ModeSymlink != 0 {
				// A symlink counts only when it resolves to a directory;
				// dangling links and links to files are skipped.
				info, statErr := os.Stat(full)
				isDir = statErr == nil && info.IsDir()
			}
			if !isDir {
				continue
			}
			if len(list.Entries) == directoryEntryCap {
				list.Truncated = true
				break read
			}
			list.Entries = append(list.Entries, api.DirectoryEntry{Name: name, Path: full})
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return api.DirectoryList{}, fmt.Errorf("%w: %w", errDirectoryInvalid, readErr)
		}
	}
	sort.Slice(list.Entries, func(i, j int) bool {
		a, b := strings.ToLower(list.Entries[i].Name), strings.ToLower(list.Entries[j].Name)
		if a != b {
			return a < b
		}
		return list.Entries[i].Name < list.Entries[j].Name
	})
	return list, nil
}

func mapDirectoryError(err error) error {
	switch {
	case errors.Is(err, errDirectoryRelative):
		return problem(http.StatusUnprocessableEntity, api.CodeValidationFailed, err.Error())
	case errors.Is(err, errDirectoryInvalid):
		return problem(http.StatusUnprocessableEntity, api.CodeDirectoryInvalid, err.Error())
	case errors.Is(err, errDirectoryTimeout):
		return problem(http.StatusGatewayTimeout, api.CodeDirectoryTimeout, err.Error())
	}
	return mapError(err)
}
