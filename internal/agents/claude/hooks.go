package claude

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/jeremytondo/atc/internal/agents"
	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/threads"
)

// SecretHeader carries the per-launch hook secret. It rides an HTTP
// header sourced from a 0600 file (curl -H @file), so the secret never
// appears in argv or a URL.
const SecretHeader = "x-atc-hook-secret"

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

// maxPayloadBytes bounds one hook payload read.
const maxPayloadBytes = 1 << 20

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

	mu         sync.Mutex
	bySecret   map[string]*registration
	byTerminal map[string]*registration
}

// registration binds one launch's secret to its terminal. sessionID is
// the provider session the terminal currently has open, learned only from
// SessionStart (or the post-restart seed check); a payload whose session
// disagrees with it is dropped. mu serializes delivery per launch end to
// end — validation, reduction, and observation move as one unit, so a
// concurrent SessionStart cannot reset the tracker under an in-flight
// event or reorder observations.
type registration struct {
	mu         sync.Mutex
	secret     string
	terminalID string
	sessionID  string
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
	// Private: the files grant status-injection on the user's threads.
	// MkdirAll's mode only applies on creation, so a pre-existing
	// permissive directory is tightened.
	if err := os.MkdirAll(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(opts.Dir, 0o700); err != nil {
		return nil, err
	}
	return &Hooks{
		dir:        opts.Dir,
		baseURL:    strings.TrimSuffix(opts.BaseURL, "/"),
		threads:    opts.Threads,
		terminals:  opts.Terminals,
		logger:     opts.Logger,
		now:        opts.Now,
		bySecret:   map[string]*registration{},
		byTerminal: map[string]*registration{},
	}, nil
}

func (h *Hooks) settingsPath(terminalID string) string {
	return filepath.Join(h.dir, terminalID+".json")
}

func (h *Hooks) headerPath(terminalID string) string {
	return filepath.Join(h.dir, terminalID+".header")
}

// Prepare mints this launch's secret, writes the header and settings
// files (0600), and registers the secret for the terminal. It returns the
// settings path for the --settings flag. Files are keyed by terminal id,
// so a re-run for the same id simply replaces them.
func (h *Hooks) Prepare(terminalID string) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	header := SecretHeader + ": " + secret
	if err := writePrivateFile(h.headerPath(terminalID), []byte(header)); err != nil {
		return "", err
	}
	settings, err := json.Marshal(hookSettings(h.command(terminalID)))
	if err != nil {
		return "", err
	}
	if err := writePrivateFile(h.settingsPath(terminalID), settings); err != nil {
		return "", err
	}
	h.mu.Lock()
	h.register(&registration{secret: secret, terminalID: terminalID})
	h.mu.Unlock()
	return h.settingsPath(terminalID), nil
}

// register indexes a registration, dropping any earlier one for the same
// terminal (a new launch's secret invalidates the old). Callers hold mu.
func (h *Hooks) register(reg *registration) {
	if previous, ok := h.byTerminal[reg.terminalID]; ok {
		delete(h.bySecret, previous.secret)
	}
	h.bySecret[reg.secret] = reg
	h.byTerminal[reg.terminalID] = reg
}

// command is the hook command line: curl POSTing the payload from stdin,
// with the secret ridden in from the header file — never argv, never the
// URL. Paths are shell-quoted; Claude runs the command through a shell.
func (h *Hooks) command(terminalID string) string {
	return "curl -fsS -m 5 -X POST -H 'Content-Type: application/json' -H @" +
		agents.Quote(h.headerPath(terminalID)) + " --data-binary @- " + agents.Quote(h.baseURL+HooksPath)
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

// LoadRegistrations rebuilds the secret registry from the hook directory
// at boot, so TUIs launched by an earlier server process keep validating.
// Files whose terminal no longer exists are launch leftovers (deleted
// terminals, abandoned launch candidates) and are removed. Session
// bindings are not persisted: the first payload after a restart re-seeds
// through the identity mapping or the next SessionStart.
func (h *Hooks) LoadRegistrations() error {
	entries, err := os.ReadDir(h.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".header") {
			continue
		}
		terminalID := strings.TrimSuffix(name, ".header")
		remove := func() {
			_ = os.Remove(h.headerPath(terminalID))
			_ = os.Remove(h.settingsPath(terminalID))
		}
		terminal, err := h.terminals.Get(terminalID)
		if err != nil || terminal.Agent != "claude" {
			remove()
			continue
		}
		content, err := os.ReadFile(h.headerPath(terminalID))
		if err != nil {
			h.logger.Warn("unreadable hook secret", "terminal", terminalID, "error", err)
			continue
		}
		secret, ok := strings.CutPrefix(string(content), SecretHeader+": ")
		if !ok || secret == "" {
			remove()
			continue
		}
		h.mu.Lock()
		h.register(&registration{secret: secret, terminalID: terminalID})
		h.mu.Unlock()
	}
	return nil
}

// payload is the slice of a hook event the reducer reads; everything else
// in the POST body is ignored. Pointer slices distinguish an absent level
// snapshot from an empty one — an empty background_tasks array is the
// authoritative "no background work".
type payload struct {
	SessionID        string             `json:"session_id"`
	HookEventName    string             `json:"hook_event_name"`
	AgentID          string             `json:"agent_id"`
	TaskID           string             `json:"task_id"`
	BackgroundTasks  *[]task            `json:"background_tasks"`
	SessionCrons     *[]json.RawMessage `json:"session_crons"`
	NotificationType string             `json:"notification_type"`
	ToolName         string             `json:"tool_name"`
	Prompt           string             `json:"prompt"`
	Reason           string             `json:"reason"`
	Cwd              string             `json:"cwd"`
	PermissionMode   string             `json:"permission_mode"`
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
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		// The route sits outside bearer auth, so an unknown peer must not
		// get to hold a handler open by trickling a body: bounded bytes
		// and a bounded read window.
		_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(10 * time.Second))
		secret := r.Header.Get(SecretHeader)
		body, err := io.ReadAll(io.LimitReader(r.Body, maxPayloadBytes))
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.WriteHeader(h.deliver(r.Context(), secret, body))
	})
}

// deliver validates and applies one hook payload, returning the response
// status.
func (h *Hooks) deliver(ctx context.Context, secret string, body []byte) int {
	var p payload
	if err := json.Unmarshal(body, &p); err != nil || p.SessionID == "" || p.HookEventName == "" {
		return http.StatusBadRequest
	}

	h.mu.Lock()
	reg, ok := h.bySecret[secret]
	h.mu.Unlock()
	if !ok {
		return http.StatusNotFound
	}

	// One launch's events are serialized end to end from here.
	reg.mu.Lock()
	defer reg.mu.Unlock()

	// Re-verified under the registration lock: a delivery that looked the
	// registration up just before a replacement must not mutate lifecycle
	// state the replacement now owns.
	h.mu.Lock()
	current := h.bySecret[secret] == reg
	h.mu.Unlock()
	if !current {
		return http.StatusNotFound
	}

	if p.HookEventName != "SessionStart" {
		if reg.ended {
			// The session ended; stragglers are dropped rather than
			// re-seeded. The next SessionStart opens the next chapter.
			return http.StatusBadRequest
		}
		if reg.sessionID == "" {
			// Post-restart seed: accept evidence only for a conversation the
			// identity mapping already ties to this same terminal.
			_, terminalID, known := h.threads.LookupIdentity("claude", p.SessionID)
			if !known || terminalID != reg.terminalID {
				return http.StatusBadRequest
			}
			reg.sessionID = p.SessionID
			reg.tracker = seededTracker()
			reg.established = false
		} else if p.SessionID != reg.sessionID {
			// A payload whose session disagrees with the registration is
			// dropped — delayed evidence from a conversation this terminal
			// no longer displays, or a forgery.
			return http.StatusBadRequest
		}
	}

	switch p.HookEventName {
	case "SessionStart":
		reg.ended = false
		if p.SessionID != reg.sessionID {
			// A genuinely new conversation: fresh reducer, at its prompt.
			reg.sessionID = p.SessionID
			reg.tracker = newTracker()
			reg.established = h.observe(ctx, reg.terminalID, p, api.ThreadIdle)
		} else {
			// The same session re-announced (compact): identity and
			// reducer state stand — an active turn may well continue, so
			// no idle claim.
			reg.established = h.observe(ctx, reg.terminalID, p, "")
		}
	case "SessionEnd":
		h.sessionEnd(ctx, reg, p)
	default:
		if !reg.established {
			// (Re-)establish the session before its evidence: the threads
			// domain accepts live statuses only for a conversation some
			// terminal holds, and the secret+session agreement is exactly
			// that proof. On failure the event is dropped and the next one
			// retries — a transient error must not silence the session.
			if !h.observe(ctx, reg.terminalID, p, "") {
				return http.StatusNoContent
			}
			reg.established = true
		}
		h.reduce(ctx, reg, p)
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
// the conversation without a successor, so the terminal deactivates.
// Caller holds reg.mu.
func (h *Hooks) sessionEnd(ctx context.Context, reg *registration, p payload) {
	reg.tracker = nil
	reg.ended = true
	if err := h.threads.ObserveStatus(ctx, threads.StatusObservation{
		Agent: "claude", ProviderID: p.SessionID, At: h.now(),
		Status: api.ThreadIdle, Metadata: metadataFrom(p),
	}); err != nil {
		h.logger.Warn("recording session end", "terminal", reg.terminalID, "error", err)
	}
	if p.Reason != "clear" && p.Reason != "resume" {
		h.threads.Deactivate(ctx, reg.terminalID)
	}
}

// reduce runs one ordinary event through the session's reducer and
// forwards the resulting evidence. Caller holds reg.mu.
func (h *Hooks) reduce(ctx context.Context, reg *registration, p payload) {
	if reg.tracker == nil {
		reg.tracker = seededTracker()
	}
	status, signal, lastError := reg.tracker.apply(p)
	if !signal {
		return
	}
	metadata := metadataFrom(p)
	if p.HookEventName == "UserPromptSubmit" && p.AgentID == "" {
		// The first-prompt title fallback: an observed title only ever
		// fills an untitled thread, so sending it on every prompt is safe.
		metadata.Title = titleFrom(p.Prompt)
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

// titleFrom condenses a first prompt into an observed default title,
// cutting on a word boundary, else a rune boundary — never mid-character.
func titleFrom(prompt string) string {
	const limit = 50
	title := strings.Join(strings.Fields(prompt), " ")
	if len(title) <= limit {
		return title
	}
	if cut := strings.LastIndexByte(title[:limit], ' '); cut > 0 {
		return title[:cut]
	}
	cut := limit
	for cut > 0 && !utf8.RuneStart(title[cut]) {
		cut--
	}
	return title[:cut]
}

func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// writePrivateFile writes content at mode 0600 and enforces the mode on a
// pre-existing file too — os.WriteFile applies its mode only on creation.
func writePrivateFile(path string, content []byte) error {
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
