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
	"strconv"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/application"
	"github.com/jeremytondo/atc/internal/artifacts"
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
// response for diagnostics (compatibility is api.Protocol's alone). A
// nil Logger discards request-level events.
type Options struct {
	Verify       func(authorization string) bool
	Version      string
	Logger       *slog.Logger
	Terminals    *terminals.Service
	Projects     *projects.Service
	Integrations *integrations.Service
	Threads      *threads.Service
	Events       *events.Hub
	// Webhooks reports webhook ingress (ATC-306); nil leaves the resource
	// unmounted.
	Webhooks WebhookReporter
	// Artifacts serves the Artifacts resource (ATC-318); nil leaves it
	// unmounted. DocumentOrigin reports the origin that serves them and
	// supplies the reader links; required with Artifacts.
	Artifacts      *artifacts.Service
	DocumentOrigin DocumentOriginReporter
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
	// HomeDir is the server user's home directory, the default root of
	// the directory browser (ATC-316); empty leaves /v1/directories
	// unmounted.
	HomeDir string
}

// NewHandler builds the /v1 API surface plus /openapi.json and /docs.
//
// Middleware order (outermost first): identity headers, then auth, then
// the protocol check, then routing — headers appear on every response
// including 401s, unauthenticated callers cannot probe which routes
// exist, and no operation ever runs for a client on another protocol.
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
		return &HealthOutput{Body: api.Health{Status: "ok", Version: opts.Version, Protocol: api.Protocol}}, nil
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
	if opts.Webhooks != nil {
		registerWebhooks(humaAPI, opts.Webhooks)
	}
	if opts.Artifacts != nil {
		if opts.DocumentOrigin == nil {
			panic("server.NewHandler: DocumentOrigin must accompany Artifacts")
		}
		registerArtifacts(humaAPI, opts.Artifacts, opts.DocumentOrigin)
	}
	if opts.DocumentOrigin != nil {
		registerDocumentOrigin(humaAPI, opts.DocumentOrigin)
	}
	if opts.HomeDir != "" {
		registerDirectories(humaAPI, opts.HomeDir)
	}

	handler := withAuth(opts.Verify, withProtocol(withWriteDeadlines(problemMux(mux))))
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
	return withIdentityHeaders(opts.Version, handler)
}

// withIdentityHeaders stamps the server's release and protocol on every
// response, internal routes and 401s included, so any caller — a
// tokenless probe, an incompatible client — can learn what answered.
func withIdentityHeaders(version string, next http.Handler) http.Handler {
	protocol := strconv.Itoa(api.Protocol)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(api.ServerVersionHeader, version)
		w.Header().Set(api.ProtocolHeader, protocol)
		next.ServeHTTP(w, r)
	})
}

// withProtocol is the server's half of the protocol contract (ATC-325):
// every request on the public surface must declare the server's
// protocol, or it is refused with a protocol_mismatch problem before any
// route runs. Release identity is never compared. It sits inside auth,
// so an unauthenticated caller still sees only a 401, and outside the
// mux, so /openapi.json and /docs are covered like every operation;
// internal routes never pass through it.
func withProtocol(next http.Handler) http.Handler {
	protocol := strconv.Itoa(api.Protocol)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if declared := r.Header.Get(api.ProtocolHeader); declared != protocol {
			detail := fmt.Sprintf("this server speaks ATC protocol %s; the request declared protocol %q", protocol, declared)
			if declared == "" {
				detail = fmt.Sprintf("this server speaks ATC protocol %s; the request declared none (send %s: %s)", protocol, api.ProtocolHeader, protocol)
			}
			writeProblem(w, problem(http.StatusUpgradeRequired, api.CodeProtocolMismatch, detail))
			return
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
