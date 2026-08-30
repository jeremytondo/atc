// Package codex is the built-in catalog registration for Codex (ATC-254)
// and its thread-evidence wiring (ATC-280): the TUI runs vanilla — its
// own version-matched in-process runtime, no shared app-server — and an
// ATC-owned launch profile makes Codex's lifecycle hooks POST structured
// events to an internal ATC route, where a stateful reducer turns them
// into thread observations. Cross-client access rides the native
// CODEX_HOME thread store, not a shared server. The id is persisted by
// terminals and threads — never rename it.
package codex

import (
	"context"

	"github.com/jeremytondo/atc/internal/agents"
)

// Entry is Codex's catalog registration. hooks wires thread observation
// into every launch and is required — a Codex launch without evidence
// wiring would silently produce a thread-less TUI.
func Entry(hooks *Hooks) agents.Entry {
	if hooks == nil {
		panic("codex.Entry: hooks must not be nil")
	}
	return agents.Entry{ID: "codex", Name: "Codex", TUI: tui{hooks: hooks}}
}

type tui struct{ hooks *Hooks }

// Command composes the launch: the profile is ensured current and
// selected with -p, and the per-launch evidence context — ingest URL and
// the header file carrying this launch's secret — rides environment
// variables, shell-quoted like every other injected value. CODEX_HOME is
// pinned to the home the profile was written into: the user's login
// shell could export a different one, and the TUI must load the profile
// ATC just prepared. No --remote, no server: the TUI must behave exactly
// like plain codex.
func (t tui) Command(_ context.Context, launch agents.LaunchContext) (string, error) {
	headerPath, err := t.hooks.Prepare(launch.TerminalID)
	if err != nil {
		return "", err
	}
	return "CODEX_HOME=" + agents.Quote(t.hooks.codexHome) +
		" " + envURL + "=" + agents.Quote(t.hooks.ingestURL()) +
		" " + envHeader + "=" + agents.Quote(headerPath) +
		" codex -p " + profileName, nil
}

func (tui) Binary() string      { return "codex" }
func (tui) InstallHint() string { return "npm install -g @openai/codex" }
