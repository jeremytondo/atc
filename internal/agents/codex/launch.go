package codex

import (
	"context"
	"path/filepath"
	"time"

	"github.com/jeremytondo/atc/internal/paths"
)

// A fresh launch is bound to its thread by timing and place: the only
// thread/started that can belong to it is one announced from the launch
// directory, by a terminal-started TUI, inside a short window after the
// launch. Two such announcements are indistinguishable, so the binding
// fails closed — the terminal stays an ordinary terminal and the reason
// is logged — and same-directory launches take turns so they never
// create that ambiguity for each other. Codex ships no client identity
// for launches; until it does, this is the only safe binding.

const (
	// launchWindow bounds how long a launch waits for its announcement.
	launchWindow = 5 * time.Second
	// launchGrace is how long after the first candidate the window stays
	// open for a second one.
	launchGrace = 500 * time.Millisecond
)

// pendingLaunch is one armed launch waiting for its announcement.
type pendingLaunch struct {
	terminalID string
	directory  string
	armedAt    time.Time
	candidates []candidate
	timer      *time.Timer
}

// candidate is one thread/started that matched a pending launch.
type candidate struct {
	threadID string
	cwd      string
	title    string
	source   string
	at       time.Time
}

// canonical is the directory identity both sides of a match are reduced
// to: absolute, symlinks resolved, cleaned (paths.CanonicalDir), with a
// cleaned absolute path for a directory that no longer resolves.
func canonical(path string) string {
	if resolved, err := paths.CanonicalDir(path); err == nil {
		return resolved
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	return filepath.Clean(abs)
}

// reserve takes the directory for one launch, waiting while another
// launch in the same directory is between its preparation and its
// window's close. Launches in different directories never wait.
func (o *Observer) reserve(ctx context.Context, dir string) error {
	for {
		o.mu.Lock()
		busy, ok := o.reserved[dir]
		if !ok {
			o.reserved[dir] = make(chan struct{})
			o.mu.Unlock()
			return nil
		}
		o.mu.Unlock()
		select {
		case <-busy:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// release frees a reserved directory, waking any launch waiting on it.
// Caller holds mu.
func (o *Observer) release(dir string) {
	if busy, ok := o.reserved[dir]; ok {
		delete(o.reserved, dir)
		close(busy)
	}
}

// abandon undoes a prepared launch whose create failed: the pending
// launch, if Command armed one, closes without binding, and the
// directory frees.
func (o *Observer) abandon(dir string) {
	o.mu.Lock()
	launch := o.pending[dir]
	if launch == nil {
		o.release(dir)
	}
	o.mu.Unlock()
	if launch != nil {
		o.closeLaunch(launch, "launch aborted")
	}
}

// arm starts a fresh launch's window (Command time, under the terminals
// commit lock — no IO): announcements from this moment on are
// candidates. The directory is reserved by prepareLaunch; arming without
// one (no LaunchPreparer in the path) reserves it here so the close can
// release it uniformly.
func (o *Observer) arm(terminalID, directory string) {
	dir := canonical(directory)
	launch := &pendingLaunch{terminalID: terminalID, directory: dir, armedAt: o.now()}
	o.mu.Lock()
	if _, ok := o.reserved[dir]; !ok {
		o.reserved[dir] = make(chan struct{})
	}
	o.pending[dir] = launch
	o.mu.Unlock()
	launch.timer = time.AfterFunc(o.window, func() { o.closeLaunch(launch, "") })
}

// announced folds one thread/started into the pending launch for its
// directory, if any. A candidate must come from a terminal-started TUI
// and land inside the window (a pending launch exists only while its
// window is open). The first candidate shortens the window to the
// grace; a second closes it at once — the outcome is decided.
func (o *Observer) announced(ctx context.Context, c candidate) {
	dir := canonical(c.cwd)
	o.mu.Lock()
	launch := o.pending[dir]
	if launch == nil || c.source != "cli" || c.at.Before(launch.armedAt) {
		o.mu.Unlock()
		return
	}
	launch.candidates = append(launch.candidates, c)
	count := len(launch.candidates)
	if count == 1 {
		remaining := o.window - c.at.Sub(launch.armedAt)
		launch.timer.Reset(min(remaining, o.grace))
	}
	o.mu.Unlock()
	if count > 1 {
		o.closeLaunch(launch, "")
	}
}

// closeLaunch ends a pending launch's window: exactly one candidate
// binds the terminal to its thread privately (no record yet — the first
// prompt mints it), anything else leaves the terminal untracked for
// good. The directory frees either way. Idempotent: the timer, a second
// candidate, an abort, and a Forget can all race to close the same
// launch.
func (o *Observer) closeLaunch(launch *pendingLaunch, reason string) {
	o.mu.Lock()
	if o.pending[launch.directory] != launch {
		o.mu.Unlock()
		return
	}
	delete(o.pending, launch.directory)
	launch.timer.Stop()
	o.release(launch.directory)
	var bound *pairing
	if reason == "" && len(launch.candidates) == 1 {
		c := launch.candidates[0]
		bound = &pairing{terminalID: launch.terminalID, threadID: c.threadID, cwd: c.cwd, title: c.title}
		o.held[c.threadID] = bound
		if conn := o.conn; conn != nil {
			// A prompt inside the grace already flipped the thread active
			// before the pairing existed to hear it; one read closes the
			// gap. Detached and async: this runs on the window timer.
			o.spawnRead(func() { o.readAndApply(context.Background(), conn, c.threadID) })
		}
	}
	o.mu.Unlock()

	switch {
	case bound != nil:
		o.logger.Info("codex terminal bound to its thread", "terminal", launch.terminalID,
			"elapsed", launch.candidates[0].at.Sub(launch.armedAt).Round(time.Millisecond))
	case reason != "":
		o.logger.Info("codex terminal left untracked", "terminal", launch.terminalID, "reason", reason)
	case len(launch.candidates) == 0:
		o.logger.Warn("codex terminal left untracked: no thread announced in its directory within the launch window",
			"terminal", launch.terminalID, "directory", launch.directory)
	default:
		o.logger.Warn("codex terminal left untracked: more than one thread announced in its directory within the launch window",
			"terminal", launch.terminalID, "directory", launch.directory, "candidates", len(launch.candidates))
	}
}
