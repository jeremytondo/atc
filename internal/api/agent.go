package api

// Agent is one entry of the /v1/agents catalog (ATC-254): a compiled-in,
// read-only registry of the coding agents ATC can work with. The resolved
// launch command never appears on the wire — clients launch by id and the
// server composes the command through the agent's adapters.
type Agent struct {
	ID           string            `json:"id" doc:"Stable catalog id; recorded on terminals launched for this agent and never renamed."`
	Name         string            `json:"name" doc:"Display name."`
	Capabilities []AgentCapability `json:"capabilities" doc:"Derived from the adapters the entry registers, with availability probed against the server's machine on every read."`
}

// AgentCapability reports one capability's availability. Availability is
// advisory: the launch-time probe and the terminal's exit evidence are the
// operative checks.
type AgentCapability struct {
	Capability  string `json:"capability" enum:"tui" doc:"Capability name; tui means the agent can be launched in an ATC-managed terminal."`
	Available   bool   `json:"available" doc:"Whether the capability's binary resolves on the server's PATH right now."`
	InstallHint string `json:"installHint" doc:"How to install the tool backing this capability; present whether or not it is available."`
}

// AgentList is the GET /v1/agents response body, in registration order.
type AgentList struct {
	Agents []Agent `json:"agents"`
}

// AgentLaunchParams is the POST /v1/agents/{id}/launch request body: the
// same params as terminal create minus command and agent. The route is a
// pure alias for POST /v1/terminals with an agent reference.
type AgentLaunchParams struct {
	ProjectID string `json:"projectId" minLength:"1" doc:"Project the terminal belongs to; its directory becomes the terminal's working directory."`
	Name      string `json:"name,omitempty" doc:"Display name; defaults to the agent's display name."`
}
