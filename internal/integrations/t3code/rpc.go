package t3code

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"

	"github.com/coder/websocket"
)

// The transport is T3's Effect RPC over one WebSocket, spoken as far as
// the Integration needs: a Request, the streamed Chunks a subscription
// answers with (each Acked so the server keeps sending), and the Exit
// that ends a request — a Success value for a one-off command, a Failure
// carrying the method's typed error. One socket carries every request
// the Integration has in flight, the long-lived shell subscription and
// the commands it dispatches alike (ATC-289), each under its own request
// id. Every connection first fetches a one-use ticket over HTTP with the
// session bearer, then dials /ws with it.

const (
	shellMethod    = "orchestration.subscribeShell"
	dispatchMethod = "orchestration.dispatchCommand"
	maxFrameBytes  = 64 << 20
	// maxBodyBytes bounds every HTTP response body ATC reads from T3.
	maxBodyBytes = 1 << 20
)

// protocolError marks an exchange ATC cannot continue with — an envelope
// it does not understand, a failed subscription — permanent for this
// connection and reported like a schema failure.
type protocolError struct{ err error }

func (e *protocolError) Error() string { return "T3 Code protocol: " + e.err.Error() }
func (e *protocolError) Unwrap() error { return e.err }

// httpError is a non-2xx answer from T3, with the status the caller
// classifies (401 re-pairs, 403 is an auth failure, the rest retry).
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string {
	if e.body == "" {
		return fmt.Sprintf("HTTP %d", e.status)
	}
	return fmt.Sprintf("HTTP %d: %s", e.status, e.body)
}

// rpcFailure is a request T3 ended with a Failure exit: the typed error
// the method declares — the first Fail in the cause, by tag and message
// — or, without one, the raw cause. The subscription's scope refusal
// arrives this way, as does a rejected command.
type rpcFailure struct {
	tag     string
	message string
	// rolledBack reports T3 deleting the thread a rejected bootstrap had
	// created before failing.
	rolledBack bool
	cause      json.RawMessage
}

func (e *rpcFailure) Error() string {
	switch {
	case e.message != "" && e.tag != "":
		return e.tag + ": " + e.message
	case e.message != "":
		return e.message
	}
	return "failure: " + excerpt(e.cause)
}

// failureFrom reads a Failure exit's cause: Effect encodes it as a list
// of Fail (a typed error), Die (a defect), and Interrupt entries.
func failureFrom(cause json.RawMessage) *rpcFailure {
	failure := &rpcFailure{cause: cause}
	type entry struct {
		Tag    string          `json:"_tag"`
		Error  json.RawMessage `json:"error"`
		Defect json.RawMessage `json:"defect"`
	}
	var entries []entry
	if err := json.Unmarshal(cause, &entries); err != nil {
		var one entry
		if json.Unmarshal(cause, &one) != nil {
			return failure
		}
		entries = []entry{one}
	}
	for _, e := range entries {
		switch e.Tag {
		case "Fail":
			var typed struct {
				Tag         string `json:"_tag"`
				Message     string `json:"message"`
				Disposition string `json:"bootstrapThreadDisposition"`
			}
			if json.Unmarshal(e.Error, &typed) == nil {
				failure.tag, failure.message = typed.Tag, typed.Message
				failure.rolledBack = typed.Disposition == "deleted"
			}
			return failure
		case "Die":
			var message string
			if json.Unmarshal(e.Defect, &message) != nil {
				message = excerpt(e.Defect)
			}
			failure.message = "defect: " + message
			return failure
		}
	}
	return failure
}

func excerpt(data []byte) string {
	text := string(bytes.TrimSpace(data))
	if len(text) > 200 {
		text = text[:200] + "…"
	}
	return text
}

type rpcEnvelope struct {
	Tag       string          `json:"_tag"`
	RequestID json.RawMessage `json:"requestId"`
	Values    json.RawMessage `json:"values"`
	Exit      *struct {
		Tag   string          `json:"_tag"`
		Cause json.RawMessage `json:"cause"`
	} `json:"exit"`
	Defect json.RawMessage `json:"defect"`
}

// doJSON performs one request and decodes a JSON success body; a non-2xx
// answer is an *httpError carrying a bounded body excerpt.
func doJSON(client *http.Client, req *http.Request, out any) error {
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &httpError{status: resp.StatusCode, body: excerpt(body)}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(body, out)
}

// websocketTicket buys one ticket for a WebSocket dial with the session.
func websocketTicket(ctx context.Context, client *http.Client, origin, token string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, origin+"/api/auth/websocket-ticket", bytes.NewReader([]byte("{}")))
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	var ticket struct {
		Ticket string `json:"ticket"`
	}
	if err := doJSON(client, req, &ticket); err != nil {
		return "", err
	}
	if ticket.Ticket == "" {
		return "", &protocolError{err: errors.New("websocket-ticket answer omitted ticket")}
	}
	return ticket.Ticket, nil
}

func websocketURL(origin, ticket string) (string, error) {
	parsed, err := url.Parse(origin)
	if err != nil {
		return "", &protocolError{err: fmt.Errorf("origin %q: %w", origin, err)}
	}
	if parsed.Scheme == "https" {
		parsed.Scheme = "wss"
	} else {
		parsed.Scheme = "ws"
	}
	parsed.Path = "/ws"
	parsed.RawQuery = url.Values{"wsTicket": {ticket}}.Encode()
	return parsed.String(), nil
}

// rpcClient multiplexes requests over one socket: it allocates request
// ids, routes each reply to the request waiting for it, and treats the
// subscription as one request among many. Nothing is queued across
// connections: a socket drop fails every request in flight, and the
// caller reconnects. Private to the Integration.
type rpcClient struct {
	conn *websocket.Conn

	mu     sync.Mutex
	nextID uint64
	calls  map[string]*rpcCall
	// err is set once the read loop has ended; every request after that
	// fails with it immediately.
	err error

	closing   chan struct{}
	closeOnce sync.Once
	done      chan struct{}
}

// rpcCall is one request in flight: its chunks, for a subscription, and
// its exit — the reply that ends it, or the connection's failure —
// delivered exactly once.
type rpcCall struct {
	chunks chan []json.RawMessage
	exit   chan rpcExit
}

// rpcExit is a request's end: nil for success — no method ATC calls
// returns a value it uses — or the failure.
type rpcExit struct {
	err error
}

// dialRPC opens the socket and starts reading; the client is live until
// close, or until the socket drops.
func dialRPC(ctx context.Context, client *http.Client, socketURL string) (*rpcClient, error) {
	conn, resp, err := websocket.Dial(ctx, socketURL, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			return nil, &httpError{status: resp.StatusCode, body: "websocket dial refused"}
		}
		return nil, fmt.Errorf("dial: %w", err)
	}
	conn.SetReadLimit(maxFrameBytes)
	c := &rpcClient{conn: conn, calls: map[string]*rpcCall{}, closing: make(chan struct{}), done: make(chan struct{})}
	go c.read()
	return c, nil
}

// close ends the connection and waits for the read loop, so every
// request in flight has failed by the time it returns.
func (c *rpcClient) close() {
	c.closeOnce.Do(func() {
		close(c.closing)
		_ = c.conn.CloseNow()
	})
	<-c.done
}

// call performs one unary request and waits for its end: nil on success,
// an *rpcFailure for T3's typed refusal, the connection's error when the
// socket dropped first, ctx's error when the caller gave up. A caller
// giving up abandons the request rather than sending Effect's Interrupt:
// T3 runs a command it was asked for to completion, whereas an interrupt
// mid-bootstrap leaves whatever it had created in place.
func (c *rpcClient) call(ctx context.Context, method string, payload any) error {
	call, id, err := c.request(ctx, method, payload, false)
	if err != nil {
		return err
	}
	select {
	case exit := <-call.exit:
		return exit.err
	case <-ctx.Done():
		c.abandon(id)
		return ctx.Err()
	}
}

// subscribe runs one streaming request to completion: every item goes
// to handle in order, each chunk is acknowledged after its items are
// handled, and the call returns when the server ends the stream (an
// error to retry, an *rpcFailure for a refused subscription), the socket
// drops, the handler refuses an item (its error, unwrapped), or ctx ends
// (nil).
func (c *rpcClient) subscribe(ctx context.Context, method string, payload any, handle func(json.RawMessage) error) error {
	call, id, err := c.request(ctx, method, payload, true)
	if err != nil {
		return err
	}
	defer c.abandon(id)
	handleAll := func(items []json.RawMessage) error {
		for _, item := range items {
			if err := handle(item); err != nil {
				return err
			}
		}
		return nil
	}
	for {
		select {
		case items := <-call.chunks:
			if err := handleAll(items); err != nil {
				return err
			}
			if err := c.write(ctx, map[string]any{"_tag": "Ack", "requestId": id}); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
		case exit := <-call.exit:
			// The chunks read before the exit are already buffered; apply
			// them before reporting the end.
			for {
				select {
				case items := <-call.chunks:
					if err := handleAll(items); err != nil {
						return err
					}
					continue
				default:
				}
				break
			}
			if exit.err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return exit.err
			}
			return errors.New("subscription ended")
		case <-ctx.Done():
			return nil
		}
	}
}

// request registers a call and sends its Request frame.
func (c *rpcClient) request(ctx context.Context, method string, payload any, streaming bool) (*rpcCall, string, error) {
	c.mu.Lock()
	if c.err != nil {
		c.mu.Unlock()
		return nil, "", c.err
	}
	c.nextID++
	id := strconv.FormatUint(c.nextID, 10)
	call := &rpcCall{exit: make(chan rpcExit, 1)}
	if streaming {
		call.chunks = make(chan []json.RawMessage, 4)
	}
	c.calls[id] = call
	c.mu.Unlock()
	if err := c.write(ctx, map[string]any{
		"_tag": "Request", "id": id, "tag": method, "payload": payload, "headers": []any{},
	}); err != nil {
		c.abandon(id)
		return nil, "", err
	}
	return call, id, nil
}

// abandon forgets a request: later replies for it are dropped.
func (c *rpcClient) abandon(id string) {
	c.mu.Lock()
	delete(c.calls, id)
	c.mu.Unlock()
}

func (c *rpcClient) write(ctx context.Context, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := c.conn.Write(ctx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write: %w", err)
	}
	return nil
}

// read is the read loop: it routes every envelope to its request and,
// when the socket ends, fails every request still waiting.
func (c *rpcClient) read() {
	err := c.readFrames()
	c.mu.Lock()
	c.err = err
	calls := c.calls
	c.calls = map[string]*rpcCall{}
	c.mu.Unlock()
	for _, call := range calls {
		call.exit <- rpcExit{err: err}
	}
	close(c.done)
}

func (c *rpcClient) readFrames() error {
	for {
		_, data, err := c.conn.Read(context.Background())
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		envelopes, err := decodeEnvelopes(data)
		if err != nil {
			return err
		}
		for _, envelope := range envelopes {
			if err := c.route(envelope); err != nil {
				return err
			}
		}
	}
}

// route delivers one envelope to its request. A reply for a request
// nobody waits on any more is dropped.
func (c *rpcClient) route(envelope rpcEnvelope) error {
	switch envelope.Tag {
	case "Pong":
		return nil
	case "Defect":
		return &protocolError{err: fmt.Errorf("defect: %s", envelope.Defect)}
	case "Chunk", "Exit":
	default:
		return &protocolError{err: fmt.Errorf("unknown envelope %q", envelope.Tag)}
	}
	id, err := requestID(envelope.RequestID)
	if err != nil {
		return err
	}
	c.mu.Lock()
	call := c.calls[id]
	if envelope.Tag == "Exit" {
		delete(c.calls, id)
	}
	c.mu.Unlock()
	if call == nil {
		return nil
	}
	if envelope.Tag == "Chunk" {
		if call.chunks == nil {
			return &protocolError{err: errors.New("chunk for a request that does not stream")}
		}
		var items []json.RawMessage
		if err := json.Unmarshal(envelope.Values, &items); err != nil {
			return &protocolError{err: fmt.Errorf("chunk: %w", err)}
		}
		select {
		case call.chunks <- items:
		case <-c.closing:
			return errors.New("closed")
		}
		return nil
	}
	// An exit ATC cannot read still ends its request — the waiter must not
	// hang on a reply that came — and then ends the connection.
	exit, err := exitOf(envelope)
	call.exit <- exit
	return err
}

func exitOf(envelope rpcEnvelope) (rpcExit, error) {
	switch {
	case envelope.Exit == nil:
		err := &protocolError{err: errors.New("exit omitted its exit")}
		return rpcExit{err: err}, err
	case envelope.Exit.Tag == "Success":
		return rpcExit{}, nil
	case envelope.Exit.Tag == "Failure":
		return rpcExit{err: failureFrom(envelope.Exit.Cause)}, nil
	default:
		err := &protocolError{err: fmt.Errorf("unknown exit %q", envelope.Exit.Tag)}
		return rpcExit{err: err}, err
	}
}

// requestID reads the id T3 echoes, a string or a number.
func requestID(raw json.RawMessage) (string, error) {
	var id string
	if err := json.Unmarshal(raw, &id); err == nil {
		return id, nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err == nil {
		return number.String(), nil
	}
	return "", &protocolError{err: errors.New("envelope without a request id")}
}

// decodeEnvelopes reads one frame: a single envelope or a batch.
func decodeEnvelopes(data []byte) ([]rpcEnvelope, error) {
	if len(data) > 0 && data[0] == '[' {
		var envelopes []rpcEnvelope
		if err := json.Unmarshal(data, &envelopes); err != nil {
			return nil, &protocolError{err: fmt.Errorf("envelope batch: %w", err)}
		}
		return envelopes, nil
	}
	var envelope rpcEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, &protocolError{err: fmt.Errorf("envelope: %w", err)}
	}
	return []rpcEnvelope{envelope}, nil
}
