package server

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
		req.Header.Set(api.ProtocolHeader, strconv.Itoa(api.Protocol))
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

func TestIdentityHeadersOnEveryResponse(t *testing.T) {
	handler := newHandler()
	for name, rec := range map[string]*httptest.ResponseRecorder{
		"success": get(handler, "/v1/health", true),
		// Identity must survive auth failure, otherwise a rotated-token
		// or incompatible client can never learn what answered.
		"unauthorized": get(handler, "/v1/health", false),
		"not found":    get(handler, "/v1/nope", true),
		"mismatch":     protocolRequest(handler, "/v1/health", "0"),
	} {
		if got := rec.Header().Get(api.ServerVersionHeader); got != testVersion {
			t.Errorf("%s: %s = %q, want %q", name, api.ServerVersionHeader, got, testVersion)
		}
		if got := rec.Header().Get(api.ProtocolHeader); got != strconv.Itoa(api.Protocol) {
			t.Errorf("%s: %s = %q, want %d", name, api.ProtocolHeader, got, api.Protocol)
		}
	}
}

func protocolRequest(handler http.Handler, path, protocol string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	if protocol != "" {
		req.Header.Set(api.ProtocolHeader, protocol)
	}
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

// The server's half of the protocol contract (ATC-325): a request on
// another protocol, or on none, is refused with a typed problem before
// any route runs — health included, since compatibility is the one thing
// health cannot be allowed to paper over. A differing release version
// alone changes nothing.
func TestProtocolMismatchIsRefusedBeforeRouting(t *testing.T) {
	handler := newHandler()
	for name, protocol := range map[string]string{"older": "0", "newer": strconv.Itoa(api.Protocol + 1), "absent": "", "garbage": "one"} {
		rec := protocolRequest(handler, "/v1/health", protocol)
		if rec.Code != http.StatusUpgradeRequired {
			t.Errorf("%s: got %d, want 426; body %s", name, rec.Code, rec.Body)
		}
		var problem api.Problem
		if err := json.Unmarshal(rec.Body.Bytes(), &problem); err != nil {
			t.Fatalf("%s: decoding %q: %v", name, rec.Body, err)
		}
		if problem.Code != api.CodeProtocolMismatch || !strings.Contains(problem.Detail, "protocol "+strconv.Itoa(api.Protocol)) {
			t.Errorf("%s: problem = %+v, want %s naming protocol %d", name, problem, api.CodeProtocolMismatch, api.Protocol)
		}
		if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
			t.Errorf("%s: Content-Type = %q", name, ct)
		}
	}
	// A matching protocol with any release identity is served.
	req := httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set("Authorization", "Bearer "+testToken)
	req.Header.Set(api.ProtocolHeader, strconv.Itoa(api.Protocol))
	req.Header.Set(api.ClientVersionHeader, "v0.0.0-other-release")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("other release, same protocol: got %d, want 200", rec.Code)
	}
	var body api.Health
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Protocol != api.Protocol {
		t.Errorf("health body = %+v, %v; want protocol %d", body, err, api.Protocol)
	}
	// Auth still comes first: an unauthenticated caller on the wrong
	// protocol learns only that it is unauthenticated.
	req = httptest.NewRequest(http.MethodGet, "/v1/health", nil)
	req.Header.Set(api.ProtocolHeader, "0")
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("unauthenticated on the wrong protocol: got %d, want 401", rec.Code)
	}
}

// A nil logger must default to discard.
func TestNilLoggerDefaultsToDiscard(t *testing.T) {
	handler := NewHandler(Options{Verify: testVerify, Version: testVersion})
	if rec := protocolRequest(handler, "/v1/health", strconv.Itoa(api.Protocol)); rec.Code != http.StatusOK {
		t.Errorf("request with nil logger: got %d, want 200", rec.Code)
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
