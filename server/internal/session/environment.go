package session

import (
	"os"
	"strings"

	"github.com/jeremytondo/atc/internal/store"
)

// actionLaunchCommand wraps an Action's fixed command and literal args in the
// host login-interactive shell used for every Action launch. No request data
// is interpolated into an Action command.
func actionLaunchCommand(action store.Action) []string {
	inner := append([]string{action.Command}, action.Args...)
	return []string{loginShell(), "-l", "-i", "-c", shellJoin(inner)}
}

// interactiveShellCommand launches the host login-interactive shell without a
// command payload.
func interactiveShellCommand() []string {
	return []string{loginShell(), "-l", "-i"}
}

func loginShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

func shellJoin(tokens []string) string {
	quoted := make([]string, len(tokens))
	for i, token := range tokens {
		quoted[i] = shellQuote(token)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s != "" && isSafeToken(s) {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func isSafeToken(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9':
		case strings.ContainsRune("_@%+=:,./-", r):
		default:
			return false
		}
	}
	return true
}
