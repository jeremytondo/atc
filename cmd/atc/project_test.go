package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/jeremytondo/atc/internal/paths"
)

// canonical mirrors the CLI's own canonicalization so assertions compare
// like with like (t.TempDir may sit behind symlinks, e.g. /tmp).
func canonical(t *testing.T, dir string) string {
	t.Helper()
	resolved, err := paths.CanonicalDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

func mkdirAll(t *testing.T, dir string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// gitInit makes dir a real git repository — toplevel detection asks git
// itself, so a bare .git entry would not count.
func gitInit(t *testing.T, dir string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	if out, err := exec.Command("git", "init", "-q", dir).CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
}

func TestProjectCLILifecycle(t *testing.T) {
	startTestServer(t)
	dir := t.TempDir()

	stdout, _, err := runCLI(t, "project", "create", dir, "--name", "atc")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	id := projectIDFormat.FindString(stdout)
	if id == "" || !strings.Contains(stdout, "atc") || !strings.Contains(stdout, canonical(t, dir)) {
		t.Fatalf("create output:\n%s", stdout)
	}

	stdout, _, err = runCLI(t, "project", "list")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "atc") || !strings.Contains(stdout, canonical(t, dir)) {
		t.Errorf("list output:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "project", "update", id, "--name", "renamed"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "renamed") {
		t.Errorf("update output:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "project", "get", id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, id) || !strings.Contains(stdout, "renamed") {
		t.Errorf("get output:\n%s", stdout)
	}

	if stdout, _, err = runCLI(t, "project", "delete", id); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, "deleted "+id) {
		t.Errorf("delete output:\n%s", stdout)
	}
	if stdout, _, err = runCLI(t, "project", "list"); err != nil || !strings.Contains(stdout, "no projects") {
		t.Errorf("list after delete = %q, %v", stdout, err)
	}
}

// Projects own no terminals: a project deletes with terminals running in
// its directory.
func TestProjectDeleteLeavesTerminals(t *testing.T) {
	startTestServer(t)
	dir := t.TempDir()
	projectID := createProjectCLI(t, dir)
	if _, _, err := runCLI(t, "terminal", "create", "--directory", dir, "--detach"); err != nil {
		t.Fatal(err)
	}
	if stdout, _, err := runCLI(t, "project", "delete", projectID); err != nil || !strings.Contains(stdout, "deleted "+projectID) {
		t.Fatalf("delete project with terminals = %q, %v", stdout, err)
	}
	if stdout, _, err := runCLI(t, "terminal", "list"); err != nil || !strings.Contains(stdout, "term-") {
		t.Errorf("terminal list after project delete = %q, %v; want the terminal kept", stdout, err)
	}
}

// --directory moves a project; the server canonicalizes and refuses
// another project's directory.
func TestProjectUpdateDirectoryCLI(t *testing.T) {
	startTestServer(t)
	id := createProjectCLI(t, t.TempDir())
	other := createProjectCLI(t, t.TempDir())
	moved, err := paths.CanonicalDir(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stdout, _, err := runCLI(t, "project", "update", id, "--directory", moved, "--name", "moved")
	if err != nil || !regexp.MustCompile(`(?m)^directory\s+`+regexp.QuoteMeta(moved)+`$`).MatchString(stdout) || !strings.Contains(stdout, "moved") {
		t.Errorf("move = %q, %v", stdout, err)
	}
	otherOut, _, err := runCLI(t, "project", "get", other)
	if err != nil {
		t.Fatal(err)
	}
	match := regexp.MustCompile(`(?m)^directory\s+(.+)$`).FindStringSubmatch(otherOut)
	if match == nil {
		t.Fatalf("get output has no directory:\n%s", otherOut)
	}
	otherDir := strings.TrimSpace(match[1])
	if _, _, err := runCLI(t, "project", "update", id, "--directory", otherDir); err == nil || !strings.Contains(err.Error(), "409") {
		t.Errorf("move onto another project = %v, want a 409 problem", err)
	}
	if _, _, err := runCLI(t, "project", "update", id); err == nil {
		t.Error("update with no flags was accepted")
	}
}

// Without a path, `project create` roots the project at the git toplevel
// when inside a repository, else at the current directory.
func TestProjectCreateDefaultsToGitToplevel(t *testing.T) {
	startTestServer(t)
	repo := t.TempDir()
	gitInit(t, repo)
	sub := mkdirAll(t, filepath.Join(repo, "pkg"))

	t.Chdir(sub)
	stdout, _, err := runCLI(t, "project", "create")
	if err != nil || !strings.Contains(stdout, canonical(t, repo)) || strings.Contains(stdout, canonical(t, sub)) {
		t.Errorf("create inside a repo = %q, %v; want the toplevel", stdout, err)
	}
	plain := mkdirAll(t, filepath.Join(t.TempDir(), "plain"))
	t.Chdir(plain)
	if stdout, _, err := runCLI(t, "project", "create"); err != nil || !strings.Contains(stdout, canonical(t, plain)) {
		t.Errorf("create outside a repo = %q, %v; want the cwd", stdout, err)
	}
}
