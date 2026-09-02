// Package integrations is the Integration catalog (ATC-294): a
// compiled-in, read-only model of the external tools ATC works with, and
// the launch glue that turns an App reference into a terminal create.
//
// An Integration is ATC's stable relationship with one tool — claude,
// codex, t3code, zmx. Its id is durable provenance, persisted on every
// thread it produces, and never renamed. An Integration may expose
// Integration-scoped agent descriptors (opaque ids: the same string under
// two Integrations implies nothing), Apps it owns (user-facing
// interaction surfaces such as codex/tui, immutable descriptors with no
// storage or lifecycle), a live connection when ATC keeps one to the
// tool's program, and implementations of the narrow typed interfaces the
// domains define — the Terminals Driver, the Threads observation seams,
// the App terminal interactions here. There is no universal Integration
// interface and no direct/external kind: a tool implements whichever
// seams apply, and the wire summary of its capabilities is display only.
//
// The catalog has no storage; entries are assembled at startup from
// built-in registrations — one package per Integration under
// internal/integrations/, one registration line in the composition root
// — and availability is probed against the machine on every read.
// Connection-backed Integrations emit integration.updated on connection
// transitions; executable probes emit nothing. The dependency direction
// is one-way: this package calls into terminals to create launch
// terminals, and the terminals domain never depends on it.
package integrations

import (
	"context"
	"strings"
	"unicode/utf8"

	"github.com/jeremytondo/atc/internal/api"
)

// Integration is one catalog registration.
type Integration struct {
	// ID is the stable id, persisted on every thread the Integration
	// produces and every terminal its Apps launch — never renamed.
	ID   string
	Name string
	// Capabilities summarizes the typed domain interfaces the Integration
	// implements, for the wire; the composition root wires the interfaces
	// themselves.
	Capabilities []api.IntegrationCapability
	// Agents lists the agent descriptors the Integration exposes, in
	// display order. Descriptors describe; they never launch.
	Agents []api.IntegrationAgent
	// Apps lists the Apps the Integration owns, in display order.
	Apps []App
	// Executable, when set, is the tool's runtime prerequisite on the
	// server's machine: the binary the availability probe resolves on PATH
	// for the Integration and its terminal Apps, and how to install it.
	Executable *Executable
	// Connection reports the live state of an Integration that keeps a
	// long-lived connection to its program; nil otherwise. A connected
	// Integration is available; its executable (if any) is not consulted.
	Connection func() api.IntegrationConnection
}

// Executable names a tool's binary and how to install it.
type Executable struct {
	Binary      string
	InstallHint string
}

// App is one interaction surface an Integration owns.
type App struct {
	// ID is the App's id within its Integration ("tui"); the qualified id
	// on the wire and in storage is integration/app.
	ID   string
	Name string
	// Agents lists the Integration-scoped ids of the agents the App
	// supports, for the descriptor; launching never selects one.
	Agents []string
	// Terminal is the App's typed terminal interaction — start and resume
	// inside an ATC terminal; nil for an App that never runs in one.
	Terminal TerminalApp
	// Handoff marks an App reached only through the links its Integration
	// derives per thread (a web UI, a desktop app). The server never
	// launches it.
	Handoff bool
}

// QualifiedAppID joins an Integration id and an App id into the wire
// form.
func QualifiedAppID(integrationID, appID string) string {
	return integrationID + "/" + appID
}

// LaunchContext is the per-launch context the composition injects into a
// terminal App (ATC-255): the terminal's working directory, its minted
// identity, and for a resume (ATC-282) the provider conversation to
// reopen. Integrations use it to wire observation — Claude hook settings,
// the Codex pending launch — without any of it appearing in the API
// contract. Grow it only when an Integration consumes the addition.
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

// TerminalApp is the typed interaction of an App that runs inside an ATC
// terminal: how to start a fresh conversation there, and how to resume
// one the App produced.
type TerminalApp interface {
	// Command composes the command string the launch terminal runs through
	// the user's login shell — the tool's fresh start, or its exact resume
	// when launch.ResumeConversationID is set. It never appears in the API
	// contract, so flags can change without a wire-visible change. Any
	// injected value must be shell-quoted (Quote) — the command is a
	// single string run through the user's login shell. An error refuses
	// the launch before the terminal record exists; the context bounds any
	// launch-time work.
	Command(ctx context.Context, launch LaunchContext) (string, error)
}

// LaunchPreparer is the optional second terminal-App seam: launch-time
// work that may block — waiting on another launch in the same directory,
// starting a provider's shared server — runs here, before the terminals
// domain takes its commit lock (Command runs under it). An error refuses
// the launch before any record exists; abort is called if the create
// fails afterwards, so the preparation can be undone.
type LaunchPreparer interface {
	PrepareLaunch(ctx context.Context, launch LaunchContext) (abort func(), err error)
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
