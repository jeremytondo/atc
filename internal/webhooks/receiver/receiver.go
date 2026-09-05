// Package receiver is the restricted webhook receiver (ATC-306): the
// process that terminates public traffic Tailscale Funnel forwards and
// relays it, bounded, to trusted Core. It is a child of the server
// (`atc __webhook-receiver`) started with an empty environment, the
// filesystem root as its directory, its inherited descriptors — the
// public-facing listener and a fixed set of preconnected channel
// connections to Core — and a stdin whose EOF is Core's death.
//
// It runs in two stages of the same binary. The first, unrestricted,
// builds a Landlock ruleset (no filesystem access beyond reading and
// executing this binary and the system loader directories, no TCP bind or
// connect, no abstract sockets or signals where the kernel scopes them)
// applies it to its own thread, and execs the second stage; the exec
// carries the restriction into every thread of the new image, which is
// what makes this robust for a multi-threaded Go runtime. The second stage
// adds the seccomp filter (no socket creation of any family, no process
// creation, no tracing, no signalling other processes) to every thread,
// then proves the restriction from the inside — credential read, root listing, file
// creation, TCP connect and bind, UDP and unix sockets, tracing and
// signalling its parent, spawning — each must be refused — before
// reporting itself ready on stdout and serving. Core establishes exposure
// only after that report; a failed proof, an unsupported kernel, or a
// non-Linux platform fails closed with the reason in the report.
//
// The channel is the only way out: the receiver never opens a connection
// of its own, so a compromised receiver has Core's channel handler as its
// entire reach. The forwarder enforces the same bounds Core enforces at
// that handler, so a bug in one hop is caught by the other, and copies the
// original method, path, query, headers, and body untouched —
// verification is Core's, on the original bytes.
package receiver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Command is the hidden subcommand the server runs the receiver as.
const Command = "__webhook-receiver"

// The wire bounds, enforced at both hops.
const (
	// MaxBodyBytes bounds one delivery's body.
	MaxBodyBytes = 1 << 20
	// MaxHeaderBytes bounds one delivery's headers.
	MaxHeaderBytes = 32 << 10
	// Concurrency bounds deliveries in flight, and is the number of channel
	// connections Core hands the receiver.
	Concurrency = 32
	// MaxConnections bounds open public connections at the receiver.
	MaxConnections = 256
	// ReadHeaderTimeout, RequestTimeout, and WriteTimeout bound one
	// exchange at each hop.
	ReadHeaderTimeout = 10 * time.Second
	RequestTimeout    = 30 * time.Second
	WriteTimeout      = 60 * time.Second

	maxResponseBody = 64 << 10
	// listenerFD and channelFD0 are where Core places the inherited
	// descriptors.
	listenerFD = 3
	channelFD0 = 4
)

// channelFD is the descriptor of the i-th inherited channel connection.
func channelFD(i int) int { return channelFD0 + i }

// Options is the receiver's command line.
type Options struct {
	// ChannelConns is how many preconnected channel connections Core
	// inherited on descriptors channelFD0 onward.
	ChannelConns int
	// DenyPort is Core's API port; the restricted stage proves it can
	// neither connect to nor bind it.
	DenyPort int
	// ProbePath is a credential file the restricted stage proves it
	// cannot read.
	ProbePath string
	// Restricted marks the second stage, running under the first stage's
	// restriction; ABI is the Landlock ABI it enforced.
	Restricted bool
	ABI        int
}

// Report is the one line the receiver writes to stdout: its isolation
// proof, or why it could not restrict itself. Permanent marks a reason no
// retry fixes (platform or kernel support).
type Report struct {
	OK        bool   `json:"ok"`
	ABI       int    `json:"abi,omitempty"`
	Reason    string `json:"reason,omitempty"`
	Permanent bool   `json:"permanent,omitempty"`
	// Checks records each self-test's outcome ("denied", "allowed", or a
	// failure), for diagnostics.
	Checks map[string]string `json:"checks,omitempty"`
}

// Main runs the receiver and returns the process exit code.
func Main(opts Options, stdin io.Reader, stdout, stderr io.Writer) int {
	if !opts.Restricted {
		return restrictAndExec(opts, stdout)
	}
	return serve(opts, stdin, stdout, stderr)
}

func report(stdout io.Writer, r Report) {
	line, _ := json.Marshal(r)
	_, _ = stdout.Write(append(line, '\n'))
}

// restrictAndExec is the first stage: restrict this thread, then exec the
// second stage on it so the whole new process inherits the restriction.
// Everything after a successful exec belongs to the restricted stage;
// reaching the end of this function means the exec failed.
func restrictAndExec(opts Options, stdout io.Writer) int {
	executable, err := os.Executable()
	if err != nil {
		report(stdout, Report{Reason: "cannot resolve the receiver executable: " + err.Error()})
		return 1
	}
	// Landlock, seccomp, and no_new_privs apply to the calling thread, and
	// execve preserves exactly that thread — so all of it happens on one
	// locked thread that then execs.
	runtime.LockOSThread()
	abi, permanent, err := enforce(executable)
	if err != nil {
		report(stdout, Report{Reason: err.Error(), Permanent: permanent})
		return 1
	}
	argv := []string{executable, Command,
		"--channel-conns", strconv.Itoa(opts.ChannelConns),
		"--deny-port", strconv.Itoa(opts.DenyPort),
		"--probe", opts.ProbePath,
		"--restricted", "--abi", strconv.Itoa(abi)}
	err = syscall.Exec(executable, argv, []string{})
	report(stdout, Report{Reason: "cannot exec the restricted stage: " + err.Error()})
	return 1
}

// serve is the second stage: finish the restriction, prove it, report,
// forward.
func serve(opts Options, stdin io.Reader, stdout, stderr io.Writer) int {
	if err := confine(); err != nil {
		report(stdout, Report{Reason: err.Error(), Permanent: true, ABI: opts.ABI})
		return 1
	}
	checks, failures := selfTest(opts)
	if len(failures) > 0 {
		report(stdout, Report{
			Reason:    "isolation not enforced: " + strings.Join(failures, "; "),
			Permanent: true,
			ABI:       opts.ABI,
			Checks:    checks,
		})
		return 1
	}
	listener, err := inheritedListener()
	if err != nil {
		report(stdout, Report{Reason: err.Error()})
		return 1
	}
	channel, err := inheritedChannel(opts.ChannelConns)
	if err != nil {
		report(stdout, Report{Reason: err.Error()})
		return 1
	}
	report(stdout, Report{OK: true, ABI: opts.ABI, Checks: checks})

	// Core holds the write end of stdin for as long as it lives; EOF means
	// Core is gone and there is nothing to forward to.
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		os.Exit(0)
	}()

	server := &http.Server{
		Handler:           newForwarder(channel),
		ReadHeaderTimeout: ReadHeaderTimeout,
		ReadTimeout:       RequestTimeout,
		WriteTimeout:      WriteTimeout,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    MaxHeaderBytes,
	}
	if err := server.Serve(limitListener(listener, MaxConnections)); err != nil && !errors.Is(err, http.ErrServerClosed) {
		_, _ = fmt.Fprintln(stderr, "receiver stopped serving:", err)
		return 1
	}
	return 0
}

func inheritedListener() (net.Listener, error) {
	file := os.NewFile(uintptr(listenerFD), "public-listener")
	defer func() { _ = file.Close() }()
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("descriptor %d is not a listener: %w", listenerFD, err)
	}
	return listener, nil
}

// inheritedChannel collects the preconnected channel connections.
func inheritedChannel(count int) ([]net.Conn, error) {
	if count <= 0 {
		return nil, errors.New("no channel connections were inherited")
	}
	conns := make([]net.Conn, 0, count)
	for i := range count {
		file := os.NewFile(uintptr(channelFD(i)), "channel")
		conn, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("descriptor %d is not a channel connection: %w", channelFD(i), err)
		}
		conns = append(conns, conn)
	}
	return conns, nil
}

// channelPool hands the inherited connections to the HTTP transport in
// place of dialling. The pool is the whole budget: the transport keeps
// each connection alive and reuses it, and if the pool ever runs dry the
// receiver exits so Core respawns it with a fresh set rather than serving
// degraded.
type channelPool struct {
	mu    sync.Mutex
	conns []net.Conn
}

func (p *channelPool) dial(context.Context, string, string) (net.Conn, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.conns) == 0 {
		_, _ = fmt.Fprintln(os.Stderr, "receiver channel connections exhausted; exiting for a fresh set")
		os.Exit(2)
	}
	conn := p.conns[0]
	p.conns = p.conns[1:]
	return conn, nil
}

// forwarder relays one public request to Core over the channel and
// Core's answer back, within the bounds.
type forwarder struct {
	client   *http.Client
	inflight chan struct{}
}

func newForwarder(channel []net.Conn) *forwarder {
	pool := &channelPool{conns: channel}
	return &forwarder{
		client: &http.Client{
			Timeout: RequestTimeout,
			Transport: &http.Transport{
				DialContext:           pool.dial,
				MaxConnsPerHost:       len(channel),
				MaxIdleConnsPerHost:   len(channel),
				ResponseHeaderTimeout: RequestTimeout,
				DisableCompression:    true,
			},
			// Redirects would turn Core's answer into a request the
			// receiver makes on its own initiative; it never does that.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		inflight: make(chan struct{}, Concurrency),
	}
}

// hopByHop are the headers that describe this connection rather than the
// delivery and must not be relayed.
var hopByHop = map[string]bool{
	"Connection": true, "Keep-Alive": true, "Proxy-Authenticate": true, "Proxy-Authorization": true,
	"Te": true, "Trailer": true, "Transfer-Encoding": true, "Upgrade": true,
}

func (f *forwarder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case f.inflight <- struct{}{}:
		defer func() { <-f.inflight }()
	default:
		w.Header().Set("Retry-After", "5")
		http.Error(w, "too many requests in flight", http.StatusServiceUnavailable)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, MaxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "request body could not be read", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), RequestTimeout)
	defer cancel()
	// The host is a placeholder: the transport dials the pool, not an
	// address.
	target := "http://core" + r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	forward, err := http.NewRequestWithContext(ctx, r.Method, target, strings.NewReader(string(body)))
	if err != nil {
		http.Error(w, "request could not be forwarded", http.StatusBadRequest)
		return
	}
	for name, values := range r.Header {
		if hopByHop[http.CanonicalHeaderKey(name)] {
			continue
		}
		forward.Header[name] = values
	}
	forward.Header.Set("X-Forwarded-Host", r.Host)
	forward.ContentLength = int64(len(body))
	response, err := f.client.Do(forward)
	if err != nil {
		w.Header().Set("Retry-After", "5")
		http.Error(w, "the receiver cannot reach its server; retry", http.StatusBadGateway)
		return
	}
	defer func() { _ = response.Body.Close() }()
	for _, name := range []string{"Content-Type", "Retry-After"} {
		if value := response.Header.Get(name); value != "" {
			w.Header().Set(name, value)
		}
	}
	w.WriteHeader(response.StatusCode)
	_, _ = io.Copy(w, io.LimitReader(response.Body, maxResponseBody))
}

// limitListener caps open connections: Accept waits while the cap is
// reached and a slot returns when its connection closes.
func limitListener(l net.Listener, limit int) net.Listener {
	return &limited{Listener: l, slots: make(chan struct{}, limit)}
}

type limited struct {
	net.Listener
	slots chan struct{}
}

func (l *limited) Accept() (net.Conn, error) {
	l.slots <- struct{}{}
	conn, err := l.Listener.Accept()
	if err != nil {
		<-l.slots
		return nil, err
	}
	return &limitedConn{Conn: conn, release: func() { <-l.slots }}, nil
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
