package monitor

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDeletedDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "worktree (deleted)")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if got := readDirectory(os.Getpid()); got != dir {
		t.Fatalf("live directory with deletion suffix = %q, want %q", got, dir)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}
	if got := readDirectory(os.Getpid()); got != "" {
		t.Errorf("deleted directory = %q, want no observation", got)
	}
}
