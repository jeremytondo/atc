package service

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"runtime"
	"strconv"

	"github.com/jeremytondo/atc/internal/paths"
)

// Logs shows the supervisor-captured server output: the journal on Linux, a
// tail of the launchd-redirected log file on macOS.
func Logs(ctx context.Context, opts Options, follow bool, lines int) error {
	if err := supported(); err != nil {
		return err
	}
	if runtime.GOOS == "linux" {
		if _, err := exec.LookPath("journalctl"); err != nil {
			return errors.New("journalctl not found: no supervisor captures logs on this machine; foreground `atc server run` logs to stderr")
		}
		// An installed unit is what makes the journal worth consulting; on
		// a never-supervised machine an empty journal would masquerade as
		// "no recent output".
		unitFile, err := UnitPath()
		if err != nil {
			return err
		}
		if _, err := os.Stat(unitFile); errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("%s is not installed, so no supervised logs exist; foreground `atc server run` logs to stderr", UnitName)
		}
		args := []string{"--user", "-u", UnitName, "--no-pager", "-n", strconv.Itoa(lines)}
		if follow {
			args = append(args, "-f")
		}
		return stream(ctx, opts, "journalctl", args...)
	}
	logFile, err := paths.LogFile()
	if err != nil {
		return err
	}
	if _, err := os.Stat(logFile); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no log file at %s; `atc server start` creates it (foreground `atc server run` logs to stderr)", logFile)
	}
	args := []string{"-n", strconv.Itoa(lines)}
	if follow {
		args = append(args, "-f")
	}
	return stream(ctx, opts, "tail", append(args, logFile)...)
}

// stream runs a log tool with its output attached to ours; cancellation
// (^C on --follow) is a clean exit, not an error.
func stream(ctx context.Context, opts Options, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}
