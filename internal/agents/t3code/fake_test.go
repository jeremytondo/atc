package t3code

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/coder/websocket"
)

// fakeT3 is an in-process T3 environment server speaking exactly what the
// adapter uses: the well-known descriptor, the OAuth token exchange, the
// WebSocket ticket, and the Effect RPC shell subscription over /ws. It
// records what the adapter asked for and lets a test push stream items,
// drop connections, or go away entirely.
type fakeT3 struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	environmentID string
	// grants are the one-use pairing credentials the fake CLI minted;
	// tokens maps issued bearer tokens to their session ids; sessions is
	// what the CLI's session list answers.
	grants   map[string]bool
	tokens   map[string]string
	sessions []fakeSession
	issued   int
	// grantScope is what the exchange answers; a test widens it to prove
	// the adapter checks.
	grantScope string
	// scopeDenied makes the ticket route answer 403; streamDenied makes
	// the subscription itself exit with T3's authorization error, as the
	// real server does for a session lacking the scope.
	scopeDenied  bool
	streamDenied bool
	// exchangeStatus, when set, is what /oauth/token answers instead of a
	// token.
	exchangeStatus int
	tickets        map[string]bool
	ticketCalls    int
	exchanges      int
	conns          []*websocket.Conn
	// subscribes records each subscription's afterSequence (nil for a
	// fresh one).
	subscribes []*uint64
	// initial answers a subscription: the items sent before live pushes.
	initial func(afterSequence *uint64) []any
}

type fakeSession struct {
	ID       string
	IssuedAt string
}

func newFakeT3(t *testing.T) *fakeT3 {
	t.Helper()
	f := &fakeT3{
		t:             t,
		environmentID: "env-1",
		grants:        map[string]bool{},
		tokens:        map[string]string{},
		grantScope:    scope,
		tickets:       map[string]bool{},
	}
	f.initial = func(*uint64) []any { return []any{snapshotItem(0, nil, nil), synchronizedItem()} }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/t3/environment", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		id := f.environmentID
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"environmentId": id, "label": "test", "serverVersion": "0.0.0-test"})
	})
	mux.HandleFunc("POST /oauth/token", f.exchange)
	mux.HandleFunc("POST /api/auth/websocket-ticket", f.ticket)
	mux.HandleFunc("GET /ws", f.subscribe)
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeT3) origin() string { return f.srv.URL }

func (f *fakeT3) exchange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.exchanges++
	if f.exchangeStatus != 0 {
		http.Error(w, `{"code":"internal_error"}`, f.exchangeStatus)
		return
	}
	form := r.PostForm
	if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" ||
		form.Get("subject_token_type") != "urn:t3:params:oauth:token-type:environment-bootstrap" ||
		form.Get("requested_token_type") != "urn:ietf:params:oauth:token-type:access_token" ||
		form.Get("scope") != scope || form.Get("client_label") != sessionLabel {
		http.Error(w, `{"code":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	credential := form.Get("subject_token")
	if !f.grants[credential] {
		http.Error(w, `{"code":"invalid_bootstrap_credential"}`, http.StatusUnauthorized)
		return
	}
	delete(f.grants, credential)
	f.issued++
	token := fmt.Sprintf("token-%d", f.issued)
	sessionID := fmt.Sprintf("sess-%d", f.issued)
	f.tokens[token] = sessionID
	f.sessions = append(f.sessions, fakeSession{ID: sessionID, IssuedAt: fmt.Sprintf("2026-09-01T00:00:%02dZ", f.issued)})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token, "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"token_type": "Bearer", "expires_in": 3600, "scope": f.grantScope,
	})
}

func (f *fakeT3) ticket(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ticketCalls++
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if _, ok := f.tokens[token]; !ok {
		http.Error(w, `{"code":"auth_invalid"}`, http.StatusUnauthorized)
		return
	}
	if f.scopeDenied {
		http.Error(w, `{"code":"insufficient_scope"}`, http.StatusForbidden)
		return
	}
	ticket := fmt.Sprintf("ticket-%d", f.ticketCalls)
	f.tickets[ticket] = true
	_ = json.NewEncoder(w).Encode(map[string]any{"ticket": ticket, "expiresAt": "2026-09-01T00:01:00Z"})
}

func (f *fakeT3) subscribe(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("wsTicket")
	f.mu.Lock()
	ok := f.tickets[ticket]
	delete(f.tickets, ticket)
	f.mu.Unlock()
	if !ok {
		http.Error(w, "bad ticket", http.StatusUnauthorized)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	f.mu.Lock()
	f.conns = append(f.conns, conn)
	f.mu.Unlock()
	ctx := context.Background()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		var frame struct {
			Tag     string `json:"_tag"`
			Payload struct {
				AfterSequence *uint64 `json:"afterSequence"`
			} `json:"payload"`
		}
		if err := json.Unmarshal(data, &frame); err != nil || frame.Tag != "Request" {
			continue
		}
		f.mu.Lock()
		f.subscribes = append(f.subscribes, frame.Payload.AfterSequence)
		initial, denied := f.initial, f.streamDenied
		f.mu.Unlock()
		if denied {
			exit := `{"_tag":"Exit","requestId":"1","exit":{"_tag":"Failure","cause":{"_tag":"Fail","error":{"_tag":"EnvironmentAuthorizationError","message":"missing scope","requiredScope":"orchestration:read"}}}}`
			_ = conn.Write(ctx, websocket.MessageText, []byte(exit))
			continue
		}
		f.send(conn, initial(frame.Payload.AfterSequence)...)
	}
}

// send writes one Chunk carrying items to a connection.
func (f *fakeT3) send(conn *websocket.Conn, items ...any) {
	if len(items) == 0 {
		return
	}
	data, err := json.Marshal(map[string]any{"_tag": "Chunk", "requestId": "1", "values": items})
	if err != nil {
		f.t.Fatal(err)
	}
	_ = conn.Write(context.Background(), websocket.MessageText, data)
}

// push sends items on every live subscription.
func (f *fakeT3) push(items ...any) {
	f.mu.Lock()
	conns := append([]*websocket.Conn(nil), f.conns...)
	f.mu.Unlock()
	for _, conn := range conns {
		f.send(conn, items...)
	}
}

// raw sends one raw frame on every live subscription — for envelopes the
// helpers refuse to build.
func (f *fakeT3) raw(frame string) {
	f.mu.Lock()
	conns := append([]*websocket.Conn(nil), f.conns...)
	f.mu.Unlock()
	for _, conn := range conns {
		_ = conn.Write(context.Background(), websocket.MessageText, []byte(frame))
	}
}

// dropConns closes every subscription while the server keeps serving —
// a dropped socket from the adapter's point of view.
func (f *fakeT3) dropConns() {
	f.mu.Lock()
	conns := f.conns
	f.conns = nil
	f.mu.Unlock()
	for _, conn := range conns {
		_ = conn.CloseNow()
	}
}

func (f *fakeT3) setInitial(initial func(afterSequence *uint64) []any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.initial = initial
}

func (f *fakeT3) subscriptions() []*uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*uint64(nil), f.subscribes...)
}

func (f *fakeT3) counts() (tickets, exchanges int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ticketCalls, f.exchanges
}

// fakeCLI stands in for `node t3 auth ...`: it mints grants the fake
// server honors, lists the sessions the server issued, and revokes them.
type fakeCLI struct {
	server *fakeT3
	mu     sync.Mutex
	calls  [][]string
	minted int
	// fail, when set, is what every call returns.
	fail error
}

func (c *fakeCLI) run(_ context.Context, args ...string) ([]byte, error) {
	c.mu.Lock()
	c.calls = append(c.calls, args)
	fail := c.fail
	c.mu.Unlock()
	if fail != nil {
		return nil, fail
	}
	command := strings.Join(args[:min(3, len(args))], " ")
	switch command {
	case "auth pairing create":
		c.mu.Lock()
		c.minted++
		credential := fmt.Sprintf("grant-%d", c.minted)
		c.mu.Unlock()
		c.server.mu.Lock()
		c.server.grants[credential] = true
		c.server.mu.Unlock()
		return json.Marshal(map[string]any{"id": "pair-" + credential, "credential": credential, "label": sessionLabel, "expiresAt": "2026-09-01T00:05:00Z"})
	case "auth session list":
		c.server.mu.Lock()
		defer c.server.mu.Unlock()
		list := make([]map[string]any, 0, len(c.server.sessions))
		for _, s := range c.server.sessions {
			list = append(list, map[string]any{"sessionId": s.ID, "issuedAt": s.IssuedAt, "client": map[string]any{"label": sessionLabel}})
		}
		return json.Marshal(list)
	case "auth session revoke":
		c.server.mu.Lock()
		defer c.server.mu.Unlock()
		for token, id := range c.server.tokens {
			if id == args[3] {
				delete(c.server.tokens, token)
			}
		}
		return []byte("Revoked session " + args[3] + ".\n"), nil
	}
	return nil, fmt.Errorf("fake CLI: unknown command %q", command)
}

func (c *fakeCLI) count(prefix string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, call := range c.calls {
		if strings.HasPrefix(strings.Join(call, " "), prefix) {
			n++
		}
	}
	return n
}

// Shell item builders, in T3's wire shape.

func snapshotItem(sequence uint64, projects []any, threads []any) map[string]any {
	if projects == nil {
		projects = []any{}
	}
	if threads == nil {
		threads = []any{}
	}
	return map[string]any{"kind": "snapshot", "snapshot": map[string]any{
		"snapshotSequence": sequence, "projects": projects, "threads": threads, "updatedAt": "2026-09-01T00:00:00Z",
	}}
}

func synchronizedItem() map[string]any { return map[string]any{"kind": "synchronized"} }

func projectItem(id, title, root string) map[string]any {
	return map[string]any{"id": id, "title": title, "workspaceRoot": root, "defaultModelSelection": nil, "scripts": []any{}}
}

// threadOpt tweaks a thread item.
type threadOpt func(map[string]any)

func withSession(status string, provider string) threadOpt {
	return func(m map[string]any) {
		m["session"] = map[string]any{"threadId": m["id"], "status": status, "providerName": provider, "activeTurnId": nil, "lastError": nil, "updatedAt": "2026-09-01T00:00:00Z"}
	}
}

func lastError(detail string) threadOpt {
	return func(m map[string]any) { m["session"].(map[string]any)["lastError"] = detail }
}

func pending(approvals, input bool) threadOpt {
	return func(m map[string]any) { m["hasPendingApprovals"], m["hasPendingUserInput"] = approvals, input }
}

func liveness(value any) threadOpt {
	return func(m map[string]any) { m["backgroundLiveness"] = value }
}

func worktree(path string) threadOpt {
	return func(m map[string]any) { m["worktreePath"] = path }
}

func threadItem(id, projectID, title string, opts ...threadOpt) map[string]any {
	m := map[string]any{
		"id": id, "projectId": projectID, "title": title,
		"modelSelection": map[string]any{"instanceId": "codex", "model": "gpt-5"},
		"runtimeMode":    "full-access", "branch": nil, "worktreePath": nil, "latestTurn": nil,
		"createdAt": "2026-09-01T00:00:00Z", "updatedAt": "2026-09-01T00:00:00Z",
		"session": nil, "latestUserMessageAt": nil,
		"hasPendingApprovals": false, "hasPendingUserInput": false, "hasActionableProposedPlan": false,
	}
	for _, opt := range opts {
		opt(m)
	}
	return m
}

func upserted(sequence uint64, thread map[string]any) map[string]any {
	return map[string]any{"kind": "thread-upserted", "sequence": sequence, "thread": thread}
}

func removed(sequence uint64, threadID string) map[string]any {
	return map[string]any{"kind": "thread-removed", "sequence": sequence, "threadId": threadID}
}

func projectUpserted(sequence uint64, project map[string]any) map[string]any {
	return map[string]any{"kind": "project-upserted", "sequence": sequence, "project": project}
}

func projectRemoved(sequence uint64, projectID string) map[string]any {
	return map[string]any{"kind": "project-removed", "sequence": sequence, "projectId": projectID}
}
