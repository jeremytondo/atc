// Package wrapper is the body of `atc __child`, the ATC-owned root task of
// every terminal session (ATC-251). It records atomic start and exit
// evidence around the real workload and forwards HUP/INT/TERM — standard
// process supervision in the containerd-shim/tini shape. zmx remains the
// sole durable supervisor; the wrapper only records.
//
// Shell invocation (decided in the spec): with no app it execs $SHELL
// (fallback /bin/sh) with the traditional login convention
// (argv[0] = "-zsh") — byte-for-byte what zmx does for a hand-opened
// session. With an app it runs $SHELL -i -l -c "<app>", so profile and rc
// files load before the app, exactly as if the user had opened a terminal
// and typed it. App exit (or shell exit) ends the terminal.
package wrapper

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jeremytondo/atc/internal/terminals/exitmarker"
)

// LaunchFailureCode is recorded when the workload never started (bad
// directory, missing shell) — the shell convention for "command not
// found", surfaced through the normal status machinery instead of a
// separate launch-error path.
const LaunchFailureCode = 127

// Options names the wrapper's inputs, passed as flags by the zmx adapter
// so the wrapper never depends on inheriting ATC's environment.
type Options struct {
	MarkerPath string
	TerminalID string
	// Directory the workload starts in.
	Directory string
	// App is the free-form command to run through the shell; empty starts
	// a plain interactive login shell.
	App string
}

// Run supervises the workload and returns the wrapper's own exit code
// (mirroring the child's). Failures to record evidence are reported on
// stderr — the session PTY — since there is nowhere else to say it.
func Run(opts Options) int {
	started := time.Now().UTC()
	marker := exitmarker.Marker{TerminalID: opts.TerminalID, StartedAt: started}

	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	var cmd *exec.Cmd
	if opts.App == "" {
		cmd = exec.Command(shell)
		cmd.Args = []string{"-" + filepath.Base(shell)}
	} else {
		cmd = exec.Command(shell, "-i", "-l", "-c", opts.App)
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
		marker.ExitedAt = &now
		marker.Code = &code
		marker.Error = err.Error()
		writeMarker(opts.MarkerPath, marker)
		return LaunchFailureCode
	}
	marker.PID = cmd.Process.Pid
	writeMarker(opts.MarkerPath, marker)

	waited := make(chan error, 1)
	go func() { waited <- cmd.Wait() }()
	for {
		select {
		case received := <-signals:
			// zmx kill signals the wrapper's process group, but a shell
			// with job control has moved to its own group — forwarding is
			// what carries the HUP through.
			_ = cmd.Process.Signal(received)
		case waitErr := <-waited:
			now := time.Now().UTC()
			marker.ExitedAt = &now
			code := LaunchFailureCode
			if state := cmd.ProcessState; state != nil {
				code = state.ExitCode()
				if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
					code = 128 + int(status.Signal())
					marker.Signal = status.Signal().String()
				}
			} else if waitErr != nil {
				marker.Error = waitErr.Error()
			}
			marker.Code = &code
			writeMarker(opts.MarkerPath, marker)
			return code
		}
	}
}

func writeMarker(path string, marker exitmarker.Marker) {
	if err := exitmarker.Write(path, marker); err != nil {
		// The marker is evidence, not control flow: the workload runs (or
		// ran) regardless, and the terminal degrades to missing.
		_, _ = os.Stderr.WriteString("atc __child: recording exit evidence: " + err.Error() + "\n")
	}
}
