package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/paths"
)

// StdinIsTTY reports whether this process's stdin is a real TTY. Callers
// pass the result (or their own knowledge) into ResolveProjectID, so the
// check stays an explicit capability rather than package state.
func StdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

// ResolveProjectID finds the project owning the current directory the way
// git finds a repository (ATC-256): canonicalize the cwd, then walk up —
// the nearest ancestor (or the directory itself) matching a project's
// directory wins. On no match, an interactive run (stdinIsTTY — a question
// is only asked of a human; stdout deliberately does not matter, since a
// redirected stdout still deserves the prompt on stderr) is offered a
// project rooted at the default (git toplevel, else cwd); a non-interactive
// run refuses with the fixes instead of prompting.
func ResolveProjectID(ctx context.Context, client *api.Client, stdin io.Reader, stderr io.Writer, stdinIsTTY bool) (string, error) {
	canonical, err := paths.CanonicalDir(".")
	if err != nil {
		return "", err
	}
	projects, err := client.Projects(ctx)
	if err != nil {
		return "", err
	}
	byDir := make(map[string]string, len(projects))
	for _, project := range projects {
		byDir[project.Directory] = project.ID
	}
	for dir := canonical; ; {
		if id, ok := byDir[dir]; ok {
			return id, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	if !stdinIsTTY {
		return "", fmt.Errorf("no project contains %s; pass --project <id> or run `atc project create`", canonical)
	}
	defaultDir := projectRootFor(canonical)
	// The conversation goes to stderr: stdout stays the command's
	// redirectable output (the created terminal).
	_, _ = fmt.Fprintf(stderr, "no project contains %s\ncreate a project at %s? [y/N] ", canonical, defaultDir)
	answer, _ := bufio.NewReader(stdin).ReadString('\n')
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
	default:
		return "", fmt.Errorf("no project selected; pass --project <id> or run `atc project create`")
	}
	project, err := client.CreateProject(ctx, api.ProjectCreateParams{Directory: defaultDir})
	if err != nil {
		return "", err
	}
	_, _ = fmt.Fprintf(stderr, "created project %s at %s\n", project.ID, project.Directory)
	return project.ID, nil
}

// DefaultProjectDir is where an unspecified project is rooted: the git
// toplevel when inside a repository, the current directory otherwise.
func DefaultProjectDir() (string, error) {
	canonical, err := paths.CanonicalDir(".")
	if err != nil {
		return "", err
	}
	return projectRootFor(canonical), nil
}

// projectRootFor asks git for the toplevel of the repository containing
// the canonical directory — git itself is the authority on what counts as
// a repository (a stray or malformed .git entry is not one). Outside a
// repository, or without git installed, the directory is its own root.
func projectRootFor(canonical string) string {
	output, err := exec.Command("git", "-C", canonical, "rev-parse", "--show-toplevel").Output()
	if err != nil {
		return canonical
	}
	toplevel, err := paths.CanonicalDir(strings.TrimSpace(string(output)))
	if err != nil {
		return canonical
	}
	return toplevel
}
