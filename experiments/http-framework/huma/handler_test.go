package huma_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	humachassis "github.com/jeremytondo/atc/experiments/http-framework/huma"
	"github.com/jeremytondo/atc/experiments/http-framework/internal/conformance"
)

func TestConformance(t *testing.T) {
	conformance.Run(t, humachassis.NewHandler(conformance.Token, conformance.ServerVersion))
}

// TestOpenAPIServed proves the generated document is reachable through the
// same authed surface as everything else.
func TestOpenAPIServed(t *testing.T) {
	handler := humachassis.NewHandler(conformance.Token, conformance.ServerVersion)

	req := httptest.NewRequest(http.MethodGet, "/openapi.json", nil)
	req.Header.Set("Authorization", "Bearer "+conformance.Token)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi.json: got %d, want 200", rec.Code)
	}
}
