// Package agents is the Agents domain (ATC-254): a compiled-in, read-only
// catalog of the coding agents ATC can work with, and the launch glue that
// turns an agent reference into a terminal create. The catalog has no
// storage and emits no events; entries are assembled at startup from
// built-in registrations — one package per agent under internal/agents/,
// one registration line in the composition root — and availability is
// probed against the machine on every read.
//
// Capabilities are derived from which adapters an entry registers, never
// declared separately, and their names align with access methods (tui
// today, direct later); protocol names stay out of the vocabulary. The
// dependency direction is one-way: this package calls into terminals to
// create launch terminals, and the terminals domain never depends on
// agents (ATC-251 doctrine).
package agents

import (
	"context"
	"strings"
	"unicode/utf8"
)

// CapabilityTUI is the one capability in ATC-254: the agent can be
// launched as a TUI in an ATC-managed terminal.
const CapabilityTUI = "tui"

// LaunchContext is the per-launch context the composition injects into an
// adapter's command (ATC-255): the minted terminal identity and its
// working directory. Adapters use it to wire observation — Claude hook
// settings, the Codex --remote socket — without any of it appearing in
// the API contract.
type LaunchContext struct {
	// TerminalID is the terminal being created for this launch.
	TerminalID string
	// Directory is the terminal's working directory (the project's).
	Directory string
}

// TUIAdapter is the per-tool seam behind the tui capability, written once
// and implemented per agent.
type TUIAdapter interface {
	// Command composes the command string a launch terminal runs through
	// the user's login shell. It never appears in the API contract, so
	// flags can change without a wire-visible change. Any injected path
	// must be shell-quoted (Quote) — the command is a single string run
	// through the user's login shell. An error refuses the launch before
	// the terminal record exists; the context bounds launch-time work
	// like starting the shared Codex app-server.
	Command(ctx context.Context, launch LaunchContext) (string, error)
	// Binary is the executable name the availability probe resolves on the
	// server's PATH.
	Binary() string
	// InstallHint tells the user how to install the tool.
	InstallHint() string
}

// Entry is one catalog registration: a stable id (persisted by terminals
// and threads — never renamed), a display name, and the tool's adapters.
// A nil adapter simply means the entry lacks that capability.
type Entry struct {
	ID   string
	Name string
	TUI  TUIAdapter
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
