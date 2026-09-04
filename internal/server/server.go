// Package server is the ATC HTTP server (ATC-259): a Huma v2 API mounted
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
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"reflect"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/application"
	"github.com/jeremytondo/atc/internal/events"
	"github.com/jeremytondo/atc/internal/integrations"
	"github.com/jeremytondo/atc/internal/projects"
	"github.com/jeremytondo/atc/internal/terminals"
	"github.com/jeremytondo/atc/internal/threads"
)

// HealthOutput is Huma routing machinery around the shared body; the
// contract itself is api.Health (ATC-264), from which the OpenAPI schema
// is derived. Wrappers like this stay server-side, never in internal/api.
type HealthOutput struct {
	Body api.Health
}

// Options wires the handler. Verify reports whether an Authorization
// header value presents the current bearer token (authtoken.Store.Verify
// in production); Version is the server build identity, sent on every
// response. A nil Logger discards request-level events.
type Options struct {
	Verify       func(authorization string) bool
	Version      string
	Logger       *slog.Logger
	Terminals    *terminals.Service
	Projects     *projects.Service
	Integrations *integrations.Service
	Threads      *threads.Service
	Events       *events.Hub
	// InternalRoutes are handlers mounted outside the public /v1 contract
	// and outside bearer auth (ATC-255): each authenticates itself — the
	// Claude hook route validates its per-launch secret, and the bearer
	// token is deliberately never used for hook delivery. Keys are
	// http.ServeMux patterns, e.g. "POST /internal/claude/hooks".
	InternalRoutes map[string]http.Handler
	// Coordinator runs the cross-domain workflows (terminal and space
	// deletion, project mutations, thread creation); required with
	// Terminals, Projects, or Threads.
	// Wired by the composition root so the domains stay decoupled and
	// every entry point runs one workflow.
	Coordinator *application.Coordinator
	// HeartbeatInterval paces SSE heartbeats; zero means the default.
	HeartbeatInterval time.Duration
}

// NewHandler builds the /v1 API surface plus /openapi.json and /docs.
//
// Middleware order (outermost first): version headers, then auth, then
// routing — headers appear on every response including 401s, and
// unauthenticated callers cannot probe which routes exist.
func NewHandler(opts Options) http.Handler {
	if opts.Verify == nil {
		// Without auth the server would panic on the first request; fail
		// at construction instead.
		panic("server.NewHandler: Verify must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.HeartbeatInterval == 0 {
		opts.HeartbeatInterval = defaultHeartbeatInterval
	}
	mux := http.NewServeMux()
	config := huma.DefaultConfig("ATC API", opts.Version)
	config.Components.SecuritySchemes = map[string]*huma.SecurityScheme{
		"bearerAuth": {Type: "http", Scheme: "bearer"},
	}
	// A merge-patch Optional[T] is exactly a nullable T on the wire; the
	// document says so instead of describing the Go wrapper.
	config.Components.Schemas.RegisterTypeAlias(reflect.TypeFor[api.Optional[string]](), reflect.TypeFor[*string]())
	config.Components.Schemas.RegisterTypeAlias(reflect.TypeFor[api.Optional[bool]](), reflect.TypeFor[*bool]())
	// Declared globally so the generated document tells client authors
	// every operation needs the token; enforcement is the middleware's.
	config.Security = []map[string][]string{{"bearerAuth": {}}}
	humaAPI := humago.New(mux, config)

	huma.Register(humaAPI, huma.Operation{
		OperationID: "get-health",
		Method:      http.MethodGet,
		Path:        "/v1/health",
		Summary:     "Server liveness",
		Description: "Source of truth for whether the server is up; `atc server status` probes this first.",
	}, func(ctx context.Context, _ *struct{}) (*HealthOutput, error) {
		return &HealthOutput{Body: api.Health{Status: "ok", Version: opts.Version}}, nil
	})

	if (opts.Terminals != nil || opts.Projects != nil || opts.Threads != nil) && opts.Coordinator == nil {
		panic("server.NewHandler: Coordinator must accompany Terminals, Projects, and Threads")
	}
	if opts.Terminals != nil {
		registerTerminals(humaAPI, opts.Terminals, opts.Threads, opts.Coordinator)
		registerSpaces(humaAPI, opts.Terminals, opts.Coordinator)
	}
	if opts.Projects != nil {
		registerProjects(humaAPI, opts.Projects, opts.Coordinator)
	}
	if opts.Integrations != nil {
		registerIntegrations(humaAPI, opts.Integrations)
	}
	if opts.Threads != nil {
		registerThreads(humaAPI, opts.Threads, opts.Coordinator)
	}
	if opts.Events != nil {
		registerEvents(humaAPI, opts.Events, opts.HeartbeatInterval)
	}

	handler := withAuth(opts.Verify, withWriteDeadlines(problemMux(mux)))
	if len(opts.InternalRoutes) > 0 {
		root := http.NewServeMux()
		for pattern, route := range opts.InternalRoutes {
			// The bearer bypass is exactly as wide as /internal/: a
			// pattern that could shadow the public surface is a wiring
			// bug, refused at construction.
			if !strings.HasPrefix(pattern, "POST /internal/") {
				panic(fmt.Sprintf("server.NewHandler: internal route %q outside POST /internal/", pattern))
			}
			root.Handle(pattern, route)
		}
		root.Handle("/", handler)
		handler = root
	}
	return withVersionHeaders(opts.Version, opts.Logger, handler)
}

func withVersionHeaders(version string, logger *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(api.ServerVersionHeader, version)
		if client := r.Header.Get(api.ClientVersionHeader); client != "" && client != version {
			logger.Debug("client version skew", "client", client, "server", version)
		}
		next.ServeHTTP(w, r)
	})
}

func withAuth(verify func(authorization string) bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !verify(r.Header.Get("Authorization")) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="atc"`)
			// Same RFC 7807 shape Huma uses for its own errors, emitted from
			// the shared struct, so clients see one error contract regardless
			// of which layer rejected.
			writeProblem(w, problem(http.StatusUnauthorized, api.CodeUnauthorized, "invalid or missing bearer token"))
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
