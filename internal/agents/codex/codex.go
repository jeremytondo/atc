// Package codex is the built-in catalog registration for Codex (ATC-254).
// The id is persisted by terminals and later threads — never rename it.
package codex

import "github.com/jeremytondo/atc/internal/agents"

// Entry is Codex's catalog registration.
func Entry() agents.Entry {
	return agents.Entry{ID: "codex", Name: "Codex", TUI: tui{}}
}

type tui struct{}

func (tui) Command() string     { return "codex" }
func (tui) Binary() string      { return "codex" }
func (tui) InstallHint() string { return "npm install -g @openai/codex" }
