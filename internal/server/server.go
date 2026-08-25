// Package server is the ATC HTTP chassis (ATC-259): a Huma v2 API mounted
// on a standard http.ServeMux, fronted by transport-level middleware for
// auth and version headers.
//
// Invariant carried from the framework spike (experiments/http-framework):
// auth and version headers live in plain net/http middleware wrapping the
// mux, never in Huma operation middleware — Huma middleware only runs for
// registered operations, so it cannot enforce 401-before-route-discovery
// on unknown paths or on /openapi.json itself. Every byte served, docs
// included, sits behind the one bearer token (ATC-247 §5).
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"
)

// Version headers both ways on every request/response — the entire skew
// handshake (ATC-247 §6).
const (
	ClientVersionHeader = "Atc-Client-Version"
	ServerVersionHeader = "Atc-Server-Version"
)

// Options configures the chassis.
type Options struct {
	// Version is the server build identity, sent on every response.
	Version string
	// Verify reports whether an Authorization header value presents the
	// current bearer token (authtoken.Store.Verify in production).
	Verify func(authorization string) bool
	// Logger receives request-level events. Required.
	Logger *slog.Logger
}

// HealthOutput doubles as the response contract and its documentation; the
// generated OpenAPI schema is derived from it.
type HealthOutput struct {
	Body struct {
		Status  string `json:"status" enum:"ok" doc:"Liveness state of the server."`
		Version string `json:"version" doc:"Version of the running server binary."`
	}
}

// NewHandler builds the /v1 API surface plus /openapi.json and /docs.
// Middleware order (outermost first): version headers, then auth, then
// routing — headers appear on every response including 401s, and
// unauthenticated callers cannot probe which routes exist.
func NewHandler(opts Options) http.Handler {
	mux := http.NewServeMux()

	config := huma.DefaultConfig("ATC API", opts.Version)
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearerAuth": {Type: "http", Scheme: "bearer"},
	}
	// Declared globally so the generated document tells adapter authors
	// every operation needs the token; enforcement is the middleware's.
	config.Security = []map[string][]string{{"bearerAuth": {}}}
	api := humago.New(mux, config)

	huma.Register(api, huma.Operation{
		OperationID: "get-health",
		Method:      http.MethodGet,
		Path:        "/v1/health",
		Summary:     "Server liveness",
		Description: "Source of truth for whether the server is up; `atc server status` probes this first.",
	}, func(ctx context.Context, _ *struct{}) (*HealthOutput, error) {
		out := &HealthOutput{}
		out.Body.Status = "ok"
		out.Body.Version = opts.Version
		return out, nil
	})

	return withVersionHeaders(opts, withAuth(opts, mux))
}

func withVersionHeaders(opts Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ServerVersionHeader, opts.Version)
		if client := r.Header.Get(ClientVersionHeader); client != "" && client != opts.Version {
			opts.Logger.Debug("client version skew", "client", client, "server", opts.Version)
		}
		next.ServeHTTP(w, r)
	})
}

func withAuth(opts Options, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !opts.Verify(r.Header.Get("Authorization")) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="atc"`)
			// Same RFC 7807 shape Huma uses for its own errors, so clients
			// see one error contract regardless of which layer rejected.
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"title":  "Unauthorized",
				"status": http.StatusUnauthorized,
				"detail": "invalid or missing bearer token",
			})
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Serve serves handler on an already-bound listener until ctx is
// cancelled, then shuts down gracefully with a bounded drain. It logs
// "server started" immediately — the line `atc server run` tests and
// supervisors key on. The listener is bound by the caller so the actual
// port is known before serving (tailnet exposure fronts the same port).
func Serve(ctx context.Context, listener net.Listener, handler http.Handler, logger *slog.Logger) error {
	logger.Info("server started", "addr", listener.Addr().String())

	srv := &http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()

	select {
	case err := <-served:
		return err
	case <-ctx.Done():
		logger.Info("server shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}
