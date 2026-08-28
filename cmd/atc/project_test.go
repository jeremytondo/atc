package main

import (
	"os"
	"os/exec"
	"path/filepath"
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

func TestProjectDeleteRefusedWhileNotEmpty(t *testing.T) {
	startTestServer(t)
	projectID := createProjectCLI(t, t.TempDir())
	if _, _, err := runCLI(t, "terminal", "create", "--project", projectID); err != nil {
		t.Fatal(err)
	}

	_, _, err := runCLI(t, "project", "delete", projectID)
	if err == nil || !strings.Contains(err.Error(), "terminal") {
		t.Fatalf("delete non-empty project = %v, want a refusal naming what remains", err)
	}
}

// The nearest ancestor wins: with projects at A and A/B, a terminal
// created from A/B/sub belongs to A/B.
func TestTerminalCreateResolvesNearestProject(t *testing.T) {
	startTestServer(t)
	root := t.TempDir()
	outer := mkdirAll(t, filepath.Join(root, "a"))
	inner := mkdirAll(t, filepath.Join(root, "a", "b"))
	sub := mkdirAll(t, filepath.Join(root, "a", "b", "sub"))
	outerID := createProjectCLI(t, outer)
	innerID := createProjectCLI(t, inner)

	t.Chdir(sub)
	stdout, _, err := runCLI(t, "terminal", "create")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, innerID) || strings.Contains(stdout, outerID) {
		t.Errorf("terminal did not land in the nearest project %s:\n%s", innerID, stdout)
	}
	if !strings.Contains(stdout, canonical(t, inner)) {
		t.Errorf("terminal directory is not the project's:\n%s", stdout)
	}
}

// A symlinked spelling of the cwd resolves to the same project: the walk
// compares canonical forms only.
func TestTerminalCreateSymlinkedCwdResolves(t *testing.T) {
	startTestServer(t)
	real := t.TempDir()
	projectID := createProjectCLI(t, real)
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}

	t.Chdir(link)
	stdout, _, err := runCLI(t, "terminal", "create")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, projectID) {
		t.Errorf("symlinked cwd did not resolve to %s:\n%s", projectID, stdout)
	}
}

// --project skips resolution entirely, even when the cwd sits inside a
// different project.
func TestTerminalCreateProjectFlagSkipsResolution(t *testing.T) {
	startTestServer(t)
	here := t.TempDir()
	elsewhere := t.TempDir()
	hereID := createProjectCLI(t, here)
	elsewhereID := createProjectCLI(t, elsewhere)

	t.Chdir(here)
	stdout, _, err := runCLI(t, "terminal", "create", "--project", elsewhereID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, elsewhereID) || strings.Contains(stdout, hereID) {
		t.Errorf("--project did not win over the cwd:\n%s", stdout)
	}
}

// Without a TTY, no match is a refusal naming the fixes — never a prompt.
func TestTerminalCreateNoMatchWithoutTTYRefuses(t *testing.T) {
	startTestServer(t)
	t.Chdir(t.TempDir())

	_, _, err := runCLI(t, "terminal", "create")
	if err == nil || !strings.Contains(err.Error(), "--project") || !strings.Contains(err.Error(), "atc project create") {
		t.Fatalf("no-match without TTY = %v, want a refusal naming --project and atc project create", err)
	}
	stdout, _, listErr := runCLI(t, "terminal", "list")
	if listErr != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("list after refusal = %q, %v", stdout, listErr)
	}
}

// With a TTY and no match, the CLI offers a project rooted at the git
// toplevel; accepting creates the project and the terminal in one go.
func TestTerminalCreateOffersProjectAtGitToplevel(t *testing.T) {
	forceTTY(t)
	startTestServer(t)
	repo := t.TempDir()
	gitInit(t, repo)
	sub := mkdirAll(t, filepath.Join(repo, "pkg"))

	t.Chdir(sub)
	stdout, stderr, err := runCLIInput(t, "y\n", "terminal", "create")
	if err != nil {
		t.Fatal(err)
	}
	// The conversation rides stderr; stdout carries the created terminal.
	toplevel := canonical(t, repo)
	if !strings.Contains(stderr, "create a project at "+toplevel+"?") {
		t.Errorf("offer does not name the git toplevel:\n%s", stderr)
	}
	if !strings.Contains(stderr, "created project proj-") {
		t.Errorf("accepting the offer did not report the created project:\n%s", stderr)
	}
	if !strings.Contains(stdout, "running") {
		t.Errorf("accepting the offer did not create the terminal:\n%s", stdout)
	}
	if listOut, _, err := runCLI(t, "project", "list"); err != nil || !strings.Contains(listOut, toplevel) {
		t.Errorf("project list = %q, %v; want the toplevel project", listOut, err)
	}
}

// Declining the offer refuses the command and creates nothing.
func TestTerminalCreateOfferDeclinedCreatesNothing(t *testing.T) {
	forceTTY(t)
	startTestServer(t)
	t.Chdir(t.TempDir())

	_, _, err := runCLIInput(t, "n\n", "terminal", "create")
	if err == nil || !strings.Contains(err.Error(), "no project selected") {
		t.Fatalf("declined offer = %v, want a refusal", err)
	}
	if stdout, _, err := runCLI(t, "project", "list"); err != nil || !strings.Contains(stdout, "no projects") {
		t.Errorf("project list after decline = %q, %v", stdout, err)
	}
	if stdout, _, err := runCLI(t, "terminal", "list"); err != nil || !strings.Contains(stdout, "no terminals") {
		t.Errorf("terminal list after decline = %q, %v", stdout, err)
	}
}

// `atc project create` without a path defaults to the git toplevel when
// inside a repository, the current directory otherwise.
func TestProjectCreateDefaultPath(t *testing.T) {
	startTestServer(t)
	repo := t.TempDir()
	gitInit(t, repo)
	sub := mkdirAll(t, filepath.Join(repo, "pkg"))

	t.Chdir(sub)
	stdout, _, err := runCLI(t, "project", "create")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, canonical(t, repo)) {
		t.Errorf("create output does not root at the toplevel:\n%s", stdout)
	}

	plain := canonical(t, t.TempDir())
	// The default honestly finds any ancestor repo (a stray /tmp/.git on a
	// developer machine, say); only a truly repo-free cwd tests this case.
	// git itself is the probe, the same authority the CLI consults.
	if exec.Command("git", "-C", plain, "rev-parse", "--show-toplevel").Run() == nil {
		t.Skipf("an ancestor of %s is a git repository; skipping the no-repo case", plain)
	}
	t.Chdir(plain)
	stdout, _, err = runCLI(t, "project", "create")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout, plain) {
		t.Errorf("create outside a repo does not root at the cwd:\n%s", stdout)
	}
}
