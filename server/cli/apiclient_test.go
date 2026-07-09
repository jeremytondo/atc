package cli

import (
	"context"
	"net"
	"net/http"
	"strings"
	"testing"
)

func TestAPIClientGetCallsAPIOverUnixSocket(t *testing.T) {
	socketPath := testSocketPath(t, "atc.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/api/health" {
				t.Fatalf("path = %q, want /api/health", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()

	body, err := newAPIClient(socketPath).get(context.Background(), "health")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if string(body) != `{"status":"ok"}` {
		t.Fatalf("body = %q, want health JSON", body)
	}
}

func TestAPIClientGetReturnsClearErrorWhenSocketUnavailable(t *testing.T) {
	socketPath := testSocketPath(t, "missing.sock")

	_, err := newAPIClient(socketPath).get(context.Background(), "health")
	if err == nil {
		t.Fatal("get returned nil error, want socket error")
	}
	if !strings.Contains(err.Error(), socketPath) {
		t.Fatalf("error = %q, want socket path", err)
	}
}

func TestAPIClientGetReturnsClearErrorForNonSuccessAPIResponse(t *testing.T) {
	socketPath := testSocketPath(t, "atc.sock")
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatalf("listen unix socket: %v", err)
	}
	defer listener.Close()

	server := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "not healthy", http.StatusServiceUnavailable)
		}),
	}
	go func() {
		_ = server.Serve(listener)
	}()
	defer server.Close()

	_, err = newAPIClient(socketPath).get(context.Background(), "health")
	if err == nil {
		t.Fatal("get returned nil error, want status error")
	}
	if !strings.Contains(err.Error(), "HTTP 503") {
		t.Fatalf("error = %q, want HTTP 503", err)
	}
}
