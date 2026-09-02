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

	"github.com/coder/websocket"
)

// The transport is T3's Effect RPC over one WebSocket, spoken only as far
// as one streaming subscription needs: a Request, the streamed Chunks it
// answers with (each Acked so the server keeps sending), and the Exit
// that ends it. Every connection first fetches a one-use ticket over
// HTTP with the session bearer, then dials /ws with it.

const (
	shellMethod   = "orchestration.subscribeShell"
	maxFrameBytes = 64 << 20
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
// classifies (401 re-pairs, 403 is an auth failure, the rest retry). The
// subscription's own scope refusal, which arrives inside the stream, is
// reported as a 403 too.
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
		excerpt := string(bytes.TrimSpace(body))
		if len(excerpt) > 200 {
			excerpt = excerpt[:200] + "…"
		}
		return &httpError{status: resp.StatusCode, body: excerpt}
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

// subscribeShell runs one shell subscription to completion: every stream
// item goes to handle in order, and the call returns when the socket
// drops (an error to retry), the server ends the stream, the handler
// refuses an item (its error, unwrapped), or ctx ends (nil).
func subscribeShell(ctx context.Context, client *http.Client, socketURL string, afterSequence *uint64, handle func(json.RawMessage) error) error {
	conn, resp, err := websocket.Dial(ctx, socketURL, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		if resp != nil {
			_ = resp.Body.Close()
			return &httpError{status: resp.StatusCode, body: "websocket dial refused"}
		}
		return fmt.Errorf("dial: %w", err)
	}
	defer func() { _ = conn.CloseNow() }()
	conn.SetReadLimit(maxFrameBytes)

	payload := map[string]any{"requestCompletionMarker": true}
	if afterSequence != nil {
		payload["afterSequence"] = *afterSequence
	}
	const requestID = "1"
	if err := writeFrame(ctx, conn, map[string]any{
		"_tag": "Request", "id": requestID, "tag": shellMethod, "payload": payload, "headers": []any{},
	}); err != nil {
		return err
	}
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		envelopes, err := decodeEnvelopes(data)
		if err != nil {
			return err
		}
		for _, envelope := range envelopes {
			switch envelope.Tag {
			case "Pong":
				continue
			case "Defect":
				return &protocolError{err: fmt.Errorf("defect: %s", envelope.Defect)}
			case "Chunk", "Exit":
			default:
				return &protocolError{err: fmt.Errorf("unknown envelope %q", envelope.Tag)}
			}
			var id string
			if err := json.Unmarshal(envelope.RequestID, &id); err != nil || id != requestID {
				return &protocolError{err: errors.New("envelope for an unknown request")}
			}
			if envelope.Tag == "Exit" {
				if envelope.Exit != nil && envelope.Exit.Tag == "Failure" {
					// The scope check for the subscription itself happens
					// here, not at the ticket: a session without it is
					// refused inside the stream.
					if bytes.Contains(envelope.Exit.Cause, []byte(`"EnvironmentAuthorizationError"`)) {
						return &httpError{status: http.StatusForbidden, body: "subscription refused: " + string(envelope.Exit.Cause)}
					}
					return &protocolError{err: fmt.Errorf("subscription failed: %s", envelope.Exit.Cause)}
				}
				return errors.New("subscription ended")
			}
			var items []json.RawMessage
			if err := json.Unmarshal(envelope.Values, &items); err != nil {
				return &protocolError{err: fmt.Errorf("chunk: %w", err)}
			}
			for _, item := range items {
				if err := handle(item); err != nil {
					return err
				}
			}
			if err := writeFrame(ctx, conn, map[string]any{"_tag": "Ack", "requestId": requestID}); err != nil {
				return err
			}
		}
	}
}

func writeFrame(ctx context.Context, conn *websocket.Conn, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
		if ctx.Err() != nil {
			return nil
		}
		return fmt.Errorf("write: %w", err)
	}
	return nil
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
