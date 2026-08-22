// Package acp contains the complete ACP v1 wire surface for the unified-core
// experiment. The core sees only ports.ChatAdapter and ATC-owned events.
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

const protocolVersion = 1

type message struct {
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

type incomingRequest struct {
	ID     json.RawMessage
	Method string
	Params json.RawMessage
}

type handler interface {
	request(incomingRequest)
	notification(string, json.RawMessage)
	raw(string, json.RawMessage)
	disconnected(error)
}

type response struct {
	result json.RawMessage
	err    error
}

type pendingCall struct {
	connection *connection
	method     string
	key        string
	response   <-chan response
}

type connection struct {
	reader  io.Reader
	writer  io.WriteCloser
	handler handler

	writeMu sync.Mutex
	mu      sync.Mutex
	nextID  int64
	pending map[string]chan response
	done    chan struct{}
	once    sync.Once
}

func newConnection(reader io.Reader, writer io.WriteCloser, handler handler) *connection {
	return &connection{reader: reader, writer: writer, handler: handler, pending: make(map[string]chan response), done: make(chan struct{})}
}

func (c *connection) start() { go c.readLoop() }

func (c *connection) call(ctx context.Context, method string, params, result any) error {
	pending, err := c.begin(method, params)
	if err != nil {
		return err
	}
	return pending.await(ctx, result)
}

func (c *connection) begin(method string, params any) (*pendingCall, error) {
	c.mu.Lock()
	c.nextID++
	id := c.nextID
	key := fmt.Sprintf("n:%d", id)
	responseChannel := make(chan response, 1)
	c.pending[key] = responseChannel
	c.mu.Unlock()
	if err := c.send(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params}); err != nil {
		c.remove(key)
		return nil, err
	}
	return &pendingCall{connection: c, method: method, key: key, response: responseChannel}, nil
}

func (p *pendingCall) await(ctx context.Context, result any) error {
	select {
	case received := <-p.response:
		if received.err != nil {
			return received.err
		}
		if result == nil || len(received.result) == 0 {
			return nil
		}
		if err := json.Unmarshal(received.result, result); err != nil {
			return fmt.Errorf("decode %s result: %w", p.method, err)
		}
		return nil
	case <-ctx.Done():
		p.connection.remove(p.key)
		return ctx.Err()
	case <-p.connection.done:
		return errors.New("ACP connection closed")
	}
}

func (c *connection) notify(method string, params any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (c *connection) respond(id json.RawMessage, result any) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
}

func (c *connection) respondError(id json.RawMessage, code int, text string) error {
	return c.send(map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": text}})
}

func (c *connection) close() error {
	err := c.writer.Close()
	c.finish(errors.New("ACP connection closed"))
	return err
}

func (c *connection) send(value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	c.handler.raw("outbound", cloneRaw(encoded))
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	if _, err := c.writer.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("write ACP frame: %w", err)
	}
	return nil
}

func (c *connection) readLoop() {
	scanner := bufio.NewScanner(c.reader)
	scanner.Buffer(make([]byte, 64*1024), 16<<20)
	for scanner.Scan() {
		frame := cloneRaw(scanner.Bytes())
		c.handler.raw("inbound", frame)
		if len(frame) > 0 && frame[0] == '[' {
			var batch []json.RawMessage
			if json.Unmarshal(frame, &batch) != nil {
				continue
			}
			for _, item := range batch {
				c.handle(item)
			}
			continue
		}
		c.handle(frame)
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	c.finish(err)
}

func (c *connection) handle(frame json.RawMessage) {
	var decoded message
	if json.Unmarshal(frame, &decoded) != nil {
		return
	}
	if decoded.Method != "" {
		if len(decoded.ID) > 0 && string(decoded.ID) != "null" {
			c.handler.request(incomingRequest{ID: cloneRaw(decoded.ID), Method: decoded.Method, Params: cloneRaw(decoded.Params)})
			return
		}
		c.handler.notification(decoded.Method, cloneRaw(decoded.Params))
		return
	}
	if len(decoded.ID) == 0 {
		return
	}
	key := idKey(decoded.ID)
	c.mu.Lock()
	responseChannel := c.pending[key]
	delete(c.pending, key)
	c.mu.Unlock()
	if responseChannel == nil {
		return
	}
	if decoded.Error != nil {
		responseChannel <- response{err: fmt.Errorf("ACP error %d: %s", decoded.Error.Code, decoded.Error.Message)}
		return
	}
	responseChannel <- response{result: cloneRaw(decoded.Result)}
}

func (c *connection) remove(key string) {
	c.mu.Lock()
	delete(c.pending, key)
	c.mu.Unlock()
}

func (c *connection) finish(err error) {
	c.once.Do(func() {
		close(c.done)
		c.mu.Lock()
		pending := c.pending
		c.pending = make(map[string]chan response)
		c.mu.Unlock()
		for _, channel := range pending {
			channel <- response{err: err}
		}
		c.handler.disconnected(err)
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

func cloneRaw(value []byte) json.RawMessage { return append(json.RawMessage(nil), value...) }
