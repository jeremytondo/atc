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

// Version headers ride both ways on every request/response — the entire
// skew handshake (ATC-247 §6). They live here, not in the server, so
// client-side code never imports server internals.
const (
	ClientVersionHeader = "Atc-Client-Version"
	ServerVersionHeader = "Atc-Server-Version"
)

// Health is the GET /v1/health response body.
type Health struct {
	Status  string `json:"status" enum:"ok" doc:"Liveness state of the server."`
	Version string `json:"version" doc:"Version of the running server binary."`
}
