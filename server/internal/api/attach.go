package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/coder/websocket"
	"github.com/jeremytondo/atelier-code/internal/session"
	"github.com/jeremytondo/atelier-code/internal/zmx"
)

// attachOriginPatterns are the authorities allowed to open an attach WebSocket.
// Same-origin (packaged mode) is always permitted by websocket.Accept; these
// extra entries cover the dev setup where the SvelteKit dev server on :5173
// proxies the upgrade to the Go server, so the browser's Origin differs from the
// request Host.
var attachOriginPatterns = []string{
	"localhost:5173",
	"127.0.0.1:5173",
	"localhost:7331",
	"127.0.0.1:7331",
}

// resizeMessage is the only client-to-server control frame: a text/JSON message
// carrying new terminal dimensions. Terminal bytes themselves travel as binary
// frames in both directions.
type resizeMessage struct {
	Type string `json:"type"`
	Cols uint16 `json:"cols"`
	Rows uint16 `json:"rows"`
}

var attachInitialResizeTimeout = 2 * time.Second

// attachInitialInputLimit caps the aggregate bytes buffered while waiting for
// the initial resize. Each frame is already capped by the read limit, but a
// client pasting heavily before its resize arrives could otherwise grow the
// buffer without bound; excess input is dropped, not fatal.
const attachInitialInputLimit = 256 * 1024

type acceptedAttachSubprotocolKey struct{}

// WithAcceptedAttachSubprotocol stores the already-authenticated WebSocket
// subprotocol token so attach can echo it during the upgrade handshake.
func WithAcceptedAttachSubprotocol(ctx context.Context, subprotocol string) context.Context {
	if subprotocol == "" {
		return ctx
	}
	return context.WithValue(ctx, acceptedAttachSubprotocolKey{}, subprotocol)
}

func acceptedAttachSubprotocol(ctx context.Context) string {
	subprotocol, _ := ctx.Value(acceptedAttachSubprotocolKey{}).(string)
	return subprotocol
}

// attachSession upgrades the request to a WebSocket and bridges it to a live PTY
// attached to the id-addressed session: PTY output streams to the browser as
// binary frames, and binary frames from the browser are written back as
// keystrokes. A text frame is decoded as a resize control message.
func (routes apiRoutes) attachSession(w http.ResponseWriter, r *http.Request) {
	if !routes.requireSessions(w) {
		return
	}
	id := r.PathValue("id")

	// Reject a missing session before the upgrade: this is cheap (no spawn) and
	// once the connection is hijacked we can no longer write an HTTP status.
	if err := routes.sessions.EnsureAttachable(r.Context(), id); err != nil {
		writeSessionError(w, err)
		return
	}

	// Accept validates the WebSocket handshake and Origin. Doing it before the
	// attach spawn means a non-WebSocket GET or a rejected Origin never starts a
	// zmx attach client — only an accepted handshake does.
	acceptOptions := &websocket.AcceptOptions{
		OriginPatterns: attachOriginPatterns,
	}
	if subprotocol := acceptedAttachSubprotocol(r.Context()); subprotocol != "" {
		acceptOptions.Subprotocols = []string{subprotocol}
	}
	conn, err := websocket.Accept(w, r, acceptOptions)
	if err != nil {
		return
	}
	defer conn.CloseNow()
	// A single terminal paste can exceed the default 32 KiB frame limit; raise it
	// so a large paste is delivered as keystrokes instead of erroring the read
	// loop and tearing down the bridge mid-session.
	conn.SetReadLimit(1 << 20)

	// Own the attach lifetime with a context independent of the HTTP request:
	// hijacked WebSocket connections may lose the request context, and we want
	// the zmx attach child to live exactly as long as this bridge.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	initial, err := readInitialAttachInput(conn)
	if err != nil {
		return
	}

	pty, err := routes.sessions.Attach(ctx, id, initial.rows, initial.cols)
	if err != nil {
		// The session vanished between the existence check and the spawn; the
		// handshake is already accepted, so report it over the WebSocket.
		closeAttachError(conn, err)
		return
	}
	defer pty.Close()

	// PTY -> WS: pump terminal output to the browser. Cancelling on exit unblocks
	// the read loop below, and the deferred pty.Close() unblocks pty.Read here.
	go func() {
		defer cancel()
		buf := make([]byte, 32*1024)
		for {
			n, readErr := pty.Read(buf)
			if n > 0 {
				// Bound each write so a stalled client cannot block this pump
				// indefinitely while holding the zmx attach child open.
				writeCtx, cancelWrite := context.WithTimeout(ctx, 10*time.Second)
				err := conn.Write(writeCtx, websocket.MessageBinary, buf[:n])
				cancelWrite()
				if err != nil {
					return
				}
			}
			if readErr != nil {
				closeSessionEnded(conn)
				return
			}
		}
	}()

	for _, data := range initial.binary {
		if _, err := pty.Write(data); err != nil {
			closeSessionEnded(conn)
			return
		}
	}

	// WS -> PTY: binary frames are keystrokes; text frames are control messages.
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		switch typ {
		case websocket.MessageBinary:
			if _, err := pty.Write(data); err != nil {
				closeSessionEnded(conn)
				return
			}
		case websocket.MessageText:
			rows, cols, ok := decodeResizeMessage(data)
			if ok {
				_ = pty.Resize(rows, cols)
			}
		}
	}
}

type initialAttachInput struct {
	rows   uint16
	cols   uint16
	binary [][]byte
	// buffered is the running total of bytes in binary, checked against
	// attachInitialInputLimit.
	buffered int
}

func defaultInitialAttachInput() initialAttachInput {
	return initialAttachInput{rows: zmx.DefaultAttachRows, cols: zmx.DefaultAttachCols}
}

func readInitialAttachInput(conn *websocket.Conn) (initialAttachInput, error) {
	input := defaultInitialAttachInput()
	readCtx, cancel := context.WithTimeout(context.Background(), attachInitialResizeTimeout)
	defer cancel()

	for {
		typ, data, err := conn.Read(readCtx)
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) {
				return input, nil
			}
			return input, err
		}

		switch typ {
		case websocket.MessageBinary:
			if input.buffered+len(data) > attachInitialInputLimit {
				continue
			}
			input.buffered += len(data)
			input.binary = append(input.binary, append([]byte(nil), data...))
		case websocket.MessageText:
			rows, cols, ok := decodeResizeMessage(data)
			if ok {
				input.rows, input.cols = rows, cols
				return input, nil
			}
		}
	}
}

func decodeResizeMessage(data []byte) (uint16, uint16, bool) {
	var msg resizeMessage
	if json.Unmarshal(data, &msg) != nil || msg.Type != "resize" || msg.Rows == 0 || msg.Cols == 0 {
		return 0, 0, false
	}
	return msg.Rows, msg.Cols, true
}

func closeAttachError(conn *websocket.Conn, err error) {
	if errors.Is(err, session.ErrSessionNotFound) || errors.Is(err, session.ErrSessionNotLive) {
		closeSessionEnded(conn)
		return
	}
	_ = conn.Close(websocket.StatusInternalError, "internal_error")
}

func closeSessionEnded(conn *websocket.Conn) {
	_ = conn.Close(websocket.StatusNormalClosure, "session_ended")
}
