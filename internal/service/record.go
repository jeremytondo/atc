package service

// The durable record of ATC-issued lifecycle operations (ATC-319): one
// line per start, restart, stop, and uninstall, appended to the state
// directory before the supervisor is asked. It names what ATC itself did
// and the process context it can vouch for — never who ran `systemctl`
// or spoke D-Bus directly; those leave only the supervisor's own trail.
// Nothing here is a secret: ATC's own arguments, pids, and the parent's
// short process name.

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/jeremytondo/atc/internal/paths"
)

// recordOperation appends the operation to the lifecycle log. Failing to
// record never blocks the operation; it is said on stderr instead.
func recordOperation(stderr io.Writer, operation string) {
	if err := appendRecord(operation); err != nil {
		say(stderr, "warning: could not record the %s in the lifecycle log: %v\n", operation, err)
	}
}

// appendRecord writes one line and sees it to stable storage: the record
// is evidence for after a crash, so a buffered write is not a record.
func appendRecord(operation string) error {
	path, err := paths.LifecycleLogFile()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	// The handler is driven directly: a Logger would swallow the write
	// error.
	record := slog.NewRecord(time.Now(), slog.LevelInfo, operation, 0)
	record.AddAttrs(
		slog.String("unit", UnitName),
		slog.Int("pid", os.Getpid()),
		slog.Int("ppid", os.Getppid()),
		slog.String("parent", parentName(os.Getppid())),
		slog.String("command", strings.Join(append([]string{"atc"}, os.Args[1:]...), " ")),
		slog.Bool("ssh", os.Getenv("SSH_CONNECTION") != ""))
	err = slog.NewTextHandler(f, nil).Handle(context.Background(), record)
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	return err
}

// parentName is the parent's short process name where the process table
// offers it (Linux /proc); empty elsewhere.
func parentName(ppid int) string {
	comm, err := os.ReadFile(filepath.Join("/proc", strconv.Itoa(ppid), "comm"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(comm))
}
