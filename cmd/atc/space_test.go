package main

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/paths"
)

var spaceIDFormat = regexp.MustCompile(`spce-[a-z2-9]{5}`)

// The space family: the Default space is listed and refuses change; a
// space is created, updated, and deleted with its terminals; terminals
// land in --space (its directory) or the Default space (the cwd, against
// this local server), and move between spaces.
func TestSpaceCLILifecycle(t *testing.T) {
	startTestServer(t)
	dir, err := paths.CanonicalDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	stdout, _, err := runCLI(t, "space", "list")
	if err != nil || !strings.Contains(stdout, "Default (default)") {
		t.Fatalf("list = %q, %v; want the Default space", stdout, err)
	}
	defaultID := spaceIDFormat.FindString(stdout)
	if _, _, err := runCLI(t, "space", "update", defaultID, "--name", "x"); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("update Default = %v, want a 409 problem", err)
	}
	if _, _, err := runCLI(t, "space", "delete", defaultID); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("delete Default = %v, want a 409 problem", err)
	}

	stdout, _, err = runCLI(t, "space", "create", dir, "--name", "work")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := spaceIDFormat.FindString(stdout)
	if id == "" || !regexp.MustCompile(`(?m)^directory\s+`+regexp.QuoteMeta(dir)+`$`).MatchString(stdout) || !strings.Contains(stdout, "work") {
		t.Fatalf("create output:\n%s", stdout)
	}
	if stdout, _, err = runCLI(t, "space", "get", id); err != nil || !strings.Contains(stdout, id) {
		t.Errorf("get = %q, %v", stdout, err)
	}

	// A terminal in the space starts in its directory and is named by it;
	// an unplaced terminal starts in this process's cwd.
	stdout, _, err = runCLI(t, "terminal", "create", "--space", id, "--detach")
	if err != nil || !regexp.MustCompile(`(?m)^directory\s+`+regexp.QuoteMeta(dir)+`$`).MatchString(stdout) ||
		!regexp.MustCompile(`(?m)^name\s+`+filepath.Base(dir)+`$`).MatchString(stdout) || !strings.Contains(stdout, id) {
		t.Fatalf("create --space = %q, %v", stdout, err)
	}
	inSpace := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	cwd, err := paths.CanonicalDir(".")
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, err = runCLI(t, "terminal", "create", "--detach")
	if err != nil || !regexp.MustCompile(`(?m)^directory\s+`+regexp.QuoteMeta(cwd)+`$`).MatchString(stdout) || !strings.Contains(stdout, defaultID) {
		t.Fatalf("unplaced create = %q, %v; want the cwd in the Default space", stdout, err)
	}
	unplaced := regexp.MustCompile(`term-[a-z2-9]{5}`).FindString(stdout)
	if stdout, _, err = runCLI(t, "terminal", "list", "--space", id); err != nil || !strings.Contains(stdout, inSpace) || strings.Contains(stdout, unplaced) {
		t.Errorf("list --space = %q, %v", stdout, err)
	}
	if stdout, _, err = runCLI(t, "terminal", "update", unplaced, "--space", id); err != nil || !regexp.MustCompile(`(?m)^space\s+`+id+`$`).MatchString(stdout) {
		t.Errorf("move = %q, %v", stdout, err)
	}

	moved, err := paths.CanonicalDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if stdout, _, err = runCLI(t, "space", "update", id, "--directory", moved, "--name", "moved"); err != nil ||
		!regexp.MustCompile(`(?m)^directory\s+`+regexp.QuoteMeta(moved)+`$`).MatchString(stdout) || !strings.Contains(stdout, "moved") {
		t.Errorf("update = %q, %v", stdout, err)
	}
	if _, _, err := runCLI(t, "space", "update", id); err == nil {
		t.Error("update with no flags was accepted")
	}

	if stdout, _, err = runCLI(t, "space", "delete", id); err != nil || !strings.Contains(stdout, "deleted "+id) {
		t.Fatalf("delete = %q, %v", stdout, err)
	}
	if stdout, _, err = runCLI(t, "terminal", "list"); err != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("terminals after space delete = %q, %v; want both gone with the space", stdout, err)
	}
	if _, _, err := runCLI(t, "space", "get", id); err == nil || !strings.Contains(err.Error(), "404") {
		t.Errorf("get deleted = %v, want a 404 problem", err)
	}
}
