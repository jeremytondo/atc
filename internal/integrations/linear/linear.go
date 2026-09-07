// Package linear is Linear's Integration (ATC-302, ATC-309): the one that
// faces the other way. Linear sends work in — an explicit @atc mention on
// an issue creates an Agent Session — and the session becomes a
// conversation: ATC starts exactly one Thread in T3 Code, under a fixed
// execution profile and in one configured Project, binds the session to
// it for good, and relays what the person does in the session through
// the shared Thread capabilities. A message continues the Thread; the
// agent's questions and approval requests are presented as elicitations
// with the exact choices offered, and a choice, a reply in words, or a
// decision goes back through the answer and decision capabilities; a
// stop goes through the stop capability. Every result — a turn's final
// response, a question resolved, a stop confirmed, a failure — is
// reported against the exact submission it belongs to, on evidence,
// never on a command's acceptance. Linear interprets nothing: a reply is
// the user's text, verbatim, and the agent reads it.
//
// The Integration owns Linear's protocol and nothing of T3's: it verifies
// deliveries for the shared webhook ingress (verify.go), turns sessions
// and prompts into durable submissions (process.go), drives each session
// through the application coordinator — the start, then every
// submission in order (session.go) — reads the normalized Thread through
// the threads domain to report outcomes and present requests
// (observe.go), and speaks Linear's GraphQL API (api.go). Its state is
// durable Integration state in ATC's store, not a domain: a session row
// binding it to its Thread, a submission row per Linear input with its
// target, operation, delivery, and outcome, a request row per question
// or approval presented with the options offered, and an outbox of the
// calls owed to Linear, each under a deterministic key so a repeated
// inbox delivery, observation, or restart can never post twice
// (outbox.go). Credentials live in one 0600 setup file (setup.go) and
// appear nowhere else — never in status, logs, responses, or the
// restricted receiver.
//
// Timing is Linear's: the receiver must answer within five seconds, so
// Verify is pure computation and Process only writes rows; the session
// must hear something within ten seconds of its creation, so the
// acknowledgement is an outbox row the sender posts immediately, before
// T3 is asked for anything. A session has no duration bound — a
// conversation that runs for days, waits for a person, or outlives a
// connectivity outage stays bound and watched — while every individual
// request is bounded and retried with backoff.
package linear

import (
	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
)

// ID is the Integration id; RoutePath is where Linear's deliveries arrive
// under the public webhook base URL.
const (
	ID        = "linear"
	RoutePath = "/linear"
)

// The fixed execution profile (ATC-302): every session starts one T3 Code
// thread under Codex at high effort. The Project comes from the setup
// file; nothing in a prompt can redirect the work elsewhere. The
// Integration ids are named as strings on purpose — Integrations never
// call each other, and this one only names what it asks the application
// coordinator for.
const (
	profileIntegration = "t3code"
	profileAgent       = "codex"
	profileModel       = "gpt-5.6-sol"
	profileEffort      = "high"
)

// Integration is Linear's catalog registration: no Apps, no agents, no
// domain capabilities — Linear starts work and receives results through
// the domains, it does not implement one — and a connection report that
// carries the setup, credential, and ingress state an operator needs.
func Integration(service *Service) integrations.Integration {
	if service == nil {
		panic("linear.Integration: service must not be nil")
	}
	return integrations.Integration{
		ID:           ID,
		Name:         "Linear",
		Capabilities: []api.IntegrationCapability{},
		Connection:   service.Connection,
	}
}
