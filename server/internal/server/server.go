// Package server owns Atelier Code's HTTP serving boundary: it builds the router,
// serves the API and embedded web UI over configured TCP and Unix socket
// listeners, and marks requests with the listener they arrived on. Callers own
// the higher-level service lifecycle and path/config resolution.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jeremytondo/atelier-code/internal/action"
	"github.com/jeremytondo/atelier-code/internal/fs"
	"github.com/jeremytondo/atelier-code/internal/project"
	"github.com/jeremytondo/atelier-code/internal/session"
	"github.com/jeremytondo/atelier-code/internal/store"
	"github.com/jeremytondo/atelier-code/internal/zmx"
)

const DefaultHTTPAddr = "127.0.0.1:7331"

type Config struct {
	HTTPAddr         string
	HTTPAddrExplicit bool
	SocketPath       string
	// DBPath is the SQLite state database path.
	DBPath string
	// ZmxBin is the zmx binary location; empty resolves zmx from PATH.
	ZmxBin string
	// ActionsPath is the JSON file where API-managed Action overlays live.
	ActionsPath string
	// Environments is the registry of launch wrappers for session start.
	Environments session.EnvironmentRegistry
	// AuthToken is the bearer token required on the TCP listener; empty disables
	// TCP authentication.
	AuthToken string
	Logger    *slog.Logger
}

func Serve(ctx context.Context, cfg Config) error {
	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = DefaultHTTPAddr
	}
	logger := cfg.logger()

	if err := validateTCPAddr(cfg.HTTPAddr); err != nil {
		return err
	}
	if unauthenticatedRemoteBind(cfg) {
		logger.Warn("explicit non-loopback TCP bind configured without TCP authentication", "http_addr", cfg.HTTPAddr)
	}

	if cfg.SocketPath == "" {
		return fmt.Errorf("Unix socket path is required")
	}
	if cfg.DBPath == "" {
		return fmt.Errorf("session database path is required")
	}
	if cfg.ActionsPath == "" {
		return fmt.Errorf("actions file path is required")
	}
	if err := prepareUnixSocket(cfg.SocketPath); err != nil {
		return err
	}

	sessionStore, err := store.Open(cfg.DBPath)
	if err != nil {
		return fmt.Errorf("open session store: %w", err)
	}
	defer sessionStore.Close()

	actions := action.NewStore(cfg.ActionsPath, session.DefaultActions())
	projects := project.NewService(sessionStore, logger)
	sessions := session.NewService(sessionStore, zmx.New(cfg.ZmxBin), actions, cfg.Environments, projects, logger)
	if err := sessions.Reconcile(ctx); err != nil {
		return fmt.Errorf("reconcile sessions: %w", err)
	}

	tcpListener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	defer tcpListener.Close()

	unixListener, err := net.Listen("unix", cfg.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on Unix socket %s: %w", cfg.SocketPath, err)
	}
	defer os.Remove(cfg.SocketPath)
	defer unixListener.Close()

	fsService := fs.NewService(logger)

	router := Router(sessions, projects, actions, fsService, cfg.AuthToken)
	tcpServer := newHTTPServer(withListenerBoundary(ListenerTCP, router))
	unixServer := newHTTPServer(withListenerBoundary(ListenerUnix, router))

	errCh := make(chan error, 2)
	go serveHTTP(errCh, tcpServer, tcpListener, logger)
	go serveHTTP(errCh, unixServer, unixListener, logger)

	select {
	case <-ctx.Done():
		shutdownErr := shutdownServers(tcpServer, unixServer)
		serveErr := drainServerResults(errCh, 2, nil)
		if shutdownErr != nil {
			return shutdownErr
		}
		if serveErr != nil {
			return fmt.Errorf("serve HTTP: %w", serveErr)
		}
		logger.Info("atc service stopped")
		return nil
	case err := <-errCh:
		shutdownErr := shutdownServers(tcpServer, unixServer)
		serveErr := drainServerResults(errCh, 1, err)
		if serveErr != nil {
			return fmt.Errorf("serve HTTP: %w", serveErr)
		}
		if shutdownErr != nil {
			return shutdownErr
		}
		return nil
	}
}

// unauthenticatedRemoteBind reports whether the configured TCP bind exposes
// the API beyond loopback without a bearer token. Startup deliberately
// continues in that case — unauthenticated use over a trusted overlay network
// (e.g. a tailnet) is a supported mode — but it must be loudly visible.
func unauthenticatedRemoteBind(cfg Config) bool {
	return cfg.HTTPAddrExplicit && !isLoopbackTCPAddr(cfg.HTTPAddr) && cfg.AuthToken == ""
}

func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		// No global Read/Write timeouts: the attach route holds a WebSocket
		// open for the life of a terminal session, and either timeout would
		// sever it. JSON request bodies are bounded per-request in the API's
		// decodeJSON instead; IdleTimeout only reaps idle keep-alive conns.
		IdleTimeout: 2 * time.Minute,
	}
}

func serveHTTP(errCh chan<- error, httpServer *http.Server, listener net.Listener, logger *slog.Logger) {
	logger.Info("atc service listening", "network", listener.Addr().Network(), "addr", listener.Addr().String())
	if err := httpServer.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		errCh <- err
		return
	}
	errCh <- nil
}

func drainServerResults(errCh <-chan error, count int, first error) error {
	serveErr := first
	for range count {
		err := <-errCh
		if serveErr == nil {
			serveErr = err
		}
	}
	return serveErr
}

func shutdownServers(servers ...*http.Server) error {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var shutdownErr error
	for _, server := range servers {
		if err := server.Shutdown(shutdownCtx); err != nil && shutdownErr == nil {
			shutdownErr = fmt.Errorf("shutdown server: %w", err)
		}
	}
	return shutdownErr
}

func (cfg Config) logger() *slog.Logger {
	if cfg.Logger != nil {
		return cfg.Logger
	}
	return slog.New(slog.NewTextHandler(os.Stderr, nil))
}
