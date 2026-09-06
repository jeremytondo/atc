// Package linear is Linear's Integration (ATC-302): the one that faces the
// other way. Linear sends work in — an explicit @atc mention on an issue
// creates an Agent Session — and receives the result: ATC starts exactly
// one Thread with its initial Turn in T3 Code, under a fixed execution
// profile and in one configured Project, tells the session where the
// conversation opens, watches that exact Turn for as long as it takes,
// and posts its final response back. Everything a person does with the
// run — answer a question, approve, follow up, stop — happens in T3;
// Linear only ever gets told where to go and how it ended.
//
// The Integration owns Linear's protocol and nothing of T3's: it verifies
// deliveries for the shared webhook ingress (verify.go), interprets
// sessions (process.go), asks the application coordinator to start the
// Thread and reads the normalized Thread through the threads domain
// (service.go), and speaks Linear's GraphQL API (api.go). Its state is
// durable Integration state in ATC's store, not a domain: one row per
// session binding it to the Thread and Turn it started, and an outbox of
// the calls owed to Linear, each under a deterministic key so a repeated
// inbox delivery, observation, or restart can never post twice
// (outbox.go). Credentials live in one 0600 setup file (setup.go) and
// appear nowhere else — never in status, logs, responses, or the
// restricted receiver.
//
// Timing is Linear's: the receiver must answer within five seconds, so
// Verify is pure computation and Process only writes rows; the session
// must hear something within ten seconds of its creation, so the
// acknowledgement is an outbox row the sender posts immediately, before
// T3 is asked for anything. Tracking has no duration bound — a Turn that
// runs for hours, waits for a person, or outlives a connectivity outage
// stays watched — while every individual request is bounded and retried
// with backoff.
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
