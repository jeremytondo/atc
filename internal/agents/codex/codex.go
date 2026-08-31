// Package codex is the built-in catalog registration for Codex (ATC-254)
// and its thread-evidence wiring (ATC-284): the TUI runs plain — `codex`
// or `codex resume <id>`, no profile, no override, no hook environment —
// so it joins the user's shared app-server, the same runtime Codex
// Desktop and the iOS app use, and ATC keeps one read-only connection to
// that server to observe thread announcements and status changes. A new
// terminal is tied to its thread by the announcement that appears in the
// launch directory right after launch; the thread record is minted at
// the first prompt, through the same neutral observations Claude feeds
// the threads domain. The id is persisted by terminals and threads —
// never rename it.
package codex

import (
	"context"
	"os"
	"path/filepath"

	"github.com/jeremytondo/atc/internal/agents"
)

// Entry is Codex's catalog registration. observer wires thread
// observation into every launch and is required — a Codex launch without
// it would silently produce a thread-less TUI.
func Entry(observer *Observer) agents.Entry {
	if observer == nil {
		panic("codex.Entry: observer must not be nil")
	}
	return agents.Entry{ID: "codex", Name: "Codex", TUI: tui{observer: observer}}
}

type tui struct{ observer *Observer }

// PrepareLaunch runs before the terminal exists and outside the
// terminals commit lock: the shared server must be answering (started
// here when it is not), and a fresh launch reserves its directory so
// same-directory launches take turns at the binding window.
func (t tui) PrepareLaunch(ctx context.Context, launch agents.LaunchContext) (func(), error) {
	return t.observer.prepareLaunch(ctx, launch.Directory, launch.ResumeConversationID)
}

// Command composes the launch. Nothing rides along: any profile, -c
// override, --strict-config, or hook environment would make the launch
// "non-replayable" and push the TUI onto a private in-process engine
// (Codex 0.146+), the very split ATC-284 removes. A fresh launch arms the
// pending launch for its directory; a resume holds the pairing directly,
// since `codex resume` announces no thread.
func (t tui) Command(_ context.Context, launch agents.LaunchContext) (string, error) {
	if launch.ResumeConversationID != "" {
		t.observer.holdResume(launch.TerminalID, launch.ResumeConversationID)
		return "codex resume " + agents.Quote(launch.ResumeConversationID), nil
	}
	t.observer.arm(launch.TerminalID, launch.Directory)
	return "codex", nil
}

func (tui) Binary() string      { return "codex" }
func (tui) InstallHint() string { return "npm install -g @openai/codex" }

// CodexHome resolves the CODEX_HOME every codex process on this machine
// uses: codex's own variable, never an ATC setting. Always absolute — the
// control socket path is derived from it.
func CodexHome() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Abs(home)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex"), nil
}

// controlDir is Codex's app-server control directory inside a Codex
// home: the well-known socket every client joins, and the log file the
// server writes when a client starts it.
func controlDir(codexHome string) string {
	return filepath.Join(codexHome, "app-server-control")
}

// ControlSocketPath is the shared server's well-known unix socket.
func ControlSocketPath(codexHome string) string {
	return filepath.Join(controlDir(codexHome), "app-server-control.sock")
}

// serverLogPath is where a server ATC starts writes its output — the
// same file Codex Desktop's own start uses.
func serverLogPath(codexHome string) string {
	return filepath.Join(controlDir(codexHome), "app-server.log")
}
