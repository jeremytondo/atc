package api

// The document origin (ATC-318): the server's report of the unprivileged
// listener that serves published artifacts to browsers — a distinct origin
// from this API, local by default, on the tailnet when the API is. There
// is one status resource; the content itself is never served here.

// DocumentsState is the coarse readiness of the document origin.
type DocumentsState string

const (
	// DocumentsStarting: the listener is being bound.
	DocumentsStarting DocumentsState = "starting"
	// DocumentsReady: the listener serves at URL.
	DocumentsReady DocumentsState = "ready"
	// DocumentsUnavailable: the listener could not bind (Reason explains,
	// typically a port conflict); retried automatically, and the rest of
	// the server is unaffected. Publishing and source retrieval keep
	// working through this API.
	DocumentsUnavailable DocumentsState = "unavailable"
)

// Documents is the GET /v1/documents response body.
type Documents struct {
	State DocumentsState `json:"state" enum:"starting,ready,unavailable" doc:"Readiness of the local document listener."`
	// URL is the local base URL readers open.
	URL string `json:"url" doc:"Local base URL of the document origin."`
	// Reason explains a starting or unavailable state.
	Reason string `json:"reason,omitempty" doc:"Why the listener is not ready."`
	// Tailnet reports exposure through Tailscale, which follows the API's
	// own tailnet exposure setting.
	Tailnet DocumentsTailnet `json:"tailnet" doc:"Exposure of the document origin on the tailnet."`
}

// DocumentsTailnetState is the readiness of the tailnet exposure.
type DocumentsTailnetState string

const (
	// TailnetDisabled: the launch does not expose the API on the tailnet,
	// so documents are local only.
	TailnetDisabled DocumentsTailnetState = "disabled"
	// TailnetStarting: exposure is converging; Reason and Action explain.
	TailnetStarting DocumentsTailnetState = "starting"
	// TailnetReady: the document origin is served at URL on the tailnet.
	TailnetReady DocumentsTailnetState = "ready"
)

// DocumentsTailnet is the tailnet exposure of the document origin.
type DocumentsTailnet struct {
	State  DocumentsTailnetState `json:"state" enum:"disabled,starting,ready" doc:"Readiness of tailnet exposure."`
	URL    string                `json:"url,omitempty" doc:"Tailnet base URL, once the node's name is known."`
	Reason string                `json:"reason,omitempty" doc:"Why exposure is not ready."`
	Action string                `json:"action,omitempty" doc:"Operator action Tailscale asked for, when one is awaited."`
}
