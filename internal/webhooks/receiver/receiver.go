// Package receiver is the restricted webhook receiver (ATC-306): the
// process that terminates public traffic Tailscale Funnel forwards and
// relays it, bounded, to trusted Core over a loopback channel. It is a
// child of the server (`atc __webhook-receiver`), started with an empty
// environment, the filesystem root as its directory, one inherited
// descriptor (the public-facing listener), and a stdin whose EOF is
// Core's death.
//
// It runs in two stages of the same binary. The first, unrestricted,
// builds a Landlock ruleset — no filesystem access beyond reading and
// executing this binary and the system loader directories, no TCP bind, no
// TCP connect except to the channel port, and where the kernel supports it
// no abstract unix sockets or signals to processes outside the sandbox —
// restricts its own thread, and execs the second stage; the exec carries
// the restriction into every thread of the new image, which is what makes
// this robust for a multi-threaded Go runtime. The second stage proves the
// restriction from the inside (credential read, root listing, file
// creation, connecting to Core's API port, tracing its parent — each must
// be refused) before reporting itself ready on stdout and serving. Core
// establishes exposure only after that report; a failed proof, an
// unsupported kernel, or a non-Linux platform fails closed with the reason
// in the report.
//
// The forwarder enforces the same bounds Core enforces at the channel
// (body and header size, timeouts, concurrency), so a bug in one hop is
// caught by the other, and copies the original method, path, query,
// headers, and body untouched — verification is Core's, on the original
// bytes.
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
	"syscall"
	"time"
)

// Command is the hidden subcommand the server runs the receiver as.
const Command = "__webhook-receiver"

const (
	maxBodyBytes    = 1 << 20
	maxHeaderBytes  = 32 << 10
	maxResponseBody = 64 << 10
	concurrency     = 64
	forwardTimeout  = 30 * time.Second
)

// Options is the receiver's command line.
type Options struct {
	// ChannelPort is Core's loopback channel, the one TCP destination the
	// receiver may connect to.
	ChannelPort int
	// DenyPort is Core's API port; the restricted stage proves it cannot
	// connect there.
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
	// Landlock and no_new_privs apply to the calling thread, and execve
	// preserves exactly that thread — so both happen on one locked thread
	// that then execs.
	runtime.LockOSThread()
	abi, permanent, err := enforce(executable, opts.ChannelPort)
	if err != nil {
		report(stdout, Report{Reason: err.Error(), Permanent: permanent})
		return 1
	}
	argv := []string{executable, Command,
		"--channel-port", strconv.Itoa(opts.ChannelPort),
		"--deny-port", strconv.Itoa(opts.DenyPort),
		"--probe", opts.ProbePath,
		"--restricted", "--abi", strconv.Itoa(abi)}
	err = syscall.Exec(executable, argv, []string{})
	report(stdout, Report{Reason: "cannot exec the restricted stage: " + err.Error()})
	return 1
}

// serve is the second stage: prove the restriction, report, forward.
func serve(opts Options, stdin io.Reader, stdout, stderr io.Writer) int {
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
	listenerFile := os.NewFile(3, "public-listener")
	if listenerFile == nil {
		report(stdout, Report{Reason: "no listener was inherited on descriptor 3"})
		return 1
	}
	listener, err := net.FileListener(listenerFile)
	_ = listenerFile.Close()
	if err != nil {
		report(stdout, Report{Reason: "descriptor 3 is not a listener: " + err.Error()})
		return 1
	}
	report(stdout, Report{OK: true, ABI: opts.ABI, Checks: checks})

	// Core holds the write end of stdin for as long as it lives; EOF means
	// Core is gone and there is nothing to forward to.
	go func() {
		_, _ = io.Copy(io.Discard, stdin)
		os.Exit(0)
	}()

	forwarder := newForwarder(opts.ChannelPort)
	server := &http.Server{
		Handler:           forwarder,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    maxHeaderBytes,
	}
	if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
		_, _ = fmt.Fprintln(stderr, "receiver stopped serving:", err)
		return 1
	}
	return 0
}

// forwarder relays one public request to the channel and the channel's
// answer back, within the bounds.
type forwarder struct {
	channel  string
	client   *http.Client
	inflight chan struct{}
}

func newForwarder(channelPort int) *forwarder {
	return &forwarder{
		channel: "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(channelPort)),
		client: &http.Client{
			Timeout: forwardTimeout,
			// Redirects would turn the channel's answer into a request the
			// receiver makes on its own initiative; it never does that.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
		inflight: make(chan struct{}, concurrency),
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
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			http.Error(w, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "request body could not be read", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), forwardTimeout)
	defer cancel()
	target := f.channel + r.URL.EscapedPath()
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
