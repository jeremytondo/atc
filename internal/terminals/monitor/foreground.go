package monitor

// Foreground observation: which program is in front of the terminal, the
// way tmux, iTerm2, and WezTerm answer the same question. The monitor
// holds the PTY as its stdin, so one tcgetpgrp (the TIOCGPGRP ioctl, the
// same call on Linux and macOS) names the foreground process group, and
// the group leader's command line names the program. Resolution is a
// pure function over (argv, short name); the platform files supply the
// process-table reads.

import (
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// interpreters are skipped when they lead argv, so a script shows as
// itself: `node …/codex.js` reads codex. A fixed set on purpose — the
// rule must not grow into a command-line parser. Runners such as npx and
// uv show as themselves.
var interpreters = map[string]bool{
	"node": true, "bun": true, "deno": true,
	"python": true, "python3": true, "ruby": true, "perl": true, "env": true,
}

// shells invoked with -c show the first word of the command string, so
// `sh -c "git log | less"` reads git.
var shells = map[string]bool{"sh": true, "bash": true, "zsh": true, "fish": true, "dash": true}

var scriptExtensions = []string{".js", ".mjs", ".cjs", ".py", ".rb", ".pl"}

// resolve names the program behind argv; short (the kernel's short
// process name) is the answer whenever a step cannot decide. Rules, in
// order: strip a login shell's leading dash; step past a leading
// interpreter, its option arguments, and env's assignments; take the
// first word of a shell's -c string; then the basename without a script
// extension.
func resolve(argv []string, short string) string {
	if len(argv) == 0 || argv[0] == "" {
		return short
	}
	candidate := strings.TrimPrefix(argv[0], "-")
	base := filepath.Base(candidate)
	switch {
	case interpreters[base]:
		rest := argv[1:]
		for len(rest) > 0 && (strings.HasPrefix(rest[0], "-") || (base == "env" && strings.Contains(rest[0], "="))) {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return short
		}
		candidate = rest[0]
	case shells[base]:
		// Options precede the command string; -c may share a group with
		// other flags (-lc, -ic).
		for i := 1; i+1 < len(argv) && strings.HasPrefix(argv[i], "-"); i++ {
			if !strings.HasPrefix(argv[i], "--") && strings.ContainsRune(argv[i], 'c') {
				fields := strings.Fields(argv[i+1])
				if len(fields) == 0 {
					return short
				}
				candidate = fields[0]
				break
			}
		}
	}
	name := filepath.Base(candidate)
	for _, ext := range scriptExtensions {
		if trimmed, ok := strings.CutSuffix(name, ext); ok {
			name = trimmed
			break
		}
	}
	if name == "" || name == "." || name == string(filepath.Separator) {
		return short
	}
	return name
}

// process is what the process table says about one live process.
type process struct {
	// argv is the full command line; empty when it cannot be read.
	argv []string
	// short is the kernel's short process name.
	short string
}

type observation struct {
	process   string
	directory string
}

// observer samples the foreground process even when its group stays the
// same: cd and exec do not require a new process group. Failed reads keep
// the last successful values.
type observer struct {
	tty int
	// workload is the monitor's direct child. While the foreground group
	// is the monitor's own — before a job-control shell moves to its own
	// group, or under a shell without job control — the workload is the
	// program in front, never the monitor.
	workload int
	own      int
	last     observation
}

func newObserver(tty, workload int, directory string) *observer {
	return &observer{tty: tty, workload: workload, own: unix.Getpgrp(), last: observation{directory: directory}}
}

// poll returns the last known observation and whether either value changed.
func (o *observer) poll() (observation, bool) {
	pgrp, err := unix.IoctlGetInt(o.tty, unix.TIOCGPGRP)
	if err != nil || pgrp <= 0 {
		return o.last, false
	}
	pid, p := o.foreground(pgrp)
	if pid == 0 {
		return o.last, false
	}
	previous := o.last
	if name := resolve(p.argv, p.short); name != "" {
		o.last.process = name
	}
	if directory := readDirectory(pid); filepath.IsAbs(directory) {
		o.last.directory = directory
	}
	return o.last, o.last != previous
}

// foreground chooses the group leader, falling back to a live member
// after the leader exits. The directory follows this same process,
// including nested shells and foreground programs, never a background job.
func (o *observer) foreground(pgrp int) (int, process) {
	if pgrp == o.own {
		if p, ok := readProcess(o.workload); ok {
			return o.workload, p
		}
		return 0, process{}
	}
	if leader, ok := readProcess(pgrp); ok {
		return pgrp, leader
	}
	for _, pid := range groupMembers(pgrp) {
		if p, ok := readProcess(pid); ok {
			return pid, p
		}
	}
	return 0, process{}
}
