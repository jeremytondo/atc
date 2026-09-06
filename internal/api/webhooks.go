package api

// Webhook ingress (ATC-306): the server's report of its public webhook
// endpoint — whether intake is enabled and ready, where deliveries arrive,
// which Integrations listen, what an operator must do to finish setup,
// and how the durable inbox is doing. Ingress is supporting
// infrastructure, not a domain: there is one status resource and no
// per-delivery surface.

// WebhookState is the coarse readiness of webhook ingress.
type WebhookState string

const (
	// WebhooksDisabled: intake is off for this launch. Deliveries accepted
	// earlier keep being processed.
	WebhooksDisabled WebhookState = "disabled"
	// WebhooksStarting: intake is enabled and converging — the receiver is
	// starting, Funnel is being established, or an operator action
	// (reported in Action) is awaited. Retried automatically.
	WebhooksStarting WebhookState = "starting"
	// WebhooksReady: the isolated receiver is serving, the acceptance path
	// is up, and the server owns a live Funnel exposure at URL.
	WebhooksReady WebhookState = "ready"
	// WebhooksUnavailable: intake cannot run on this machine — the platform
	// or its isolation capabilities are unsupported. Reason explains; the
	// rest of the server is unaffected.
	WebhooksUnavailable WebhookState = "unavailable"
)

// Webhooks is the GET /v1/webhooks response body.
type Webhooks struct {
	State WebhookState `json:"state" enum:"disabled,starting,ready,unavailable" doc:"Readiness of webhook intake."`
	// URL is the public base URL deliveries arrive at, once Funnel
	// exposure is established (or the expected URL while converging).
	URL string `json:"url,omitempty" doc:"Public base URL of the webhook endpoint; empty until the node's name is known."`
	// Routes lists every registered Integration route, in registration
	// order, whether or not the endpoint is up.
	Routes []WebhookRoute `json:"routes" doc:"Registered Integration webhook routes."`
	// Action is what the operator must do to finish setup — typically the
	// Funnel approval link Tailscale printed — empty when nothing is
	// awaited.
	Action string `json:"action,omitempty" doc:"Operator action required to finish setup, when one is awaited."`
	// Reason explains a starting or unavailable state.
	Reason string `json:"reason,omitempty" doc:"Why intake is not ready."`
	// Pending counts accepted deliveries whose processing has not finished.
	Pending int `json:"pending" doc:"Accepted deliveries not yet processed."`
	// IntakeBlocked reports that new deliveries are being refused with a
	// retryable failure because the inbox is full or storage is failing;
	// accepted work is never dropped to make room.
	IntakeBlocked bool `json:"intakeBlocked" doc:"New deliveries are refused (retryable) because the inbox is at capacity or storage is failing."`
	// Rejected counts deliveries refused before acceptance since the
	// server started; LastRejection summarizes the most recent one without
	// request data.
	Rejected      int    `json:"rejected" doc:"Deliveries refused before acceptance since the server started."`
	LastRejection string `json:"lastRejection,omitempty" doc:"Summary of the most recent refusal."`
	// ProcessingFailures counts failed processing attempts since the
	// server started; LastProcessingFailure summarizes the most recent.
	ProcessingFailures    int    `json:"processingFailures" doc:"Failed processing attempts since the server started; each is retried."`
	LastProcessingFailure string `json:"lastProcessingFailure,omitempty" doc:"Summary of the most recent processing failure."`
}

// WebhookRoute is one Integration's registered route.
type WebhookRoute struct {
	IntegrationID string `json:"integrationId" doc:"Integration that verifies and processes deliveries on this route."`
	Path          string `json:"path" doc:"Route path under the public base URL."`
}
