package portal

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

func defaultZMXDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err == nil && home != "" {
			base = filepath.Join(home, ".local", "state")
		} else {
			base = os.TempDir()
		}
	}
	return filepath.Join(base, "portal-poc", "zmx")
}

func (a *App) managerExitMarker() string {
	return filepath.Join(filepath.Dir(a.zmxDir), ".manager-exited")
}

func (a *App) zmxEnv(independentClient bool) []string {
	env := setEnv(a.env, "ZMX_DIR", a.zmxDir)
	if independentClient {
		env = unsetEnv(env, "ZMX_SESSION")
	}
	return env
}

func setEnv(env []string, key, value string) []string {
	env = unsetEnv(env, key)
	return append(env, key+"="+value)
}

func unsetEnv(env []string, key string) []string {
	prefix := key + "="
	result := make([]string, 0, len(env))
	for _, item := range env {
		if !strings.HasPrefix(item, prefix) {
			result = append(result, item)
		}
	}
	return result
}

func (a *App) printSessions() error {
	sessions, err := a.selectableSessions()
	if err != nil {
		return err
	}
	for _, session := range sessions {
		fmt.Fprintln(a.out, session)
	}
	return nil
}

type inventory struct {
	Protocol int
	Sessions []string
}

func (a *App) printInventoryJSON() error {
	sessions, err := a.selectableSessions()
	if err != nil {
		return err
	}
	return json.NewEncoder(a.out).Encode(inventory{
		Protocol: protocolVersion,
		Sessions: sessions,
	})
}

func (a *App) doctor() error {
	if err := os.MkdirAll(a.zmxDir, 0o700); err != nil {
		return fmt.Errorf("create private zmx directory: %w", err)
	}
	version := exec.Command(a.zmxBin, "version")
	version.Env = a.zmxEnv(true)
	output, err := version.CombinedOutput()
	if err != nil {
		return fmt.Errorf("run zmx version: %w: %s", err, strings.TrimSpace(string(output)))
	}

	fmt.Fprintf(a.out, "portal executable: %s\n", executableOrUnknown())
	fmt.Fprintf(a.out, "zmx executable:    %s\n", a.zmxBin)
	fmt.Fprintf(a.out, "private ZMX_DIR:  %s\n", a.zmxDir)
	fmt.Fprintf(a.out, "platform:         %s/%s\n", runtime.GOOS, runtime.GOARCH)
	fmt.Fprintln(a.out, "\nzmx reports:")
	fmt.Fprint(a.out, string(output))
	return nil
}

func executableOrUnknown() string {
	path, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return path
	}
	return path
}
