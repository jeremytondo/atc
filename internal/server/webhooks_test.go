package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/jeremytondo/atc/internal/api"
)

type fakeWebhooks struct{ status api.Webhooks }

func (f fakeWebhooks) Status(context.Context) api.Webhooks { return f.status }

// The ingress report rides the bearer-authenticated API unchanged, so the
// CLI and any client read the same state the server holds.
func TestWebhooksStatusRequiresTokenAndRelaysReport(t *testing.T) {
	want := api.Webhooks{
		State: api.WebhooksStarting, URL: "https://host.tailnet.ts.net",
		Routes: []api.WebhookRoute{{IntegrationID: "probe", Path: "/probe"}},
		Action: "To approve, visit https://login.tailscale.com/f/funnel", Reason: "tailscale funnel exited",
		Pending: 3, IntakeBlocked: true, Rejected: 2, LastRejection: "/probe: 401 bad signature",
	}
	handler := NewHandler(Options{
		Verify: testVerify, Version: testVersion, Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Webhooks: fakeWebhooks{status: want},
	})
	if rec := get(handler, "/v1/webhooks", false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("tokenless = %d, want 401", rec.Code)
	}
	rec := get(handler, "/v1/webhooks", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d: %s", rec.Code, rec.Body)
	}
	var got api.Webhooks
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("report mismatch (-want +got):\n%s", diff)
	}
}
