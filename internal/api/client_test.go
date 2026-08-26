package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	testToken         = "atc_test-token"
	testClientVersion = "v1.2.3-client"
	testServerVersion = "v1.2.3-server"
)

// testServer answers /v1/health the way the real chassis does: version
// header on every response, 401s included, problem+json on rejection.
func testServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ServerVersionHeader, testServerVersion)
		if r.Header.Get("Authorization") != "Bearer "+testToken {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(Problem{
				Title:  "Unauthorized",
				Status: http.StatusUnauthorized,
				Detail: "invalid or missing bearer token",
			})
			return
		}
		_ = json.NewEncoder(w).Encode(Health{Status: "ok", Version: testServerVersion})
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestHealth(t *testing.T) {
	srv := testServer(t)
	client := NewClient(srv.URL, testToken, testClientVersion, nil, nil)
	health, err := client.Health(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := Health{Status: "ok", Version: testServerVersion}
	if diff := cmp.Diff(want, health); diff != "" {
		t.Errorf("Health() mismatch (-want +got):\n%s", diff)
	}
}

func TestRequestCarriesTokenAndClientVersion(t *testing.T) {
	var authorization, clientVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorization = r.Header.Get("Authorization")
		clientVersion = r.Header.Get(ClientVersionHeader)
		_ = json.NewEncoder(w).Encode(Health{Status: "ok"})
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, testToken, testClientVersion, nil, nil).Health(context.Background()); err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer "+testToken {
		t.Errorf("Authorization = %q, want bearer token", authorization)
	}
	if clientVersion != testClientVersion {
		t.Errorf("%s = %q, want %q", ClientVersionHeader, clientVersion, testClientVersion)
	}
}

// A tokenless client (the probe) must not send an empty Authorization
// header, and must still learn the server version from the 401's typed
// error — the contract that lets `atc upgrade` verify a swap without
// credentials.
func TestTokenlessProbeReadsVersionOffUnauthorized(t *testing.T) {
	srv := testServer(t)
	_, err := NewClient(srv.URL, "", testClientVersion, nil, nil).Health(context.Background())
	problem, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("err = %v, want *Problem", err)
	}
	if problem.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want 401", problem.Status)
	}
	if problem.ServerVersion != testServerVersion {
		t.Errorf("ServerVersion = %q, want %q", problem.ServerVersion, testServerVersion)
	}
	if got, want := problem.Error(), "invalid or missing bearer token (HTTP 401)"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

// The Atc-Server-Version header is reported to the callback on every
// response that carries one, success and rejection alike — the client's
// half of the skew handshake.
func TestServerVersionCallbackOnEveryResponse(t *testing.T) {
	srv := testServer(t)
	for name, token := range map[string]string{"success": testToken, "unauthorized": ""} {
		var reported string
		client := NewClient(srv.URL, token, testClientVersion, nil,
			func(version string) { reported = version })
		_, _ = client.Health(context.Background())
		if reported != testServerVersion {
			t.Errorf("%s: callback got %q, want %q", name, reported, testServerVersion)
		}
	}
}

// A 2xx whose body cannot be decoded is still an HTTP response: it must
// surface as *Problem carrying the real status and server version, not as
// a plain error a caller would mistake for "nothing answered".
func TestMalformedSuccessBodyIsAProblem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(ServerVersionHeader, testServerVersion)
		_, _ = w.Write([]byte("<html>not json</html>"))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, testToken, testClientVersion, nil, nil).Health(context.Background())
	problem, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("err = %v, want *Problem", err)
	}
	if problem.Status != http.StatusOK {
		t.Errorf("Status = %d, want 200", problem.Status)
	}
	if problem.ServerVersion != testServerVersion {
		t.Errorf("ServerVersion = %q, want %q", problem.ServerVersion, testServerVersion)
	}
}

// The problem body's status member is advisory; branching trusts what the
// transport actually said, immune to a lying or rewritten body.
func TestProblemStatusComesFromTransport(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/problem+json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(Problem{Title: "Server Error", Status: http.StatusInternalServerError, Detail: "lying body"})
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "", testClientVersion, nil, nil).Health(context.Background())
	problem, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("err = %v, want *Problem", err)
	}
	if problem.Status != http.StatusUnauthorized {
		t.Errorf("Status = %d, want the transport's 401", problem.Status)
	}
	if problem.Detail != "lying body" {
		t.Errorf("Detail = %q, want the body's fields retained", problem.Detail)
	}
}

// A non-problem error body (a proxy page, some other process on the port)
// degrades to the status line; the caller still gets a typed error.
func TestNonProblemBodyDegradesToStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>nope</html>"))
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, testToken, testClientVersion, nil, nil).Health(context.Background())
	problem, ok := errors.AsType[*Problem](err)
	if !ok {
		t.Fatalf("err = %v, want *Problem", err)
	}
	want := &Problem{Title: "Bad Gateway", Status: http.StatusBadGateway}
	if diff := cmp.Diff(want, problem); diff != "" {
		t.Errorf("problem mismatch (-want +got):\n%s", diff)
	}
}

// Only an HTTP response becomes a *Problem; transport failure stays a
// plain error so "responding" and "rejected" remain distinguishable.
func TestTransportErrorIsNotAProblem(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	srv.Close() // nothing listening anymore

	_, err := NewClient(srv.URL, testToken, testClientVersion, nil, nil).Health(context.Background())
	if err == nil {
		t.Fatal("want an error from a dead server")
	}
	if problem, ok := errors.AsType[*Problem](err); ok {
		t.Errorf("transport failure decoded as *Problem: %v", problem)
	}
}

func TestProblemErrorFallbacks(t *testing.T) {
	for name, tc := range map[string]struct {
		problem Problem
		want    string
	}{
		"detail":      {Problem{Title: "Unauthorized", Status: 401, Detail: "bad token"}, "bad token (HTTP 401)"},
		"title":       {Problem{Title: "Unauthorized", Status: 401}, "Unauthorized (HTTP 401)"},
		"status only": {Problem{Status: 502}, "Bad Gateway (HTTP 502)"},
	} {
		if got := tc.problem.Error(); got != tc.want {
			t.Errorf("%s: Error() = %q, want %q", name, got, tc.want)
		}
	}
}
