package linear

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"time"

	"github.com/jeremytondo/atc/internal/webhooks"
)

// Verification (the shared webhook contract's Verify): pure computation
// over one untrusted request, well inside the receipt budget. Linear
// signs the raw body with the webhook's signing secret (HMAC-SHA256, hex,
// in Linear-Signature), stamps it with webhookTimestamp (Unix
// milliseconds; fresh within a minute, per Linear's own client), and
// identifies each delivery in Linear-Delivery. Beyond authenticity and
// freshness, a delivery must come from the configured workspace and,
// for session events, the configured app. Event types the Integration
// does not act on are accepted and dropped in Process rather than
// refused: refusing would count as failures on Linear's side for
// categories the operator merely left enabled.
const (
	signatureHeader = "Linear-Signature"
	deliveryHeader  = "Linear-Delivery"
	freshness       = time.Minute
	eventType       = "AgentSessionEvent"
)

// envelope is the part of every Linear delivery verification reads.
type envelope struct {
	Type             string  `json:"type"`
	Action           string  `json:"action"`
	OrganizationID   string  `json:"organizationId"`
	OAuthClientID    string  `json:"oauthClientId"`
	WebhookTimestamp float64 `json:"webhookTimestamp"`
}

// Verify implements webhooks.Handler.
func (s *Service) Verify(_ context.Context, req webhooks.Request) (webhooks.Delivery, error) {
	if req.Method != http.MethodPost {
		return webhooks.Delivery{}, webhooks.Reject(http.StatusMethodNotAllowed, "deliveries are POSTed")
	}
	setup, ok := s.currentSetup()
	if !ok {
		// Not a refusal of the sender: Linear retries, and the operator
		// action is in the Integration's status.
		return webhooks.Delivery{}, webhooks.Reject(http.StatusServiceUnavailable, "Linear is not configured")
	}
	if !signed(req.Body, req.Header.Get(signatureHeader), setup.WebhookSigningSecret) {
		return webhooks.Delivery{}, webhooks.Reject(http.StatusUnauthorized, "bad signature")
	}
	var head envelope
	if err := json.Unmarshal(req.Body, &head); err != nil || head.Type == "" {
		return webhooks.Delivery{}, webhooks.Reject(http.StatusBadRequest, "malformed body")
	}
	if head.WebhookTimestamp == 0 {
		return webhooks.Delivery{}, webhooks.Reject(http.StatusBadRequest, "missing webhookTimestamp")
	}
	sent := time.UnixMilli(int64(head.WebhookTimestamp))
	if age := s.now().Sub(sent); age > freshness || age < -freshness {
		return webhooks.Delivery{}, webhooks.Reject(http.StatusUnauthorized, "stale delivery")
	}
	if head.OrganizationID != setup.OrganizationID {
		return webhooks.Delivery{}, webhooks.Reject(http.StatusForbidden, "workspace not authorized")
	}
	if head.Type == eventType && head.OAuthClientID != setup.ClientID {
		return webhooks.Delivery{}, webhooks.Reject(http.StatusForbidden, "app not authorized")
	}
	id := req.Header.Get(deliveryHeader)
	if id == "" {
		return webhooks.Delivery{}, webhooks.Reject(http.StatusBadRequest, "missing "+deliveryHeader)
	}
	return webhooks.Delivery{ID: id, Payload: req.Body}, nil
}

// signed checks the body's HMAC in constant time.
func signed(body []byte, signature, secret string) bool {
	if signature == "" || secret == "" {
		return false
	}
	presented, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(presented, mac.Sum(nil))
}
