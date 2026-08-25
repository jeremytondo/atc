// Package conformance holds the behavioral contract both framework
// candidates must satisfy, so the spike compares implementations of the
// same thing rather than two different servers. The contract is the
// ATC-259 chassis surface: an authenticated /v1/health route, one bearer
// token everywhere (loopback included), and version headers both ways.
package conformance

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Values every handler under test is constructed with.
const (
	Token         = "atc_conformance-test-token"
	ServerVersion = "v0.0.0-spike"
)

// Header names both ways. Placeholder names: the real chassis ticket picks
// the final spelling; the spike only needs both directions to exist.
const (
	ClientVersionHeader = "Atc-Client-Version"
	ServerVersionHeader = "Atc-Server-Version"
)

// Run exercises one handler against the shared contract.
func Run(t *testing.T, handler http.Handler) {
	t.Helper()

	get := func(path string, decorate func(*http.Request)) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		if decorate != nil {
			decorate(req)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}
	authed := func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+Token)
		req.Header.Set(ClientVersionHeader, "v9.9.9-client")
	}

	t.Run("health requires token", func(t *testing.T) {
		rec := get("/v1/health", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("no token: got %d, want 401", rec.Code)
		}
	})

	t.Run("wrong token rejected", func(t *testing.T) {
		rec := get("/v1/health", func(req *http.Request) {
			req.Header.Set("Authorization", "Bearer atc_wrong")
		})
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("wrong token: got %d, want 401", rec.Code)
		}
	})

	t.Run("health with token", func(t *testing.T) {
		rec := get("/v1/health", authed)
		if rec.Code != http.StatusOK {
			t.Fatalf("got %d, want 200; body: %s", rec.Code, rec.Body)
		}
		var body struct {
			Status string `json:"status"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("decoding body %q: %v", rec.Body, err)
		}
		if body.Status != "ok" {
			t.Fatalf("status = %q, want ok", body.Status)
		}
	})

	t.Run("server version header on success", func(t *testing.T) {
		rec := get("/v1/health", authed)
		if got := rec.Header().Get(ServerVersionHeader); got != ServerVersion {
			t.Fatalf("%s = %q, want %q", ServerVersionHeader, got, ServerVersion)
		}
	})

	// The skew warning must work even when the request fails auth,
	// otherwise a rotated-token client can never learn about skew.
	t.Run("server version header on 401", func(t *testing.T) {
		rec := get("/v1/health", nil)
		if got := rec.Header().Get(ServerVersionHeader); got != ServerVersion {
			t.Fatalf("%s = %q, want %q", ServerVersionHeader, got, ServerVersion)
		}
	})

	t.Run("unknown route is 404, not 401 leak", func(t *testing.T) {
		rec := get("/v1/nope", authed)
		if rec.Code != http.StatusNotFound {
			t.Fatalf("got %d, want 404", rec.Code)
		}
	})

	t.Run("unknown route without token is still 401", func(t *testing.T) {
		// Auth wraps routing: an unauthenticated caller learns nothing
		// about which routes exist.
		rec := get("/v1/nope", nil)
		if rec.Code != http.StatusUnauthorized {
			t.Fatalf("got %d, want 401", rec.Code)
		}
	})
}
