package cli

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/jeremytondo/atc/internal/api"
)

// serve answers health as a server on protocol speaking would, on a
// release that is never the client's own.
func serve(t *testing.T, protocol int) *httptest.Server {
	t.Helper()
	text := strconv.Itoa(protocol)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set(api.ServerVersionHeader, "v9.9.9-other-release")
		w.Header().Set(api.ProtocolHeader, text)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"ok","version":"v9.9.9-other-release","protocol":` + text + `}`))
	}))
	t.Cleanup(srv.Close)
	return srv
}

// A server on another release is used as it is: release identity never
// gates a client command. A server on another protocol fails the call
// with the typed refusal the shared client synthesizes.
func TestNewClientUsesProtocolNotRelease(t *testing.T) {
	t.Setenv("ATC_TOKEN", "atc_cli-test-token")

	srv := serve(t, api.Protocol)
	t.Setenv("ATC_SERVER", srv.URL)
	client, baseURL, err := NewClient()
	if err != nil {
		t.Fatal(err)
	}
	if baseURL != srv.URL {
		t.Fatalf("baseURL = %q, want ATC_SERVER %q", baseURL, srv.URL)
	}
	if _, err := client.Health(context.Background()); err != nil {
		t.Errorf("Health against another release = %v, want nil", err)
	}

	other := serve(t, api.Protocol+1)
	t.Setenv("ATC_SERVER", other.URL)
	if client, _, err = NewClient(); err != nil {
		t.Fatal(err)
	}
	_, err = client.Health(context.Background())
	problem, ok := errors.AsType[*api.Problem](err)
	if !ok || problem.Code != api.CodeProtocolMismatch || problem.ServerProtocol != api.Protocol+1 {
		t.Errorf("Health against another protocol = %v, want a %s problem", err, api.CodeProtocolMismatch)
	}
}
