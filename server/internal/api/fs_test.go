package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/diagnostics"
	"github.com/jeremytondo/atc/internal/fs"
)

func fsHandler(t *testing.T) http.Handler {
	t.Helper()
	return Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, fs.NewService(nil))
}

func decodeErrorBody(t *testing.T, body *json.Decoder) (code, message string) {
	t.Helper()
	var resp struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if err := body.Decode(&resp); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return resp.Error, resp.Message
}

func TestFSListHappyPath(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".hidden"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := do(t, fsHandler(t), http.MethodGet, "/fs/list?path="+root, "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp struct {
		Path      string `json:"path"`
		Truncated bool   `json:"truncated"`
		Entries   []struct {
			Name       string  `json:"name"`
			Path       string  `json:"path"`
			Kind       string  `json:"kind"`
			IsSymlink  bool    `json:"isSymlink"`
			Size       *int64  `json:"size"`
			ModifiedAt *string `json:"modifiedAt"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Path != root || resp.Truncated {
		t.Fatalf("path = %q truncated = %v", resp.Path, resp.Truncated)
	}
	if len(resp.Entries) != 2 {
		t.Fatalf("entries = %+v, want sub + readme.md (hidden filtered by default)", resp.Entries)
	}
	dir, file := resp.Entries[0], resp.Entries[1]
	if dir.Name != "sub" || dir.Kind != "directory" || dir.Size != nil || dir.ModifiedAt == nil {
		t.Errorf("dir entry = %+v", dir)
	}
	if file.Name != "readme.md" || file.Kind != "file" || file.IsSymlink {
		t.Errorf("file entry = %+v", file)
	}
	if file.Size == nil || *file.Size != 5 {
		t.Errorf("file size = %v, want 5", file.Size)
	}
	if file.Path != filepath.Join(root, "readme.md") {
		t.Errorf("file path = %q", file.Path)
	}
	if file.ModifiedAt == nil {
		t.Fatal("file modifiedAt missing")
	}
	if _, err := time.Parse(time.RFC3339Nano, *file.ModifiedAt); err != nil {
		t.Errorf("modifiedAt %q is not RFC3339Nano: %v", *file.ModifiedAt, err)
	}
}

func TestFSListDefaultsToHome(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skipf("no home directory: %v", err)
	}
	rec := do(t, fsHandler(t), http.MethodGet, "/fs/list", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp struct {
		Path    string `json:"path"`
		Entries []any  `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.Path != filepath.Clean(home) {
		t.Fatalf("path = %q, want home %q", resp.Path, filepath.Clean(home))
	}
	if resp.Entries == nil {
		t.Fatal("entries = nil, want array")
	}
}

func TestFSListShowHidden(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".hidden"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	rec := do(t, fsHandler(t), http.MethodGet, "/fs/list?path="+root+"&showHidden=true", "")

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body)
	}
	var resp struct {
		Entries []struct {
			Name string `json:"name"`
		} `json:"entries"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Entries) != 1 || resp.Entries[0].Name != ".hidden" {
		t.Fatalf("entries = %+v, want [.hidden]", resp.Entries)
	}
}

func TestFSListInvalidShowHidden(t *testing.T) {
	rec := do(t, fsHandler(t), http.MethodGet, "/fs/list?path=/&showHidden=banana", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (%s)", rec.Code, rec.Body)
	}
}

func TestFSListErrorCodes(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	handler := fsHandler(t)

	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantCode   string
	}{
		{name: "relative path", query: "?path=relative/path", wantStatus: http.StatusBadRequest, wantCode: "invalid_path"},
		{name: "not found", query: "?path=" + filepath.Join(root, "missing"), wantStatus: http.StatusNotFound, wantCode: "not_found"},
		{name: "not directory", query: "?path=" + filepath.Join(root, "file"), wantStatus: http.StatusBadRequest, wantCode: "not_directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, handler, http.MethodGet, "/fs/list"+tt.query, "")
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (%s)", rec.Code, tt.wantStatus, rec.Body)
			}
			code, message := decodeErrorBody(t, json.NewDecoder(rec.Body))
			if code != tt.wantCode {
				t.Fatalf("error = %q, want %q", code, tt.wantCode)
			}
			if message == "" {
				t.Fatal("message is empty")
			}
		})
	}

	t.Run("permission denied", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("running as root: permission modes are not enforced")
		}
		if err := os.Chmod(locked, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

		rec := do(t, handler, http.MethodGet, "/fs/list?path="+locked, "")
		if rec.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (%s)", rec.Code, rec.Body)
		}
		if code, _ := decodeErrorBody(t, json.NewDecoder(rec.Body)); code != "permission_denied" {
			t.Fatalf("error = %q, want permission_denied", code)
		}
	})
}

func TestFSRoutesRequireService(t *testing.T) {
	handler := Routes(diagnostics.DefaultDiagnostics(), nil, nil, nil, nil)
	rec := do(t, handler, http.MethodGet, "/fs/list?path=/", "")
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", rec.Code, rec.Body)
	}
	if code, _ := decodeErrorBody(t, json.NewDecoder(rec.Body)); code != "internal_error" {
		t.Fatalf("error = %q, want internal_error", code)
	}
}
