package webhooks

// The ingress lifecycle: the restricted receiver child and the Funnel
// exposure fronting it, owned by the server's lifetime. Order is the
// contract — the receiver must prove its isolation over a channel Core
// already serves before exposure is established, and exposure is withdrawn
// the moment the receiver is gone. Nothing here trusts the receiver: it
// gets a listener, its channel connections, and nothing else.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/tailscale"
	"github.com/jeremytondo/atc/internal/webhooks/receiver"
)

const (
	receiverReadyTimeout = 15 * time.Second
	receiverWaitDelay    = 3 * time.Second
	ingressRetryBase     = time.Second
	ingressRetryMax      = 30 * time.Second
)

// IngressOptions enables intake.
type IngressOptions struct {
	// Executable is the atc binary that runs the receiver child.
	Executable string
	// TailscaleExecutable is the resolved tailscale CLI for Funnel.
	TailscaleExecutable string
	// PublicPort is the Funnel HTTPS port (443, 8443, or 10000).
	PublicPort int
	// DenyPort is the server's own API port: the receiver must prove it
	// cannot connect there.
	DenyPort int
	// ProbePath is a credential file (the bearer token's) the receiver
	// must prove it cannot read.
	ProbePath string

	// readyTimeout shrinks the receiver readiness budget in tests.
	readyTimeout time.Duration
}

type ingress struct {
	opts    IngressOptions
	service *Service

	mu       sync.Mutex
	receiver *os.Process // the running receiver, for tests and diagnostics
	target   net.Addr    // the receiver listener, for tests
}

func newIngress(opts IngressOptions, service *Service) *ingress {
	if opts.readyTimeout == 0 {
		opts.readyTimeout = receiverReadyTimeout
	}
	return &ingress{opts: opts, service: service}
}

// run keeps intake up until ctx is cancelled: the receiver listener bound
// once, the receiver respawned with backoff whenever it exits, Funnel
// established only while a proven receiver serves. An unsupported platform
// or kernel ends the loop with intake unavailable; the rest of the server
// is untouched.
func (g *ingress) run(ctx context.Context) {
	logger := g.service.logger
	if runtime.GOOS != "linux" {
		g.unavailable("webhook ingress requires Linux: the receiver is isolated with Landlock and seccomp")
		return
	}
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		g.unavailable("cannot bind the receiver listener: " + err.Error())
		return
	}
	defer func() { _ = target.Close() }()
	g.mu.Lock()
	g.target = target.Addr()
	g.mu.Unlock()

	delay := ingressRetryBase
	for {
		g.service.setExposure(exposureState{state: api.WebhooksStarting, reason: "starting the restricted receiver"})
		started := time.Now()
		permanent, err := g.runReceiver(ctx, target.(*net.TCPListener))
		if ctx.Err() != nil {
			g.service.setExposure(exposureState{state: api.WebhooksStarting, reason: "stopped"})
			return
		}
		if permanent {
			g.unavailable(err.Error())
			return
		}
		logger.Warn("webhook receiver stopped", "error", err)
		g.service.setExposure(exposureState{state: api.WebhooksStarting, reason: err.Error()})
		if time.Since(started) > time.Minute {
			delay = ingressRetryBase
		}
		jittered := delay/2 + time.Duration(rand.Int64N(int64(delay/2)+1))
		select {
		case <-time.After(jittered):
		case <-ctx.Done():
			return
		}
		delay = min(delay*2, ingressRetryMax)
	}
}

func (g *ingress) unavailable(reason string) {
	g.service.setExposure(exposureState{state: api.WebhooksUnavailable, reason: reason})
}

// runReceiver runs one receiver child to completion: spawn it with the
// listener, the channel connections, and nothing else; wait for its
// isolation proof; front it with Funnel while it lives; withdraw Funnel
// when it exits. permanent reports a failure no retry can fix (platform or
// kernel support).
func (g *ingress) runReceiver(ctx context.Context, target *net.TCPListener) (permanent bool, err error) {
	// The parent-death signal is tied to the thread that forks the child,
	// so that thread stays alive — locked to this goroutine — until the
	// receiver has exited.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	logger := g.service.logger
	listenerFile, err := target.File()
	if err != nil {
		return false, fmt.Errorf("cannot share the receiver listener: %w", err)
	}
	defer func() { _ = listenerFile.Close() }()
	// The channel is a fixed set of preconnected socket pairs: Core serves
	// its ends, the receiver's transport reuses the others. The receiver
	// never dials anything, and nothing else can reach this handler.
	channel, err := newChannel(receiver.Concurrency)
	if err != nil {
		return false, err
	}
	defer channel.close()
	server := &http.Server{
		Handler:           http.HandlerFunc(g.service.serveDelivery),
		ReadHeaderTimeout: receiver.ReadHeaderTimeout,
		WriteTimeout:      receiver.WriteTimeout,
		MaxHeaderBytes:    receiver.MaxHeaderBytes,
		// Idle channel connections live as long as the receiver: closing
		// one would shrink its fixed budget. Body reads are bounded per
		// request by the handler instead.
		IdleTimeout: 0,
		ReadTimeout: 0,
	}
	served := make(chan struct{})
	go func() {
		defer close(served)
		if err := server.Serve(channel); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("webhook channel server stopped", "error", err)
		}
	}()
	defer func() {
		_ = server.Close()
		<-served
	}()

	childCtx, cancelChild := context.WithCancel(ctx)
	defer cancelChild()
	cmd := exec.CommandContext(childCtx, g.opts.Executable, receiver.Command,
		"--channel-conns", strconv.Itoa(len(channel.receiverEnds)),
		"--deny-port", strconv.Itoa(g.opts.DenyPort),
		"--probe", g.opts.ProbePath)
	// The receiver inherits nothing it could use: an empty environment,
	// the filesystem root as its directory, and exactly the descriptors it
	// serves with — the public-facing listener (fd 3) and the channel
	// connections after it. Its stdin is held open by Core so it notices
	// Core's death even where the parent-death signal cannot fire.
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.ExtraFiles = append([]*os.File{listenerFile}, channel.receiverEnds...)
	cmd.SysProcAttr = receiverAttr()
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = receiverWaitDelay
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return false, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return false, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return false, err
	}
	if err := cmd.Start(); err != nil {
		return false, fmt.Errorf("cannot start the webhook receiver: %w", err)
	}
	channel.closeReceiverEnds()
	g.mu.Lock()
	g.receiver = cmd.Process
	g.mu.Unlock()
	logger.Info("webhook receiver started", "pid", cmd.Process.Pid)

	var errTail lines
	var readers sync.WaitGroup
	readers.Go(func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			errTail.add(scanner.Text())
			logger.Warn("webhook receiver", "output", scanner.Text())
		}
	})
	// The first stdout line is the receiver's report: its isolation proof,
	// or why it could not restrict itself. Anything after is ignored.
	reports := make(chan receiver.Report, 1)
	readers.Go(func() {
		defer func() { _, _ = io.Copy(io.Discard, stdout) }()
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			var report receiver.Report
			if err := json.Unmarshal(scanner.Bytes(), &report); err != nil {
				report = receiver.Report{Reason: "unreadable receiver report: " + scanner.Text()}
			}
			reports <- report
		}
	})
	exited := make(chan error, 1)
	go func() {
		readers.Wait()
		exited <- cmd.Wait()
	}()
	forget := func() {
		g.mu.Lock()
		g.receiver = nil
		g.mu.Unlock()
	}
	// finish ends a receiver that must not serve: stdin closed, interrupt
	// sent, exit awaited.
	finish := func() {
		_ = stdin.Close()
		cancelChild()
		<-exited
		forget()
	}

	var report receiver.Report
	select {
	case report = <-reports:
	case err := <-exited:
		forget()
		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		return false, fmt.Errorf("webhook receiver exited before reporting: %s", exitReason(err, &errTail))
	case <-time.After(g.opts.readyTimeout):
		finish()
		return false, fmt.Errorf("webhook receiver did not report within %s", g.opts.readyTimeout)
	}
	if !report.OK {
		finish()
		return report.Permanent, fmt.Errorf("webhook receiver cannot be isolated: %s", report.Reason)
	}
	logger.Info("webhook receiver isolated", "pid", cmd.Process.Pid, "landlock_abi", report.ABI, "checks", report.Checks)

	// Receiver proven and channel up: expose. Exposure lives inside the
	// receiver's lifetime — cancelled, and its child reaped, before this
	// function returns.
	g.service.setExposure(exposureState{state: api.WebhooksStarting, reason: "establishing tailscale funnel"})
	funnelCtx, cancelFunnel := context.WithCancel(ctx)
	supervisor := tailscale.NewFunnelSupervisor(g.opts.TailscaleExecutable, g.opts.PublicPort,
		target.Addr().(*net.TCPAddr).Port, g.observe, logger)
	var funnel sync.WaitGroup
	funnel.Go(func() { supervisor.Run(funnelCtx) })

	waitErr := <-exited
	cancelFunnel()
	funnel.Wait()
	_ = stdin.Close()
	forget()
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	return false, fmt.Errorf("webhook receiver exited: %s", exitReason(waitErr, &errTail))
}

// channel is the preconnected socket pairs between Core and one receiver:
// a net.Listener over Core's ends (Accept hands them out once, then blocks
// until Close) and the receiver's ends as files to inherit.
type channel struct {
	coreEnds     chan net.Conn
	receiverEnds []*os.File
	done         chan struct{}
	once         sync.Once
}

func newChannel(count int) (*channel, error) {
	c := &channel{coreEnds: make(chan net.Conn, count), done: make(chan struct{})}
	for range count {
		fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
		if err != nil {
			c.close()
			return nil, fmt.Errorf("cannot create the receiver channel: %w", err)
		}
		coreFile := os.NewFile(uintptr(fds[0]), "channel-core")
		conn, err := net.FileConn(coreFile)
		_ = coreFile.Close()
		if err != nil {
			_ = unix.Close(fds[1])
			c.close()
			return nil, fmt.Errorf("cannot adopt the receiver channel: %w", err)
		}
		c.coreEnds <- conn
		c.receiverEnds = append(c.receiverEnds, os.NewFile(uintptr(fds[1]), "channel-receiver"))
	}
	return c, nil
}

func (c *channel) Accept() (net.Conn, error) {
	select {
	case conn := <-c.coreEnds:
		return conn, nil
	case <-c.done:
		return nil, net.ErrClosed
	}
}

// Close stops Accept; the served connections are the http.Server's to
// close.
func (c *channel) Close() error {
	c.once.Do(func() { close(c.done) })
	return nil
}

func (c *channel) Addr() net.Addr { return channelAddr{} }

// closeReceiverEnds drops Core's references to the receiver's ends once
// the child holds them, so a receiver exit closes them for good.
func (c *channel) closeReceiverEnds() {
	for _, file := range c.receiverEnds {
		_ = file.Close()
	}
}

// close releases everything not handed to the server or the child.
func (c *channel) close() {
	_ = c.Close()
	c.closeReceiverEnds()
	for {
		select {
		case conn := <-c.coreEnds:
			_ = conn.Close()
		default:
			return
		}
	}
}

type channelAddr struct{}

func (channelAddr) Network() string { return "unix" }
func (channelAddr) String() string  { return "webhook-channel" }

// observe maps Funnel reports onto ingress state: only a serving report
// is readiness; everything else is converging, with Tailscale's own
// instruction attached when it printed one.
func (g *ingress) observe(report tailscale.Report) {
	if report.Serving {
		g.service.setExposure(exposureState{state: api.WebhooksReady, url: report.URL})
		return
	}
	g.service.setExposure(exposureState{state: api.WebhooksStarting, url: report.URL, reason: report.Problem, action: report.Action})
}

// receiverProcess is the running receiver, for tests that fault it.
func (g *ingress) receiverProcess() *os.Process {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.receiver
}

// targetAddr is the receiver's public-facing listener, for tests that
// stand in for Funnel.
func (g *ingress) targetAddr() net.Addr {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.target
}

func exitReason(waitErr error, tail *lines) string {
	if text := tail.String(); text != "" {
		return text
	}
	if waitErr != nil {
		return waitErr.Error()
	}
	return "exit code 0"
}

// lines keeps the last few output lines for failure reports.
type lines struct {
	mu    sync.Mutex
	lines []string
}

func (l *lines) add(line string) {
	if line = strings.TrimSpace(line); line == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, line)
	if len(l.lines) > 8 {
		l.lines = l.lines[1:]
	}
}

func (l *lines) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, " ")
}
