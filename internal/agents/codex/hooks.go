package codex

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/agents/hookauth"
	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// HooksPath is the internal ingest route. It sits outside the public API
// contract and outside bearer auth — the per-launch secret is its whole
// authentication; the bearer token must never be used for hook delivery.
const HooksPath = "/internal/codex/hooks"

// ThreadObserver is the seam into the threads domain: neutral
// observations in, no provider vocabulary out.
type ThreadObserver interface {
	ObserveSession(ctx context.Context, o threads.SessionObservation) (string, error)
	ObserveStatus(ctx context.Context, o threads.StatusObservation) error
	Deactivate(ctx context.Context, terminalID string)
	LookupIdentity(agent, providerID string) (threadID, terminalID string, ok bool)
}

// TerminalReader resolves a terminal's record — the observing terminal's
// project is copied onto a thread at first observation.
type TerminalReader interface {
	Get(id string) (api.Terminal, error)
}

// HooksOptions wires a Hooks.
type HooksOptions struct {
	// Dir is where the per-launch secret files live (paths.CodexHookDir —
	// its own directory, so Claude's boot cleanup never touches them),
	// created 0700.
	Dir string
	// BaseURL is where hooks POST, e.g. "http://127.0.0.1:4779" — always
	// loopback: the hook runs on the server's own machine.
	BaseURL string
	// CodexHome is where the launch profile lives (CodexHome()).
	CodexHome string
	Threads   ThreadObserver
	Terminals TerminalReader
	Logger    *slog.Logger
	Now       func() time.Time
}

// Hooks owns Codex's thread evidence: the launch profile, per-launch
// secret registrations, the internal ingest endpoint, and the per-session
// reducers. Unlike Claude's single-conversation registration, one Codex
// launch accumulates sessions — the TUI switches with /new, /resume, and
// /fork, and its exit emits a SessionEnd for every session it touched.
type Hooks struct {
	baseURL   string
	codexHome string
	threads   ThreadObserver
	terminals TerminalReader
	logger    *slog.Logger
	now       func() time.Time

	registry *hookauth.Registry[terminalState]
}

// terminalState is one launch's lifecycle state, mutated only under the
// registry's per-launch lock.
type terminalState struct {
	// active is the session the terminal currently displays; only
	// SessionStart (or the post-restart seed) moves it. Empty until the
	// first submitted prompt — a zero-turn TUI has no thread.
	active string
	// sessions holds every session this launch has touched, keyed by the
	// payload's session_id — the authoritative provider conversation id
	// (CODEX_THREAD_ID is absent from hook subprocesses in practice).
	sessions map[string]*session
}

// session is one provider conversation's state within a launch. A
// SessionEnd tombstones the record (ended alone matters from then on);
// a SessionStart replaces it outright.
type session struct {
	// established records that the threads domain has accepted the
	// session observation. A transient failure leaves it false, and the
	// next event retries instead of silently dropping the whole session.
	established bool
	// ended records this session's SessionEnd: its stragglers are dropped
	// rather than re-seeded, until a SessionStart reopens it.
	ended   bool
	tracker *tracker
}

// NewHooks prepares the hook directory and an empty registry. Call
// LoadRegistrations at boot so launches that predate this server process
// keep validating.
func NewHooks(opts HooksOptions) (*Hooks, error) {
	if opts.Threads == nil || opts.Terminals == nil {
		panic("codex.NewHooks: Threads and Terminals must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	registry, err := hookauth.NewRegistry[terminalState](opts.Dir, opts.Logger)
	if err != nil {
		return nil, err
	}
	return &Hooks{
		baseURL:   strings.TrimSuffix(opts.BaseURL, "/"),
		codexHome: opts.CodexHome,
		threads:   opts.Threads,
		terminals: opts.Terminals,
		logger:    opts.Logger,
		now:       opts.Now,
		registry:  registry,
	}, nil
}

// Prepare readies one launch: the profile is brought current (rewritten
// when stale, refused when foreign), and the launch's secret is minted,
// written to its 0600 header file, and registered. It returns the header
// path for the launch environment.
func (h *Hooks) Prepare(terminalID string) (string, error) {
	if err := ensureProfile(h.codexHome); err != nil {
		return "", err
	}
	return h.registry.Prepare(terminalID)
}

// ingestURL is where the profile's hook command POSTs, carried to the
// hook through the per-launch environment.
func (h *Hooks) ingestURL() string {
	return h.baseURL + HooksPath
}

// Deregister drops a deleted terminal's secret and header file — wired
// by the composition root to the terminal delete, so the secret stops
// validating immediately rather than at the next boot's cleanup.
func (h *Hooks) Deregister(terminalID string) {
	h.registry.Deregister(terminalID)
}

// LoadRegistrations rebuilds the secret registry from the hook directory
// at boot, so TUIs launched by an earlier server process keep validating.
// Session bindings are not persisted: the first payload after a restart
// re-seeds through the identity mapping or the next SessionStart.
func (h *Hooks) LoadRegistrations() error {
	return h.registry.Load(func(terminalID string) bool {
		terminal, err := h.terminals.Get(terminalID)
		return err == nil && terminal.Agent == "codex"
	}, nil)
}

// payload is the slice of a hook event the reducer reads; everything else
// in the POST body is ignored.
type payload struct {
	SessionID      string `json:"session_id"`
	HookEventName  string `json:"hook_event_name"`
	TurnID         string `json:"turn_id"`
	Prompt         string `json:"prompt"`
	Cwd            string `json:"cwd"`
	Model          string `json:"model"`
	PermissionMode string `json:"permission_mode"`
}

// Handler is the ingest endpoint. Responses deliberately say nothing
// about why a delivery was refused: 404 for an unknown secret, 400 for a
// payload that cannot be honored, 204 for accepted (including events the
// reducer has no opinion about).
func (h *Hooks) Handler() http.Handler {
	return hookauth.Handler(h.deliver)
}

// deliver validates and applies one hook payload, returning the response
// status. The registry serializes one launch's deliveries end to end.
func (h *Hooks) deliver(ctx context.Context, secret string, body []byte) int {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil || p.SessionID == "" || p.HookEventName == "" {
		return http.StatusBadRequest
	}
	return h.registry.Deliver(secret, func(terminalID string, st *terminalState) int {
		return h.apply(ctx, terminalID, st, p)
	})
}

// apply runs one validated payload against the launch's lifecycle state.
// The registry holds the launch's lock.
func (h *Hooks) apply(ctx context.Context, terminalID string, st *terminalState, p payload) int {
	if st.sessions == nil {
		st.sessions = map[string]*session{}
	}
	sess := st.sessions[p.SessionID]

	switch p.HookEventName {
	case "SessionStart":
		return h.sessionStart(ctx, terminalID, st, sess, p)
	case "SessionEnd":
		return h.sessionEnd(ctx, terminalID, st, sess, p)
	default:
		if sess == nil {
			// Post-restart seed: accept evidence only for the conversation
			// the identity mapping already ties to this same terminal, only
			// while no other session holds the terminal, and only from
			// UserPromptSubmit — the one event a session the TUI does not
			// display can never produce. A displaced session's straggling
			// tool events (an interrupted turn can run long past a switch)
			// must not seed the wrong conversation as active; anything the
			// gate drops is re-covered at the next prompt or SessionStart.
			if p.HookEventName != "UserPromptSubmit" ||
				!h.mappedHere(terminalID, p.SessionID) || st.active != "" {
				return http.StatusBadRequest
			}
			sess = &session{tracker: seededTracker()}
			st.sessions[p.SessionID] = sess
			st.active = p.SessionID
		}
		if sess.ended {
			// The session ended; stragglers are dropped rather than
			// re-seeded. A SessionStart reopens the conversation.
			return http.StatusBadRequest
		}
		if !sess.established && st.active == p.SessionID {
			// (Re-)establish the session before its evidence: the threads
			// domain accepts live statuses only for a conversation some
			// terminal holds, and the secret+session agreement is exactly
			// that proof. On failure the event is dropped and the next one
			// retries — a transient error must not silence the session.
			// A displaced session never re-establishes here: that would
			// move the active thread on delayed evidence.
			if !h.observe(ctx, terminalID, p, "") {
				return http.StatusNoContent
			}
			sess.established = true
		}
		h.reduce(ctx, sess, p)
	}
	return http.StatusNoContent
}

// sessionStart moves the terminal's active session — the one transition
// for every path that changes the active thread: fresh launch, in-TUI
// /new, /resume, /fork, and shell resume (all proven in ATC-281). The
// source field distinguishes resume from startup but startup reveals no
// fresh-vs-fork lineage, so none is recorded and every path lands the
// same way. The registry holds the launch's lock.
func (h *Hooks) sessionStart(ctx context.Context, terminalID string, st *terminalState, sess *session, p payload) int {
	if p.SessionID == st.active && sess != nil && !sess.ended {
		// The same session re-announced: identity and reducer state stand
		// — an active turn may well continue, so no idle claim.
		sess.established = h.observe(ctx, terminalID, p, "")
		return http.StatusNoContent
	}
	if sess == nil || sess.ended {
		sess = &session{tracker: newTracker()}
		st.sessions[p.SessionID] = sess
	} else {
		// Switching back to a conversation this TUI already touched: at
		// its prompt again, but the interrupt latch survives — an
		// interrupted turn's straggling tool may still finish late.
		sess.tracker.root = api.ThreadIdle
	}
	st.active = p.SessionID
	sess.established = h.observe(ctx, terminalID, p, api.ThreadIdle)
	return http.StatusNoContent
}

// sessionEnd tombstones one session, keyed by session_id: one TUI exit
// emits an end for every session it touched, never just the active one.
// Only the active session's end deactivates the terminal; a displaced
// session's live status was already coerced when its successor took the
// terminal. The tombstone is the whole record — a SessionStart replaces
// the session outright, so nothing else needs resetting. The registry
// holds the launch's lock.
func (h *Hooks) sessionEnd(ctx context.Context, terminalID string, st *terminalState, sess *session, p payload) int {
	if sess == nil {
		// An end for a session this process never saw (post-restart exit):
		// honor it only for a conversation mapped to this same terminal.
		if !h.mappedHere(terminalID, p.SessionID) {
			return http.StatusBadRequest
		}
		sess = &session{}
		st.sessions[p.SessionID] = sess
	}
	sess.ended = true
	if st.active == p.SessionID {
		st.active = ""
		// End of observation: the threads domain keeps idle and coerces
		// unverifiable live states — never an idle claim the evidence
		// does not back.
		h.threads.Deactivate(ctx, terminalID)
	}
	return http.StatusNoContent
}

// mappedHere reports whether the identity mapping ties the conversation
// to this same terminal.
func (h *Hooks) mappedHere(terminalID, providerID string) bool {
	_, mapped, known := h.threads.LookupIdentity("codex", providerID)
	return known && mapped == terminalID
}

// observe records a session observation for the terminal, reporting
// success. status "" keeps whatever the thread already shows (a seed or
// re-announce must not claim idle for a possibly mid-turn conversation).
func (h *Hooks) observe(ctx context.Context, terminalID string, p payload, status api.ThreadStatus) bool {
	terminal, err := h.terminals.Get(terminalID)
	if err != nil {
		// The terminal vanished mid-flight; there is nothing honest to
		// record the conversation against.
		h.logger.Warn("hook event for a missing terminal dropped", "terminal", terminalID)
		return false
	}
	threadID, err := h.threads.ObserveSession(ctx, threads.SessionObservation{
		Agent:      "codex",
		ProviderID: p.SessionID,
		TerminalID: terminalID,
		ProjectID:  terminal.ProjectID,
		At:         h.now(),
		Status:     status,
		Metadata:   metadataFrom(p),
	})
	if err != nil || threadID == "" {
		h.logger.Warn("recording session observation", "terminal", terminalID, "error", err)
		return false
	}
	return true
}

// reduce runs one ordinary event through the session's reducer and
// forwards the resulting evidence. A displaced session's live statuses
// are ignored downstream — the threads domain accepts them only for the
// conversation a terminal holds. The registry holds the launch's lock.
func (h *Hooks) reduce(ctx context.Context, sess *session, p payload) {
	status, signal, lastError := sess.tracker.apply(p)
	if !signal {
		return
	}
	metadata := metadataFrom(p)
	if p.HookEventName == "UserPromptSubmit" {
		// The first-prompt title fallback: an observed title only ever
		// fills an untitled thread, so sending it on every prompt is safe.
		metadata.Title = agents.CondenseTitle(p.Prompt)
	}
	if err := h.threads.ObserveStatus(ctx, threads.StatusObservation{
		Agent: "codex", ProviderID: p.SessionID, At: h.now(),
		Status: status, LastError: lastError, Metadata: metadata,
	}); err != nil {
		h.logger.Warn("recording status observation", "error", err)
	}
}

func metadataFrom(p payload) threads.Metadata {
	return threads.Metadata{
		Cwd:            p.Cwd,
		Model:          p.Model,
		PermissionMode: p.PermissionMode,
	}
}
