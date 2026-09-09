// Package monitor is the body of `atc __child`, the ATC-owned root task
// of every terminal session (ATC-251). It starts the real workload with
// inherited descriptors, records atomic start and exit evidence around it
// in the per-terminal report, forwards HUP/INT/TERM, and observes which
// program is in the terminal's foreground (ATC-317) — standard process
// supervision in the containerd-shim/tini shape. It never sits on the
// PTY data path; zmx remains the sole durable supervisor, and the
// monitor only records.
//
// Shell invocation (decided in the spec): with no command it execs $SHELL
// (fallback /bin/sh) with the traditional login convention
// (argv[0] = "-zsh") — byte-for-byte what zmx does for a hand-opened
// session. With a command it runs $SHELL -i -l -c "<command>", so profile
// and rc files load before the command, exactly as if the user had opened
// a terminal and typed it. Command exit (or shell exit) ends the terminal.
package monitor

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jeremytondo/atc/internal/terminals/report"
)

// LaunchFailureCode is recorded when the workload never started (bad
// directory, missing shell) — the shell convention for "command not
// found", surfaced through the normal status machinery instead of a
// separate launch-error path.
const LaunchFailureCode = 127

// PollInterval is how often the foreground group is read while the
// workload runs, attached or not. Flat: an idle poll is one ioctl.
const PollInterval = 2 * time.Second

// Options names the monitor's inputs, passed as flags by the zmx driver
// so the monitor never depends on inheriting ATC's environment.
type Options struct {
	ReportPath string
	TerminalID string
	// Directory the workload starts in.
	Directory string
	// Command is the free-form command to run through the shell; empty
	// starts a plain interactive login shell.
	Command string
	// PollInterval overrides PollInterval; zero means the default.
	PollInterval time.Duration
}

// Run supervises the workload and returns the monitor's own exit code
// (mirroring the child's). Failures to record evidence are reported on
// stderr — the session PTY — since there is nowhere else to say it.
// Foreground observation is best-effort throughout: it can never affect
// the workload, the evidence, or the exit code.
func Run(opts Options) int {
	interval := opts.PollInterval
	if interval <= 0 {
		interval = PollInterval
	}
	started := time.Now().UTC()
	rep := report.Report{TerminalID: opts.TerminalID, StartedAt: started}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	var cmd *exec.Cmd
	if opts.Command == "" {
		cmd = exec.Command(shell)
		cmd.Args = []string{"-" + filepath.Base(shell)}
	} else {
		cmd = exec.Command(shell, "-i", "-l", "-c", opts.Command)
	}
	cmd.Dir = opts.Directory
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr

	// Forwarding starts before the child so a signal in the startup window
	// is queued rather than lost.
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	if err := cmd.Start(); err != nil {
		code := LaunchFailureCode
		now := time.Now().UTC()
		rep.ExitedAt = &now
		rep.Code = &code
		rep.Error = err.Error()
		writeReport(opts.ReportPath, rep)
		return LaunchFailureCode
	}
	rep.PID = cmd.Process.Pid
	// The first observation rides on the start write; later ones are
	// written only when the resolved name changes.
	foreground := newObserver(int(os.Stdin.Fd()), cmd.Process.Pid)
	if name, changed := foreground.poll(); changed {
		rep.Process = name
	}
	writeReport(opts.ReportPath, rep)

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if name, changed := foreground.poll(); changed {
				rep.Process = name
				writeReport(opts.ReportPath, rep)
			}
		case received := <-signals:
			// zmx kill signals the monitor's process group, but a shell
			// with job control has moved to its own group — forwarding is
			// what carries the HUP through.
			_ = cmd.Process.Signal(received)
		case waitErr := <-waited:
			now := time.Now().UTC()
			rep.ExitedAt = &now
			code := LaunchFailureCode
			if state := cmd.ProcessState; state != nil {
				code = state.ExitCode()
				if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					code = 128 + int(status.Signal())
					rep.Signal = status.Signal().String()
				}
			} else if waitErr != nil {
				rep.Error = waitErr.Error()
			}
			rep.Code = &code
			writeReport(opts.ReportPath, rep)
			return code
		}
	}
}

func writeReport(path string, rep report.Report) {
	if err := report.Write(path, rep); err != nil {
		// The report is evidence, not control flow: the workload runs (or
		// ran) regardless, and the terminal degrades to missing.
		_, _ = os.Stderr.WriteString("atc __child: recording exit evidence: " + err.Error() + "\n")
	}
}
