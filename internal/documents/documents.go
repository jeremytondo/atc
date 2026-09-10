// Package documents runs the document origin (ATC-318): the unprivileged
// listener that serves published artifacts to browsers, on its own port
// so it is a distinct browser origin from the API — local by default,
// on the tailnet whenever the API is. It holds no credential and is
// never a route on the API. Bind failure never fails the server: the
// listener is retried with backoff and its state, endpoints, and
// failure reason are reported through Status.
package documents

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/tailscale"
)

const (
	bindRetryBase = time.Second
	bindRetryMax  = 30 * time.Second
	// A listener that served this long was healthy; its eventual failure
	// starts a fresh backoff curve.
	healthyRunReset = time.Minute
)

// Options wires the service.
type Options struct {
	Resolver Resolver
	// Bind and Port are the listener address; port 0 is OS-assigned.
	Bind string
	Port int
	// TailscaleExecutable enables tailnet exposure of the origin through
	// `tailscale serve` on Port when set — the resolved CLI path, present
	// exactly when the API is exposed too.
	TailscaleExecutable string
	Logger              *slog.Logger

	// listen is the bind seam for tests; nil means net.Listen.
	listen func(network, address string) (net.Listener, error)
}

// Service is the document origin's lifecycle.
type Service struct {
	opts    Options
	handler http.Handler
	logger  *slog.Logger

	mu     sync.Mutex
	status api.DocumentOrigin
}

// New builds the service; Run serves it.
func New(opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.listen == nil {
		opts.listen = net.Listen
	}
	s := &Service{opts: opts, handler: Handler(opts.Resolver, opts.Logger), logger: opts.Logger}
	s.status = api.DocumentOrigin{State: api.OriginStarting, Reason: "starting", URL: s.localURL(opts.Port)}
	if opts.TailscaleExecutable != "" {
		s.status.Tailnet = api.DocumentOriginTailnet{State: api.TailnetStarting, Reason: "starting"}
	} else {
		s.status.Tailnet = api.DocumentOriginTailnet{State: api.TailnetDisabled}
	}
	return s
}

// Run serves until ctx is cancelled: the listener bound (retried with
// backoff on failure, so a port conflict is a reported state rather than
// a crash), the tailnet exposure supervised only while the listener is
// up, and both torn down before it returns.
func (s *Service) Run(ctx context.Context) {
	delay := bindRetryBase
	for {
		started := time.Now()
		err := s.serve(ctx)
		if ctx.Err() != nil {
			s.set(func(d *api.DocumentOrigin) {
				d.State, d.Reason = api.OriginStarting, "stopped"
			})
			return
		}
		s.logger.Warn("document origin failed", "error", err)
		s.set(func(d *api.DocumentOrigin) {
			d.State, d.Reason = api.OriginUnavailable, err.Error()
		})
		if time.Since(started) > healthyRunReset {
			delay = bindRetryBase
		}
		jittered := delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		select {
		case <-time.After(jittered):
		case <-ctx.Done():
			return
		}
		delay = min(delay*2, bindRetryMax)
	}
}

// serve binds and serves once, returning why it stopped.
func (s *Service) serve(ctx context.Context) error {
	listener, err := s.opts.listen("tcp", net.JoinHostPort(s.opts.Bind, strconv.Itoa(s.opts.Port)))
	if err != nil {
		return fmt.Errorf("cannot bind the document listener: %w", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	s.set(func(d *api.DocumentOrigin) {
		d.State, d.Reason, d.URL = api.OriginReady, "", s.localURL(port)
	})
	s.logger.Info("document origin serving", "addr", listener.Addr().String())

	serveCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	var exposure sync.WaitGroup
	if s.opts.TailscaleExecutable != "" {
		supervisor := tailscale.NewServeSupervisor(s.opts.TailscaleExecutable, port, s.observe, s.logger)
		exposure.Go(func() { supervisor.Run(serveCtx) })
	}

	srv := &http.Server{Handler: s.handler, ReadHeaderTimeout: 10 * time.Second}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(listener) }()
	select {
	case err = <-served:
	case <-ctx.Done():
		shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		err = srv.Shutdown(shutdownCtx)
	}
	cancel()
	exposure.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// observe maps exposure reports onto the tailnet status.
func (s *Service) observe(report tailscale.Report) {
	s.set(func(d *api.DocumentOrigin) {
		if report.Serving {
			d.Tailnet = api.DocumentOriginTailnet{State: api.TailnetReady, URL: report.URL}
			return
		}
		d.Tailnet = api.DocumentOriginTailnet{State: api.TailnetStarting, URL: report.URL, Reason: report.Problem, Action: report.Action}
	})
}

func (s *Service) set(update func(*api.DocumentOrigin)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := s.status
	update(&s.status)
	if s.status != before {
		s.logger.Info("document origin "+string(s.status.State), "url", s.status.URL, "reason", s.status.Reason,
			"tailnet", s.status.Tailnet.State, "tailnetUrl", s.status.Tailnet.URL, "tailnetReason", s.status.Tailnet.Reason)
	}
}

// Status is the origin's current report.
func (s *Service) Status(context.Context) api.DocumentOrigin {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Bases are the origin's base URLs for reader links: the local one
// always, the tailnet one while exposure is serving.
func (s *Service) Bases() (local, tailnet string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.status.Tailnet.State == api.TailnetReady {
		tailnet = s.status.Tailnet.URL
	}
	return s.status.URL, tailnet
}

// localURL is the address a local browser opens: 127.0.0.1 when bound to
// every interface (or localhost), the bind address itself otherwise.
func (s *Service) localURL(port int) string {
	host := s.opts.Bind
	if ip := net.ParseIP(host); host == "" || host == "localhost" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, strconv.Itoa(port))
}
