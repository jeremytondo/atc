// Package claude is Claude Code's Integration (ATC-294): its catalog
// registration — one terminal App, claude/tui — and its thread-evidence
// wiring (ATC-255): per-launch hook settings that make Claude's lifecycle
// hooks POST structured events to an internal ATC route, and the stateful
// reducer that turns those events into thread observations. Claude
// announces a session before any prompt, so the reducer defers minting a
// thread to the first root prompt (ATC-282), as Codex's observer does at
// its first live status. The ids are persisted by terminals and threads
// — never rename them.
package claude

import (
	"context"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
)

// ID is the Integration id and the identity namespace of every Claude
// thread; AppID is the qualified id of its one App, recorded on every
// terminal it launches and every thread started there; AgentID is its
// one agent, recorded on every thread.
const (
	ID      = "claude"
	AppID   = ID + "/tui"
	AgentID = "claude"
)

// Integration is Claude Code's catalog registration: one agent
// descriptor and one terminal App. hooks wires thread observation into
// every launch and is required — a Claude launch without evidence wiring
// would silently produce a thread-less TUI.
func Integration(hooks *Hooks) integrations.Integration {
	if hooks == nil {
		panic("claude.Integration: hooks must not be nil")
	}
	return integrations.Integration{
		ID:           ID,
		Name:         "Claude Code",
		Capabilities: []api.IntegrationCapability{api.CapabilityThreadObservation},
		Agents:       []api.IntegrationAgent{{ID: AgentID, Name: "Claude Code"}},
		Apps: []integrations.App{
			{ID: "tui", Name: "Claude Code CLI", Agents: []string{AgentID}, Terminal: tui{hooks: hooks}},
		},
		Executable: &integrations.Executable{Binary: "claude", InstallHint: "npm install -g @anthropic-ai/claude-code"},
	}
}

type tui struct{ hooks *Hooks }

// Command composes the launch: hook settings are prepared under the
// terminal's identity and injected with --settings, shell-quoted — the
// command is one string through the user's login shell. A resume adds
// --resume with the exact session id.
func (t tui) Command(_ context.Context, launch integrations.LaunchContext) (string, error) {
	settings, err := t.hooks.Prepare(launch.TerminalID)
	if err != nil {
		return "", err
	}
	command := "claude --settings " + integrations.Quote(settings)
	if launch.ResumeConversationID != "" {
		command += " --resume " + integrations.Quote(launch.ResumeConversationID)
	}
	return command, nil
}
