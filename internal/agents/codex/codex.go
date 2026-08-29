// Package codex is the built-in catalog registration for Codex (ATC-254).
// The id is persisted by terminals and threads — never rename it.
package codex

import (
	"context"

	"github.com/jeremytondo/atc/internal/agents"
)

// Entry is Codex's catalog registration.
func Entry() agents.Entry {
	return agents.Entry{ID: "codex", Name: "Codex", TUI: tui{}}
}

type tui struct{}

// Command is the plain TUI launch; the ATC-279 follow-up wires the shared
// app-server (--remote) through the launch context.
func (tui) Command(context.Context, agents.LaunchContext) (string, error) { return "codex", nil }
func (tui) Binary() string                                                { return "codex" }
func (tui) InstallHint() string                                           { return "npm install -g @openai/codex" }
