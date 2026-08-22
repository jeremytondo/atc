package portal

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"
)

type remoteBackend struct {
	app *App
}

func newRemoteBackend(app *App) *remoteBackend {
	return &remoteBackend{app: app}
}

func (a *App) remoteConnect(backend *remoteBackend) error {
	if !term.IsTerminal(int(a.in.Fd())) || !term.IsTerminal(int(a.out.Fd())) {
		return errors.New("remote connect needs an interactive terminal")
	}
	return a.runTUI(backend, nil)
}

func (b *remoteBackend) Location() string {
	return fmt.Sprintf("%s · remote zmx", b.app.remoteHost)
}

func (b *remoteBackend) ListSessions() ([]string, error) {
	command := b.remotePortalCommand("list", "--json")
	cmd := exec.Command(b.app.sshBin, b.sshArgs(false, command)...)
	cmd.Stdin = b.app.in
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = b.app.errOut
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("query %s: %w", b.app.remoteHost, err)
	}

	var response inventory
	if err := json.Unmarshal(stdout.Bytes(), &response); err != nil {
		return nil, fmt.Errorf("decode portal response from %s: %w", b.app.remoteHost, err)
	}
	if response.Protocol != protocolVersion {
		return nil, fmt.Errorf(
			"portal protocol mismatch: local=%d remote=%d",
			protocolVersion,
			response.Protocol,
		)
	}
	return response.Sessions, nil
}

func (b *remoteBackend) Attach(name string) error {
	command := b.remotePortalCommand("attach", name)
	delay := time.Second

	for {
		cmd := exec.Command(b.app.sshBin, b.sshArgs(true, command)...)
		cmd.Stdin = b.app.in
		cmd.Stdout = b.app.out
		cmd.Stderr = b.app.errOut
		err := cmd.Run()
		if err == nil {
			return nil
		}
		if !isSSHConnectionError(err) {
			return fmt.Errorf("remote session %q failed: %w", name, err)
		}

		fmt.Fprintf(
			b.app.errOut,
			"\nportal: connection to %s lost; reconnecting to %s in %s (Ctrl-C to stop)\n",
			b.app.remoteHost,
			name,
			delay,
		)
		time.Sleep(delay)
		if delay < 8*time.Second {
			delay *= 2
		}
	}
}

func (b *remoteBackend) Doctor() error {
	command := b.remotePortalCommand("doctor")
	cmd := exec.Command(b.app.sshBin, b.sshArgs(false, command)...)
	cmd.Stdin = b.app.in
	cmd.Stdout = b.app.out
	cmd.Stderr = b.app.errOut
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("remote doctor on %s: %w", b.app.remoteHost, err)
	}
	return nil
}

func (b *remoteBackend) remotePortalCommand(args ...string) string {
	parts := []string{b.app.remotePortal}
	if b.app.remoteZMXDir != "" {
		parts = append(parts, "--zmx-dir", b.app.remoteZMXDir)
	}
	if b.app.remoteZMXBin != "" {
		parts = append(parts, "--zmx-bin", b.app.remoteZMXBin)
	}
	parts = append(parts, args...)

	quoted := make([]string, len(parts))
	for index, part := range parts {
		quoted[index] = shellQuote(part)
	}
	return strings.Join(quoted, " ")
}

func (b *remoteBackend) sshArgs(tty bool, command string) []string {
	args := make([]string, 0, 12)
	if tty {
		args = append(args, "-tt")
	}
	args = append(args,
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=2",
		"-o", "ConnectTimeout=5",
		b.app.remoteHost,
		command,
	)
	return args
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func isSSHConnectionError(err error) bool {
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 255
}
