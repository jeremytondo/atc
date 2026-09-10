package zmx

// The managed runtime (ATC-324): which zmx a terminal namespace runs on.
//
// Three things stay distinct. The build's desired version (Desired) is
// what it would activate in an empty namespace. Installed runtimes are
// verified bytes under the runtime directory, one directory per version,
// retained forever. The namespace's active runtime is the one recorded in
// the selection file beside the socket directory — its version, the
// SHA-256 of its executable, and the executable's location — and it is
// the only runtime any server operation or local attachment ever runs.
// Installing changes nothing about the selection; activation is a
// separate, startup-only step that the Driver performs after proving the
// namespace empty (Driver.Startup).
//
// A selection that cannot be read, names a version this build does not
// support, or points at an executable whose identity no longer matches
// makes the runtime unavailable with the reason in status. Nothing
// guesses, resets, or probes with an unvalidated client.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/jeremytondo/atc/internal/api"
)

// selection is the persisted active runtime of one namespace.
type selection struct {
	Version    string `json:"version"`
	Executable string `json:"executable"`
	SHA256     string `json:"sha256"`
}

// readSelection loads the namespace's selection. fs.ErrNotExist means no
// runtime has ever been activated; any other failure, including a file
// that does not parse or lacks a field, is reported as is.
func readSelection(file string) (selection, error) {
	data, err := os.ReadFile(file)
	if err != nil {
		return selection{}, err
	}
	var sel selection
	if err := json.Unmarshal(data, &sel); err != nil {
		return selection{}, fmt.Errorf("%s: %w", file, err)
	}
	if sel.Version == "" || sel.Executable == "" || sel.SHA256 == "" {
		return selection{}, fmt.Errorf("%s: incomplete runtime selection", file)
	}
	return sel, nil
}

// writeSelection commits a selection atomically: the file is complete or
// absent, never partial.
func writeSelection(file string, sel selection) error {
	data, err := json.Marshal(sel)
	if err != nil {
		return err
	}
	tmp := file + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, file); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// ResolveActive is the one resolver for a namespace's active executable,
// shared by the server (through Runtime) and the attaching CLI: the
// selection beside socketDir, validated against its recorded identity.
// It judges only identity, never this build's support table — a newer
// CLI attaches to whatever runtime the running server recorded.
func ResolveActive(selectionFile string) (string, error) {
	sel, err := readSelection(selectionFile)
	if errors.Is(err, fs.ErrNotExist) {
		return "", errors.New("no terminal runtime is active for this namespace: the server activates one at startup; see `atc integration get zmx`")
	}
	if err != nil {
		return "", fmt.Errorf("terminal runtime selection unreadable: %w", err)
	}
	if err := validExecutable(sel.Executable, sel.SHA256); err != nil {
		return "", fmt.Errorf("active zmx %s is unusable: %w; restart the server (`atc server restart`) to reinstall it", sel.Version, err)
	}
	return sel.Executable, nil
}

// RuntimeOptions wires a Runtime.
type RuntimeOptions struct {
	// RuntimeDir is ATC's runtime directory (paths.RuntimeDir); the
	// installed versions live under its zmx subdirectory.
	RuntimeDir string
	// SelectionFile is the namespace's selection (paths.TerminalRuntimeFile).
	SelectionFile string
	Logger        *slog.Logger
	// Releases and Desired override the shipped table and pin; tests
	// inject controlled executables through them. Nil and empty keep the
	// shipped values.
	Releases map[string]Release
	Desired  string
	// DownloadBase overrides where assets are fetched from.
	DownloadBase string
}

// Runtime is the server's view of the namespace's runtime: the validated
// active executable, this build's desire, and what stands between them.
type Runtime struct {
	installDir    string
	selectionFile string
	lockFile      string
	releases      map[string]Release
	desired       string
	downloadBase  string
	logger        *slog.Logger

	mu sync.Mutex
	// active is the validated selection; nil while none is usable.
	active *selection
	// activeSize and activeMod are the active executable's size and
	// modification time when its full identity was last verified. A later
	// resolution re-hashes only when the file has changed, so tampering or
	// replacement is caught without hashing megabytes on every inventory.
	activeSize int64
	activeMod  time.Time
	// problem explains an unusable active runtime: an unreadable
	// selection, an unsupported version, or a missing or damaged
	// executable (the last is repaired at startup).
	problem error
	// unsupported marks problem as a version this build does not
	// validate: the executable stays untouched and nothing runs it.
	unsupported bool
	// repairable marks problem as a damaged active executable: startup
	// reinstalls its exact identity.
	repairable bool
	// installing is the version a startup installation is fetching.
	installing string
	// installErr is this run's startup installation failure; it clears
	// only by restarting.
	installErr error
	// blocked explains why the desired runtime cannot be activated in
	// this run: sessions remain, or the first-rollout cleanup is
	// incomplete.
	blocked string
}

// NewRuntime loads the namespace's selection and validates its executable
// offline. It never installs or activates anything.
func NewRuntime(opts RuntimeOptions) *Runtime {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Releases == nil {
		opts.Releases = releases
	}
	if opts.Desired == "" {
		opts.Desired = Desired
	}
	if opts.DownloadBase == "" {
		opts.DownloadBase = releaseBase
	}
	r := &Runtime{
		installDir:    filepath.Join(opts.RuntimeDir, executableName),
		selectionFile: opts.SelectionFile,
		lockFile:      opts.SelectionFile + ".lock",
		releases:      opts.Releases,
		desired:       opts.Desired,
		downloadBase:  opts.DownloadBase,
		logger:        opts.Logger,
	}
	r.load()
	return r
}

// load reads and validates the selection into the runtime's state.
func (r *Runtime) load() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.active, r.problem, r.unsupported, r.repairable = nil, nil, false, false
	sel, err := readSelection(r.selectionFile)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		r.problem = err
		return
	}
	a, err := asset(r.releases, sel.Version)
	if err != nil {
		r.problem, r.unsupported = err, true
		return
	}
	// The recorded identity must be this build's pinned identity for that
	// version and platform, not merely self-consistent: ATC is the sole
	// authority on the bytes it runs, so a selection pointing at a
	// wrong-architecture or otherwise non-pinned executable is incompatible,
	// never silently trusted.
	if sel.SHA256 != a.ExecutableSHA256 {
		r.problem, r.unsupported = fmt.Errorf("recorded zmx %s does not match this build's pinned identity for %s", sel.Version, target()), true
		return
	}
	if err := validExecutable(sel.Executable, sel.SHA256); err != nil {
		r.problem, r.repairable = err, true
		return
	}
	if info, err := os.Stat(sel.Executable); err == nil {
		r.activeSize, r.activeMod = info.Size(), info.ModTime()
	}
	r.active = &sel
}

// checkActive confirms the active executable still carries the identity it
// was validated with. Callers hold mu. The full hash runs only when the
// file's size or modification time has changed since the last check, so a
// replacement, truncation, or deletion is caught while an unchanged file
// costs one stat.
func (r *Runtime) checkActive() error {
	info, err := os.Stat(r.active.Executable)
	if err != nil {
		return err
	}
	if info.Mode().IsRegular() && info.Mode()&0o100 != 0 && info.Size() == r.activeSize && info.ModTime().Equal(r.activeMod) {
		return nil
	}
	if err := validExecutable(r.active.Executable, r.active.SHA256); err != nil {
		return err
	}
	r.activeSize, r.activeMod = info.Size(), info.ModTime()
	return nil
}

// Executable returns the active runtime's validated executable, or the
// reason there is none. It is cheap — every zmx invocation goes through
// it — and initiates nothing.
func (r *Runtime) Executable() (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return "", r.unavailable()
	}
	if err := r.checkActive(); err != nil {
		return "", fmt.Errorf("active zmx %s became unusable: %w; restart the server (`atc server restart`) to reinstall it", r.active.Version, err)
	}
	return r.active.Executable, nil
}

// unavailable is the actionable refusal while no runtime is usable.
// Callers hold mu.
func (r *Runtime) unavailable() error {
	switch {
	case r.problem == nil && r.installing != "":
		return fmt.Errorf("terminal runtime unavailable: installing zmx %s", r.installing)
	case r.problem == nil && r.installErr != nil:
		return fmt.Errorf("terminal runtime unavailable: %w; restart the server (`atc server restart`) to retry", r.installErr)
	case r.problem == nil && r.blocked != "":
		return fmt.Errorf("terminal runtime unavailable: %s", r.blocked)
	case r.problem == nil:
		return errors.New("terminal runtime unavailable: no zmx is active for this namespace yet")
	case r.unsupported:
		return fmt.Errorf("terminal runtime incompatible: %w; return to an ATC release that supports it, or deliberately end its sessions with a compatible client", r.problem)
	case r.repairable && r.installing != "":
		return fmt.Errorf("terminal runtime unavailable: reinstalling zmx %s", r.installing)
	case r.repairable:
		return fmt.Errorf("terminal runtime unavailable: %w; restart the server (`atc server restart`) to reinstall it", r.problem)
	default:
		return fmt.Errorf("terminal runtime unavailable: %w; ATC will not guess a runtime for this namespace", r.problem)
	}
}

// Available reports whether the active runtime is ready to run.
func (r *Runtime) Available() bool {
	_, ready := r.Report()
	return ready
}

// Status is the wire report: active and desired versions, the executable
// in use, whether the desired version still waits, and why.
func (r *Runtime) Status() api.IntegrationRuntime {
	status, _ := r.Report()
	return status
}

// Report is the single locked snapshot the catalog reads: the wire status
// and whether the runtime is ready to run, decided together so the two can
// never describe different moments (a race the catalog would otherwise
// expose by asking twice).
func (r *Runtime) Report() (api.IntegrationRuntime, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	status := api.IntegrationRuntime{Desired: r.desired}
	if r.active != nil {
		status.Active, status.Executable = r.active.Version, r.active.Executable
	} else if r.problem != nil {
		if sel, err := readSelection(r.selectionFile); err == nil {
			status.Active = sel.Version
		}
	}
	if r.active == nil {
		// No usable runtime, so the desired one is by definition not the
		// running one — always pending, whatever the reason.
		status.Detail = r.unavailable().Error()
		status.Pending = true
		return status, false
	}
	if err := r.checkActive(); err != nil {
		// Recorded and validated at load, but the file has since gone or
		// no longer carries its verified identity.
		status.Pending = true
		status.Detail = fmt.Sprintf("active zmx %s became unusable: %v; restart the server (`atc server restart`) to reinstall it", r.active.Version, err)
		return status, false
	}
	if r.active.Version == r.desired {
		return status, true
	}
	status.Pending = true
	switch {
	case r.installing != "":
		status.Detail = fmt.Sprintf("installing zmx %s; it activates once no sessions remain and the server restarts", r.installing)
	case r.installErr != nil:
		status.Detail = fmt.Sprintf("zmx %s could not be installed: %v; terminals keep running on %s, and the next server restart retries", r.desired, r.installErr, r.active.Version)
	case r.blocked != "":
		status.Detail = r.blocked
	default:
		status.Detail = fmt.Sprintf("zmx %s is installed; switching from %s waits for all sessions to end and the server to restart", r.desired, r.active.Version)
	}
	// A usable older supported runtime keeps terminals available while the
	// desired one is pending.
	return status, true
}

// Desired is this build's pin.
func (r *Runtime) Desired() string { return r.desired }

// activeSelection is the validated selection, if any.
func (r *Runtime) activeSelection() *selection {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active == nil {
		return nil
	}
	sel := *r.active
	return &sel
}

// selectionProblem is the reason no runtime is usable that is not a
// repairable executable — an unreadable or unsupported selection — or nil
// when there is a usable runtime, none is recorded, or one only needs
// reinstalling.
func (r *Runtime) selectionProblem() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil || r.repairable {
		return nil
	}
	return r.problem
}

// needsRepair reports the recorded runtime whose executable is missing
// or damaged, if that is the runtime's problem.
func (r *Runtime) needsRepair() (selection, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.repairable {
		return selection{}, false
	}
	sel, err := readSelection(r.selectionFile)
	return sel, err == nil
}

func (r *Runtime) setInstalling(version string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.installing = version
}

func (r *Runtime) setInstallErr(err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.installing, r.installErr = "", err
}

func (r *Runtime) setBlocked(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blocked = reason
}

// holdActive takes the shared activation lock: while any holder exists,
// the selection cannot change. Session creation holds it from resolving
// the runtime until the session is visible, so an emptiness proof taken
// under the exclusive lock cannot miss a session being born.
func (r *Runtime) holdActive(ctx context.Context) (func(), error) {
	return lock(ctx, r.lockFile, syscall.LOCK_SH)
}

// activate commits sel as the namespace's runtime, under the exclusive
// activation lock; empty proves the namespace has nothing running, and
// it runs with the lock held so nothing can be born between the proof
// and the commit. A refusal names the reason and changes nothing.
func (r *Runtime) activate(ctx context.Context, sel selection, empty func(ctx context.Context) (string, error)) error {
	if err := validExecutable(sel.Executable, sel.SHA256); err != nil {
		return err
	}
	unlock, err := lock(ctx, r.lockFile, syscall.LOCK_EX)
	if err != nil {
		return fmt.Errorf("waiting for in-flight terminal creation: %w", err)
	}
	defer unlock()
	reason, err := empty(ctx)
	if err != nil {
		return fmt.Errorf("cannot prove the terminal namespace empty: %w", err)
	}
	if reason != "" {
		return errors.New(reason)
	}
	if err := writeSelection(r.selectionFile, sel); err != nil {
		return err
	}
	r.load()
	r.setBlocked("")
	r.logger.Info("activated zmx", "version", sel.Version, "executable", sel.Executable)
	return nil
}

// selectionFor describes an installed executable as a selection.
func selectionFor(version, executable string) (selection, error) {
	sum, err := hashFile(executable)
	if err != nil {
		return selection{}, err
	}
	abs, err := filepath.Abs(executable)
	if err != nil {
		return selection{}, err
	}
	return selection{Version: version, Executable: abs, SHA256: sum}, nil
}
