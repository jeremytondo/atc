package portal

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/term"
)

func (a *App) connect() error {
	if !term.IsTerminal(int(a.in.Fd())) || !term.IsTerminal(int(a.out.Fd())) {
		return errors.New("connect needs an interactive terminal")
	}
	if err := os.MkdirAll(a.zmxDir, 0o700); err != nil {
		return fmt.Errorf("create private zmx directory: %w", err)
	}
	if err := os.Remove(a.managerExitMarker()); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clear manager exit marker: %w", err)
	}

	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate portal executable: %w", err)
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return fmt.Errorf("resolve portal executable: %w", err)
	}

	managerExists, err := a.sessionExists(managerSession)
	if err != nil {
		return err
	}

	for {
		args := []string{"attach", managerSession}
		if !managerExists {
			args = append(args, executable,
				"--zmx-dir", a.zmxDir,
				"--zmx-bin", a.zmxBin,
				"tui",
			)
		}

		cmd := exec.Command(a.zmxBin, args...)
		cmd.Env = a.zmxEnv(true)
		cmd.Stdin = a.in
		cmd.Stdout = a.out
		cmd.Stderr = a.errOut
		runErr := cmd.Run()

		if _, markerErr := os.Stat(a.managerExitMarker()); markerErr == nil {
			_ = os.Remove(a.managerExitMarker())
			return nil
		}

		managerExists, err = a.sessionExists(managerSession)
		if err != nil {
			return err
		}
		if !managerExists {
			if runErr != nil {
				return fmt.Errorf("manager session exited unexpectedly: %w", runErr)
			}
			return errors.New("manager session exited unexpectedly")
		}
		if runErr != nil {
			return fmt.Errorf("zmx attach failed while manager is still running: %w", runErr)
		}

		// A successful return while __portal still exists is a detach. Reattach
		// immediately; this is the entire supervisor loop the POC is proving.
	}
}

func (a *App) sessions() ([]string, error) {
	if err := os.MkdirAll(a.zmxDir, 0o700); err != nil {
		return nil, fmt.Errorf("create private zmx directory: %w", err)
	}
	cmd := exec.Command(a.zmxBin, "list", "--short")
	cmd.Env = a.zmxEnv(true)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil && len(bytes.TrimSpace(output)) == 0 {
		message := strings.ToLower(stderr.String())
		if strings.Contains(message, "no session") || strings.Contains(message, "no active") {
			return nil, nil
		}
		return nil, fmt.Errorf("list zmx sessions: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	sessions := make([]string, 0, len(lines))
	seen := make(map[string]bool, len(lines))
	for _, line := range lines {
		name := strings.TrimSpace(line)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		sessions = append(sessions, name)
	}
	sort.Strings(sessions)
	return sessions, nil
}

func (a *App) sessionExists(want string) (bool, error) {
	sessions, err := a.sessions()
	if err != nil {
		return false, err
	}
	for _, session := range sessions {
		if session == want {
			return true, nil
		}
	}
	return false, nil
}

func (a *App) switchTo(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("session name cannot be empty")
	}
	if name == managerSession {
		return nil
	}
	if strings.ContainsAny(name, "\t\r\n") {
		return errors.New("session name cannot contain tabs or newlines")
	}

	cmd := exec.Command(a.zmxBin, "attach", name)
	// Deliberately retain ZMX_SESSION here. zmx uses it to retarget the
	// existing client rather than nesting another zmx attachment.
	cmd.Env = a.zmxEnv(false)
	cmd.Stdin = a.in
	cmd.Stdout = a.out
	cmd.Stderr = a.errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("switch to %q: %w", name, err)
	}
	return nil
}
