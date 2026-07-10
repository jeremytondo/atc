package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/jeremytondo/atc/internal/zmx"
)

// pipePTY is a fake zmx.PTY backing Read/Write with an in-memory pipe pair and
// recording resize calls, so the attach bridge can be exercised without a real
// pseudo-terminal or zmx process.
type pipePTY struct {
	toClient   *io.PipeReader // bridge reads agent output from here
	toClientIn *io.PipeWriter // test writes "agent output" here
	fromClient *io.PipeWriter // bridge writes keystrokes here
	fromCliOut *io.PipeReader // test reads keystrokes here

	mu        sync.Mutex
	lastRows  uint16
	lastCols  uint16
	closed    bool
	closeOnce sync.Once
}

func newPipePTY() *pipePTY {
	outR, outW := io.Pipe()
	inR, inW := io.Pipe()
	return &pipePTY{toClient: outR, toClientIn: outW, fromClient: inW, fromCliOut: inR}
}

func (p *pipePTY) Read(b []byte) (int, error)  { return p.toClient.Read(b) }
func (p *pipePTY) Write(b []byte) (int, error) { return p.fromClient.Write(b) }

func (p *pipePTY) Resize(rows, cols uint16) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.lastRows, p.lastCols = rows, cols
	return nil
}

func (p *pipePTY) Close() error {
	p.closeOnce.Do(func() {
		p.mu.Lock()
		p.closed = true
		p.mu.Unlock()
		p.toClient.Close()
		p.fromClient.Close()
	})
	return nil
}

func (p *pipePTY) size() (uint16, uint16) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.lastRows, p.lastCols
}

func (p *pipePTY) isClosed() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.closed
}

// dialAttach starts the API over httptest, opens an attach WebSocket to id, and
// sends the initial resize frame expected from real clients.
func dialAttach(t *testing.T, mux *fakeMux, id string) (*websocket.Conn, *httptest.Server) {
	t.Helper()
	conn, srv := dialAttachRaw(t, mux, id)
	writeResize(t, conn, 80, 24)
	return conn, srv
}

func dialAttachRaw(t *testing.T, mux *fakeMux, id string) (*websocket.Conn, *httptest.Server) {
	t.Helper()
	h, st := newHandler(t, mux)
	seedRunning(t, st, id, "Attach")
	mux.sessions = append(mux.sessions, liveSession(id))
	srv := httptest.NewServer(h)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/sessions/" + id + "/attach"
	conn, _, err := websocket.Dial(context.Background(), url, nil)
	if err != nil {
		srv.Close()
		t.Fatalf("dial: %v", err)
	}
	return conn, srv
}

func writeResize(t *testing.T, conn *websocket.Conn, cols, rows uint16) {
	t.Helper()
	msg := resizeMessage{Type: "resize", Cols: cols, Rows: rows}
	data, err := json.Marshal(msg)
	if err != nil {
		t.Fatalf("marshal resize: %v", err)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, data); err != nil {
		t.Fatalf("write resize: %v", err)
	}
}

func TestAttachStreamsPTYOutputToClient(t *testing.T) {
	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttach(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.Close(websocket.StatusNormalClosure, "")

	go func() { _, _ = pty.toClientIn.Write([]byte("hello from agent")) }()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("type = %v, want binary", typ)
	}
	if string(data) != "hello from agent" {
		t.Fatalf("data = %q", data)
	}
}

func TestAttachForwardsKeystrokesToPTY(t *testing.T) {
	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttach(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("ls\r")); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 8)
	n, err := pty.fromCliOut.Read(buf)
	if err != nil {
		t.Fatalf("pty read: %v", err)
	}
	if string(buf[:n]) != "ls\r" {
		t.Fatalf("keystrokes = %q", buf[:n])
	}
}

func TestAttachUsesInitialResizeForSpawn(t *testing.T) {
	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttachRaw(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.Close(websocket.StatusNormalClosure, "")

	writeResize(t, conn, 132, 43)

	deadline := time.Now().Add(2 * time.Second)
	for {
		if mux.attachCalls == 1 && mux.attachRows == 43 && mux.attachCols == 132 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("attach = calls:%d size:%dx%d, want calls:1 size:43x132", mux.attachCalls, mux.attachRows, mux.attachCols)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAttachBuffersKeystrokesBeforeInitialResize(t *testing.T) {
	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttachRaw(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("pre-resize\r")); err != nil {
		t.Fatalf("write keystrokes: %v", err)
	}
	writeResize(t, conn, 100, 30)

	buf := make([]byte, 32)
	n, err := pty.fromCliOut.Read(buf)
	if err != nil {
		t.Fatalf("pty read: %v", err)
	}
	if string(buf[:n]) != "pre-resize\r" {
		t.Fatalf("buffered keystrokes = %q", buf[:n])
	}
	if mux.attachRows != 30 || mux.attachCols != 100 {
		t.Fatalf("attach size = %dx%d, want 30x100", mux.attachRows, mux.attachCols)
	}
}

func TestAttachDropsPreResizeInputOverAggregateCap(t *testing.T) {
	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttachRaw(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.Close(websocket.StatusNormalClosure, "")

	// A single frame larger than the aggregate cap is dropped entirely;
	// later input that fits is still buffered and delivered in order.
	oversized := make([]byte, attachInitialInputLimit+1)
	if err := conn.Write(context.Background(), websocket.MessageBinary, oversized); err != nil {
		t.Fatalf("write oversized: %v", err)
	}
	if err := conn.Write(context.Background(), websocket.MessageBinary, []byte("kept\r")); err != nil {
		t.Fatalf("write kept: %v", err)
	}
	writeResize(t, conn, 100, 30)

	buf := make([]byte, 32)
	n, err := pty.fromCliOut.Read(buf)
	if err != nil {
		t.Fatalf("pty read: %v", err)
	}
	if string(buf[:n]) != "kept\r" {
		t.Fatalf("delivered = %q, want only the frame under the cap", buf[:n])
	}
}

func TestAttachFallsBackWhenInitialResizeIsMissing(t *testing.T) {
	oldTimeout := attachInitialResizeTimeout
	attachInitialResizeTimeout = 25 * time.Millisecond
	t.Cleanup(func() { attachInitialResizeTimeout = oldTimeout })

	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttachRaw(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(2 * time.Second)
	for {
		if mux.attachCalls == 1 {
			if mux.attachRows != zmx.DefaultAttachRows || mux.attachCols != zmx.DefaultAttachCols {
				t.Fatalf("attach size = %dx%d, want %dx%d", mux.attachRows, mux.attachCols, zmx.DefaultAttachRows, zmx.DefaultAttachCols)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("attach did not spawn after initial resize timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAttachResizeControlMessage(t *testing.T) {
	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttach(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.Close(websocket.StatusNormalClosure, "")

	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"resize","cols":120,"rows":40}`)); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Poll briefly: the resize is applied asynchronously on the server.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if rows, cols := pty.size(); rows == 40 && cols == 120 {
			return
		}
		if time.Now().After(deadline) {
			rows, cols := pty.size()
			t.Fatalf("size = %dx%d, want 40x120", rows, cols)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAttachClosesPTYWhenClientDisconnects(t *testing.T) {
	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttach(t, mux, "ses_attach")
	defer srv.Close()

	conn.Close(websocket.StatusNormalClosure, "")

	deadline := time.Now().Add(2 * time.Second)
	for {
		if pty.isClosed() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("pty was not closed after client disconnect")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestAttachClosesWithSessionEndedWhenPTYEnds(t *testing.T) {
	pty := newPipePTY()
	mux := &fakeMux{attachPTY: pty}
	conn, srv := dialAttach(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.CloseNow()

	_ = pty.toClientIn.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	assertWebSocketClose(t, err, websocket.StatusNormalClosure, "session_ended")
}

func TestAttachClosesWithInternalErrorWhenSpawnFails(t *testing.T) {
	mux := &fakeMux{attachErr: errors.New("spawn failed")}
	conn, srv := dialAttach(t, mux, "ses_attach")
	defer srv.Close()
	defer conn.CloseNow()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _, err := conn.Read(ctx)
	assertWebSocketClose(t, err, websocket.StatusInternalError, "internal_error")
}

func TestAttachMissingNameIs400(t *testing.T) {
	h, _ := newHandler(t, &fakeMux{})
	rec := do(t, h, http.MethodGet, "/sessions/attach", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

func TestAttachUnknownSessionIs404(t *testing.T) {
	// No sessions registered, so requireExists fails before any upgrade.
	h, _ := newHandler(t, &fakeMux{})
	rec := do(t, h, http.MethodGet, "/sessions/ses_gone/attach", "")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404 (%s)", rec.Code, rec.Body)
	}
}

// TestAttachDoesNotSpawnWithoutHandshake guards the ordering fix: a non-WebSocket
// GET to an existing session must fail the upgrade without ever spawning a zmx
// attach client.
func TestAttachDoesNotSpawnWithoutHandshake(t *testing.T) {
	mux := &fakeMux{attachPTY: newPipePTY()}
	h, st := newHandler(t, mux)
	seedRunning(t, st, "ses_attach", "Attach")
	mux.sessions = []zmx.Session{liveSession("ses_attach")}
	rec := do(t, h, http.MethodGet, "/sessions/ses_attach/attach", "")
	if rec.Code == http.StatusSwitchingProtocols {
		t.Fatalf("plain GET should not upgrade, got %d", rec.Code)
	}
	if mux.attachCalls != 0 {
		t.Fatalf("attach spawned %d times without a handshake, want 0", mux.attachCalls)
	}
}

func assertWebSocketClose(t *testing.T, err error, code websocket.StatusCode, reason string) {
	t.Helper()
	var closeErr websocket.CloseError
	if !errors.As(err, &closeErr) {
		t.Fatalf("err = %T %v, want websocket close", err, err)
	}
	if closeErr.Code != code || closeErr.Reason != reason {
		t.Fatalf("close = %d/%q, want %d/%q", closeErr.Code, closeErr.Reason, code, reason)
	}
}
