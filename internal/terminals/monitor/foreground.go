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
		for len(rest) > 0 && (strings.HasPrefix(rest[0], "-") || (base == "env" && isAssignment(rest[0]))) {
			rest = rest[1:]
		}
		if len(rest) == 0 {
			return short
		}
		candidate = rest[0]
	case shells[base]:
		for i := 1; i+1 < len(argv); i++ {
			if argv[i] == "-c" {
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

// isAssignment reports an env-style NAME=value argument.
func isAssignment(arg string) bool {
	name, _, ok := strings.Cut(arg, "=")
	if !ok || name == "" {
		return false
	}
	for i, r := range name {
		letter := r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z'
		digit := i > 0 && r >= '0' && r <= '9'
		if !letter && !digit {
			return false
		}
	}
	return true
}

// process is what the process table says about one process.
type process struct {
	// argv is the full command line; empty when the process has exited
	// or its command line cannot be read.
	argv []string
	// short is the kernel's short process name.
	short string
}

// observer polls the terminal's foreground group and resolves a name
// only when the group changes.
type observer struct {
	tty int
	// workload is the monitor's direct child. While the foreground group
	// is the monitor's own — before a job-control shell moves to its own
	// group, or under a shell without job control — the workload is the
	// program in front, never the monitor.
	workload int
	own      int
	pgrp     int
	process  string
}

func newObserver(tty, workload int) *observer {
	return &observer{tty: tty, workload: workload, own: unix.Getpgrp()}
}

// poll reads the foreground group and returns the program name when it
// differs from the last one returned. A failed read, or a group whose
// members cannot be named, changes nothing.
func (o *observer) poll() (string, bool) {
	pgrp, err := unix.IoctlGetInt(o.tty, unix.TIOCGPGRP)
	if err != nil || pgrp <= 0 || pgrp == o.pgrp {
		return "", false
	}
	o.pgrp = pgrp
	name := o.name(pgrp)
	if name == "" || name == o.process {
		return "", false
	}
	o.process = name
	return name, true
}

// name resolves the group: its leader, then any live member when the
// leader has already exited, then the leader's short name when its
// command line is unreadable.
func (o *observer) name(pgrp int) string {
	if pgrp == o.own {
		if p, ok := readProcess(o.workload); ok {
			return resolve(p.argv, p.short)
		}
		return ""
	}
	leader, ok := readProcess(pgrp)
	if ok && len(leader.argv) > 0 {
		return resolve(leader.argv, leader.short)
	}
	for _, pid := range groupMembers(pgrp) {
		if pid == pgrp {
			continue
		}
		if p, ok := readProcess(pid); ok && len(p.argv) > 0 {
			return resolve(p.argv, p.short)
		}
	}
	if ok {
		return resolve(nil, leader.short)
	}
	return ""
}
