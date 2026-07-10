package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	"github.com/jeremytondo/atelier-code/internal/session"
	"github.com/jeremytondo/atelier-code/internal/store"
	"github.com/jeremytondo/atelier-code/internal/zmx"
)

// authedRouter wraps the router in the given listener boundary so withAuth can
// see which transport a request arrived on.
func authedRouter(kind ListenerKind, token string) http.Handler {
	return withListenerBoundary(kind, Router(nil, nil, nil, nil, token))
}

func TestAuthAllowsTCPWhenNoTokenConfigured(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)

	authedRouter(ListenerTCP, "").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAuthRejectsTCPWithoutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)

	authedRouter(ListenerTCP, "secret").ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthRejectsFSListWithoutToken(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/fs/list", nil)

	authedRouter(ListenerTCP, "secret").ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthRejectsTCPWithWrongToken(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer nope")

	authedRouter(ListenerTCP, "secret").ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthAllowsTCPWithToken(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Authorization", "Bearer secret")

	authedRouter(ListenerTCP, "secret").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAuthRejectsSubprotocolTokenOutsideAttach(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "secret")

	authedRouter(ListenerTCP, "secret").ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAuthAlwaysAllowsUnixSocket(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/health", nil)

	// No token presented, but the owner-only Unix socket is always trusted.
	authedRouter(ListenerUnix, "secret").ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

func TestAuthRejectsAttachQueryToken(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/ses_abc/attach?token=secret", nil)

	authedRouter(ListenerTCP, "secret").ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestAttachSubprotocolTokenMatchesIDAttachPath(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/ses_abc/attach", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "other, secret")
	protocol, ok := attachSubprotocolToken(req, "secret")
	if !ok {
		t.Fatal("attach subprotocol token did not match id-based attach path")
	}
	if protocol != "secret" {
		t.Fatalf("protocol = %q, want secret", protocol)
	}
}

func TestAttachEncodedSubprotocolTokenMatchesSpecialToken(t *testing.T) {
	token := `sec ret,with"quotes`
	protocol := attachSubprotocolForToken(token)
	req := httptest.NewRequest(http.MethodGet, "/api/sessions/ses_abc/attach", nil)
	req.Header.Set("Sec-WebSocket-Protocol", "other, "+protocol)

	got, ok := attachSubprotocolToken(req, token)
	if !ok {
		t.Fatal("encoded attach subprotocol token did not match")
	}
	if got != protocol {
		t.Fatalf("protocol = %q, want encoded protocol", got)
	}
}

func TestAttachSubprotocolRejectsNonAttachPaths(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
	}{
		{name: "old path", method: http.MethodGet, path: "/api/sessions/attach"},
		{name: "nested path", method: http.MethodGet, path: "/api/sessions/ses_abc/extra/attach"},
		{name: "post", method: http.MethodPost, path: "/api/sessions/ses_abc/attach"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.path, nil)
			req.Header.Set("Sec-WebSocket-Protocol", "secret")
			if _, ok := attachSubprotocolToken(req, "secret"); ok {
				t.Fatalf("attach subprotocol matched %s %s, want reject", tt.method, tt.path)
			}
		})
	}
}

func TestAttachSubprotocolAuthEchoesAcceptedProtocol(t *testing.T) {
	srv, url := newAuthAttachServer(t)
	defer srv.Close()

	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		Subprotocols: []string{"secret"},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if conn.Subprotocol() != "secret" {
		t.Fatalf("subprotocol = %q, want secret", conn.Subprotocol())
	}
}

func TestAttachEncodedSubprotocolAuthEchoesAcceptedProtocol(t *testing.T) {
	token := `sec ret,with"quotes`
	protocol := attachSubprotocolForToken(token)
	srv, url := newAuthAttachServerWithToken(t, token)
	defer srv.Close()

	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		Subprotocols: []string{protocol},
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if conn.Subprotocol() != protocol {
		t.Fatalf("subprotocol = %q, want %q", conn.Subprotocol(), protocol)
	}
}

func TestAttachAuthorizationAuthDoesNotNegotiateSubprotocol(t *testing.T) {
	srv, url := newAuthAttachServer(t)
	defer srv.Close()

	headers := make(http.Header)
	headers.Set("Authorization", "Bearer secret")
	conn, _, err := websocket.Dial(context.Background(), url, &websocket.DialOptions{
		HTTPHeader: headers,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if conn.Subprotocol() != "" {
		t.Fatalf("subprotocol = %q, want none", conn.Subprotocol())
	}
}

func newAuthAttachServer(t *testing.T) (*httptest.Server, string) {
	return newAuthAttachServerWithToken(t, "secret")
}

func newAuthAttachServerWithToken(t *testing.T, token string) (*httptest.Server, string) {
	t.Helper()
	st, err := store.Open(t.TempDir() + "/atc.db")
	if err != nil {
		t.Fatalf("Open store: %v", err)
	}
	t.Cleanup(func() { _ = st.Close() })

	mux := &authFakeMux{attachPTY: newAuthPTY()}
	svc := session.NewService(st, mux, session.ActionRegistry{
		"claude": {Command: "claude"},
	}, session.EnvironmentRegistry{
		"host-login-shell": {Kind: session.EnvironmentKindHostLoginShell},
	}, nil, nil)
	started, err := svc.Start(context.Background(), session.StartInput{
		Action:      "claude",
		Environment: "host-login-shell",
		WorkingDir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	handler := withListenerBoundary(ListenerTCP, routerWithWeb(svc, nil, nil, nil, token, http.NotFoundHandler()))
	srv := httptest.NewServer(handler)
	url := "ws" + strings.TrimPrefix(srv.URL, "http") + "/api/sessions/" + started.ID + "/attach"
	return srv, url
}

type authFakeMux struct {
	sessions  []zmx.Session
	attachPTY zmx.PTY
}

func (f *authFakeMux) Start(_ context.Context, name, dir string, argv []string) error {
	f.sessions = append(f.sessions, zmx.Session{Name: name, StartDir: dir, Cmd: strings.Join(argv, " ")})
	return nil
}

func (f *authFakeMux) Send(context.Context, string, []byte) error {
	return nil
}

func (f *authFakeMux) Attach(context.Context, string, uint16, uint16) (zmx.PTY, error) {
	return f.attachPTY, nil
}

func (f *authFakeMux) List(context.Context) ([]zmx.Session, error) {
	return f.sessions, nil
}

func (f *authFakeMux) Terminate(context.Context, string) error {
	return nil
}

type authPTY struct {
	outputReader *io.PipeReader
	outputWriter *io.PipeWriter
}

func newAuthPTY() *authPTY {
	outputReader, outputWriter := io.Pipe()
	return &authPTY{outputReader: outputReader, outputWriter: outputWriter}
}

func (p *authPTY) Read(b []byte) (int, error) {
	return p.outputReader.Read(b)
}

func (p *authPTY) Write(b []byte) (int, error) {
	return len(b), nil
}

func (p *authPTY) Resize(uint16, uint16) error {
	return nil
}

func (p *authPTY) Close() error {
	_ = p.outputReader.Close()
	_ = p.outputWriter.Close()
	return nil
}
