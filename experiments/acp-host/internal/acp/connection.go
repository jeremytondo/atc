package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
)

const maxFrameBytes = 16 << 20

type wireMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *wireError      `json:"error,omitempty"`
}

type wireError struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

type response struct {
	result json.RawMessage
	err    error
}

type PendingCall struct {
	connection *Connection
	method     string
	key        string
	responseCh <-chan response
}

type RPCError struct {
	Code    int
	Message string
	Data    json.RawMessage
}

func (e *RPCError) Error() string {
	if len(e.Data) == 0 {
		return fmt.Sprintf("ACP error %d: %s", e.Code, e.Message)
	}
	return fmt.Sprintf("ACP error %d: %s (%s)", e.Code, e.Message, e.Data)
}

type Connection struct {
	reader  io.Reader
	writer  io.WriteCloser
	handler Handler

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[string]chan response
	done    chan struct{}
	once    sync.Once
}

func NewConnection(reader io.Reader, writer io.WriteCloser, handler Handler) *Connection {
	return &Connection{
		reader:  reader,
		writer:  writer,
		handler: handler,
		pending: make(map[string]chan response),
		done:    make(chan struct{}),
	}
}

func (c *Connection) Start() {
	go c.readLoop()
}

func (c *Connection) Done() <-chan struct{} {
	return c.done
}

func (c *Connection) Call(ctx context.Context, method string, params any, result any) error {
	pending, err := c.BeginCall(method, params)
	if err != nil {
		return err
	}
	return pending.Await(ctx, result)
}

// BeginCall sends a request before returning, allowing callers to retain a
// pending request while they continue handling notifications or cancellation.
func (c *Connection) BeginCall(method string, params any) (*PendingCall, error) {
	id, key, responseCh := c.addPending()
	request := map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"method":  method,
		"params":  params,
	}
	if err := c.send(request); err != nil {
		c.removePending(key)
		return nil, err
	}
	return &PendingCall{connection: c, method: method, key: key, responseCh: responseCh}, nil
}

func (p *PendingCall) Await(ctx context.Context, result any) error {
	select {
	case received := <-p.responseCh:
		if received.err != nil {
			return received.err
		}
		if result == nil || len(received.result) == 0 {
			return nil
		}
		if err := json.Unmarshal(received.result, result); err != nil {
			return fmt.Errorf("decode %s response: %w", p.method, err)
		}
		return nil
	case <-ctx.Done():
		p.connection.removePending(p.key)
		return ctx.Err()
	case <-p.connection.done:
		return errors.New("ACP connection closed")
	}
}

func (c *Connection) Notify(method string, params any) error {
	return c.send(map[string]any{
		"jsonrpc": "2.0",
		"method":  method,
		"params":  params,
	})
}

func (c *Connection) Respond(id json.RawMessage, result any) error {
	return c.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func (c *Connection) RespondError(id json.RawMessage, code int, message string) error {
	return c.send(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error": map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func (c *Connection) Close() error {
	err := c.writer.Close()
	c.finish(errors.New("ACP connection closed"))
	return err
}

func (c *Connection) addPending() (int64, string, chan response) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextID++
	id := c.nextID
	key := fmt.Sprintf("n:%d", id)
	responseCh := make(chan response, 1)
	c.pending[key] = responseCh
	return id, key, responseCh
}

func (c *Connection) removePending(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *Connection) send(message any) error {
	encoded, err := json.Marshal(message)
	if err != nil {
		return err
	}
	c.handler.HandleRaw(RawTraffic{Direction: "outbound", Message: cloneRaw(encoded)})

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write ACP message: %w", err)
	}
	return nil
}

func (c *Connection) readLoop() {
	scanner := bufio.NewScanner(c.reader)
	scanner.Buffer(make([]byte, 64*1024), maxFrameBytes)
	for scanner.Scan() {
		frame := cloneRaw(scanner.Bytes())
		c.handler.HandleRaw(RawTraffic{Direction: "inbound", Message: frame})
		if len(frame) > 0 && frame[0] == '[' {
			c.handleBatch(frame)
			continue
		}
		c.handleFrame(frame)
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.finish(err)
}

func (c *Connection) handleBatch(frame json.RawMessage) {
	var messages []json.RawMessage
	if err := json.Unmarshal(frame, &messages); err != nil {
		c.handler.HandleDisconnect(fmt.Errorf("decode ACP batch: %w", err))
		return
	}
	for _, message := range messages {
		c.handleFrame(message)
	}
}

func (c *Connection) handleFrame(frame json.RawMessage) {
	var message wireMessage
	if err := json.Unmarshal(frame, &message); err != nil {
		c.handler.HandleDisconnect(fmt.Errorf("decode ACP frame: %w", err))
		return
	}
	if message.Method != "" {
		if len(message.ID) > 0 && string(message.ID) != "null" {
			c.handler.HandleRequest(IncomingRequest{ID: cloneRaw(message.ID), Method: message.Method, Params: cloneRaw(message.Params)})
			return
		}
		c.handler.HandleNotification(Notification{Method: message.Method, Params: cloneRaw(message.Params)})
		return
	}
	if len(message.ID) == 0 {
		return
	}
	key := idKey(message.ID)
	c.mu.Lock()
	responseCh := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if responseCh == nil {
		return
	}
	if message.Error != nil {
		responseCh <- response{err: &RPCError{Code: message.Error.Code, Message: message.Error.Message, Data: message.Error.Data}}
		return
	}
	responseCh <- response{result: cloneRaw(message.Result)}
}

func (c *Connection) finish(err error) {
	c.once.Do(func() {
		close(c.done)
		c.mu.Lock()
		pending := c.pending
		c.pending = make(map[string]chan response)
		c.mu.Unlock()
		for _, responseCh := range pending {
			responseCh <- response{err: err}
		}
		c.handler.HandleDisconnect(err)
	})
}

func idKey(id json.RawMessage) string {
	if len(id) > 0 && id[0] == '"' {
		var value string
		if json.Unmarshal(id, &value) == nil {
			return "s:" + value
		}
	}
	return "n:" + string(id)
}

func cloneRaw(value []byte) json.RawMessage {
	return append(json.RawMessage(nil), value...)
}
