package portal

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// attachIndependent is the remote protocol endpoint used by the local
// controller. It creates its own zmx client rather than switching an inherited
// client from the hosted manager.
func (a *App) attachIndependent(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("session name cannot be empty")
	}
	if name == managerSession {
		return errors.New("the manager session cannot be attached through this endpoint")
	}
	if strings.ContainsAny(name, "\t\r\n") {
		return errors.New("session name cannot contain tabs or newlines")
	}
	if err := os.MkdirAll(a.zmxDir, 0o700); err != nil {
		return fmt.Errorf("create private zmx directory: %w", err)
	}

	cmd := exec.Command(a.zmxBin, "attach", name)
	cmd.Env = a.zmxEnv(true)
	cmd.Stdin = a.in
	cmd.Stdout = a.out
	cmd.Stderr = a.errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("attach %q: %w", name, err)
	}
	return nil
}
