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

func TestAPIErrorPresentsSessionLifecycleFailures(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   string
	}{
		{
			name: "zmx unavailable", status: http.StatusServiceUnavailable,
			body: `{"error":"zmx_unavailable","message":"zmx session inventory is unavailable"}`,
			want: "zmx is unavailable; session state could not be confirmed — retry shortly",
		},
		{
			name: "session ended", status: http.StatusConflict,
			body: `{"error":"session_ended","message":"session ended: ses_dead","sessionId":"ses_dead"}`,
			want: "session ended: ses_dead",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := apiError(tt.status, []byte(tt.body)).Error(); !strings.Contains(got, tt.want) {
				t.Fatalf("error = %q, want %q", got, tt.want)
			}
		})
	}
}
