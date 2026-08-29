// Package claude is the built-in catalog registration for Claude Code
// (ATC-254) and its thread-evidence wiring (ATC-255): per-launch hook
// settings that make Claude's lifecycle hooks POST structured events to
// an internal ATC route, and the stateful reducer that turns those events
// into thread observations. The id is persisted by terminals and threads
// — never rename it.
package claude

import (
	"context"

	"github.com/jeremytondo/atc/internal/agents"
)

// Entry is Claude Code's catalog registration. hooks wires thread
// observation into every launch and is required — a Claude launch
// without evidence wiring would silently produce a thread-less TUI.
func Entry(hooks *Hooks) agents.Entry {
	if hooks == nil {
		panic("claude.Entry: hooks must not be nil")
	}
	return agents.Entry{ID: "claude", Name: "Claude Code", TUI: tui{hooks: hooks}}
}

type tui struct{ hooks *Hooks }

// Command composes the launch: hook settings are prepared under the
// terminal's identity and injected with --settings, shell-quoted — the
// command is one string through the user's login shell.
func (t tui) Command(_ context.Context, launch agents.LaunchContext) (string, error) {
	settings, err := t.hooks.Prepare(launch.TerminalID)
	if err != nil {
		return "", err
	}
	return "claude --settings " + agents.Quote(settings), nil
}

func (tui) Binary() string      { return "claude" }
func (tui) InstallHint() string { return "npm install -g @anthropic-ai/claude-code" }
