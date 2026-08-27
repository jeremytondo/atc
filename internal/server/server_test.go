package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
)

const (
	testToken   = "atc_test-token"
	testVersion = "v1.2.3-test"
)

func testVerify(authorization string) bool {
	return authorization == "Bearer "+testToken
}

func newHandler() http.Handler {
	return NewHandler(Options{Verify: testVerify, Version: testVersion, Logger: slog.New(slog.NewTextHandler(io.Discard, nil))})
}

func get(handler http.Handler, path string, token bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token {
		req.Header.Set("Authorization", "Bearer "+testToken)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestHealthRequiresTokenEvenOnLoopback(t *testing.T) {
	rec := get(newHandler(), "/v1/health", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no token: got %d, want 401", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Errorf("401 Content-Type = %q, want problem+json", ct)
	}
}

func TestHealthWithToken(t *testing.T) {
	rec := get(newHandler(), "/v1/health", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body)
	}
	// Decoding into the shared contract type is the point of ATC-264: the
	// wire body and api.Health are one definition.
	var body api.Health
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %q: %v", rec.Body, err)
	}
	if body.Status != "ok" || body.Version != testVersion {
		t.Errorf("body = %+v, want ok/%s", body, testVersion)
	}
}

func TestServerVersionHeaderOnEveryResponse(t *testing.T) {
	handler := newHandler()
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"success": get(handler, "/v1/health", true),
		// The skew warning must survive auth failure, otherwise a
		// rotated-token client can never learn about skew.
		"unauthorized": get(handler, "/v1/health", false),
		"not found":    get(handler, "/v1/nope", true),
	} {
		if got := rec.Header().Get(api.ServerVersionHeader); got != testVersion {
			t.Errorf("%s: %s = %q, want %q", name, api.ServerVersionHeader, got, testVersion)
		}
	}
}

// A nil logger must default to discard: the skew log on a version-skewed
// request was previously a latent nil dereference.
func TestNilLoggerDefaultsToDiscard(t *testing.T) {
	handler := NewHandler(Options{Verify: testVerify, Version: testVersion})
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(api.ClientVersionHeader, "v0.0.0-skewed")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("skewed request with nil logger: got %d, want 200", rec.Code)
	}
}

func TestAuthWrapsRouting(t *testing.T) {
	handler := newHandler()
	// An unauthenticated caller learns nothing about which routes exist.
	if rec := get(handler, "/v1/nope", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown route without token: got %d, want 401", rec.Code)
	}
	if rec := get(handler, "/v1/nope", true); rec.Code != http.StatusNotFound {
		t.Errorf("unknown route with token: got %d, want 404", rec.Code)
	}
}

func TestOpenAPIBehindSameToken(t *testing.T) {
	handler := newHandler()
	if rec := get(handler, "/openapi.json", false); rec.Code != http.StatusUnauthorized {
		t.Errorf("openapi without token: got %d, want 401", rec.Code)
	}
	rec := get(handler, "/openapi.json", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("openapi with token: got %d, want 200", rec.Code)
	}
	var doc struct {
		OpenAPI string         `json:"openapi"`
		Paths   map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &doc); err != nil {
		t.Fatal(err)
	}
	if doc.OpenAPI == "" {
		t.Error("document missing openapi version field")
	}
	if _, ok := doc.Paths["/v1/health"]; !ok {
		t.Error("document does not describe /v1/health")
	}
}
