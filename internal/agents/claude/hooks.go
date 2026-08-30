package claude

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
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
const HooksPath = "/internal/claude/hooks"

// hookEvents are the lifecycle hooks every launch wires (the legacy
// product's proven set). Each POSTs its full payload to HooksPath; the
// reducer reads what it understands and ignores the rest.
var hookEvents = []string{
	"SessionStart", "SessionEnd", "UserPromptSubmit", "PreToolUse", "PostToolUse",
	"PostToolUseFailure", "Stop", "StopFailure", "SubagentStart", "SubagentStop",
	"TaskCreated", "TaskCompleted", "Notification", "PermissionRequest", "PermissionDenied",
}

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
	// Dir is where the per-launch settings and secret files live
	// (paths.HookDir), created 0700.
	Dir string
	// BaseURL is where hooks POST, e.g. "http://127.0.0.1:4779" — always
	// loopback: the hook runs on the server's own machine.
	BaseURL   string
	Threads   ThreadObserver
	Terminals TerminalReader
	Logger    *slog.Logger
	Now       func() time.Time
}

// Hooks owns Claude's thread evidence: per-launch secret registrations,
// the settings files injected into launches, the internal ingest
// endpoint, and the per-session reducers. Events run through a stateful
// reducer — individual hook events are not a state machine.
type Hooks struct {
	dir       string
	baseURL   string
	threads   ThreadObserver
	terminals TerminalReader
	logger    *slog.Logger
	now       func() time.Time

	registry *hookauth.Registry[session]
}

// session is one launch's lifecycle state, mutated only under the
// registry's per-launch lock. sessionID is the provider session the
// terminal currently has open,
// learned only from SessionStart (or the post-restart seed check); a
// payload whose session disagrees with it is dropped.
type session struct {
	sessionID string
	// established records that the threads domain has accepted the
	// session observation. A transient failure leaves it false, and the
	// next event retries instead of silently dropping the whole session.
	established bool
	// ended records a SessionEnd: stragglers for the departed session are
	// dropped rather than re-seeded, until the next SessionStart.
	ended   bool
	tracker *tracker
}

// NewHooks prepares the hook directory and an empty registry. Call
// LoadRegistrations at boot so launches that predate this server process
// keep validating.
func NewHooks(opts HooksOptions) (*Hooks, error) {
	if opts.Threads == nil || opts.Terminals == nil {
		panic("claude.NewHooks: Threads and Terminals must not be nil")
	}
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	registry, err := hookauth.NewRegistry[session](opts.Dir, opts.Logger)
	if err != nil {
		return nil, err
	}
	return &Hooks{
		dir:       opts.Dir,
		baseURL:   strings.TrimSuffix(opts.BaseURL, "/"),
		threads:   opts.Threads,
		terminals: opts.Terminals,
		logger:    opts.Logger,
		now:       opts.Now,
		registry:  registry,
	}, nil
}

func (h *Hooks) settingsPath(terminalID string) string {
	return filepath.Join(h.dir, terminalID+".json")
}

// Prepare mints this launch's secret, writes the header and settings
// files (0600), and registers the secret for the terminal. It returns the
// settings path for the --settings flag. Files are keyed by terminal id,
// so a re-run for the same id simply replaces them.
func (h *Hooks) Prepare(terminalID string) (string, error) {
	headerPath, err := h.registry.Prepare(terminalID)
	if err != nil {
		return "", err
	}
	settings, err := json.Marshal(hookSettings(h.command(headerPath)))
	if err != nil {
		return "", err
	}
	if err := hookauth.WritePrivateFile(h.settingsPath(terminalID), settings); err != nil {
		return "", err
	}
	return h.settingsPath(terminalID), nil
}

// command is the hook command line: curl POSTing the payload from stdin,
// with the secret ridden in from the header file — never argv, never the
// URL. Paths are shell-quoted; Claude runs the command through a shell.
func (h *Hooks) command(headerPath string) string {
	return "curl -fsS -m 5 -X POST -H 'Content-Type: application/json' -H @" +
		agents.Quote(headerPath) + " --data-binary @- " + agents.Quote(h.baseURL+HooksPath)
}

// hookSettings is the --settings document: every lifecycle event wired to
// the one command.
func hookSettings(command string) map[string]any {
	hook := []map[string]any{{
		"hooks": []map[string]any{{"type": "command", "command": command}},
	}}
	events := make(map[string]any, len(hookEvents))
	for _, event := range hookEvents {
		events[event] = hook
	}
	return map[string]any{"hooks": events}
}

// Deregister drops a deleted terminal's secret and per-launch files —
// wired by the composition root to the terminal delete, so the secret
// stops validating immediately rather than at the next boot's cleanup.
func (h *Hooks) Deregister(terminalID string) {
	h.registry.Deregister(terminalID)
	_ = os.Remove(h.settingsPath(terminalID))
}

// LoadRegistrations rebuilds the secret registry from the hook directory
// at boot, so TUIs launched by an earlier server process keep validating.
// Files whose terminal no longer exists are launch leftovers (deleted
// terminals, abandoned launch candidates) and are removed. Session
// bindings are not persisted: the first payload after a restart re-seeds
// through the identity mapping or the next SessionStart.
func (h *Hooks) LoadRegistrations() error {
	return h.registry.Load(func(terminalID string) bool {
		terminal, err := h.terminals.Get(terminalID)
		return err == nil && terminal.Agent == "claude"
	}, func(terminalID string) {
		_ = os.Remove(h.settingsPath(terminalID))
	})
}

// payload is the slice of a hook event the reducer reads; everything else
// in the POST body is ignored. Pointer slices distinguish an absent level
// snapshot from an empty one — an empty background_tasks array is the
// authoritative "no background work".
type payload struct {
	SessionID        string  `json:"session_id"`
	HookEventName    string  `json:"hook_event_name"`
	AgentID          string  `json:"agent_id"`
	TaskID           string  `json:"task_id"`
	BackgroundTasks  *[]task `json:"background_tasks"`
	NotificationType string  `json:"notification_type"`
	ToolName         string  `json:"tool_name"`
	Prompt           string  `json:"prompt"`
	Reason           string  `json:"reason"`
	Cwd              string  `json:"cwd"`
	PermissionMode   string  `json:"permission_mode"`
	Effort           struct {
		Level string `json:"level"`
	} `json:"effort"`
}

type task struct {
	ID string `json:"id"`
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
	return h.registry.Deliver(secret, func(terminalID string, st *session) int {
		return h.apply(ctx, terminalID, st, p)
	})
}

// apply runs one validated payload against the launch's lifecycle state.
// The registry holds the launch's lock.
func (h *Hooks) apply(ctx context.Context, terminalID string, st *session, p payload) int {
	if p.HookEventName != "SessionStart" {
		if st.ended {
			// The session ended; stragglers are dropped rather than
			// re-seeded. The next SessionStart opens the next chapter.
			return http.StatusBadRequest
		}
		if st.sessionID == "" {
			// Post-restart seed: accept evidence only for a conversation the
			// identity mapping already ties to this same terminal, and only
			// from a root UserPromptSubmit — the one event a conversation
			// the TUI does not display can never produce (the Codex gate's
			// reasoning). A displaced session's straggler (a late
			// TaskCompleted, a notification) must not seed the wrong
			// conversation as active and wedge the displayed one behind the
			// session-match check; anything the gate drops is re-covered at
			// the next prompt or SessionStart.
			if p.HookEventName != "UserPromptSubmit" || p.AgentID != "" {
				return http.StatusBadRequest
			}
			_, mapped, known := h.threads.LookupIdentity("claude", p.SessionID)
			if !known || mapped != terminalID {
				return http.StatusBadRequest
			}
			st.sessionID = p.SessionID
			st.tracker = seededTracker()
			st.established = false
		} else if p.SessionID != st.sessionID {
			// A payload whose session disagrees with the registration is
			// dropped — delayed evidence from a conversation this terminal
			// no longer displays, or a forgery.
			return http.StatusBadRequest
		}
	}

	switch p.HookEventName {
	case "SessionStart":
		st.ended = false
		if p.SessionID != st.sessionID {
			// A genuinely new conversation: fresh reducer, at its prompt.
			st.sessionID = p.SessionID
			st.tracker = newTracker()
			st.established = h.observe(ctx, terminalID, p, api.ThreadIdle)
		} else {
			// The same session re-announced (compact): identity and
			// reducer state stand — an active turn may well continue, so
			// no idle claim.
			st.established = h.observe(ctx, terminalID, p, "")
		}
	case "SessionEnd":
		h.sessionEnd(ctx, terminalID, st, p)
	default:
		if !st.established {
			// (Re-)establish the session before its evidence: the threads
			// domain accepts live statuses only for a conversation some
			// terminal holds, and the secret+session agreement is exactly
			// that proof. On failure the event is dropped and the next one
			// retries — a transient error must not silence the session.
			if !h.observe(ctx, terminalID, p, "") {
				return http.StatusNoContent
			}
			st.established = true
		}
		h.reduce(ctx, st, p)
	}
	return http.StatusNoContent
}

// observe records a session observation for the terminal, reporting
// success. status "" keeps whatever the thread already shows (a seed or
// compact must not claim idle for a possibly mid-turn conversation).
func (h *Hooks) observe(ctx context.Context, terminalID string, p payload, status api.ThreadStatus) bool {
	terminal, err := h.terminals.Get(terminalID)
	if err != nil {
		// The terminal vanished mid-flight; there is nothing honest to
		// record the conversation against.
		h.logger.Warn("hook event for a missing terminal dropped", "terminal", terminalID)
		return false
	}
	threadID, err := h.threads.ObserveSession(ctx, threads.SessionObservation{
		Agent:      "claude",
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

// sessionEnd closes the reducer's book on the session. A clear or resume
// is a switch — the successor's SessionStart is already on its way and
// moves the active thread itself; anything else means the TUI is leaving
// the conversation without a successor, so the terminal deactivates. No
// idle claim rides the end: a SessionEnd can land mid-turn (the TUI
// killed while working), so the last evidence stands and the threads
// domain coerces unverifiable live states — never an idle claim the
// evidence does not back. The registry holds the launch's lock.
func (h *Hooks) sessionEnd(ctx context.Context, terminalID string, st *session, p payload) {
	st.tracker = nil
	st.ended = true
	if p.Reason != "clear" && p.Reason != "resume" {
		h.threads.Deactivate(ctx, terminalID)
	}
}

// reduce runs one ordinary event through the session's reducer and
// forwards the resulting evidence. The registry holds the launch's lock.
func (h *Hooks) reduce(ctx context.Context, st *session, p payload) {
	if st.tracker == nil {
		st.tracker = seededTracker()
	}
	status, signal, lastError := st.tracker.apply(p)
	if !signal {
		return
	}
	metadata := metadataFrom(p)
	if p.HookEventName == "UserPromptSubmit" && p.AgentID == "" {
		// The first-prompt title fallback: an observed title only ever
		// fills an untitled thread, so sending it on every prompt is safe.
		metadata.Title = agents.CondenseTitle(p.Prompt)
	}
	if err := h.threads.ObserveStatus(ctx, threads.StatusObservation{
		Agent: "claude", ProviderID: p.SessionID, At: h.now(),
		Status: status, LastError: lastError, Metadata: metadata,
	}); err != nil {
		h.logger.Warn("recording status observation", "error", err)
	}
}

func metadataFrom(p payload) threads.Metadata {
	return threads.Metadata{
		Cwd:            p.Cwd,
		PermissionMode: p.PermissionMode,
		Effort:         p.Effort.Level,
	}
}
