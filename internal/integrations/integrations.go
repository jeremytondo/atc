// Package integrations is the Integration catalog (ATC-294): a
// compiled-in, read-only model of the external systems ATC integrates with, the
// launch glue that turns an App reference into a terminal create, and the
// routing of a thread create to the Integration that performs it
// (ATC-289).
//
// An Integration is ATC's stable relationship with one external system — claude,
// codex, t3code, zmx. Its id is durable provenance, persisted on every
// thread it produces, and never renamed. An Integration may expose
// Integration-scoped agent descriptors (opaque ids: the same string under
// two Integrations implies nothing), Apps it owns (user-facing
// interaction surfaces such as codex/tui, immutable descriptors with no
// storage or lifecycle), a live connection when ATC keeps one to the
// tool's program, and implementations of the narrow typed interfaces the
// domains define — the Terminals Driver, the Threads observation seams,
// the App terminal interactions and the thread creation seam here. There
// is no universal Integration
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
	"time"
	"unicode/utf8"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
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
	// PrepareThread is the Integration's thread-creation seam (ATC-289):
	// it resolves a create against the program's live state without
	// sending anything, refusing with ErrNotConnected (the state and
	// detail in the message) while the program is not reachable and with
	// ErrProjectNotRegistered when the directory is not a project the
	// program knows. Nil for an Integration that cannot start
	// conversations in its program.
	PrepareThread func(ctx context.Context, req ThreadCreation) (PreparedThread, error)
	// Messages is the Integration's message seam (ATC-307): sending text
	// into a conversation its program owns. Nil for an Integration that
	// cannot; declared together with the threads.send capability.
	Messages ThreadMessenger
	// Approvals is the Integration's approval seam (ATC-307): deciding
	// a request its program reports pending. Nil for an Integration that
	// cannot; declared together with the threads.decide capability.
	Approvals ApprovalDecider
	// Inputs is the Integration's structured-answer seam (ATC-308):
	// answering the questions its program reports pending. Nil for an
	// Integration that cannot; declared together with the threads.answer
	// capability.
	Inputs InputAnswerer
	// Stops is the Integration's stop seam (ATC-308): stopping a
	// conversation's work in its program. Nil for an Integration that
	// cannot; declared together with the threads.stop capability.
	Stops ThreadStopper
}

// InputAnswerer answers pending structured requests (ATC-308). The
// domain has validated the answer set against the request and recorded
// it; the Integration translates it into the program's command, under
// identities derived from the answer's, so a retry after a lost answer
// is deduplicated by the program. The program committing the command is
// not the request being resolved: that evidence arrives through the
// Integration's observation of the request.
type InputAnswerer interface {
	// PrepareAnswer resolves an answer against the program's live state
	// without sending anything: ErrNotConnected while the program is not
	// reachable. It returns the dispatch, which sends the answer and
	// returns once the program has committed it: ErrAnswerRejected with
	// the program's own reason when it refuses, ErrDeliveryUncertain when
	// it never answers, ErrNotConnected when the connection went away
	// since preparation.
	PrepareAnswer(ctx context.Context, providerID string) (AnswerDispatch, error)
}

// AnswerDispatch sends one prepared answer.
type AnswerDispatch func(ctx context.Context, answer InputAnswer) error

// InputAnswer is one answer to dispatch: the provider's request id
// (private), the answers keyed by the provider's own question ids (a
// conversational reply arrives translated by the domain, as the first
// question's custom answer), the ATC answer id the program-side
// identities derive from, and when it was submitted.
type InputAnswer struct {
	RequestID string
	Answers   []ProviderAnswer
	Key       string
	CreatedAt time.Time
}

// ProviderAnswer is one question's answer in the provider's terms, as
// the threads domain translates it.
type ProviderAnswer = threads.ProviderAnswer

// ThreadStopper stops a conversation's work in its program (ATC-308):
// the turn running and any submitted turn that has not started, while
// preserving the conversation for a later message to continue. The
// domain records the operation before dispatch and resolves it only on
// the evidence the Integration observes afterwards; the program
// committing the command is not the work being stopped.
type ThreadStopper interface {
	// PrepareStop resolves a stop against the program's live state without
	// sending anything: ErrNotConnected while the program is not
	// reachable. It returns the dispatch, which sends the stop and returns
	// once the program has committed it: ErrStopRejected with the program's
	// own reason when it refuses, ErrDeliveryUncertain when it never
	// answers, ErrNotConnected when the connection went away since
	// preparation.
	PrepareStop(ctx context.Context, providerID string) (StopDispatch, error)
}

// StopDispatch sends one prepared stop.
type StopDispatch func(ctx context.Context, stop ThreadStop) error

// ThreadStop is one stop to dispatch: the ATC stop id the program-side
// identities derive from, and when it was accepted — the instant the
// program's evidence is correlated against.
type ThreadStop struct {
	Key       string
	CreatedAt time.Time
}

// ThreadMessenger sends messages into existing conversations (ATC-307).
// The domain owns the message's identity and text; the Integration owns
// the command it becomes, and presents the same command for the same
// message every time, so a retry after a lost answer is deduplicated by
// the program rather than delivered twice.
type ThreadMessenger interface {
	// PrepareMessage resolves a message against the program's live state
	// without sending anything: ErrNotConnected while the program is not
	// reachable. It reports how the program treats a message while a turn
	// runs, and returns the dispatch.
	PrepareMessage(ctx context.Context, providerID string) (PreparedMessage, error)
}

// PreparedMessage is a message the Integration has resolved but not yet
// sent. Steers reports that the program folds a message sent while a
// turn runs into that turn (the agent continues the same execution with
// the new direction) rather than starting another once it ends; the
// domain then directs the running turn instead of minting a pending
// one. Dispatch sends the message and returns once the program has
// committed it: ErrMessageRejected with the program's own reason when it
// refuses, ErrDeliveryUncertain when it never answers — the program may
// hold the message, and the same dispatch again reconciles —
// ErrNotConnected when the connection went away since preparation.
type PreparedMessage struct {
	Steers   bool
	Dispatch func(ctx context.Context, msg ThreadMessage) error
}

// ThreadMessage is one message to dispatch: its ATC identity — what the
// Integration derives its stable program-side identities from — its
// text, and when it was submitted.
type ThreadMessage struct {
	ID        string
	Text      string
	CreatedAt time.Time
}

// ApprovalDecider decides pending approval requests (ATC-307). The
// domain has validated the decision against the request; the Integration
// translates it and reports the program's answer: nil once committed,
// ErrDecisionRejected with the program's reason when refused,
// ErrDeliveryUncertain when the program never answered — the same
// request again, under the same key, reconciles — ErrNotConnected while
// the program is not reachable.
type ApprovalDecider interface {
	DecideApproval(ctx context.Context, req ApprovalDecision) error
}

// ApprovalDecision is one decision to dispatch: the private request
// identities, the decision in ATC's vocabulary, and a key stable for the
// (request, decision) pair, for the Integration's deduplication.
type ApprovalDecision struct {
	ProviderID string
	RequestID  string
	Decision   api.ApprovalDecision
	Key        string
}

// ThreadCreation is one request to start a conversation in an
// Integration's program (ATC-289): what the caller chose, with the Project
// already resolved to its canonical directory. Model and options are
// opaque strings the Integration copies to its program untouched — ATC
// keeps no model catalog and judges neither.
type ThreadCreation struct {
	AgentID   string
	Directory string
	Prompt    string
	Model     string
	Options   []api.ThreadOption
}

// PreparedThread is a creation the Integration has resolved against its
// live state but not yet sent: the provider conversation id it chose —
// the private identity the thread record is created under before
// anything is dispatched, and how the program's later reports of the
// conversation find that record — the title it will give the thread, and
// Dispatch, which sends the command and returns once the program has
// committed the thread and its first turn. A failed Dispatch wraps
// ErrThreadCreationFailed with the program's own message; ErrNotConnected
// when the connection went away since preparation.
type PreparedThread struct {
	ProviderID string
	Title      string
	Dispatch   func(ctx context.Context) error
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
	// Directory is the session's working directory: the request's, else
	// the space's — for a resume as much as for a fresh start.
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
