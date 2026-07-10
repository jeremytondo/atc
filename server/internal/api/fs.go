package api

import (
	"errors"
	"net/http"

	"github.com/jeremytondo/atc/internal/fs"
)

type fsListResponse struct {
	Path      string            `json:"path"`
	Truncated bool              `json:"truncated"`
	Entries   []fsEntryResponse `json:"entries"`
}

type fsEntryResponse struct {
	Name       string  `json:"name"`
	Path       string  `json:"path"`
	Kind       string  `json:"kind"`
	IsSymlink  bool    `json:"isSymlink"`
	Size       *int64  `json:"size,omitempty"`
	ModifiedAt *string `json:"modifiedAt,omitempty"`
}

func (routes apiRoutes) fsList(w http.ResponseWriter, r *http.Request) {
	if !routes.requireFS(w) {
		return
	}
	showHidden, err := boolQuery(r, "showHidden")
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	listing, err := routes.fs.List(r.Context(), r.URL.Query().Get("path"), showHidden)
	if err != nil {
		writeFSError(w, err)
		return
	}
	resp := fsListResponse{
		Path:      listing.Path,
		Truncated: listing.Truncated,
		Entries:   make([]fsEntryResponse, 0, len(listing.Entries)),
	}
	for _, entry := range listing.Entries {
		resp.Entries = append(resp.Entries, fsEntryResponse{
			Name:       entry.Name,
			Path:       entry.Path,
			Kind:       entry.Kind,
			IsSymlink:  entry.IsSymlink,
			Size:       entry.Size,
			ModifiedAt: formatOptionalTime(entry.ModifiedAt),
		})
	}
	writeJSON(w, http.StatusOK, resp)
}

func (routes apiRoutes) requireFS(w http.ResponseWriter) bool {
	if routes.fs != nil {
		return true
	}
	writeError(w, http.StatusInternalServerError, "internal_error", "filesystem service is unavailable")
	return false
}

// writeFSError maps an fs-domain error to the appropriate status code.
func writeFSError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, fs.ErrInvalidPath):
		writeError(w, http.StatusBadRequest, "invalid_path", err.Error())
	case errors.Is(err, fs.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error())
	case errors.Is(err, fs.ErrNotDirectory):
		writeError(w, http.StatusBadRequest, "not_directory", err.Error())
	case errors.Is(err, fs.ErrPermissionDenied):
		writeError(w, http.StatusForbidden, "permission_denied", err.Error())
	default:
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
	}
}
