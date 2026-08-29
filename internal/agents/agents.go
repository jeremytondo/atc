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

// CapabilityTUI is the one capability in ATC-254: the agent can be
// launched as a TUI in an ATC-managed terminal.
const CapabilityTUI = "tui"

// TUIAdapter is the per-tool seam behind the tui capability, written once
// and implemented per agent. Launch-only in this ticket; the threads
// effort owns extending it with status wiring.
type TUIAdapter interface {
	// Command is the command string a launch terminal runs through the
	// user's login shell. It never appears in the API contract, so flags
	// can change without a wire-visible change.
	Command() string
	// Binary is the executable name the availability probe resolves on the
	// server's PATH.
	Binary() string
	// InstallHint tells the user how to install the tool.
	InstallHint() string
}

// Entry is one catalog registration: a stable id (persisted by terminals
// and later threads — never renamed), a display name, and the tool's
// adapters. A nil adapter simply means the entry lacks that capability.
type Entry struct {
	ID   string
	Name string
	TUI  TUIAdapter
}
