package assets

import (
	"io/fs"
	"testing"
)

func TestWebFSExposesEmbeddedAssets(t *testing.T) {
	webFS, err := WebFS()
	if err != nil {
		t.Fatalf("WebFS() error = %v", err)
	}

	if _, err := fs.Stat(webFS, ".embedkeep"); err != nil {
		t.Fatalf("stat embedded .embedkeep: %v", err)
	}
	if _, err := fs.Stat(webFS, "_app/.embedkeep"); err != nil {
		t.Fatalf("stat embedded _app/.embedkeep: %v", err)
	}
}
