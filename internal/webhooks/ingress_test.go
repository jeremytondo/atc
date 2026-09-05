//go:build linux

package webhooks

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/webhooks/receiver"
)

// atcBinary is the real atc build the ingress tests spawn as the receiver:
// the sandbox is applied by the child's own first stage and proven by its
// second, so only the actual binary exercises it. Built once per package
// run; empty when the toolchain is unavailable.
var atcBinary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "atc-webhooks-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	atcBinary = filepath.Join(dir, "atc")
	build := exec.Command("go", "build", "-o", atcBinary, "../../cmd/atc")
	build.Env = append(os.Environ(), "CGO_ENABLED=0")
	if out, err := build.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "building atc for receiver tests: %v\n%s", err, out)
		atcBinary = ""
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func requireBinary(t *testing.T) string {
	t.Helper()
	if atcBinary == "" {
		t.Skip("atc binary unavailable (go toolchain missing)")
	}
	return atcBinary
}

// listen binds a loopback listener held open for the test: connecting
// to it succeeds at the TCP level, so a receiver that can reach it is
// visibly unrestricted.
func listen(t *testing.T) *net.TCPListener {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = l.Close() })
	return l.(*net.TCPListener)
}

func port(l net.Listener) int { return l.Addr().(*net.TCPAddr).Port }

// fakeTailscale writes a tailscale stand-in: status reports a running
// node; funnel records its pid and arguments, prints the banner, and
// lives until signalled.
func fakeTailscale(t *testing.T) (executable, dir string) {
	t.Helper()
	dir = t.TempDir()
	executable = filepath.Join(dir, "tailscale")
	script := `#!/bin/sh
if [ "$1" = status ]; then printf '%s' '{"BackendState":"Running","Self":{"DNSName":"host.tailnet.ts.net."}}'; exit 0; fi
echo "$$" > "` + dir + `/funnel.pid"
echo "$@" > "` + dir + `/funnel.args"
echo 'Available on the internet:'; echo; echo 'https://host.tailnet.ts.net/'
exec sleep 300
`
	if err := os.WriteFile(executable, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return executable, dir
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

func funnelPID(t *testing.T, dir string) int {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, "funnel.pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		t.Fatal(err)
	}
	return pid
}

// runReceiverDirect spawns the receiver command the way ingress does —
// listener on fd 3, channel connections after it, empty environment,
// stdin held — with Core's channel ends served by handler. It returns the
// receiver's report, the process, and the stdin to release it with.
func runReceiverDirect(t *testing.T, executable string, public *net.TCPListener, handler http.Handler, denyPort int, probe string) (receiver.Report, *exec.Cmd, io.WriteCloser) {
	t.Helper()
	file, err := public.File()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = file.Close() }()
	channel, err := newChannel(receiver.Concurrency)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	go func() { _ = server.Serve(channel) }()
	t.Cleanup(func() { _ = server.Close(); channel.close() })
	cmd := exec.Command(executable, receiver.Command,
		"--channel-conns", strconv.Itoa(len(channel.receiverEnds)), "--deny-port", strconv.Itoa(denyPort), "--probe", probe)
	cmd.Env = []string{}
	cmd.Dir = "/"
	cmd.ExtraFiles = append([]*os.File{file}, channel.receiverEnds...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	channel.closeReceiverEnds()
	t.Cleanup(func() {
		_ = stdin.Close()
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
	})
	line := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		if scanner.Scan() {
			line <- scanner.Text()
		}
		close(line)
	}()
	var report receiver.Report
	select {
	case text, ok := <-line:
		if !ok {
			t.Fatal("receiver exited without a report")
		}
		if err := json.Unmarshal([]byte(text), &report); err != nil {
			t.Fatalf("receiver report %q: %v", text, err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("receiver did not report")
	}
	return report, cmd, stdin
}

// The actual restricted process attempts what it must not be able to do —
// read a credential, list the filesystem, create a file, connect to or
// bind the API port, open UDP or unix sockets, trace or signal its parent,
// spawn a process — and every attempt is refused by policy; its inherited
// channel works and the forwarding path carries requests intact. Closing
// stdin (Core gone) ends it.
func TestReceiverProvesIsolationBeforeServing(t *testing.T) {
	executable := requireBinary(t)
	probe := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(probe, []byte("atc_secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	public := listen(t)
	deny := listen(t)
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusTeapot)
		_, _ = fmt.Fprintf(w, "%s %s?%s host=%s probe=%s body=%s", r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("X-Forwarded-Host"), r.Header.Get("X-Probe-Secret"), body)
	})

	report, cmd, stdin := runReceiverDirect(t, executable, public, echo, port(deny), probe)
	if !report.OK {
		if report.Permanent && strings.Contains(strings.ToLower(report.Reason), "landlock") {
			t.Skipf("this kernel cannot run the receiver: %s", report.Reason)
		}
		t.Fatalf("receiver refused to serve: %+v", report)
	}
	for _, check := range []string{"read_credential", "list_root", "create_file", "connect_tcp", "bind_tcp", "udp_socket", "unix_socket", "trace_parent", "signal_parent", "spawn_process"} {
		if report.Checks[check] != "denied" {
			t.Errorf("check %s = %q, want denied", check, report.Checks[check])
		}
	}
	if got := report.Checks["channel"]; got != strconv.Itoa(receiver.Concurrency)+" connections" {
		t.Errorf("channel = %q, want every inherited connection usable", got)
	}
	if report.ABI < 1 {
		t.Errorf("landlock abi = %d, want at least 1", report.ABI)
	}
	if env := report.Checks["environment"]; env != "0 variables" && env != "1 variables" {
		t.Errorf("environment = %q, want none inherited", env)
	}

	// Forwarding: the public listener carries the request to the channel
	// with method, path, query, headers, and body intact, and relays the
	// channel's status and body back.
	req, _ := http.NewRequest(http.MethodPut, "http://"+public.Addr().String()+"/probe/x?k=v", strings.NewReader("hello"))
	req.Header.Set("X-Probe-Secret", "sig")
	req.Host = "host.tailnet.ts.net"
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusTeapot || string(body) != "PUT /probe/x?k=v host=host.tailnet.ts.net probe=sig body=hello" {
		t.Errorf("forwarded = %d %q", resp.StatusCode, body)
	}
	big, _ := http.NewRequest(http.MethodPost, "http://"+public.Addr().String()+"/probe", strings.NewReader(strings.Repeat("x", receiver.MaxBodyBytes+1)))
	resp, err = http.DefaultClient.Do(big)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Errorf("oversized through receiver = %d, want 413", resp.StatusCode)
	}

	_ = stdin.Close()
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		t.Fatal("receiver did not exit when its stdin closed")
	}
}

type ingressFixture struct {
	*fixture
	dir    string
	deny   *net.TCPListener
	cancel context.CancelFunc
	done   chan struct{}
}

func newIngressFixture(t *testing.T, executable, tailscaleExecutable, dir string, readyTimeout time.Duration) *ingressFixture {
	t.Helper()
	db, _ := openInbox(t)
	deny := listen(t)
	probe := filepath.Join(t.TempDir(), "auth-token")
	if err := os.WriteFile(probe, []byte("atc_secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := &probeHandler{failUntil: map[string]int{}}
	service, err := New(Options{
		Repository: db.Webhooks(),
		Routes:     []Route{{IntegrationID: "probe", Path: "/probe", Handler: handler}},
		Logger:     slog.New(slog.NewTextHandler(io.Discard, nil)),
		Ingress: &IngressOptions{
			Executable:          executable,
			TailscaleExecutable: tailscaleExecutable,
			PublicPort:          443,
			DenyPort:            port(deny),
			ProbePath:           probe,
			readyTimeout:        readyTimeout,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	service.poll = 10 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(10 * time.Second):
			t.Error("Run did not return after cancellation")
		}
	})
	return &ingressFixture{
		fixture: &fixture{service: service, handler: handler, inbox: db.Webhooks()},
		dir:     dir, deny: deny, cancel: cancel, done: done,
	}
}

func (f *ingressFixture) status() api.Webhooks { return f.service.Status(context.Background()) }

func (f *ingressFixture) waitState(t *testing.T, want api.WebhookState) api.Webhooks {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		status := f.status()
		if status.State == want {
			return status
		}
		if time.Now().After(deadline) {
			t.Fatalf("state = %+v, want %s", status, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// post delivers to the receiver's public listener, standing in for Funnel.
func (f *ingressFixture) post(t *testing.T, path, secret, id, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "http://"+f.service.ingress.targetAddr().String()+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if secret != "" {
		req.Header.Set("X-Probe-Secret", secret)
	}
	if id != "" {
		req.Header.Set("X-Probe-Delivery", id)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp
}

// The whole path: receiver proven, Funnel established against the
// receiver's port and nothing else, deliveries flowing end to end, the
// public surface limited to routes; a receiver failure withdraws exposure
// and recovers; shutdown leaves no receiver or exposure process behind.
func TestIngressExposesOnlyRoutesAndRecoversFromReceiverFailure(t *testing.T) {
	executable := requireBinary(t)
	tailscaleExecutable, dir := fakeTailscale(t)
	f := newIngressFixture(t, executable, tailscaleExecutable, dir, 0)

	status := f.status()
	deadline := time.Now().Add(30 * time.Second)
	for status.State != api.WebhooksReady && status.State != api.WebhooksUnavailable && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
		status = f.status()
	}
	if status.State == api.WebhooksUnavailable {
		if strings.Contains(strings.ToLower(status.Reason), "landlock") {
			t.Skipf("this kernel cannot run the receiver: %s", status.Reason)
		}
		t.Fatalf("ingress unavailable: %s", status.Reason)
	}
	if status.State != api.WebhooksReady || status.URL != "https://host.tailnet.ts.net" {
		t.Fatalf("status = %+v, want ready at the public URL", status)
	}
	args, err := os.ReadFile(filepath.Join(dir, "funnel.args"))
	if err != nil {
		t.Fatal(err)
	}
	targetPort := f.service.ingress.targetAddr().(*net.TCPAddr).Port
	if got := strings.TrimSpace(string(args)); got != fmt.Sprintf("funnel --https=443 localhost:%d", targetPort) {
		t.Errorf("funnel args = %q, want the receiver's port on 443", got)
	}

	if resp := f.post(t, "/probe", probeSecret, "evt-1", `{"n":1}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("valid delivery = %d, want 202", resp.StatusCode)
	}
	waitFor(t, "processing", func() bool { return f.handler.count() == 1 })
	if resp := f.post(t, "/probe", "wrong", "evt-2", `{}`); resp.StatusCode != http.StatusUnauthorized {
		t.Errorf("bad signature = %d, want 401", resp.StatusCode)
	}
	for _, path := range []string{"/v1/health", "/openapi.json", "/docs", "/internal/claude/hooks", "/v1/webhooks", "/"} {
		if resp := f.post(t, path, probeSecret, "evt-3", `{}`); resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s through the public endpoint = %d, want 404", path, resp.StatusCode)
		}
	}
	if pending, _ := f.inbox.Pending(context.Background()); pending != 0 || f.handler.count() != 1 {
		t.Errorf("rejected traffic reached the inbox: pending=%d processed=%d", pending, f.handler.count())
	}

	// Receiver failure: exposure is withdrawn (the funnel child dies) and
	// a fresh proven receiver brings it back.
	first := funnelPID(t, dir)
	receiverProcess := f.service.ingress.receiverProcess()
	if receiverProcess == nil {
		t.Fatal("no receiver process recorded while ready")
	}
	firstReceiver := receiverProcess.Pid
	if err := receiverProcess.Kill(); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "funnel withdrawn", func() bool { return !alive(first) })
	status = f.waitState(t, api.WebhooksReady)
	second := funnelPID(t, dir)
	if second == first {
		t.Error("ready again without a new funnel child")
	}
	if p := f.service.ingress.receiverProcess(); p == nil || p.Pid == firstReceiver {
		t.Error("ready again without a new receiver")
	}
	if resp := f.post(t, "/probe", probeSecret, "evt-4", `{}`); resp.StatusCode != http.StatusAccepted {
		t.Errorf("delivery after recovery = %d, want 202", resp.StatusCode)
	}

	// Shutdown: nothing outlives Run.
	secondReceiver := f.service.ingress.receiverProcess().Pid
	f.cancel()
	<-f.done
	if alive(second) || alive(secondReceiver) {
		t.Errorf("orphans after shutdown: funnel alive=%v receiver alive=%v", alive(second), alive(secondReceiver))
	}
}

// A receiver that cannot be isolated leaves intake unavailable with its
// reason, never exposes anything, and the rest keeps working: deliveries
// already accepted still process.
func TestIngressUnavailableWhenIsolationFails(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "atc")
	script := "#!/bin/sh\necho '{\"ok\":false,\"reason\":\"Landlock is not available: this kernel was built without it\",\"permanent\":true}'\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tailscaleExecutable, tsDir := fakeTailscale(t)
	f := newIngressFixture(t, fake, tailscaleExecutable, tsDir, 0)
	status := f.waitState(t, api.WebhooksUnavailable)
	if !strings.Contains(status.Reason, "Landlock is not available") {
		t.Errorf("reason = %q, want the receiver's explanation", status.Reason)
	}
	if _, err := os.Stat(filepath.Join(tsDir, "funnel.pid")); !errors.Is(err, os.ErrNotExist) {
		t.Error("funnel was started without an isolated receiver")
	}
	rec := f.deliver(http.MethodPost, "/probe", probeSecret, "evt-1", `{}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("channel delivery = %d, want 202 (processing keeps working)", rec.Code)
	}
	waitFor(t, "processing", func() bool { return f.handler.count() == 1 })
}

// A receiver that fails for a reason a retry might fix is retried with
// backoff, and status explains what happened meanwhile.
func TestIngressRetriesTransientReceiverFailure(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "atc")
	script := "#!/bin/sh\necho x >> \"" + dir + "/spawns\"\necho 'receiver boom' >&2\nexit 1\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tailscaleExecutable, tsDir := fakeTailscale(t)
	f := newIngressFixture(t, fake, tailscaleExecutable, tsDir, 0)
	waitFor(t, "a reported failure", func() bool {
		return strings.Contains(f.status().Reason, "receiver boom")
	})
	if state := f.status().State; state != api.WebhooksStarting {
		t.Errorf("state = %s, want starting while retrying", state)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		data, _ := os.ReadFile(filepath.Join(dir, "spawns"))
		if strings.Count(string(data), "x") >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("receiver was not retried")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// A receiver that never reports is killed after the readiness budget.
func TestIngressKillsSilentReceiver(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "atc")
	script := "#!/bin/sh\necho \"$$\" > \"" + dir + "/pid\"\nexec sleep 300\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tailscaleExecutable, tsDir := fakeTailscale(t)
	f := newIngressFixture(t, fake, tailscaleExecutable, tsDir, 300*time.Millisecond)
	waitFor(t, "readiness timeout reported", func() bool {
		return strings.Contains(f.status().Reason, "did not report")
	})
	data, err := os.ReadFile(filepath.Join(dir, "pid"))
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := strconv.Atoi(strings.TrimSpace(string(data)))
	waitFor(t, "silent receiver killed", func() bool { return !alive(pid) })
}
