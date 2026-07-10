// Package paths resolves where atc keeps its Unix socket and PID file, so
// the background service and the CLI commands that manage it (start, stop,
// status, health) all agree on the same locations. The directory is chosen
// from the environment: XDG_RUNTIME_DIR, then TMPDIR, then /tmp.
package paths

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	SocketName = "atc.sock"
	PIDName    = "atc.pid"
	DBName     = "atc.db"
)

type EnvLookup func(string) string

type Paths struct {
	Dir        string
	SocketPath string
	PIDPath    string
}

func DefaultPaths() Paths {
	return PathsForEnv(os.Getenv, os.Getuid())
}

func StateDir() string {
	return StateDirForEnv(os.Getenv)
}

func DBPath() string {
	return DBPathForEnv(os.Getenv)
}

func PathsForEnv(lookup EnvLookup, uid int) Paths {
	return PathsForDir(ControlDirForEnv(lookup, uid))
}

// PathsForDir derives the socket and PID paths under an explicit control
// directory, such as one configured via the config file's control_dir setting.
func PathsForDir(dir string) Paths {
	return Paths{
		Dir:        dir,
		SocketPath: filepath.Join(dir, SocketName),
		PIDPath:    filepath.Join(dir, PIDName),
	}
}

func ControlDirForEnv(lookup EnvLookup, uid int) string {
	if xdgDir := lookup("XDG_RUNTIME_DIR"); xdgDir != "" {
		return filepath.Join(xdgDir, "atc")
	}

	if tmpDir := lookup("TMPDIR"); tmpDir != "" {
		return filepath.Join(tmpDir, fmt.Sprintf("atc-%d", uid))
	}

	return filepath.Join("/tmp", fmt.Sprintf("atc-%d", uid))
}

func StateDirForEnv(lookup EnvLookup) string {
	if xdgDir := lookup("XDG_STATE_HOME"); xdgDir != "" {
		return filepath.Join(xdgDir, "atc")
	}
	if home := lookup("HOME"); home != "" {
		return filepath.Join(home, ".local", "state", "atc")
	}
	return filepath.Join(os.TempDir(), "atc")
}

func DBPathForEnv(lookup EnvLookup) string {
	return filepath.Join(StateDirForEnv(lookup), DBName)
}

func EnsureControlDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create service control directory %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("set service control directory permissions on %s: %w", path, err)
	}
	return nil
}
