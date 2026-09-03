package cli

import (
	"os"
	"os/exec"
	"strings"

	"github.com/jeremytondo/atc/internal/paths"
)

// StdinIsTTY reports whether this process's stdin is a real TTY; an
// explicit capability rather than package state.
func StdinIsTTY() bool {
	info, err := os.Stdin.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
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
