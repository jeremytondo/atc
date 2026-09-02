package api

import "time"

// The three provenance terms (ATC-285): a model is what a conversation
// runs on (gpt-5); an agent is the harness+model label a thread runs
// under (claude, codex); an adapter is the thing that produces threads —
// a local TUI launcher (claude, codex) or an external program ATC
// observes (t3code). One adapter may produce threads for several agents.

// Agent is one entry of the /v1/agents catalog: a launchable agent,
// derived from the compiled-in adapter list. Clients launch by id through
// terminal create; the server resolves the command through whichever
// adapter can start the agent.
type Agent struct {
	ID        string   `json:"id" doc:"Stable agent id; recorded on terminals and threads and never renamed."`
	Name      string   `json:"name" doc:"Display name."`
	Available bool     `json:"available" doc:"Whether some adapter can launch the agent right now (its binary resolves on the server's PATH). Advisory: the launch-time probe is the operative check."`
	Adapters  []string `json:"adapters" doc:"Ids of the adapters that produce threads for this agent, in registration order."`
}

// AgentList is the GET /v1/agents response body, in registration order.
type AgentList struct {
	Agents []Agent `json:"agents"`
}

// AgentAdapterConnectionState is the live state of an adapter that
// observes an external program.
type AgentAdapterConnectionState string

const (
	// AdapterConnected: the subscription is live and thread statuses are
	// current.
	AdapterConnected AgentAdapterConnectionState = "connected"
	// AdapterConnecting: the program is present but the subscription is
	// not up — first connect, or a drop being retried with backoff. Live
	// statuses of its threads are coerced to unknown meanwhile.
	AdapterConnecting AgentAdapterConnectionState = "connecting"
	// AdapterUnavailable: the program is not installed or not running, or
	// answered with a payload ATC cannot read.
	AdapterUnavailable AgentAdapterConnectionState = "unavailable"
	// AdapterAuthFailed: pairing or the credential exchange failed; detail
	// carries the reason. Retried slowly, never in a tight loop.
	AdapterAuthFailed AgentAdapterConnectionState = "auth_failed"
)

// AgentAdapterConnection reports an observing adapter's connection.
type AgentAdapterConnection struct {
	State  AgentAdapterConnectionState `json:"state" enum:"connected,connecting,unavailable,auth_failed" doc:"Connection state."`
	Since  time.Time                   `json:"since" doc:"When the state was last entered."`
	Detail string                      `json:"detail" doc:"Human-readable explanation of the current state."`
}

// AgentAdapter is one entry of /v1/agents/adapters: a compiled-in producer
// of threads and its health on this machine.
type AgentAdapter struct {
	ID        string   `json:"id" doc:"Stable adapter id; recorded on every thread it produces."`
	Name      string   `json:"name" doc:"Display name."`
	Agents    []string `json:"agents" doc:"Ids of the agents this adapter produces threads for."`
	Available bool     `json:"available" doc:"For a launching adapter, whether its binary resolves on the server's PATH; for an observing adapter, whether it is connected."`
	// InstallHint is present for launching adapters whether or not the
	// binary is; observing adapters explain themselves through connection.
	InstallHint string                  `json:"installHint,omitempty" doc:"How to install the tool behind a launching adapter."`
	Connection  *AgentAdapterConnection `json:"connection,omitempty" doc:"Live connection of an adapter that observes an external program; omitted for launching adapters."`
}

// AgentAdapterList is the GET /v1/agents/adapters response body, in
// registration order.
type AgentAdapterList struct {
	Adapters []AgentAdapter `json:"adapters"`
}
