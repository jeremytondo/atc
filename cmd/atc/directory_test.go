package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDirectoryListCLI(t *testing.T) {
	startTestServer(t)
	root := canonical(t, t.TempDir())
	for _, name := range []string{"beta", "Alpha", ".hidden"} {
		if err := os.Mkdir(filepath.Join(root, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stdout, _, err := runCLI(t, "directory", "list", root)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"path", root, "parent", filepath.Dir(root), "Alpha", filepath.Join(root, "beta")} {
		if !strings.Contains(stdout, want) {
			t.Errorf("list output missing %q:\n%s", want, stdout)
		}
	}
	if strings.Contains(stdout, ".hidden") || strings.Index(stdout, "Alpha") > strings.Index(stdout, "beta") {
		t.Errorf("list output should skip hidden names and sort case-insensitively:\n%s", stdout)
	}
	// Omitted path lists the server user's home directory.
	if stdout, _, err = runCLI(t, "directory", "list"); err != nil || !strings.Contains(stdout, "path") {
		t.Errorf("home list = %q, %v", stdout, err)
	}
	if _, _, err := runCLI(t, "directory", "list", "relative"); err == nil || !strings.Contains(err.Error(), "absolute") {
		t.Errorf("relative path error = %v, want the server's refusal", err)
	}
	if _, _, err := runCLI(t, "directory"); err == nil {
		t.Error("bare `atc directory` should fail with usage")
	}
}
