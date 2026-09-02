// Package agents is the Agents domain (ATC-254, ATC-285): a compiled-in,
// read-only catalog of the adapters ATC works with and the launch glue
// that turns an agent reference into a terminal create.
//
// Three terms: a model is what a conversation runs on; an agent is the
// harness+model label a thread runs under (claude, codex); an adapter is
// what produces threads — a local TUI launcher (claude, codex), or an
// observer of an external program that owns its own conversations
// (t3code). Every adapter declares the agents it produces threads for, and
// /v1/agents is derived from those declarations: an agent is launchable
// when some adapter can start its TUI.
//
// The catalog has no storage; entries are assembled at startup from
// built-in registrations — one package per adapter under internal/integrations/,
// one registration line in the composition root — and availability is
// probed against the machine on every read. Adapters that observe an
// external program emit agent_adapter.updated on connection changes;
// launchers emit nothing. The dependency direction is one-way: this
// package calls into terminals to create launch terminals, and the
// terminals domain never depends on agents (ATC-251 doctrine).
package agents

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jeremytondo/atc/internal/api"
)

// LaunchContext is the per-launch context the composition injects into a
// TUI (ATC-255): the terminal's working directory, its minted identity,
// and for a resume (ATC-282) the provider conversation to reopen.
// Adapters use it to wire observation — Claude hook settings, the Codex
// pending launch — without any of it appearing in the API contract. Grow
// it only when an adapter consumes the addition.
type LaunchContext struct {
	// TerminalID is the terminal being created for this launch. Empty in
	// PrepareLaunch, which runs before the identity is minted.
	TerminalID string
	// Directory is the session's working directory: the project's, or a
	// resumed conversation's recorded one.
	Directory string
	// ResumeConversationID is the provider's own conversation id to resume
	// exactly; empty launches a fresh conversation. It is the private
	// identity the threads domain holds and never appears on the wire.
	ResumeConversationID string
}

// LaunchPreparer is the optional second TUI seam: launch-time work that
// may block — waiting on another launch in the same directory, starting a
// provider's shared server — runs here, before the terminals domain takes
// its commit lock (Command runs under it). An error refuses the launch
// before any record exists; abort is called if the create fails
// afterwards, so the preparation can be undone.
type LaunchPreparer interface {
	PrepareLaunch(ctx context.Context, launch LaunchContext) (abort func(), err error)
}

// TUI is the seam a launching adapter implements per agent: how to start
// the agent's own terminal UI in an ATC terminal.
type TUI interface {
	// Command composes the command string a launch terminal runs through
	// the user's login shell — the provider's fresh start, or its exact
	// resume when launch.ResumeConversationID is set. It never appears in
	// the API contract, so flags can change without a wire-visible change.
	// Any injected value must be shell-quoted (Quote) — the command is a
	// single string run through the user's login shell. An error refuses
	// the launch before the terminal record exists; the context bounds any
	// launch-time work.
	Command(ctx context.Context, launch LaunchContext) (string, error)
	// Binary is the executable name the availability probe resolves on the
	// server's PATH.
	Binary() string
	// InstallHint tells the user how to install the tool.
	InstallHint() string
}

// Adapter is one catalog registration: a stable id (persisted on every
// thread it produces — never renamed), a display name, the agents it
// produces threads for, and for an observer of an external program its
// live connection.
type Adapter struct {
	ID   string
	Name string
	// Agents lists what the adapter produces threads for, in display
	// order. An agent with a TUI is launchable through this adapter.
	Agents []AgentSpec
	// Connection reports the live state of an adapter that observes an
	// external program; nil for launchers, whose availability is their
	// binaries.
	Connection func() api.AgentAdapterConnection
}

// AgentSpec is one agent an adapter declares: a stable id (persisted by
// terminals and threads — never renamed), a display name, and the TUI
// seam when the adapter can launch it.
type AgentSpec struct {
	ID   string
	Name string
	TUI  TUI
}

// Quote wraps a string in single quotes for the user's login shell, the
// one quoting that is safe across POSIX shells: injected paths ride a
// single command string, and an unquoted space or metacharacter would
// split or execute.
func Quote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// CondenseTitle trims provider-reported text (a first prompt, a thread
// preview) into a one-line observed default title, cutting on a word
// boundary, else a rune boundary — never mid-character.
func CondenseTitle(s string) string {
	const limit = 50
	title := strings.Join(strings.Fields(s), " ")
	if len(title) <= limit {
		return title
	}
	if cut := strings.LastIndexByte(title[:limit], ' '); cut > 0 {
		return title[:cut]
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(title[cut]) {
		cut--
	}
	return title[:cut]
}
