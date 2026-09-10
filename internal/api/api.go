// Package api is the /v1 wire contract and its Go client (ATC-264). The
// structs here are the single source of truth for the API shape: the
// server's Huma handlers wrap them (the served OpenAPI document derives
// from these same definitions), and every in-repo Go consumer imports
// them, so a contract change that breaks a consumer is a compile error.
// The validation/doc tags are Huma's; they are inert to clients.
//
// The package is deliberately stdlib-only, which keeps a later promotion
// out of internal/ for third-party Go consumers cheap. Codegen exists only
// across language boundaries: external consumers generate from the served
// /openapi.json, never from a checked-in artifact and never in this repo.
package api

// Protocol is the ATC client/server contract generation (ATC-325): the
// one compatibility fact clients and servers exchange. It is independent
// of the app release, channel, and build identity — a breaking change to
// the /v1 contract bumps it; anything else leaves it alone. Equal
// protocols connect whatever the releases; unequal protocols refuse
// every ordinary operation on both sides (the server with a
// protocol_mismatch problem, the client before it reads a body) while
// the headers below still let a probe explain what it found. There is
// no negotiation, capability list, or supported range: one generation.
const Protocol = 1

// Headers ride both ways on every request/response. Atc-Protocol carries
// the sender's Protocol; the version headers carry release identity for
// diagnostics only (ATC-247 §6, amended by ATC-325). They live here, not
// in the server, so client-side code never imports server internals.
const (
	ProtocolHeader      = "Atc-Protocol"
	ClientVersionHeader = "Atc-Client-Version"
	ServerVersionHeader = "Atc-Server-Version"
)

// Health is the GET /v1/health response body.
type Health struct {
	Status   string `json:"status" enum:"ok" doc:"Liveness state of the server."`
	Version  string `json:"version" doc:"Version of the running server binary."`
	Protocol int    `json:"protocol" doc:"ATC protocol the server speaks; clients must speak the same."`
}
