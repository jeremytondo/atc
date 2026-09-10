package api

// The document origin (ATC-318): the server's report of the unprivileged
// listener that serves published artifacts to browsers — a distinct origin
// from this API, local by default, on the tailnet when the API is. There
// is one status resource; the content itself is never served here.

// DocumentOriginState is the coarse readiness of the document origin.
type DocumentOriginState string

const (
	// OriginStarting: the listener is being bound.
	OriginStarting DocumentOriginState = "starting"
	// OriginReady: the listener serves at URL.
	OriginReady DocumentOriginState = "ready"
	// OriginUnavailable: the listener could not bind (Reason explains,
	// typically a port conflict); retried automatically, and the rest of
	// the server is unaffected. Publishing and source retrieval keep
	// working through this API.
	OriginUnavailable DocumentOriginState = "unavailable"
)

// DocumentOrigin is the GET /v1/document-origin response body.
type DocumentOrigin struct {
	State DocumentOriginState `json:"state" enum:"starting,ready,unavailable" doc:"Readiness of the local document listener."`
	// URL is the local base URL readers open.
	URL string `json:"url" doc:"Local base URL of the document origin."`
	// Reason explains a starting or unavailable state.
	Reason string `json:"reason,omitempty" doc:"Why the listener is not ready."`
	// Tailnet reports exposure through Tailscale, which follows the API's
	// own tailnet exposure setting.
	Tailnet DocumentOriginTailnet `json:"tailnet" doc:"Exposure of the document origin on the tailnet."`
}

// TailnetState is the readiness of the document origin's tailnet exposure.
type TailnetState string

const (
	// TailnetDisabled: the launch does not expose the API on the tailnet,
	// so documents are local only.
	TailnetDisabled TailnetState = "disabled"
	// TailnetStarting: exposure is converging; Reason and Action explain.
	TailnetStarting TailnetState = "starting"
	// TailnetReady: the document origin is served at URL on the tailnet.
	TailnetReady TailnetState = "ready"
)

// DocumentOriginTailnet is the tailnet exposure of the document origin.
type DocumentOriginTailnet struct {
	State  TailnetState `json:"state" enum:"disabled,starting,ready" doc:"Readiness of tailnet exposure."`
	URL    string       `json:"url,omitempty" doc:"Tailnet base URL, once the node's name is known."`
	Reason string       `json:"reason,omitempty" doc:"Why exposure is not ready."`
	Action string       `json:"action,omitempty" doc:"Operator action Tailscale asked for, when one is awaited."`
}
