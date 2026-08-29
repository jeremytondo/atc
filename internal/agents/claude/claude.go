// Package claude is the built-in catalog registration for Claude Code
// (ATC-254). The id is persisted by terminals and later threads — never
// rename it.
package claude

import "github.com/jeremytondo/atc/internal/agents"

// Entry is Claude Code's catalog registration.
func Entry() agents.Entry {
	return agents.Entry{ID: "claude", Name: "Claude Code", TUI: tui{}}
}

type tui struct{}

func (tui) Command() string     { return "claude" }
func (tui) Binary() string      { return "claude" }
func (tui) InstallHint() string { return "npm install -g @anthropic-ai/claude-code" }
