package api

import "time"

// The tool-level vocabulary (ATC-294): an Integration is ATC's compiled-in
// relationship with one external system (claude, codex, t3code, zmx); an
// App is a user-facing interaction surface the Integration owns
// (codex/tui); an agent is an Integration-scoped descriptor of what the
// tool runs conversations under, opaque outside its Integration. There is
// no global agent catalog: the same agent id under two Integrations
// implies nothing.

// IntegrationCapability names one typed domain interface an
// Integration implements, as domain.verb — display only; runtime behavior dispatches
// through the typed interfaces themselves.
type IntegrationCapability string

const (
	// CapabilityTerminalDriver: the Integration drives terminal sessions
	// (the Terminals domain's Driver seam).
	CapabilityTerminalDriver IntegrationCapability = "terminals.drive"
	// CapabilityThreadObservation: the Integration feeds thread evidence to
	// the Threads domain — from an ATC-launched App or from a provider's
	// own program it observes.
	CapabilityThreadObservation IntegrationCapability = "threads.observe"
	// CapabilityThreadCreation: the Integration starts new conversations in
	// its program with a first prompt (POST /v1/threads, ATC-289).
	CapabilityThreadCreation IntegrationCapability = "threads.create"
)

// AppInteraction is one typed interaction an App offers.
type AppInteraction string

const (
	// AppTerminalStart: the App starts a new conversation inside an ATC
	// terminal (terminal create with appId).
	AppTerminalStart AppInteraction = "terminal_start"
	// AppTerminalResume: the App resumes a thread it produced inside an ATC
	// terminal.
	AppTerminalResume AppInteraction = "terminal_resume"
	// AppHandoff: the App is reached through links the Integration derives
	// per thread (a web UI, a desktop app); the server never launches it.
	AppHandoff AppInteraction = "handoff"
)

// IntegrationConnectionState is the live state of an Integration that
// keeps an observable long-lived connection to its program.
type IntegrationConnectionState string

const (
	// IntegrationConnected: the connection is live and thread statuses are
	// current.
	IntegrationConnected IntegrationConnectionState = "connected"
	// IntegrationConnecting: the program is present but the connection is
	// not up — first connect, or a drop being retried with backoff. Live
	// statuses of its threads are coerced to unknown meanwhile.
	IntegrationConnecting IntegrationConnectionState = "connecting"
	// IntegrationUnavailable: the program is not installed or not running,
	// or answered with a payload ATC cannot read.
	IntegrationUnavailable IntegrationConnectionState = "unavailable"
	// IntegrationAuthFailed: pairing or the credential exchange failed;
	// detail carries the reason. Retried slowly, never in a tight loop.
	IntegrationAuthFailed IntegrationConnectionState = "auth_failed"
)

// IntegrationConnection reports an Integration's live connection.
type IntegrationConnection struct {
	State  IntegrationConnectionState `json:"state" enum:"connected,connecting,unavailable,auth_failed" doc:"Connection state."`
	Since  time.Time                  `json:"since" doc:"When the state was last entered."`
	Detail string                     `json:"detail" doc:"Human-readable explanation of the current state."`
}

// IntegrationAgent is one agent descriptor an Integration exposes. Ids
// are opaque and scoped to the Integration; threads record them as
// reported, whether or not they appear here.
type IntegrationAgent struct {
	ID   string `json:"id" doc:"Integration-scoped agent id, as the Integration reports it on threads."`
	Name string `json:"name" doc:"Display name."`
}

// App is one interaction surface an Integration owns: an immutable
// catalog descriptor, never a stored resource or an installed instance.
type App struct {
	ID           string           `json:"id" doc:"Integration-qualified id (integration/app); recorded on terminals and threads and never renamed."`
	Name         string           `json:"name" doc:"Display name."`
	Agents       []string         `json:"agents" doc:"Integration-scoped ids of the agents the App supports."`
	Interactions []AppInteraction `json:"interactions" doc:"Typed interactions the App offers: terminal_start and terminal_resume run inside an ATC terminal; handoff opens through a thread's links."`
	Available    *bool            `json:"available,omitempty" doc:"Whether the App can run in an ATC terminal right now (its executable resolves on the server's PATH). Omitted for Apps ATC has no server-side evidence about (desktop, web). Advisory: the launch-time probe is the operative check."`
}

// Integration is one entry of the /v1/integrations catalog.
type Integration struct {
	ID           string                  `json:"id" doc:"Stable Integration id; recorded on every thread it produces and never renamed."`
	Name         string                  `json:"name" doc:"Display name."`
	Capabilities []IntegrationCapability `json:"capabilities" doc:"Typed domain capabilities the Integration implements, for display."`
	Agents       []IntegrationAgent      `json:"agents" doc:"Agent descriptors the Integration exposes, in display order."`
	Apps         []App                   `json:"apps" doc:"Apps the Integration owns, in display order."`
	Available    bool                    `json:"available" doc:"Evidence-based health: for an Integration with a connection, whether it is connected; otherwise whether its executable resolves on the server's PATH."`
	// InstallHint is present whenever the Integration is backed by an
	// executable, whether or not it currently resolves; connection-backed
	// Integrations explain themselves through connection.
	InstallHint string                 `json:"installHint,omitempty" doc:"How to install the tool behind the Integration."`
	Connection  *IntegrationConnection `json:"connection,omitempty" doc:"Live connection of an Integration that observes a provider's own program; omitted otherwise."`
}

// IntegrationList is the GET /v1/integrations response body, in
// registration order.
type IntegrationList struct {
	Integrations []Integration `json:"integrations"`
}
