// Package t3code is T3 Code's Integration (ATC-294, ATC-285): a
// read-only mirror of the threads a local T3 Code environment owns. T3
// stays the source of truth — ATC never starts, prompts, approves,
// archives, or otherwise mutates a T3 thread — and its threads appear in
// ATC's normal thread list as ordinary records with near-real-time status
// and deep links back into T3's Apps (its web UI and desktop app), which
// are handoff Apps: the server never launches them, and no thread records
// which one started it.
//
// The Integration is always on and self-discovering: it finds the local
// server through T3's runtime state file, pairs with it zero-touch (a
// pairing grant minted by T3's own CLI, exchanged for a session scoped to
// exactly orchestration:read, persisted 0600 under ATC's data dir), and
// keeps one long-lived subscription to T3's shell projection over its
// Effect RPC WebSocket. There is no configuration and no remote origin:
// the only T3 it ever talks to is on this machine. The id is persisted on
// every thread it produces — never rename it.
package t3code

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"

	"github.com/jeremytondo/atc/internal/api"
	"github.com/jeremytondo/atc/internal/integrations"
)

// ID is the Integration id and the identity namespace of every T3 thread.
const ID = "t3code"

// agents are T3's provider driver kinds as T3 names them — the agent ids
// its threads report, opaque to ATC — with display names. A kind T3 adds
// later is recorded on threads as reported whether or not it is listed.
var agents = []api.IntegrationAgent{
	{ID: "claudeAgent", Name: "Claude Code"},
	{ID: "codex", Name: "Codex"},
	{ID: "cursor", Name: "Cursor"},
	{ID: "grok", Name: "Grok"},
	{ID: "opencode", Name: "OpenCode"},
}

// Integration is T3 Code's catalog registration: the agents T3 drives,
// its two handoff Apps, and its live connection. It launches nothing —
// a T3 conversation opens in T3, through the thread's links.
func Integration(observer *Observer) integrations.Integration {
	if observer == nil {
		panic("t3code.Integration: observer must not be nil")
	}
	agentIDs := make([]string, 0, len(agents))
	for _, agent := range agents {
		agentIDs = append(agentIDs, agent.ID)
	}
	return integrations.Integration{
		ID:           ID,
		Name:         "T3 Code",
		Capabilities: []api.IntegrationCapability{api.CapabilityThreadObservation},
		Agents:       agents,
		Apps: []integrations.App{
			{ID: "web", Name: "T3 Code (web)", Agents: agentIDs, Handoff: true},
			{ID: "desktop", Name: "T3 Code (desktop)", Agents: agentIDs, Handoff: true},
		},
		Connection: observer.Connection,
	}
}

// Home resolves the T3 home every T3 process on this machine uses: T3's
// own variable, never an ATC setting. Always absolute.
func Home() (string, error) {
	if home := os.Getenv("T3CODE_HOME"); home != "" {
		return filepath.Abs(home)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".t3"), nil
}

// runtime is T3's server runtime state: written by the running
// environment server with its origin and pid, the one discovery input.
type runtime struct {
	PID    int    `json:"pid"`
	Origin string `json:"origin"`
}

func runtimeFile(home string) string {
	return filepath.Join(home, "userdata", "server-runtime.json")
}

func serviceStateFile(home string) string {
	return filepath.Join(home, "runtime", "service-state.json")
}

// errNotRunning marks discovery finding no live T3 server: not installed,
// no runtime file, or a runtime file whose process is gone. It is the
// quiet, expected state — never logged.
var errNotRunning = errors.New("T3 Code is not running")

// discover reads the runtime file and checks its process is alive.
func discover(home string, alive func(pid int) bool) (runtime, error) {
	data, err := os.ReadFile(runtimeFile(home))
	if errors.Is(err, fs.ErrNotExist) {
		return runtime{}, fmt.Errorf("%w: no runtime state at %s", errNotRunning, runtimeFile(home))
	}
	if err != nil {
		return runtime{}, err
	}
	var state runtime
	if err := json.Unmarshal(data, &state); err != nil || state.Origin == "" || state.PID <= 0 {
		return runtime{}, fmt.Errorf("T3 Code runtime state at %s is not readable", runtimeFile(home))
	}
	if !alive(state.PID) {
		return runtime{}, fmt.Errorf("%w: process %d recorded at %s is gone", errNotRunning, state.PID, runtimeFile(home))
	}
	return state, nil
}

// processAlive is the production liveness check: signal 0 reaches a
// live process (or one we may not signal, which is still alive).
func processAlive(pid int) bool {
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

// cliPath resolves the versioned T3 CLI entrypoint the service runner
// keeps under the T3 home, from the active version it records. Only
// pairing uses the CLI; the data path is HTTP and WebSocket.
func cliPath(home string) (string, error) {
	data, err := os.ReadFile(serviceStateFile(home))
	if err != nil {
		return "", fmt.Errorf("T3 Code CLI not found: %w", err)
	}
	var state struct {
		ActiveVersion string `json:"activeVersion"`
	}
	if err := json.Unmarshal(data, &state); err != nil || state.ActiveVersion == "" {
		return "", fmt.Errorf("T3 Code CLI not found: %s records no active version", serviceStateFile(home))
	}
	bin := filepath.Join(home, "runtime", "versions", state.ActiveVersion, "node_modules", "t3", "dist", "bin.mjs")
	if _, err := os.Stat(bin); err != nil {
		return "", fmt.Errorf("T3 Code CLI not found: %w", err)
	}
	return bin, nil
}

// links are the deep links into T3 for one thread — its handoff Apps:
// the web UI served by the environment (t3code/web), and the desktop
// app's URL scheme (t3code/desktop).
func links(origin, environmentID, threadID string) *api.ThreadLinks {
	if origin == "" || environmentID == "" {
		return nil
	}
	return &api.ThreadLinks{
		Web: origin + "/" + environmentID + "/" + threadID,
		App: "t3code://threads/" + environmentID + "/" + threadID,
	}
}
