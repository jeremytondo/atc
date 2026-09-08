package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

func decodeDirectoryList(t *testing.T, rec *httptest.ResponseRecorder) api.DirectoryList {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d; body %s", rec.Code, rec.Body)
	}
	var list api.DirectoryList
	decodeInto(t, rec, &list)
	return list
}

func mkdirs(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

// The directory browser over the wire (ATC-316): home by default, the
// canonical path and parent, directories only with hidden names skipped,
// symlinks admitted only when they resolve to a directory, and a
// case-insensitive sort.
func TestDirectoriesOverTheWire(t *testing.T) {
	f := newFixture(t)
	root := f.projectDir
	mkdirs(t, root, "beta", "Alpha", ".hidden", "gamma")
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "beta"), filepath.Join(root, "link-dir")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "file.txt"), filepath.Join(root, "link-file")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(root, "link-dangling")); err != nil {
		t.Fatal(err)
	}

	parent := filepath.Dir(root)
	want := api.DirectoryList{
		Path:   root,
		Parent: &parent,
		Entries: []api.DirectoryEntry{
			{Name: "Alpha", Path: filepath.Join(root, "Alpha")},
			{Name: "beta", Path: filepath.Join(root, "beta")},
			{Name: "gamma", Path: filepath.Join(root, "gamma")},
			{Name: "link-dir", Path: filepath.Join(root, "link-dir")},
		},
	}
	// Omitted path lists the server user's home — the fixture's Default
	// space directory.
	if diff := cmp.Diff(want, decodeDirectoryList(t, f.request(t, http.MethodGet, "/v1/directories", ""))); diff != "" {
		t.Errorf("home listing (-want +got):\n%s", diff)
	}
	// A symlinked path is answered in canonical form.
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, decodeDirectoryList(t, f.request(t, http.MethodGet, "/v1/directories?path="+alias, ""))); diff != "" {
		t.Errorf("aliased listing (-want +got):\n%s", diff)
	}
	// The root has no parent.
	if got := decodeDirectoryList(t, f.request(t, http.MethodGet, "/v1/directories?path=/", "")); got.Path != "/" || got.Parent != nil {
		t.Errorf("root listing = path %q parent %v; want / and null", got.Path, got.Parent)
	}
	// An empty directory answers an empty array, not null.
	empty := filepath.Join(root, "gamma")
	rec := f.request(t, http.MethodGet, "/v1/directories?path="+empty, "")
	if got := decodeDirectoryList(t, rec); len(got.Entries) != 0 || !strings.Contains(rec.Body.String(), `"entries":[]`) {
		t.Errorf("empty listing = %+v; body %s", got, rec.Body)
	}
}

func TestDirectoriesTruncateAtTheCap(t *testing.T) {
	f := newFixture(t)
	root := filepath.Join(f.projectDir, "many")
	mkdirs(t, f.projectDir, "many")
	for i := range directoryEntryCap + 1 {
		mkdirs(t, root, fmt.Sprintf("d%05d", i))
	}
	got := decodeDirectoryList(t, f.request(t, http.MethodGet, "/v1/directories?path="+root, ""))
	if len(got.Entries) != directoryEntryCap || !got.Truncated {
		t.Errorf("entries = %d truncated %v; want %d and true", len(got.Entries), got.Truncated, directoryEntryCap)
	}
	if err := os.Remove(filepath.Join(root, "d00000")); err != nil {
		t.Fatal(err)
	}
	if got := decodeDirectoryList(t, f.request(t, http.MethodGet, "/v1/directories?path="+root, "")); len(got.Entries) != directoryEntryCap || got.Truncated {
		t.Errorf("at the cap: entries = %d truncated %v; want %d and false", len(got.Entries), got.Truncated, directoryEntryCap)
	}
}

func TestDirectoriesRefusals(t *testing.T) {
	f := newFixture(t)
	file := filepath.Join(f.projectDir, "file.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, tc := range map[string]struct {
		path   string
		status int
		code   string
	}{
		"relative":      {"relative/dir", http.StatusUnprocessableEntity, api.CodeValidationFailed},
		"missing":       {filepath.Join(f.projectDir, "nope"), http.StatusUnprocessableEntity, api.CodeDirectoryInvalid},
		"not directory": {file, http.StatusUnprocessableEntity, api.CodeDirectoryInvalid},
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.request(t, http.MethodGet, "/v1/directories?path="+tc.path, "")
			if rec.Code != tc.status || !strings.Contains(rec.Body.String(), `"code":"`+tc.code+`"`) {
				t.Errorf("got %d; body %s; want %d %s", rec.Code, rec.Body, tc.status, tc.code)
			}
		})
	}

	t.Run("timeout", func(t *testing.T) {
		// A request whose deadline has already passed exercises the
		// bound: the listing must answer with an error, not a partial
		// list.
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		req := httptest.NewRequest(http.MethodGet, "/v1/directories", nil).WithContext(ctx)
		req.Header.Set("Authorization", "Bearer "+testToken)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusGatewayTimeout || !strings.Contains(rec.Body.String(), `"code":"`+api.CodeDirectoryTimeout+`"`) {
			t.Errorf("got %d; body %s; want 504 %s", rec.Code, rec.Body, api.CodeDirectoryTimeout)
		}
	})

	t.Run("unauthenticated", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/v1/directories", nil)
		rec := httptest.NewRecorder()
		f.handler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("got %d, want 401", rec.Code)
		}
	})
}
