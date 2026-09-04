// Package t3codetest is an in-process fake of a T3 Code environment
// server for tests of the T3 Code Integration and of everything above it
// (the API and the CLI). It speaks exactly what the Integration uses: the
// well-known descriptor, the OAuth token exchange, the WebSocket ticket,
// and Effect RPC over /ws — the shell subscription and command dispatch,
// multiplexed on one socket as T3 does. It records what the Integration
// asked for and lets a test push stream items, answer or hold commands,
// drop connections, or go away entirely. It imports nothing from the
// Integration: the wire contract is pinned here independently.
package t3codetest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/jeremytondo/atc/internal/api"
)

const (
	// Scope is the scope set the Integration is expected to request and
	// the fake grants by default.
	Scope = "orchestration:read orchestration:operate"
	// SessionLabel is the client label the Integration pairs under.
	SessionLabel = "atc"

	shellMethod    = "orchestration.subscribeShell"
	dispatchMethod = "orchestration.dispatchCommand"
)

// Server is the fake environment server. Construct with NewServer.
type Server struct {
	t   *testing.T
	srv *httptest.Server

	mu            sync.Mutex
	environmentID string
	// grants are the one-use pairing credentials the fake CLI minted;
	// tokens maps issued bearer tokens to their session ids; sessions is
	// what the CLI's session list answers.
	grants   map[string]bool
	tokens   map[string]string
	sessions []session
	issued   int
	// grantScope is what the exchange answers; a test changes it to prove
	// the Integration checks.
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
	conns          []*conn
	// subscribes records each subscription's afterSequence (nil for a
	// fresh one).
	subscribes []*uint64
	// initial answers a subscription: the items sent before live pushes.
	initial func(afterSequence *uint64) []any
	// dispatch answers each dispatched command; commands records them in
	// arrival order; sequence numbers the successes.
	dispatch func(command map[string]any) DispatchReply
	commands []map[string]any
	sequence uint64
	// details answers thread detail snapshot reads by thread id (absent
	// answers 404); detailReads counts the reads per thread.
	details     map[string]map[string]any
	detailReads map[string]int
}

type session struct {
	ID       string
	IssuedAt string
}

// conn is one accepted socket and the id of the shell subscription it
// carries, once requested.
type conn struct {
	ws           *websocket.Conn
	subscription json.RawMessage
}

// DispatchReply is what the fake answers a dispatched command with: the
// zero value is success with a fresh sequence; Reject is T3's typed
// rejection with its message, RolledBack adding that T3 deleted the
// thread the bootstrap had created; Denied is the authorization error a
// session lacking the operate scope meets.
type DispatchReply struct {
	Reject     string
	RolledBack bool
	Denied     bool
}

// NewServer starts a fake environment that answers a subscription with an
// empty snapshot and every command with success, and stops it when the
// test ends.
func NewServer(t *testing.T) *Server {
	t.Helper()
	s := &Server{
		t:             t,
		environmentID: "env-1",
		grants:        map[string]bool{},
		tokens:        map[string]string{},
		grantScope:    Scope,
		tickets:       map[string]bool{},
		initial:       func(*uint64) []any { return []any{SnapshotItem(0, nil, nil), SynchronizedItem()} },
		dispatch:      func(map[string]any) DispatchReply { return DispatchReply{} },
		details:       map[string]map[string]any{},
		detailReads:   map[string]int{},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/t3/environment", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		id := s.environmentID
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"environmentId": id, "label": "test", "serverVersion": "0.0.0-test"})
	})
	mux.HandleFunc("POST /oauth/token", s.exchange)
	mux.HandleFunc("POST /api/auth/websocket-ticket", s.ticket)
	mux.HandleFunc("GET /api/orchestration/threads/{threadId}", s.threadDetail)
	mux.HandleFunc("GET /ws", s.serve)
	s.srv = httptest.NewServer(mux)
	t.Cleanup(s.srv.Close)
	return s
}

// Origin is the server's URL, what T3's runtime file would record.
func (s *Server) Origin() string { return s.srv.URL }

// SetEnvironmentID changes the environment the descriptor reports.
func (s *Server) SetEnvironmentID(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.environmentID = id
}

// SetGrantScope changes the scope the token exchange answers with.
func (s *Server) SetGrantScope(scope string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.grantScope = scope
}

// SetScopeDenied makes the ticket route refuse every session with 403.
func (s *Server) SetScopeDenied(denied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.scopeDenied = denied
}

// SetStreamDenied makes every shell subscription exit with T3's
// authorization error.
func (s *Server) SetStreamDenied(denied bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streamDenied = denied
}

// SetExchangeStatus makes the token exchange answer with an HTTP status
// instead of a token; zero restores the exchange.
func (s *Server) SetExchangeStatus(status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exchangeStatus = status
}

// SetInitial sets what a subscription is answered with before live
// pushes.
func (s *Server) SetInitial(initial func(afterSequence *uint64) []any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.initial = initial
}

// SetDispatch sets how dispatched commands are answered. The function
// runs on its own goroutine per command, so it may block — holding a
// command in flight while the test pushes stream items — and may push
// items itself before returning.
func (s *Server) SetDispatch(dispatch func(command map[string]any) DispatchReply) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dispatch = dispatch
}

// SetThreadDetail sets what the thread detail snapshot read answers for
// a thread (ThreadDetailItem); nil answers 404 again.
func (s *Server) SetThreadDetail(threadID string, detail map[string]any) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if detail == nil {
		delete(s.details, threadID)
		return
	}
	s.details[threadID] = detail
}

// DetailReads reports how many thread detail snapshot reads a thread has
// received.
func (s *Server) DetailReads(threadID string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.detailReads[threadID]
}

// threadDetail is the one-shot thread snapshot read: the session bearer
// authenticates it, and an unknown thread is 404.
func (s *Server) threadDetail(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if _, ok := s.tokens[token]; !ok {
		http.Error(w, `{"code":"auth_invalid"}`, http.StatusUnauthorized)
		return
	}
	threadID := r.PathValue("threadId")
	s.detailReads[threadID]++
	detail, ok := s.details[threadID]
	if !ok {
		http.Error(w, `{"code":"not_found","reason":"thread_not_found"}`, http.StatusNotFound)
		return
	}
	_ = json.NewEncoder(w).Encode(detail)
}

// Commands returns every command dispatched so far, in arrival order.
func (s *Server) Commands() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.commands...)
}

// Subscriptions returns each subscription's afterSequence, nil for a
// fresh one.
func (s *Server) Subscriptions() []*uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]*uint64(nil), s.subscribes...)
}

// Counts reports how many tickets and token exchanges were requested.
func (s *Server) Counts() (tickets, exchanges int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ticketCalls, s.exchanges
}

func (s *Server) exchange(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.exchanges++
	if s.exchangeStatus != 0 {
		http.Error(w, `{"code":"internal_error"}`, s.exchangeStatus)
		return
	}
	form := r.PostForm
	if form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:token-exchange" ||
		form.Get("subject_token_type") != "urn:t3:params:oauth:token-type:environment-bootstrap" ||
		form.Get("requested_token_type") != "urn:ietf:params:oauth:token-type:access_token" ||
		form.Get("scope") != Scope || form.Get("client_label") != SessionLabel {
		http.Error(w, `{"code":"invalid_request"}`, http.StatusBadRequest)
		return
	}
	credential := form.Get("subject_token")
	if !s.grants[credential] {
		http.Error(w, `{"code":"invalid_bootstrap_credential"}`, http.StatusUnauthorized)
		return
	}
	delete(s.grants, credential)
	s.issued++
	token := fmt.Sprintf("token-%d", s.issued)
	sessionID := fmt.Sprintf("sess-%d", s.issued)
	s.tokens[token] = sessionID
	s.sessions = append(s.sessions, session{ID: sessionID, IssuedAt: fmt.Sprintf("2026-09-01T00:00:%02dZ", s.issued)})
	_ = json.NewEncoder(w).Encode(map[string]any{
		"access_token": token, "issued_token_type": "urn:ietf:params:oauth:token-type:access_token",
		"token_type": "Bearer", "expires_in": 3600, "scope": s.grantScope,
	})
}

func (s *Server) ticket(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ticketCalls++
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if _, ok := s.tokens[token]; !ok {
		http.Error(w, `{"code":"auth_invalid"}`, http.StatusUnauthorized)
		return
	}
	if s.scopeDenied {
		http.Error(w, `{"code":"insufficient_scope"}`, http.StatusForbidden)
		return
	}
	ticket := fmt.Sprintf("ticket-%d", s.ticketCalls)
	s.tickets[ticket] = true
	_ = json.NewEncoder(w).Encode(map[string]any{"ticket": ticket, "expiresAt": "2026-09-01T00:01:00Z"})
}

// serve accepts one socket and answers every request on it: the shell
// subscription with the initial items, commands with the dispatch
// answer, anything else with a defect. Acks are ignored.
func (s *Server) serve(w http.ResponseWriter, r *http.Request) {
	ticket := r.URL.Query().Get("wsTicket")
	s.mu.Lock()
	ok := s.tickets[ticket]
	delete(s.tickets, ticket)
	s.mu.Unlock()
	if !ok {
		http.Error(w, "bad ticket", http.StatusUnauthorized)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	c := &conn{ws: ws}
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	ctx := context.Background()
	for {
		_, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var frame struct {
			Tag     string          `json:"_tag"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"tag"`
			Payload json.RawMessage `json:"payload"`
		}
		if err := json.Unmarshal(data, &frame); err != nil || frame.Tag != "Request" {
			continue
		}
		switch frame.Method {
		case shellMethod:
			var payload struct {
				AfterSequence *uint64 `json:"afterSequence"`
			}
			_ = json.Unmarshal(frame.Payload, &payload)
			s.mu.Lock()
			c.subscription = frame.ID
			s.subscribes = append(s.subscribes, payload.AfterSequence)
			initial, denied := s.initial, s.streamDenied
			s.mu.Unlock()
			if denied {
				s.write(c, exitFailure(frame.ID, map[string]any{
					"_tag": "EnvironmentAuthorizationError", "message": "missing scope", "requiredScope": "orchestration:read",
				}))
				continue
			}
			s.chunk(c, frame.ID, initial(payload.AfterSequence)...)
		case dispatchMethod:
			var command map[string]any
			if err := json.Unmarshal(frame.Payload, &command); err != nil {
				s.write(c, exitDefect(frame.ID, "command payload: "+err.Error()))
				continue
			}
			s.mu.Lock()
			s.commands = append(s.commands, command)
			dispatch := s.dispatch
			s.mu.Unlock()
			go func() {
				reply := dispatch(command)
				switch {
				case reply.Denied:
					s.write(c, exitFailure(frame.ID, map[string]any{
						"_tag": "EnvironmentAuthorizationError", "message": "missing scope", "requiredScope": "orchestration:operate",
					}))
				case reply.Reject != "":
					rejection := map[string]any{"_tag": "OrchestrationDispatchCommandError", "message": reply.Reject}
					if reply.RolledBack {
						rejection["bootstrapThreadDisposition"] = "deleted"
					}
					s.write(c, exitFailure(frame.ID, rejection))
				default:
					s.mu.Lock()
					s.sequence++
					sequence := s.sequence
					s.mu.Unlock()
					s.write(c, map[string]any{"_tag": "Exit", "requestId": frame.ID, "exit": map[string]any{
						"_tag": "Success", "value": map[string]any{"sequence": sequence},
					}})
				}
			}()
		default:
			s.write(c, exitDefect(frame.ID, "unknown method "+frame.Method))
		}
	}
}

func exitFailure(requestID json.RawMessage, typed map[string]any) map[string]any {
	return map[string]any{"_tag": "Exit", "requestId": requestID, "exit": map[string]any{
		"_tag": "Failure", "cause": []any{map[string]any{"_tag": "Fail", "error": typed}},
	}}
}

func exitDefect(requestID json.RawMessage, defect string) map[string]any {
	return map[string]any{"_tag": "Exit", "requestId": requestID, "exit": map[string]any{
		"_tag": "Failure", "cause": []any{map[string]any{"_tag": "Die", "defect": defect}},
	}}
}

func (s *Server) write(c *conn, frame any) {
	data, err := json.Marshal(frame)
	if err != nil {
		s.t.Fatal(err)
	}
	_ = c.ws.Write(context.Background(), websocket.MessageText, data)
}

// chunk writes one Chunk carrying items to a subscription.
func (s *Server) chunk(c *conn, requestID json.RawMessage, items ...any) {
	if len(items) == 0 {
		return
	}
	s.write(c, map[string]any{"_tag": "Chunk", "requestId": requestID, "values": items})
}

// Push sends items on every live subscription.
func (s *Server) Push(items ...any) {
	for _, c := range s.subscribed() {
		s.chunk(c, c.subscription, items...)
	}
}

// Raw sends one raw frame on every live subscription — for envelopes the
// helpers refuse to build. SUB in the frame is replaced by the
// subscription's request id.
func (s *Server) Raw(frame string) {
	for _, c := range s.subscribed() {
		id, _ := json.Marshal(c.subscription)
		_ = c.ws.Write(context.Background(), websocket.MessageText, []byte(strings.ReplaceAll(frame, "SUB", strings.Trim(string(id), `"`))))
	}
}

func (s *Server) subscribed() []*conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	var conns []*conn
	for _, c := range s.conns {
		if c.subscription != nil {
			conns = append(conns, c)
		}
	}
	return conns
}

// DropConns closes every socket while the server keeps serving — a
// dropped connection from the Integration's point of view.
func (s *Server) DropConns() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.ws.CloseNow()
	}
}

// CLI stands in for `node t3 auth ...`: it mints grants the fake server
// honors, lists the sessions the server issued, and revokes them.
type CLI struct {
	server *Server
	mu     sync.Mutex
	calls  [][]string
	minted int
	fail   error
}

// NewCLI returns a CLI bound to a server.
func NewCLI(server *Server) *CLI {
	return &CLI{server: server}
}

// Run is the Integration's CLI runner seam.
func (c *CLI) Run(_ context.Context, args ...string) ([]byte, error) {
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
		return json.Marshal(map[string]any{"id": "pair-" + credential, "credential": credential, "label": SessionLabel, "expiresAt": "2026-09-01T00:05:00Z"})
	case "auth session list":
		c.server.mu.Lock()
		defer c.server.mu.Unlock()
		list := make([]map[string]any, 0, len(c.server.sessions))
		for _, s := range c.server.sessions {
			list = append(list, map[string]any{"sessionId": s.ID, "issuedAt": s.IssuedAt, "client": map[string]any{"label": SessionLabel}})
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

// SetFail makes every call fail with err; nil restores the CLI.
func (c *CLI) SetFail(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.fail = err
}

// Calls returns every invocation's arguments, in order.
func (c *CLI) Calls() [][]string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([][]string(nil), c.calls...)
}

// Count reports how many calls started with prefix ("auth pairing
// create").
func (c *CLI) Count(prefix string) int {
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

// SnapshotItem is a snapshot stream item.
func SnapshotItem(sequence uint64, projects []any, threads []any) map[string]any {
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

// SynchronizedItem is the completion marker.
func SynchronizedItem() map[string]any { return map[string]any{"kind": "synchronized"} }

// ProjectItem is a project shell.
func ProjectItem(id, title, root string) map[string]any {
	return map[string]any{"id": id, "title": title, "workspaceRoot": root, "defaultModelSelection": nil, "scripts": []any{}}
}

// ThreadOpt tweaks a thread item.
type ThreadOpt func(map[string]any)

// WithSession gives the thread a provider session in a status.
func WithSession(status string, provider string) ThreadOpt {
	return func(m map[string]any) {
		m["session"] = map[string]any{"threadId": m["id"], "status": status, "providerName": provider, "activeTurnId": nil, "lastError": nil, "updatedAt": "2026-09-01T00:00:00Z"}
	}
}

// LastError sets the session's error text; use after WithSession.
func LastError(detail string) ThreadOpt {
	return func(m map[string]any) { m["session"].(map[string]any)["lastError"] = detail }
}

// LatestTurn sets T3's latest-turn projection; startedAt and completedAt
// are ISO strings or nil.
func LatestTurn(id, state string, startedAt, completedAt any) ThreadOpt {
	return func(m map[string]any) {
		m["latestTurn"] = map[string]any{
			"turnId": id, "state": state, "requestedAt": "2026-09-01T00:00:01Z",
			"startedAt": startedAt, "completedAt": completedAt, "assistantMessageId": nil,
		}
	}
}

// AssistantMessage names the latest turn's final assistant message; use
// after LatestTurn.
func AssistantMessage(id string) ThreadOpt {
	return func(m map[string]any) { m["latestTurn"].(map[string]any)["assistantMessageId"] = id }
}

// MessageItem is one thread message in the detail snapshot.
func MessageItem(id, role, text, turnID string, streaming bool) map[string]any {
	return map[string]any{
		"id": id, "role": role, "text": text, "turnId": turnID, "streaming": streaming,
		"createdAt": "2026-09-01T00:00:02Z", "updatedAt": "2026-09-01T00:00:03Z",
	}
}

// ThreadDetailItem is a thread detail snapshot: the thread shell
// (ThreadItem) carrying these messages.
func ThreadDetailItem(thread map[string]any, messages ...map[string]any) map[string]any {
	detail := make(map[string]any, len(thread)+4)
	for k, v := range thread {
		detail[k] = v
	}
	list := make([]any, 0, len(messages))
	for _, message := range messages {
		list = append(list, message)
	}
	detail["messages"], detail["activities"], detail["checkpoints"], detail["proposedPlans"] = list, []any{}, []any{}, []any{}
	return map[string]any{"snapshotSequence": 1, "thread": detail}
}

// Pending sets the pending-approval and pending-input flags.
func Pending(approvals, input bool) ThreadOpt {
	return func(m map[string]any) { m["hasPendingApprovals"], m["hasPendingUserInput"] = approvals, input }
}

// Liveness sets the background liveness value.
func Liveness(value any) ThreadOpt {
	return func(m map[string]any) { m["backgroundLiveness"] = value }
}

// SettledOverride sets T3's settlement field: "settled", "active", or nil.
func SettledOverride(value any) ThreadOpt {
	return func(m map[string]any) { m["settledOverride"] = value }
}

// Worktree sets the thread's worktree path.
func Worktree(path string) ThreadOpt {
	return func(m map[string]any) { m["worktreePath"] = path }
}

// Model sets the thread's model selection.
func Model(instanceID, model string) ThreadOpt {
	return func(m map[string]any) {
		m["modelSelection"] = map[string]any{"instanceId": instanceID, "model": model}
	}
}

// ThreadItem is a thread shell running at its project root under Codex
// with no session and no turn, adjusted by opts.
func ThreadItem(id, projectID, title string, opts ...ThreadOpt) map[string]any {
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

// Upserted is a thread-upserted event.
func Upserted(sequence uint64, thread map[string]any) map[string]any {
	return map[string]any{"kind": "thread-upserted", "sequence": sequence, "thread": thread}
}

// Removed is a thread-removed event.
func Removed(sequence uint64, threadID string) map[string]any {
	return map[string]any{"kind": "thread-removed", "sequence": sequence, "threadId": threadID}
}

// ProjectUpserted is a project-upserted event.
func ProjectUpserted(sequence uint64, project map[string]any) map[string]any {
	return map[string]any{"kind": "project-upserted", "sequence": sequence, "project": project}
}

// ProjectRemoved is a project-removed event.
func ProjectRemoved(sequence uint64, projectID string) map[string]any {
	return map[string]any{"kind": "project-removed", "sequence": sequence, "projectId": projectID}
}

// WriteRuntime plants T3's runtime state file under home, pointing at
// origin with this process as the live server pid — what the Integration
// discovers.
func WriteRuntime(t *testing.T, home, origin string) {
	t.Helper()
	path := filepath.Join(home, "userdata", "server-runtime.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(map[string]any{"version": 1, "pid": os.Getpid(), "port": 0, "origin": origin, "startedAt": "2026-09-01T00:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// Connect brings an Integration up against the fake for a test above the
// Integration: it plants the runtime file under home, answers the shell
// subscription with one project rooted at root, runs run until the test
// ends, and returns once connection reports connected.
func Connect(t *testing.T, s *Server, home, root string, run func(ctx context.Context), connection func() api.IntegrationConnection) {
	t.Helper()
	WriteRuntime(t, home, s.Origin())
	s.SetInitial(func(*uint64) []any {
		return []any{SnapshotItem(1, []any{ProjectItem("p1", "T3", root)}, nil), SynchronizedItem()}
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	deadline := time.Now().Add(5 * time.Second)
	for connection().State != api.IntegrationConnected {
		if time.Now().After(deadline) {
			t.Fatalf("T3 Code never connected: %+v", connection())
		}
		time.Sleep(5 * time.Millisecond)
	}
}
