// Package codex is the built-in catalog registration for Codex (ATC-254)
// and its thread-evidence wiring (ATC-255): the shared app-server
// lifecycle (adopt-or-start over Codex's well-known control socket) and
// the passive observer that turns its broadcasts into thread
// observations. The id is persisted by terminals and threads — never
// rename it.
package codex

import (
	"context"

	"github.com/jeremytondo/atc/internal/agents"
)

// Entry is Codex's catalog registration. observer wires the shared
// app-server and thread observation into every launch and is required.
func Entry(observer *Observer) agents.Entry {
	if observer == nil {
		panic("codex.Entry: observer must not be nil")
	}
	return agents.Entry{ID: "codex", Name: "Codex", TUI: tui{observer: observer}}
}

type tui struct{ observer *Observer }

// Prewarm adopts or starts the shared app-server and brings observation
// up before the terminals domain takes its commit lock — Command then
// only hits the supervisor's fast path there.
func (t tui) Prewarm(ctx context.Context) error {
	return t.observer.Prewarm(ctx)
}

// Command composes the launch: the shared app-server is adopted or
// started, this launch's identity capture is armed, and the TUI connects
// with --remote. --cd is load-bearing: in remote mode the TUI does not
// forward its own working directory, and without it the server would
// stamp new threads with its own neutral cwd — breaking the cwd-matched
// identity capture.
func (t tui) Command(ctx context.Context, launch agents.LaunchContext) (string, error) {
	socket, err := t.observer.EnsureForLaunch(ctx, launch.TerminalID, launch.Directory)
	if err != nil {
		return "", err
	}
	return "codex --cd " + agents.Quote(launch.Directory) + " --remote " + agents.Quote("unix://"+socket), nil
}

func (tui) Binary() string      { return "codex" }
func (tui) InstallHint() string { return "npm install -g @openai/codex" }
