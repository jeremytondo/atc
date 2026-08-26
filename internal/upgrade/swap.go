package upgrade

// The binary swap: stage beside the real target (same filesystem), then an
// atomic rename. Split so the caller can prove the staged binary runs
// before anything is replaced.

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// executablePath resolves where this process's binary really lives —
// symlinks resolved so the rename lands on the actual file rather than
// replacing a symlink.
func executablePath() (string, error) {
	executable, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(executable)
}

// stage writes the new binary to a temp file in target's directory with the
// executable mode set, returning its path.
func stage(target string, binary []byte) (string, error) {
	dir := filepath.Dir(target)
	tmp, err := os.CreateTemp(dir, ".atc-upgrade-*")
	if err != nil {
		return "", writeDiagnostic(dir, err)
	}
	_, err = tmp.Write(binary)
	if err == nil {
		err = tmp.Chmod(0o755)
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return tmp.Name(), nil
}

// promote atomically replaces target with the staged binary.
func promote(staged, target string) error {
	if err := os.Rename(staged, target); err != nil {
		return writeDiagnostic(filepath.Dir(target), err)
	}
	return nil
}

// writeDiagnostic names the remedy for an unwritable install directory; atc
// never invokes sudo.
func writeDiagnostic(dir string, err error) error {
	if errors.Is(err, fs.ErrPermission) {
		return fmt.Errorf("cannot write to %s (atc never invokes sudo): fix the directory's ownership or reinstall atc somewhere writable: %w", dir, err)
	}
	return err
}
