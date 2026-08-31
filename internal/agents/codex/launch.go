package codex

import (
	"context"
	"errors"
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

// errUnprepared reports a Command without the PrepareLaunch that must
// precede it — an invariant of the launch composition, not a runtime
// condition.
var errUnprepared = errors.New("codex launch was not prepared")

// launchSlot is one directory's turn: held from a launch's preparation
// until its window closes (or its create aborts), released to whichever
// same-directory launch waits next. launch is set once Command arms the
// window.
type launchSlot struct {
	released chan struct{}
	launch   *pendingLaunch
}

// pendingLaunch is one armed launch waiting for its announcement.
type pendingLaunch struct {
	terminalID string
	directory  string
	slot       *launchSlot
	armedAt    time.Time
	// armedSeq is the wire sequence at arming: only frames received after
	// it can be candidates, however late they are processed.
	armedSeq   uint64
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
	seq      uint64
	// promptSeen records a live status heard for the thread while the
	// window was still open — a prompt inside the grace.
	promptSeen bool
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

// reserve takes the directory's slot for one launch, waiting while
// another launch in the same directory holds it. Launches in different
// directories never wait.
func (o *Observer) reserve(ctx context.Context, dir string) (*launchSlot, error) {
	for {
		o.mu.Lock()
		busy, ok := o.slots[dir]
		if !ok {
			slot := &launchSlot{released: make(chan struct{})}
			o.slots[dir] = slot
			o.mu.Unlock()
			return slot, nil
		}
		o.mu.Unlock()
		select {
		case <-busy.released:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// release frees the directory if it still holds this slot, waking any
// launch waiting on it. Caller holds mu.
func (o *Observer) release(dir string, slot *launchSlot) {
	if o.slots[dir] == slot {
		delete(o.slots, dir)
		close(slot.released)
	}
}

// abandon undoes a prepared launch whose create failed: an armed window
// closes without binding, and the directory frees.
func (o *Observer) abandon(dir string, slot *launchSlot) {
	o.mu.Lock()
	launch := slot.launch
	if launch == nil {
		o.release(dir, slot)
	}
	o.mu.Unlock()
	if launch != nil {
		o.closeLaunch(launch, "launch aborted")
	}
}

// arm starts a fresh launch's window (Command time, under the terminals
// commit lock — no IO): frames received from this moment on are
// candidates. The slot must have been reserved by prepareLaunch.
func (o *Observer) arm(terminalID, directory string) error {
	dir := canonical(directory)
	o.mu.Lock()
	defer o.mu.Unlock()
	slot, ok := o.slots[dir]
	if !ok || slot.launch != nil {
		return errUnprepared
	}
	launch := &pendingLaunch{
		terminalID: terminalID, directory: dir, slot: slot,
		armedAt: o.now(), armedSeq: o.seq.Load(),
	}
	// Installed under mu — an announcement must never find the launch
	// without its timer.
	launch.timer = time.AfterFunc(o.window, func() { o.closeLaunch(launch, "") })
	slot.launch = launch
	return nil
}

// announced folds one thread/started into the pending launch for its
// directory, if any. It runs on the read loop, at receipt, so the
// dispatcher's pace never delays a candidate past the timer. A candidate
// must come from a terminal-started TUI and have been received after the
// arming and inside the window. The first candidate shortens the window
// to the grace; a second closes it at once — the outcome is decided.
func (o *Observer) announced(c candidate) {
	dir := canonical(c.cwd)
	o.mu.Lock()
	slot := o.slots[dir]
	if slot == nil || slot.launch == nil {
		o.mu.Unlock()
		return
	}
	launch := slot.launch
	if c.source != "cli" || c.seq <= launch.armedSeq || c.at.After(launch.armedAt.Add(o.window)) {
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

// candidateHeardLive records a live status for a thread that is some
// pending launch's candidate: a prompt typed inside the grace, before
// the pairing exists to hear it. Caller holds mu.
func (o *Observer) candidateHeardLive(threadID string) {
	for _, slot := range o.slots {
		if slot.launch == nil {
			continue
		}
		for i := range slot.launch.candidates {
			if slot.launch.candidates[i].threadID == threadID {
				slot.launch.candidates[i].promptSeen = true
			}
		}
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
	if launch.slot.launch != launch {
		o.mu.Unlock()
		return
	}
	launch.slot.launch = nil
	launch.timer.Stop()
	o.release(launch.directory, launch.slot)
	var bound *pairing
	if reason == "" && len(launch.candidates) == 1 {
		c := launch.candidates[0]
		bound = &pairing{terminalID: launch.terminalID, threadID: c.threadID, cwd: c.cwd, title: c.title,
			promptSeen: c.promptSeen}
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
