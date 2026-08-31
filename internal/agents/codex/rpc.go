package codex

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

// maxMessageBytes caps one server message (thread/read with turns can be
// large; ATC never asks for turns, but the fan-out is not ATC's to size).
const maxMessageBytes = 64 << 20

// notificationBuffer bounds the dispatch queue between the read loop and
// the handler goroutine.
const notificationBuffer = 256

// rpcConn is JSON-RPC over one WebSocket to the shared app-server:
// numbered requests matched to replies, notifications handed to one
// dispatcher goroutine in arrival order, server requests refused, and a
// close callback for teardown.
//
// Every inbound frame gets a sequence number from a counter shared across
// connections. A reply and a notification about the same thread can
// cross: the notification that was on the wire before a reply is older
// than the snapshot the reply carries, and the observer uses the numbers
// to apply evidence in wire order rather than arrival-of-processing order.
type rpcConn struct {
	ws     *websocket.Conn
	logger *slog.Logger
	seq    *atomic.Uint64
	now    func() time.Time

	// onAnnouncement runs on the read loop itself, at receipt: a launch
	// window is decided by when its announcement arrived, and the
	// dispatcher may be busy for seconds. It must not block.
	onAnnouncement func(n notification)
	onNotification func(n notification)
	onClose        func(reason error)

	// notifications decouples dispatch from the read loop: a handler may
	// issue RPCs (thread/read) whose replies the read loop must stay free
	// to receive. Overflow fails the connection — the observer re-derives
	// on reconnect rather than silently losing transitions. done ends
	// both loops; the channel itself is never closed.
	notifications chan notification
	done          chan struct{}
	// dispatcherDone closes when the dispatch loop has exited; fail joins
	// it before onClose, so no handler applies evidence after teardown.
	dispatcherDone chan struct{}
	// finished closes once onClose has run: the connection is fully
	// retired and a successor may be dialed.
	finished chan struct{}

	writeMu sync.Mutex

	mu      sync.Mutex
	nextID  int64
	pending map[int64]chan rpcReply
	closed  bool
}

type notification struct {
	method string
	params json.RawMessage
	// seq and at are the frame's wire sequence and receipt time.
	seq uint64
	at  time.Time
}

type rpcReply struct {
	result json.RawMessage
	seq    uint64
	err    error
}

type rpcMessage struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *rpcError       `json:"error,omitempty"`
}

// rpcError is a JSON-RPC error object; as a Go error it marks a reply
// the server produced, as opposed to a transport failure.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("rpc %d: %s", e.Code, e.Message) }

// errConnClosed reports a call on a connection that is gone.
var errConnClosed = errors.New("codex app-server connection closed")

// dialSocket opens the WebSocket over the unix control socket.
func dialSocket(ctx context.Context, socketPath string) (*websocket.Conn, error) {
	client := &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socketPath)
		},
	}}
	ws, _, err := websocket.Dial(ctx, "ws://localhost/", &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return nil, err
	}
	ws.SetReadLimit(maxMessageBytes)
	return ws, nil
}

func newRPCConn(ws *websocket.Conn, seq *atomic.Uint64, now func() time.Time, logger *slog.Logger) *rpcConn {
	return &rpcConn{
		ws: ws, logger: logger, seq: seq, now: now, nextID: 1,
		pending:        map[int64]chan rpcReply{},
		notifications:  make(chan notification, notificationBuffer),
		done:           make(chan struct{}),
		dispatcherDone: make(chan struct{}),
		finished:       make(chan struct{}),
	}
}

// isClosed reports whether the connection has failed (its retirement
// may still be in progress; finished says when it is complete).
func (c *rpcConn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

func (c *rpcConn) start() {
	go c.readLoop()
	go c.dispatchLoop()
}

func (c *rpcConn) dispatchLoop() {
	defer close(c.dispatcherDone)
	for {
		select {
		case <-c.done:
			return
		case n := <-c.notifications:
			if c.onNotification != nil {
				c.onNotification(n)
			}
		}
	}
}

func (c *rpcConn) readLoop() {
	ctx := context.Background()
	for {
		_, data, err := c.ws.Read(ctx)
		if err != nil {
			c.fail(err)
			return
		}
		seq := c.seq.Add(1)
		var message rpcMessage
		if err := json.Unmarshal(data, &message); err != nil {
			c.fail(fmt.Errorf("invalid frame from codex app-server: %w", err))
			return
		}
		switch {
		case len(message.ID) > 0 && message.Method != "":
			// A server-to-client request. ATC is a passive observer and
			// serves none of them: method not found, per the contract.
			c.logger.Debug("codex app-server request refused", "method", message.Method)
			_ = c.write(ctx, rpcMessage{ID: message.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
		case len(message.ID) > 0:
			var id int64
			if err := json.Unmarshal(message.ID, &id); err != nil {
				continue
			}
			c.mu.Lock()
			waiter, ok := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ok {
				reply := rpcReply{result: message.Result, seq: seq}
				if message.Error != nil {
					reply.err = message.Error
				}
				waiter <- reply
			}
		case message.Method != "":
			n := notification{method: message.Method, params: message.Params, seq: seq, at: c.now()}
			if message.Method == "thread/started" && c.onAnnouncement != nil {
				c.onAnnouncement(n)
				continue
			}
			select {
			case c.notifications <- n:
			case <-c.done:
				return
			default:
				c.fail(errors.New("codex app-server notifications overflowed the dispatch queue"))
				return
			}
		}
	}
}

// fail ends the connection: every pending call errors and onClose fires
// exactly once, after the dispatcher has drained.
func (c *rpcConn) fail(reason error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	pending := c.pending
	c.pending = map[int64]chan rpcReply{}
	c.mu.Unlock()
	for _, waiter := range pending {
		waiter <- rpcReply{err: reason}
	}
	close(c.done)
	_ = c.ws.CloseNow()
	// An in-flight handler's calls fail fast (pending was just flushed),
	// so the join is bounded, and it guarantees no evidence lands after
	// teardown coerced everything to unknown.
	<-c.dispatcherDone
	if c.onClose != nil {
		c.onClose(reason)
	}
	close(c.finished)
}

func (c *rpcConn) close() {
	c.fail(errConnClosed)
}

// call sends one request and waits for its reply, returning the reply's
// wire sequence with the result. The caller's context is the deadline;
// every caller bounds it.
func (c *rpcConn) call(ctx context.Context, method string, params any) (json.RawMessage, uint64, error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return nil, 0, errConnClosed
	}
	id := c.nextID
	c.nextID++
	waiter := make(chan rpcReply, 1)
	c.pending[id] = waiter
	c.mu.Unlock()

	rawID, _ := json.Marshal(id)
	if err := c.write(ctx, rpcMessage{ID: rawID, Method: method, Params: mustMarshal(params)}); err != nil {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, 0, err
	}
	select {
	case reply := <-waiter:
		return reply.result, reply.seq, reply.err
	case <-ctx.Done():
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return nil, 0, ctx.Err()
	}
}

// notify sends one notification; the caller's context bounds the write.
func (c *rpcConn) notify(ctx context.Context, method string, params any) error {
	return c.write(ctx, rpcMessage{Method: method, Params: mustMarshal(params)})
}

func (c *rpcConn) write(ctx context.Context, message rpcMessage) error {
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.ws.Write(ctx, websocket.MessageText, data)
}

func mustMarshal(params any) json.RawMessage {
	data, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	return data
}
