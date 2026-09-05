package server

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/jeremytondo/atc/internal/api"
)

// WebhookReporter is the webhook ingress's status seam
// (webhooks.Service in production).
type WebhookReporter interface {
	Status(ctx context.Context) api.Webhooks
}

type webhooksOutput struct {
	Body api.Webhooks
}

// registerWebhooks mounts the one webhook resource: the ingress report.
// Deliveries themselves never touch this API — they arrive on the
// receiver's channel, which serves registered routes and nothing else.
func registerWebhooks(humaAPI huma.API, reporter WebhookReporter) {
	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-webhooks",
		Method:      http.MethodGet,
		Path:        "/v1/webhooks",
		Summary:     "Webhook ingress status",
		Description: "Readiness of webhook intake, its public URL, registered Integration routes, any operator action awaited, and inbox counters. `disabled` means intake is off for this launch while accepted deliveries keep being processed.",
	}, func(ctx context.Context, _ *struct{}) (*webhooksOutput, error) {
		return &webhooksOutput{Body: reporter.Status(ctx)}, nil
	})
}
