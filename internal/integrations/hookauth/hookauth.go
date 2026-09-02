// Package hookauth is the authentication plumbing for agent hook ingest
// (ATC-278): a per-launch secret minted at launch, carried
// to hook subprocesses through a 0600 header file (curl -H @file, so the
// secret never appears in argv or a URL), and validated on an internal
// ingest route that sits outside bearer auth. Each agent package owns its
// route, payload shape, and lifecycle policy; this package owns the parts
// that must not drift between them — secret minting, header files, the
// launch registry, and the bounded ingest handler shell.
package hookauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// SecretHeader carries the per-launch hook secret on ingest requests and
// in the header file's single line.
const SecretHeader = "x-atc-hook-secret"

// maxPayloadBytes bounds one hook payload read.
const maxPayloadBytes = 1 << 20

// registration binds one launch's secret to its terminal, plus the
// agent's own per-launch state.
type registration[S any] struct {
	mu         sync.Mutex
	terminalID string
	state      S

	secret string
}

// Registry owns a hook directory and the launch registrations living in
// it: one <terminal-id>.header file per launch, mode 0600, holding the
// "<SecretHeader>: <secret>" line the hook command rides in.
type Registry[S any] struct {
	dir    string
	logger *slog.Logger

	mu         sync.Mutex
	bySecret   map[string]*registration[S]
	byTerminal map[string]*registration[S]
}

// NewRegistry prepares the hook directory and an empty registry. The
// directory is private: its files grant status-injection on the user's
// threads. MkdirAll's mode only applies on creation, so a pre-existing
// permissive directory is tightened.
func NewRegistry[S any](dir string, logger *slog.Logger) (*Registry[S], error) {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return nil, err
	}
	return &Registry[S]{
		dir:        dir,
		logger:     logger,
		bySecret:   map[string]*registration[S]{},
		byTerminal: map[string]*registration[S]{},
	}, nil
}

// HeaderPath is the launch's header file, keyed by terminal id.
func (r *Registry[S]) HeaderPath(terminalID string) string {
	return filepath.Join(r.dir, terminalID+".header")
}

// Prepare mints this launch's secret, writes the header file, and
// registers the secret for the terminal, returning the header path. Files
// are keyed by terminal id, so a re-run for the same id replaces them and
// the earlier secret stops validating.
func (r *Registry[S]) Prepare(terminalID string) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	path := r.HeaderPath(terminalID)
	if err := WritePrivateFile(path, []byte(SecretHeader+": "+secret)); err != nil {
		return "", err
	}
	r.mu.Lock()
	r.register(&registration[S]{terminalID: terminalID, secret: secret})
	r.mu.Unlock()
	return path, nil
}

// register indexes a registration, dropping any earlier one for the same
// terminal (a new launch's secret invalidates the old). Callers hold mu.
func (r *Registry[S]) register(reg *registration[S]) {
	if previous, ok := r.byTerminal[reg.terminalID]; ok {
		delete(r.bySecret, previous.secret)
	}
	r.bySecret[reg.secret] = reg
	r.byTerminal[reg.terminalID] = reg
}

// Deliver resolves a delivery's secret and runs the agent's deliver
// callback against the launch's state, returning 404 for a secret no
// registration owns. One launch's deliveries are serialized end to end —
// validation, reduction, and observation move as one unit, so a
// concurrent lifecycle event cannot reset agent state under an in-flight
// delivery or reorder observations. Ownership is re-verified under the
// registration's lock: a delivery that looked the registration up just
// before a replacement must not mutate lifecycle state the replacement
// now owns.
func (r *Registry[S]) Deliver(secret string, deliver func(terminalID string, state *S) int) int {
	r.mu.Lock()
	reg, ok := r.bySecret[secret]
	r.mu.Unlock()
	if !ok {
		return http.StatusNotFound
	}
	reg.mu.Lock()
	defer reg.mu.Unlock()
	r.mu.Lock()
	current := r.bySecret[secret] == reg
	r.mu.Unlock()
	if !current {
		return http.StatusNotFound
	}
	return deliver(reg.terminalID, &reg.state)
}

// Deregister drops a terminal's registration and header file: the
// terminal was deleted, so its secret must stop validating now rather
// than at the next boot's cleanup. It is a revocation barrier — it
// returns only after any in-flight delivery for the launch has finished,
// so once the caller regains control no delivery can mutate state on the
// launch's behalf. Unknown terminals are a no-op.
func (r *Registry[S]) Deregister(terminalID string) {
	r.mu.Lock()
	reg, ok := r.byTerminal[terminalID]
	if ok {
		delete(r.byTerminal, terminalID)
		delete(r.bySecret, reg.secret)
	}
	r.mu.Unlock()
	if ok {
		// Acquiring the registration's lock is the barrier: a delivery
		// that resolved the secret before the removal still holds it, and
		// no later one can start — the secret no longer resolves. Taken
		// after r.mu is released; Deliver nests r.mu inside reg.mu, never
		// the reverse.
		reg.mu.Lock()
		defer reg.mu.Unlock()
	}
	_ = os.Remove(r.HeaderPath(terminalID))
}

// Load rebuilds the registry from the hook directory at boot, so TUIs
// launched by an earlier server process keep validating. Files keep
// rejects are launch leftovers (deleted terminals, another agent's
// abandoned candidates) and are removed, along with whatever extra
// per-launch files remove cleans up. Unreadable files are kept and logged
// — transient errors must not revoke a live launch. Agent session
// bindings are not persisted: the agent re-seeds from its first payload.
func (r *Registry[S]) Load(keep func(terminalID string) bool, remove func(terminalID string)) error {
	entries, err := os.ReadDir(r.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		terminalID, ok := strings.CutSuffix(entry.Name(), ".header")
		if !ok || entry.IsDir() {
			continue
		}
		discard := func() {
			_ = os.Remove(r.HeaderPath(terminalID))
			if remove != nil {
				remove(terminalID)
			}
		}
		if !keep(terminalID) {
			discard()
			continue
		}
		content, err := os.ReadFile(r.HeaderPath(terminalID))
		if err != nil {
			r.logger.Warn("unreadable hook secret", "terminal", terminalID, "error", err)
			continue
		}
		secret, ok := strings.CutPrefix(string(content), SecretHeader+": ")
		if !ok || secret == "" {
			discard()
			continue
		}
		r.mu.Lock()
		r.register(&registration[S]{terminalID: terminalID, secret: secret})
		r.mu.Unlock()
	}
	return nil
}

// Handler is the ingest endpoint shell: method check, bounded read, and
// the deliver callback deciding the status. Responses deliberately say
// nothing about why a delivery was refused: 404 for an unknown secret,
// 400 for a payload that cannot be honored, 204 for accepted.
func Handler(deliver func(ctx context.Context, secret string, body []byte) int) http.Handler {
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
		w.WriteHeader(deliver(r.Context(), secret, body))
	})
}

func newSecret() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// WritePrivateFile writes content at mode 0600 and enforces the mode on a
// pre-existing file too — os.WriteFile applies its mode only on creation.
func WritePrivateFile(path string, content []byte) error {
	if err := os.WriteFile(path, content, 0o600); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}
